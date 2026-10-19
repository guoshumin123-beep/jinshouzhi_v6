// Package htxmock 是火币 U 本位联合保证金 V5 接口的本地模拟服务，仅用于测试。
//
// 行为依据 docs/htx_api/U本位联合保证金API文档：
//   - 校验 HmacSHA256 签名（GET 业务参数参与签名，POST 仅认证参数参与签名）与 5 分钟时间戳有效期；
//   - 严格解析请求体：出现文档之外的字段即返回 1067（字段不合法），用于发现客户端字段名错误；
//   - 按 AccessKey 区分子账户，记录每个请求（用于断言“使用了哪个子账户的 Key”与限频行为）。
package htxmock

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"sync"
	"time"

	"backend_v2/internal/htx"
)

// Account 一个模拟子账户。
type Account struct {
	UID       int64
	AccessKey string
	Secret    string
	Master    bool // 母账户：可调用子用户管理、母子划转、开通合约权限等接口
}

// Request 一条已验签的请求记录。
type Request struct {
	At     time.Time
	Method string
	Path   string
	UID    int64
	Query  url.Values
	Body   map[string]any
}

// Order 模拟订单。
type Order struct {
	UID           int64
	OrderID       string
	ClientOrderID string
	ContractCode  string
	MarginMode    string
	Side          string
	Type          string
	Price         string
	Volume        string
	ReduceOnly    bool
	State         string
	Lever         int
	ChannelCode   string
}

type Server struct {
	*httptest.Server

	mu        sync.Mutex
	accounts  map[string]Account // by AccessKey
	requests  []Request
	orders    map[string]*Order // by order_id
	byClient  map[string]string // uid:client_order_id -> order_id
	levers    map[string]int    // uid:contract:mode -> lever
	modes     map[int64]string  // uid -> position mode
	positions map[int64]json.RawMessage
	balances  map[int64]json.RawMessage
	nextID    int64
	failNext  []failure
	headers   http.Header
	sub       *subState
}

type failure struct {
	path   string
	status int
	code   int
	msg    string
}

func New(accounts ...Account) *Server {
	s := &Server{
		accounts: map[string]Account{}, orders: map[string]*Order{}, byClient: map[string]string{},
		levers: map[string]int{}, modes: map[int64]string{}, positions: map[int64]json.RawMessage{},
		balances: map[int64]json.RawMessage{}, nextID: 1463958523020918784, headers: http.Header{},
	}
	for _, a := range accounts {
		s.accounts[a.AccessKey] = a
		s.modes[a.UID] = htx.PositionModeSingle
	}
	s.initSubUser()
	s.Server = httptest.NewServer(http.HandlerFunc(s.handle))
	return s
}

// Credentials 返回该账户的客户端凭证（带 UID）。
func (a Account) Credentials() *htx.Credentials {
	c, _ := htx.NewCredentials(a.AccessKey, a.Secret)
	c.UID = a.UID
	return c
}

// FailNext 让下一次访问 path 的请求返回指定的 HTTP 状态与业务错误码（status=0 表示 200）。
func (s *Server) FailNext(path string, status, code int, msg string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.failNext = append(s.failNext, failure{path, status, code, msg})
}

// SetHeader 为之后的所有响应附加响应头（如 ratelimit-remaining / recovery-time）。
func (s *Server) SetHeader(k, v string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.headers.Set(k, v)
}

// SetPositions / SetBalance 设置查询接口返回的 data（原样 JSON）。
func (s *Server) SetPositions(uid int64, data string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.positions[uid] = json.RawMessage(data)
}

func (s *Server) SetBalance(uid int64, data string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.balances[uid] = json.RawMessage(data)
}

// SetOrderState 模拟订单状态变化（成交/撤销）。
func (s *Server) SetOrderState(orderID, state string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if o := s.orders[orderID]; o != nil {
		o.State = state
	}
}

func (s *Server) Requests() []Request {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]Request(nil), s.requests...)
}

func (s *Server) Orders() []Order {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]Order, 0, len(s.orders))
	for _, o := range s.orders {
		out = append(out, *o)
	}
	return out
}

func (s *Server) Lever(uid int64, contract, mode string) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.levers[fmt.Sprintf("%d:%s:%s", uid, contract, mode)]
}

func (s *Server) PositionMode(uid int64) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.modes[uid]
}

// ---------------------------------------------------------------------------

func (s *Server) reply(w http.ResponseWriter, status int, code int, msg string, data any) {
	s.mu.Lock()
	for k, v := range s.headers {
		w.Header()[k] = v
	}
	s.mu.Unlock()
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	body := map[string]any{"code": code, "message": msg, "data": data, "ts": time.Now().UnixMilli()}
	_ = json.NewEncoder(w).Encode(body)
}

func (s *Server) handle(w http.ResponseWriter, r *http.Request) {
	acc, ok := s.authenticate(r)
	if !ok {
		s.reply(w, http.StatusOK, 403, "signature verification failed", nil)
		return
	}
	var body map[string]any
	var raw []byte
	if r.Method == http.MethodPost {
		buf := new(bytes.Buffer)
		_, _ = buf.ReadFrom(r.Body)
		raw = buf.Bytes()
		if err := json.Unmarshal(raw, &body); err != nil {
			s.reply(w, http.StatusOK, 1067, "invalid json", nil)
			return
		}
	}
	q := r.URL.Query()
	q.Del("Signature")

	s.mu.Lock()
	s.requests = append(s.requests, Request{At: time.Now(), Method: r.Method, Path: r.URL.Path, UID: acc.UID, Query: q, Body: body})
	var fail *failure
	for i, f := range s.failNext {
		if f.path == r.URL.Path {
			fail = &f
			s.failNext = append(s.failNext[:i], s.failNext[i+1:]...)
			break
		}
	}
	s.mu.Unlock()
	if fail != nil {
		st := fail.status
		if st == 0 {
			st = http.StatusOK
		}
		s.reply(w, st, fail.code, fail.msg, nil)
		return
	}

	switch r.Method + " " + r.URL.Path {
	case "POST /v5/trade/order":
		s.placeOrder(w, acc, raw)
	case "GET /v5/trade/order":
		s.orderInfo(w, acc, q)
	case "POST /v5/trade/cancel_order":
		s.cancelOrder(w, acc, raw)
	case "GET /v5/trade/position/opens":
		s.mu.Lock()
		data := s.positions[acc.UID]
		s.mu.Unlock()
		if data == nil {
			data = json.RawMessage("[]")
		}
		s.reply(w, 200, 200, "Success", data)
	case "GET /v5/account/balance":
		s.mu.Lock()
		data := s.balances[acc.UID]
		s.mu.Unlock()
		s.reply(w, 200, 200, "Success", data)
	case "GET /v5/account/asset_mode":
		// 文档示例该接口使用 msg 字段
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"code": 200, "msg": "操作成功", "data": map[string]int{"asset_mode": 1}, "ts": time.Now().UnixMilli()})
	case "GET /v5/position/mode":
		s.mu.Lock()
		m := s.modes[acc.UID]
		s.mu.Unlock()
		s.reply(w, 200, 200, "Success", map[string]string{"position_mode": m})
	case "POST /v5/position/mode":
		var req struct {
			PositionMode string `json:"position_mode"`
		}
		if !strict(raw, &req) {
			s.reply(w, 200, 1067, "字段不合法", nil)
			return
		}
		s.mu.Lock()
		s.modes[acc.UID] = req.PositionMode
		s.mu.Unlock()
		s.reply(w, 200, 200, "Success", map[string]string{"position_mode": req.PositionMode})
	case "POST /v5/position/lever":
		var req struct {
			ContractCode string `json:"contract_code"`
			MarginMode   string `json:"margin_mode"`
			PositionSide string `json:"position_side"`
			LeverRate    string `json:"lever_rate"`
		}
		if !strict(raw, &req) {
			s.reply(w, 200, 1067, "字段不合法", nil)
			return
		}
		lever, err := strconv.Atoi(req.LeverRate)
		if err != nil || req.ContractCode == "" {
			s.reply(w, 200, 1067, "字段不合法", nil)
			return
		}
		s.mu.Lock()
		s.levers[fmt.Sprintf("%d:%s:%s", acc.UID, req.ContractCode, req.MarginMode)] = lever
		s.mu.Unlock()
		s.reply(w, 200, 200, "Success", map[string]any{"contract_code": req.ContractCode, "margin_mode": req.MarginMode,
			"position_side": "both", "lever_rate": lever})
	default:
		if !s.handleSubUser(w, r, acc, raw, q) {
			s.reply(w, http.StatusNotFound, 404, "not found", nil)
		}
	}
}

// strict 严格解析：拒绝文档未定义的字段。
func strict(raw []byte, v any) bool {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	return dec.Decode(v) == nil
}

func (s *Server) authenticate(r *http.Request) (Account, bool) {
	q := r.URL.Query()
	s.mu.Lock()
	acc, ok := s.accounts[q.Get("AccessKeyId")]
	s.mu.Unlock()
	if !ok || q.Get("SignatureMethod") != "HmacSHA256" || q.Get("SignatureVersion") != "2" {
		return Account{}, false
	}
	ts, err := time.Parse("2006-01-02T15:04:05", q.Get("Timestamp"))
	if err != nil || time.Since(ts) > 5*time.Minute || time.Until(ts) > 5*time.Minute {
		return Account{}, false
	}
	want := htx.Sign([]byte(acc.Secret), r.Method, r.Host, r.URL.Path, q)
	return acc, q.Get("Signature") == want
}

type placeReq struct {
	ContractCode     string `json:"contract_code"`
	MarginMode       string `json:"margin_mode"`
	PositionSide     string `json:"position_side"`
	Side             string `json:"side"`
	Type             string `json:"type"`
	PriceMatch       string `json:"price_match"`
	ClientOrderID    string `json:"client_order_id"`
	Price            string `json:"price"`
	Volume           string `json:"volume"`
	ReduceOnly       int    `json:"reduce_only"`
	TimeInForce      string `json:"time_in_force"`
	SelfMatchPrevent string `json:"self_match_prevent"`
	ChannelCode      string `json:"channel_code"` // 白名单：Broker ID（未收录于官方文档）
}

func (s *Server) placeOrder(w http.ResponseWriter, acc Account, raw []byte) {
	var req placeReq
	if !strict(raw, &req) {
		s.reply(w, 200, 1067, "字段不合法", nil)
		return
	}
	if req.ContractCode == "" || req.MarginMode == "" || req.Side == "" || req.Type == "" || req.Volume == "" {
		s.reply(w, 200, 1066, "字段不能为空", nil)
		return
	}
	s.mu.Lock()
	if s.modes[acc.UID] == htx.PositionModeSingle && req.PositionSide != "" && req.PositionSide != "both" {
		s.mu.Unlock()
		s.reply(w, 200, 1067, "字段不合法", nil)
		return
	}
	key := fmt.Sprintf("%d:%s", acc.UID, req.ClientOrderID)
	if req.ClientOrderID != "" {
		if _, dup := s.byClient[key]; dup {
			s.mu.Unlock()
			s.reply(w, 200, 1067, "client_order_id 重复", nil)
			return
		}
	}
	s.nextID++
	id := strconv.FormatInt(s.nextID, 10)
	s.orders[id] = &Order{
		UID: acc.UID, OrderID: id, ClientOrderID: req.ClientOrderID, ContractCode: req.ContractCode,
		MarginMode: req.MarginMode, Side: req.Side, Type: req.Type, Price: req.Price, Volume: req.Volume,
		ReduceOnly: req.ReduceOnly == 1, State: htx.OrderStateNew, ChannelCode: req.ChannelCode,
		Lever: s.levers[fmt.Sprintf("%d:%s:%s", acc.UID, req.ContractCode, req.MarginMode)],
	}
	if req.ClientOrderID != "" {
		s.byClient[key] = id
	}
	s.mu.Unlock()
	data := map[string]string{"order_id": id}
	if req.ClientOrderID != "" {
		data["client_order_id"] = req.ClientOrderID
	}
	s.reply(w, 200, 200, "Success", data)
}

func (s *Server) find(acc Account, orderID, clientOrderID string) *Order {
	s.mu.Lock()
	defer s.mu.Unlock()
	if orderID == "" {
		orderID = s.byClient[fmt.Sprintf("%d:%s", acc.UID, clientOrderID)]
	}
	o := s.orders[orderID]
	if o == nil || o.UID != acc.UID {
		return nil
	}
	cp := *o
	return &cp
}

func (s *Server) orderInfo(w http.ResponseWriter, acc Account, q url.Values) {
	o := s.find(acc, q.Get("order_id"), q.Get("client_order_id"))
	if o == nil || o.ContractCode != q.Get("contract_code") {
		s.reply(w, 200, 1061, "The order does not exist.", nil)
		return
	}
	tradeVol := "0"
	if o.State == htx.OrderStateFilled {
		tradeVol = o.Volume
	}
	s.reply(w, 200, 200, "Success", map[string]any{
		"id": o.OrderID, "order_id": o.OrderID, "client_order_id": o.ClientOrderID, "contract_code": o.ContractCode,
		"side": o.Side, "position_side": "both", "type": o.Type, "price": o.Price, "volume": o.Volume,
		"margin_mode": o.MarginMode, "lever_rate": o.Lever, "state": o.State, "order_source": "api",
		"reduce_only": o.ReduceOnly, "time_in_force": "gtc", "trade_avg_price": "0", "trade_volume": tradeVol,
		"trade_turnover": "0", "fee": "0", "profit": nil, "real_profit": nil, "price_match": nil,
		"contract_type": "swap", "cancel_reason": nil, "cancel_volume": "0",
		"created_time": strconv.FormatInt(time.Now().UnixMilli(), 10), "updated_time": time.Now().UnixMilli(),
	})
}

func (s *Server) cancelOrder(w http.ResponseWriter, acc Account, raw []byte) {
	var req struct {
		ContractCode  string `json:"contract_code"`
		OrderID       string `json:"order_id"`
		ClientOrderID string `json:"client_order_id"`
	}
	if !strict(raw, &req) {
		s.reply(w, 200, 1067, "字段不合法", nil)
		return
	}
	o := s.find(acc, req.OrderID, req.ClientOrderID)
	if o == nil || o.State == htx.OrderStateCanceled || o.State == htx.OrderStateFilled {
		s.reply(w, 200, 1061, "The order does not exist.", nil)
		return
	}
	s.SetOrderState(o.OrderID, htx.OrderStateCanceled)
	s.reply(w, 200, 200, "Success", map[string]string{"order_id": o.OrderID, "client_order_id": o.ClientOrderID})
}
