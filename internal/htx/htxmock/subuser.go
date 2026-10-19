package htxmock

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"

	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/shopspring/decimal"
)

// 子用户管理、母子划转、现货-合约划转的模拟实现（docs/htx_api/子用户管理API文档、现货API文档）。

type mockSub struct {
	UID          int64
	UserName     string
	Note         string
	Type         string
	Locked       bool
	ContractAuth bool
	DeductMode   string
	AssetMode    int
	Spot         decimal.Decimal
	Swap         map[string]decimal.Decimal // margin-account -> 余额
	Keys         map[string]mockKey         // accessKey -> key
}

type mockKey struct {
	AccessKey  string
	Note       string
	Permission string
	IPs        string
	Created    time.Time
}

type subState struct {
	byName       map[string]*mockSub
	byUID        map[int64]*mockSub
	takenNames   map[string]bool // 被平台其他用户占用的用户名
	nextUID      int64
	masterSpot   decimal.Decimal
	xferByClient map[string]int64
	nextXferID   int64
	requireOTP   bool
	pageSize     int
	dropAfter    map[string]int // path -> 剩余次数：执行成功后丢弃响应（模拟结果未知）
}

func (s *Server) initSubUser() {
	s.sub = &subState{
		byName: map[string]*mockSub{}, byUID: map[int64]*mockSub{}, takenNames: map[string]bool{},
		nextUID: 500000000, masterSpot: decimal.NewFromInt(1_000_000), xferByClient: map[string]int64{},
		nextXferID: 9000, pageSize: 2, dropAfter: map[string]int{},
	}
}

// ---------------------------------------------------------------------------
// 测试控制
// ---------------------------------------------------------------------------

// RequireOTP 让生成子用户 API Key 的接口要求 otpToken。
func (s *Server) RequireOTP(v bool) { s.mu.Lock(); s.sub.requireOTP = v; s.mu.Unlock() }

// TakeName 模拟用户名已被平台其他用户占用。
func (s *Server) TakeName(name string) {
	s.mu.Lock()
	s.sub.takenNames[strings.ToLower(name)] = true
	s.mu.Unlock()
}

// DropResponseAfterApply 让接下来 n 次访问 path 的请求在服务端执行成功后断开连接（客户端得到网络错误，结果未知）。
func (s *Server) DropResponseAfterApply(path string, n int) {
	s.mu.Lock()
	s.sub.dropAfter[path] = n
	s.mu.Unlock()
}

// SubUser 返回子用户快照。
func (s *Server) SubUser(uid int64) (mockSub, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	u, ok := s.sub.byUID[uid]
	if !ok {
		return mockSub{}, false
	}
	cp := *u
	cp.Swap = map[string]decimal.Decimal{}
	for k, v := range u.Swap {
		cp.Swap[k] = v
	}
	cp.Keys = map[string]mockKey{}
	for k, v := range u.Keys {
		cp.Keys[k] = v
	}
	return cp, true
}

// SubUsers 返回全部子用户 UID（升序）。
func (s *Server) SubUsers() []int64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []int64
	for uid := range s.sub.byUID {
		out = append(out, uid)
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}

func (s *Server) MasterSpot() decimal.Decimal {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.sub.masterSpot
}

// CountRequests 统计访问 path 的已验签请求数。
func (s *Server) CountRequests(path string) int {
	n := 0
	for _, r := range s.Requests() {
		if r.Path == path {
			n++
		}
	}
	return n
}

// ---------------------------------------------------------------------------

func (s *Server) replyV1(w http.ResponseWriter, ok bool, data any, errCode, errMsg string) {
	w.Header().Set("Content-Type", "application/json")
	if ok {
		_ = json.NewEncoder(w).Encode(map[string]any{"status": "ok", "data": data})
		return
	}
	_ = json.NewEncoder(w).Encode(map[string]any{"status": "error", "err-code": errCode, "err-msg": errMsg, "data": nil})
}

func randHex(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// dropIfArmed 若该路径设置了“执行后丢弃响应”，则劫持连接直接关闭并返回 true。
func (s *Server) dropIfArmed(w http.ResponseWriter, path string) bool {
	s.mu.Lock()
	n := s.sub.dropAfter[path]
	if n > 0 {
		s.sub.dropAfter[path] = n - 1
	}
	s.mu.Unlock()
	if n <= 0 {
		return false
	}
	if hj, ok := w.(http.Hijacker); ok {
		if conn, _, err := hj.Hijack(); err == nil {
			_ = conn.Close()
			return true
		}
	}
	return false
}

func (s *Server) handleSubUser(w http.ResponseWriter, r *http.Request, acc Account, raw []byte, q url.Values) bool {
	path := r.URL.Path
	route := r.Method + " " + path
	if strings.HasPrefix(path, "/v1/account/accounts/") && r.Method == http.MethodGet {
		route = "GET /v1/account/accounts/{sub-uid}"
	}
	masterOnly := map[string]bool{
		"POST /v2/sub-user/creation": true, "GET /v2/sub-user/user-list": true, "POST /v2/sub-user/api-key-generation": true,
		"GET /v2/user/api-key": true, "POST /v2/sub-user/api-key-deletion": true, "POST /v2/sub-user/deduct-mode": true,
		"POST /v2/sub-user/management": true, "GET /v2/sub-user/user-state": true, "POST /v1/subuser/transfer": true,
		"GET /v1/account/accounts/{sub-uid}": true, "POST /linear-swap-api/v1/swap_sub_auth": true,
	}
	known := masterOnly[route] || route == "GET /v2/user/uid" || route == "POST /v2/account/transfer" || route == "POST /v5/account/asset_mode"
	if !known {
		return false
	}
	if masterOnly[route] && !acc.Master {
		s.reply(w, 200, 1002, "forbidden", nil)
		return true
	}
	if !acc.Master {
		s.mu.Lock()
		u := s.sub.byUID[acc.UID]
		locked := u != nil && u.Locked
		s.mu.Unlock()
		if locked {
			s.reply(w, 200, 1002, "forbidden", nil)
			return true
		}
	}

	switch route {
	case "POST /v2/sub-user/creation":
		var req struct {
			UserList []struct {
				UserName string `json:"userName"`
				Note     string `json:"note"`
			} `json:"userList"`
			SubAccountType string `json:"subAccountType"`
		}
		if !strict(raw, &req) || len(req.UserList) == 0 {
			s.reply(w, 200, 2002, "invalid field value", nil)
			return true
		}
		var out []map[string]any
		s.mu.Lock()
		for _, u := range req.UserList {
			key := strings.ToLower(u.UserName)
			if s.sub.byName[key] != nil || s.sub.takenNames[key] {
				out = append(out, map[string]any{"userName": u.UserName, "note": u.Note, "errCode": "2002",
					"errMessage": "valueinusernameduplicatedwithexistingrecord"})
				continue
			}
			s.sub.nextUID++
			ms := &mockSub{UID: s.sub.nextUID, UserName: u.UserName, Note: u.Note, Type: req.SubAccountType,
				DeductMode: "master", Swap: map[string]decimal.Decimal{}, Keys: map[string]mockKey{}}
			s.sub.byName[key], s.sub.byUID[ms.UID] = ms, ms
			s.modes[ms.UID] = "dual_side" // 新账户默认非单向，需开户流程设置
			out = append(out, map[string]any{"userName": u.UserName, "note": u.Note, "uid": ms.UID,
				"spotAccountId": strconv.FormatInt(ms.UID*10, 10)})
		}
		s.mu.Unlock()
		if s.dropIfArmed(w, path) {
			return true
		}
		s.reply(w, 200, 200, "", out)

	case "GET /v2/sub-user/user-list":
		from, _ := strconv.ParseInt(q.Get("fromId"), 10, 64)
		s.mu.Lock()
		var uids []int64
		for uid := range s.sub.byUID {
			if uid >= from {
				uids = append(uids, uid)
			}
		}
		sort.Slice(uids, func(i, j int) bool { return uids[i] < uids[j] })
		page := uids
		var next any
		if len(page) > s.sub.pageSize {
			next = page[s.sub.pageSize]
			page = page[:s.sub.pageSize]
		}
		data := []map[string]any{}
		for _, uid := range page {
			u := s.sub.byUID[uid]
			st := "normal"
			if u.Locked {
				st = "lock"
			}
			data = append(data, map[string]any{"uid": uid, "userState": st, "subUserName": u.UserName, "note": u.Note})
		}
		s.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		body := map[string]any{"code": 200, "data": data}
		if next != nil {
			body["nextId"] = next
		}
		_ = json.NewEncoder(w).Encode(body)

	case "POST /v2/sub-user/api-key-generation":
		var req struct {
			OTPToken    string `json:"otpToken"`
			SubUID      int64  `json:"subUid"`
			Note        string `json:"note"`
			Permission  string `json:"permission"`
			IPAddresses string `json:"ipAddresses"`
		}
		if !strict(raw, &req) {
			s.reply(w, 200, 2002, "invalid field value", nil)
			return true
		}
		s.mu.Lock()
		u := s.sub.byUID[req.SubUID]
		requireOTP := s.sub.requireOTP
		s.mu.Unlock()
		if requireOTP && req.OTPToken == "" {
			s.reply(w, 200, 1003, "otp token is required", nil)
			return true
		}
		if u == nil || req.Note == "" || !strings.Contains(req.Permission, "readOnly") {
			s.reply(w, 200, 2002, "invalid field value", nil)
			return true
		}
		ak, sk := "mock-ak-"+randHex(8), "mock-sk-"+randHex(16)
		s.mu.Lock()
		u.Keys[ak] = mockKey{AccessKey: ak, Note: req.Note, Permission: req.Permission, IPs: req.IPAddresses, Created: time.Now()}
		s.accounts[ak] = Account{UID: u.UID, AccessKey: ak, Secret: sk}
		s.mu.Unlock()
		if s.dropIfArmed(w, path) {
			return true
		}
		s.reply(w, 200, 200, "", map[string]string{"accessKey": ak, "secretKey": sk, "note": req.Note,
			"permission": req.Permission, "ipAddresses": req.IPAddresses})

	case "GET /v2/user/api-key":
		uid, _ := strconv.ParseInt(q.Get("uid"), 10, 64)
		s.mu.Lock()
		u := s.sub.byUID[uid]
		data := []map[string]any{}
		if u != nil {
			for _, k := range u.Keys {
				if ak := q.Get("accessKey"); ak != "" && ak != k.AccessKey {
					continue
				}
				data = append(data, map[string]any{"accessKey": k.AccessKey, "note": k.Note, "permission": k.Permission,
					"ipAddresses": k.IPs, "validDays": -1, "status": "normal", "createTime": k.Created.UnixMilli(),
					"updateTime": k.Created.UnixMilli()})
			}
		}
		s.mu.Unlock()
		s.reply(w, 200, 200, "success", data)

	case "POST /v2/sub-user/api-key-deletion":
		var req struct {
			SubUID    int64  `json:"subUid"`
			AccessKey string `json:"accessKey"`
		}
		if !strict(raw, &req) {
			s.reply(w, 200, 2002, "invalid field value", nil)
			return true
		}
		s.mu.Lock()
		u := s.sub.byUID[req.SubUID]
		exists := false
		if u != nil {
			_, exists = u.Keys[req.AccessKey]
		}
		if exists {
			delete(u.Keys, req.AccessKey)
			delete(s.accounts, req.AccessKey)
		}
		s.mu.Unlock()
		if u == nil || !exists {
			s.reply(w, 200, 2002, "invalid field value", nil)
			return true
		}
		s.reply(w, 200, 200, "", nil)

	case "POST /v2/sub-user/deduct-mode":
		var req struct {
			SubUIDs    string `json:"subUids"`
			DeductMode string `json:"deductMode"`
		}
		if !strict(raw, &req) {
			s.reply(w, 200, 2002, "invalid field value", nil)
			return true
		}
		uid, _ := strconv.ParseInt(req.SubUIDs, 10, 64)
		s.mu.Lock()
		u := s.sub.byUID[uid]
		if u != nil {
			u.DeductMode = req.DeductMode
		}
		s.mu.Unlock()
		item := map[string]any{"subUid": req.SubUIDs, "deductMode": req.DeductMode}
		if u == nil {
			item = map[string]any{"subUid": req.SubUIDs, "deductMode": nil, "errCode": 1002, "errMessage": "forbidden"}
		}
		s.reply(w, 200, 200, "", []any{item})

	case "POST /v2/sub-user/management":
		var req struct {
			SubUID int64  `json:"subUid"`
			Action string `json:"action"`
		}
		if !strict(raw, &req) || (req.Action != "lock" && req.Action != "unlock") {
			s.reply(w, 200, 2002, "invalid field value", nil)
			return true
		}
		s.mu.Lock()
		u := s.sub.byUID[req.SubUID]
		if u != nil {
			u.Locked = req.Action == "lock"
		}
		s.mu.Unlock()
		if u == nil {
			s.reply(w, 200, 2016, "invalid request while value specified in sub user states", nil)
			return true
		}
		st := "normal"
		if req.Action == "lock" {
			st = "lock"
		}
		s.reply(w, 200, 200, "", map[string]any{"subUid": req.SubUID, "userState": st})

	case "GET /v2/sub-user/user-state":
		uid, _ := strconv.ParseInt(q.Get("subUid"), 10, 64)
		s.mu.Lock()
		u := s.sub.byUID[uid]
		s.mu.Unlock()
		if u == nil {
			s.reply(w, 200, 2002, "invalid field value", nil)
			return true
		}
		st := "normal"
		if u.Locked {
			st = "lock"
		}
		s.reply(w, 200, 200, "", map[string]any{"uid": uid, "userState": st})

	case "GET /v2/user/uid":
		s.reply(w, 200, 200, "", acc.UID)

	case "POST /linear-swap-api/v1/swap_sub_auth":
		var req struct {
			SubUID  string `json:"sub_uid"`
			SubAuth int    `json:"sub_auth"`
		}
		if !strict(raw, &req) {
			s.replyV1(w, false, nil, "1067", "字段不合法")
			return true
		}
		uid, _ := strconv.ParseInt(req.SubUID, 10, 64)
		s.mu.Lock()
		u := s.sub.byUID[uid]
		if u != nil {
			u.ContractAuth = req.SubAuth == 1
		}
		s.mu.Unlock()
		if u == nil {
			s.replyV1(w, true, map[string]any{"errors": []any{map[string]any{"sub_uid": req.SubUID, "err_code": 1010, "err_msg": "用户不存在"}}, "successes": ""}, "", "")
			return true
		}
		s.replyV1(w, true, map[string]any{"errors": []any{}, "successes": req.SubUID}, "", "")

	case "POST /v5/account/asset_mode":
		var req struct {
			AssetMode int `json:"asset_mode"`
		}
		if !strict(raw, &req) {
			s.reply(w, 200, 1067, "字段不合法", nil)
			return true
		}
		s.mu.Lock()
		u := s.sub.byUID[acc.UID]
		ok := u != nil && u.ContractAuth
		if ok {
			u.AssetMode = req.AssetMode
		}
		s.mu.Unlock()
		if !ok {
			s.reply(w, 200, 1010, "用户不存在", nil)
			return true
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"code": 200, "msg": "操作成功", "data": map[string]int{"asset_mode": req.AssetMode}})

	case "POST /v1/subuser/transfer":
		var req struct {
			SubUID        int64  `json:"sub-uid"`
			Currency      string `json:"currency"`
			Amount        string `json:"amount"`
			ClientOrderID string `json:"client-order-id"`
			Type          string `json:"type"`
		}
		if !strict(raw, &req) {
			s.replyV1(w, false, nil, "invalid-parameter", "unknown field")
			return true
		}
		amt, err := decimal.NewFromString(req.Amount)
		if err != nil || !amt.IsPositive() || len(req.ClientOrderID) != 10 {
			s.replyV1(w, false, nil, "invalid-parameter", "invalid amount or client-order-id")
			return true
		}
		s.mu.Lock()
		if id, dup := s.sub.xferByClient[req.ClientOrderID]; dup { // 幂等：同 client-order-id 返回原结果
			s.mu.Unlock()
			s.replyV1(w, true, id, "", "")
			return true
		}
		u := s.sub.byUID[req.SubUID]
		var failMsg string
		switch {
		case u == nil:
			failMsg = "sub user not found"
		case req.Type == "master-transfer-out" && s.sub.masterSpot.LessThan(amt):
			failMsg = "insufficient balance"
		case req.Type == "master-transfer-in" && u.Spot.LessThan(amt):
			failMsg = "insufficient balance"
		case req.Type == "master-transfer-out":
			s.sub.masterSpot, u.Spot = s.sub.masterSpot.Sub(amt), u.Spot.Add(amt)
		case req.Type == "master-transfer-in":
			s.sub.masterSpot, u.Spot = s.sub.masterSpot.Add(amt), u.Spot.Sub(amt)
		default:
			failMsg = "invalid type"
		}
		var id int64
		if failMsg == "" {
			s.sub.nextXferID++
			id = s.sub.nextXferID
			s.sub.xferByClient[req.ClientOrderID] = id
		}
		s.mu.Unlock()
		if failMsg != "" {
			s.replyV1(w, false, nil, "account-frozen-balance-insufficient-error", failMsg)
			return true
		}
		if s.dropIfArmed(w, path) {
			return true
		}
		s.replyV1(w, true, id, "", "")

	case "GET /v1/account/accounts/{sub-uid}":
		uid, _ := strconv.ParseInt(strings.TrimPrefix(path, "/v1/account/accounts/"), 10, 64)
		s.mu.Lock()
		u := s.sub.byUID[uid]
		var spot decimal.Decimal
		if u != nil {
			spot = u.Spot
		}
		s.mu.Unlock()
		if u == nil {
			s.replyV1(w, false, nil, "base-record-invalid", "sub user not found")
			return true
		}
		s.replyV1(w, true, []any{
			map[string]any{"id": uid * 10, "type": "spot", "state": "working", "symbol": "", "list": []any{
				map[string]any{"currency": "usdt", "type": "trade", "balance": spot.String(), "debt": "invalid", "available": "invalid"},
				map[string]any{"currency": "usdt", "type": "frozen", "balance": "0"},
			}},
			map[string]any{"id": uid*10 + 1, "type": "point", "state": "working", "symbol": "", "list": []any{
				map[string]any{"currency": "hbpoint", "type": "trade", "balance": "0"},
			}},
		}, "", "")

	case "POST /v2/account/transfer":
		var req struct {
			From          string `json:"from"`
			To            string `json:"to"`
			Currency      string `json:"currency"`
			Amount        string `json:"amount"`
			MarginAccount string `json:"margin-account"`
		}
		if !strict(raw, &req) {
			s.reply(w, 200, 1067, "字段不合法", nil)
			return true
		}
		amt, err := decimal.NewFromString(req.Amount)
		if err != nil || !amt.IsPositive() || req.MarginAccount == "" {
			s.reply(w, 200, 1067, "字段不合法", nil)
			return true
		}
		s.mu.Lock()
		u := s.sub.byUID[acc.UID]
		var failCode int
		var failMsg string
		switch {
		case u == nil || !u.ContractAuth:
			failCode, failMsg = 1010, "合约账户未开通"
		case req.From == "spot" && req.To == "linear-swap":
			if u.Spot.LessThan(amt) {
				failCode, failMsg = 1047, "可用余额不足"
			} else {
				u.Spot = u.Spot.Sub(amt)
				u.Swap[req.MarginAccount] = u.Swap[req.MarginAccount].Add(amt)
			}
		case req.From == "linear-swap" && req.To == "spot":
			if u.Swap[req.MarginAccount].LessThan(amt) {
				failCode, failMsg = 1047, "可用余额不足"
			} else {
				u.Swap[req.MarginAccount] = u.Swap[req.MarginAccount].Sub(amt)
				u.Spot = u.Spot.Add(amt)
			}
		default:
			failCode, failMsg = 1067, "字段不合法"
		}
		var id int64
		if failCode == 0 {
			s.sub.nextXferID++
			id = s.sub.nextXferID
		}
		s.mu.Unlock()
		if failCode != 0 {
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{"code": failCode, "data": nil, "message": failMsg, "success": false})
			return true
		}
		if s.dropIfArmed(w, path) {
			return true
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"code": 200, "data": id, "message": "Succeed", "success": true})
	}
	return true
}

// SetSwapBalance 直接设置子账户合约余额（模拟交易盈亏等）。
func (s *Server) SetSwapBalance(uid int64, marginAccount string, amt decimal.Decimal) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if u := s.sub.byUID[uid]; u != nil {
		u.Swap[marginAccount] = amt
	}
}
