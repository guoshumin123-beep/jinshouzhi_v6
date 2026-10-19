package htx

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/shopspring/decimal"
)

// 子用户管理与资金划转接口。依据 docs/htx_api/子用户管理API文档 与 docs/htx_api/现货API文档。
// 这些接口位于现货域名（SpotBaseURL），除 UserUID / AccountTransfer 外均须使用母账户凭证。
// 金额一律以 decimal.Decimal 表示，序列化为字符串发送，严禁 float64。

const (
	pathSubUserCreate  = "/v2/sub-user/creation"
	pathSubUserList    = "/v2/sub-user/user-list"
	pathSubUserState   = "/v2/sub-user/user-state"
	pathSubUserManage  = "/v2/sub-user/management"
	pathSubUserDeduct  = "/v2/sub-user/deduct-mode"
	pathSubAPIKeyGen   = "/v2/sub-user/api-key-generation"
	pathSubAPIKeyDel   = "/v2/sub-user/api-key-deletion"
	pathAPIKeyInfo     = "/v2/user/api-key"
	pathUserUID        = "/v2/user/uid"
	pathSubUserXfer    = "/v1/subuser/transfer"
	pathSubUserBalance = "/v1/account/accounts/" // + {sub-uid}
	pathAccountXfer    = "/v2/account/transfer"
)

var (
	// ErrOTPRequired 火币要求母账户谷歌验证码（otpToken）。本系统不保存验证器种子，遇到即终止，需人工处理。
	ErrOTPRequired = errors.New("htx: OTP token required by HTX")
	// ErrIPBindingRequired 生成带交易权限的子账户 Key 必须绑定服务器出口 IP（安全铁律）。
	ErrIPBindingRequired = errors.New("htx: sub-account API key must be bound to server egress IPs")
)

// ItemError 批量接口中单个条目的失败（整体 code=200 时仍可能出现）。
type ItemError struct {
	Code    string
	Message string
}

func (e *ItemError) Error() string { return "htx item error: code=" + e.Code + " msg=" + e.Message }

// rawCode 兼容字符串与数字形式的 errCode。
type rawCode string

func (c *rawCode) UnmarshalJSON(b []byte) error {
	*c = rawCode(strings.Trim(string(b), `"`))
	if *c == "null" {
		*c = ""
	}
	return nil
}

// ---------------------------------------------------------------------------
// 子用户创建 / 查询
// ---------------------------------------------------------------------------

// SubAccountType 创建子用户时的账户类型。
const (
	SubAccountGeneral  = "GENERAL"
	SubAccountContract = "CONTRACT"
)

type SubUserResult struct {
	UserName      string `json:"userName"`
	Note          string `json:"note"`
	UID           int64  `json:"uid"`
	SpotAccountID string `json:"spotAccountId"`
}

// CreateSubUser 创建一个子用户。用户名重复等条目级失败以 *ItemError 返回（如 2002）。
func (c *Client) CreateSubUser(ctx context.Context, master *Credentials, userName, note, accountType string) (*SubUserResult, error) {
	if !validSubUserName(userName) {
		return nil, fmt.Errorf("%w: userName %q must be 6-20 letters/digits starting with a letter", ErrInvalidRequest, userName)
	}
	body := map[string]any{"userList": []map[string]string{{"userName": userName, "note": note}}}
	if accountType != "" {
		body["subAccountType"] = accountType
	}
	var out []struct {
		SubUserResult
		ErrCode    rawCode `json:"errCode"`
		ErrMessage string  `json:"errMessage"`
	}
	if err := c.Do(ctx, http.MethodPost, pathSubUserCreate, BucketTrade, master, nil, body, &out); err != nil {
		return nil, err
	}
	if len(out) != 1 {
		return nil, fmt.Errorf("htx create sub user: expected 1 result, got %d", len(out))
	}
	if out[0].ErrCode != "" {
		return nil, &ItemError{Code: string(out[0].ErrCode), Message: out[0].ErrMessage}
	}
	if out[0].UID <= 0 {
		return nil, fmt.Errorf("htx create sub user: missing uid in result")
	}
	return &out[0].SubUserResult, nil
}

// validSubUserName 文档：6 至 20 位字母和数字的组合，可为纯字母；若含数字需以字母开头。
func validSubUserName(s string) bool {
	if len(s) < 6 || len(s) > 20 {
		return false
	}
	for i, r := range s {
		isLetter := (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z')
		if i == 0 && !isLetter {
			return false
		}
		if !isLetter && (r < '0' || r > '9') {
			return false
		}
	}
	return true
}

type SubUser struct {
	UID         int64  `json:"uid"`
	UserState   string `json:"userState"` // lock / normal
	SubUserName string `json:"subUserName"`
	Note        string `json:"note"`
}

// ListSubUsers 分页获取子用户列表；nextID 为 0 表示没有下一页。
func (c *Client) ListSubUsers(ctx context.Context, master *Credentials, fromID int64) ([]SubUser, int64, error) {
	q := url.Values{}
	if fromID > 0 {
		q.Set("fromId", strconv.FormatInt(fromID, 10))
	}
	var out []SubUser
	env, err := c.do(ctx, http.MethodGet, pathSubUserList, BucketQuery, master, q, nil, &out)
	if err != nil {
		return nil, 0, err
	}
	var next int64
	if env.NextID != nil {
		next = *env.NextID
	}
	return out, next, nil
}

// FindSubUserByName 遍历子用户列表按用户名查找（开户崩溃恢复用）。
func (c *Client) FindSubUserByName(ctx context.Context, master *Credentials, userName string) (*SubUser, error) {
	var from int64
	for page := 0; page < 1000; page++ {
		users, next, err := c.ListSubUsers(ctx, master, from)
		if err != nil {
			return nil, err
		}
		for i := range users {
			if strings.EqualFold(users[i].SubUserName, userName) {
				return &users[i], nil
			}
		}
		if next == 0 {
			return nil, nil
		}
		from = next
	}
	return nil, fmt.Errorf("htx find sub user: too many pages")
}

func (c *Client) SubUserState(ctx context.Context, master *Credentials, subUID int64) (string, error) {
	var out struct {
		UserState string `json:"userState"`
	}
	q := url.Values{"subUid": {strconv.FormatInt(subUID, 10)}}
	if err := c.Do(ctx, http.MethodGet, pathSubUserState, BucketQuery, master, q, nil, &out); err != nil {
		return "", err
	}
	return out.UserState, nil
}

// SetSubUserLock 冻结（lock=true）或解冻子用户，返回火币侧的 userState（lock / normal）。
func (c *Client) SetSubUserLock(ctx context.Context, master *Credentials, subUID int64, lock bool) (string, error) {
	action := "unlock"
	if lock {
		action = "lock"
	}
	var out struct {
		UserState string `json:"userState"`
	}
	if err := c.Do(ctx, http.MethodPost, pathSubUserManage, BucketTrade, master, nil,
		map[string]any{"subUid": subUID, "action": action}, &out); err != nil {
		return "", err
	}
	want := "normal"
	if lock {
		want = "lock"
	}
	if out.UserState != want {
		return out.UserState, fmt.Errorf("htx set sub user lock: requested %s, state %q", action, out.UserState)
	}
	return out.UserState, nil
}

// SetDeductMode 设置子用户手续费抵扣模式（master / sub）。
func (c *Client) SetDeductMode(ctx context.Context, master *Credentials, subUID int64, mode string) error {
	if mode != "master" && mode != "sub" {
		return fmt.Errorf("%w: deductMode %q", ErrInvalidRequest, mode)
	}
	var out []struct {
		SubUID     rawCode `json:"subUid"`
		DeductMode string  `json:"deductMode"`
		ErrCode    rawCode `json:"errCode"`
		ErrMessage string  `json:"errMessage"`
	}
	if err := c.Do(ctx, http.MethodPost, pathSubUserDeduct, BucketTrade, master, nil,
		map[string]string{"subUids": strconv.FormatInt(subUID, 10), "deductMode": mode}, &out); err != nil {
		return err
	}
	for _, r := range out {
		if r.ErrCode != "" {
			return &ItemError{Code: string(r.ErrCode), Message: r.ErrMessage}
		}
	}
	return nil
}

// ---------------------------------------------------------------------------
// 子用户 API Key
// ---------------------------------------------------------------------------

// SubAPIKeyRequest 生成子用户 API Key。
type SubAPIKeyRequest struct {
	SubUID      int64
	Note        string
	Permission  string   // 本系统固定 "readOnly,trade"
	IPAddresses []string // 必填：服务器出口 IP（最多 20 个）
	OTPToken    string   // 可选；本系统不保存验证器种子，通常为空
}

// SubAPIKey 新生成的 Key。Secret 仅此一次可得，不导出、格式化输出时脱敏。
type SubAPIKey struct {
	AccessKey   string
	Permission  string
	IPAddresses string
	Note        string
	secret      []byte
}

// Secret 返回 Secret 副本，调用方应在加密存储后立即清零。
func (k *SubAPIKey) Secret() []byte              { return append([]byte(nil), k.secret...) }
func (k SubAPIKey) String() string               { return "htx.SubAPIKey(" + maskKey(k.AccessKey) + ")" }
func (k SubAPIKey) GoString() string             { return k.String() }
func (k SubAPIKey) LogValue() slog.Value         { return slog.StringValue(k.String()) }
func (k SubAPIKey) MarshalJSON() ([]byte, error) { return []byte(`"` + k.String() + `"`), nil }
func (k *SubAPIKey) Credentials(uid int64) *Credentials {
	c := &Credentials{AccessKey: k.AccessKey, UID: uid, secret: k.Secret()}
	return c
}

// Wipe 清零内存中的 Secret。
func (k *SubAPIKey) Wipe() {
	for i := range k.secret {
		k.secret[i] = 0
	}
}

func (c *Client) GenerateSubAPIKey(ctx context.Context, master *Credentials, req SubAPIKeyRequest) (*SubAPIKey, error) {
	switch {
	case req.SubUID <= 0 || req.Note == "" || len(req.Note) > 255:
		return nil, fmt.Errorf("%w: subUid and note(1-255) required", ErrInvalidRequest)
	case !strings.Contains(req.Permission, "readOnly"):
		return nil, fmt.Errorf("%w: permission must include readOnly", ErrInvalidRequest)
	case len(req.IPAddresses) == 0:
		return nil, ErrIPBindingRequired
	case len(req.IPAddresses) > 20:
		return nil, fmt.Errorf("%w: at most 20 ip addresses", ErrInvalidRequest)
	}
	body := map[string]any{
		"subUid":      req.SubUID,
		"note":        req.Note,
		"permission":  req.Permission,
		"ipAddresses": strings.Join(req.IPAddresses, ","),
	}
	if req.OTPToken != "" {
		body["otpToken"] = req.OTPToken
	}
	var out struct {
		AccessKey   string `json:"accessKey"`
		SecretKey   string `json:"secretKey"`
		Note        string `json:"note"`
		Permission  string `json:"permission"`
		IPAddresses string `json:"ipAddresses"`
	}
	if err := c.Do(ctx, http.MethodPost, pathSubAPIKeyGen, BucketTrade, master, nil, body, &out); err != nil {
		if e, ok := IsAPIError(err); ok && strings.Contains(strings.ToLower(e.Message), "otp") {
			return nil, fmt.Errorf("%w: %v", ErrOTPRequired, err)
		}
		return nil, err
	}
	if out.AccessKey == "" || out.SecretKey == "" {
		return nil, fmt.Errorf("htx generate sub api key: empty key in response")
	}
	k := &SubAPIKey{AccessKey: out.AccessKey, Permission: out.Permission, IPAddresses: out.IPAddresses, Note: out.Note,
		secret: []byte(out.SecretKey)}
	out.SecretKey = ""
	return k, nil
}

type APIKeyInfo struct {
	AccessKey   string `json:"accessKey"`
	Note        string `json:"note"`
	Permission  string `json:"permission"`
	IPAddresses string `json:"ipAddresses"`
	ValidDays   int    `json:"validDays"` // -1 永久
	Status      string `json:"status"`    // normal / expired
	CreateTime  Millis `json:"createTime"`
	UpdateTime  Millis `json:"updateTime"`
}

// ListAPIKeys 查询 uid（母或子）的 API Key；accessKey 为空返回全部。
func (c *Client) ListAPIKeys(ctx context.Context, master *Credentials, uid int64, accessKey string) ([]APIKeyInfo, error) {
	q := url.Values{"uid": {strconv.FormatInt(uid, 10)}}
	if accessKey != "" {
		q.Set("accessKey", accessKey)
	}
	var out []APIKeyInfo
	if err := c.Do(ctx, http.MethodGet, pathAPIKeyInfo, BucketQuery, master, q, nil, &out); err != nil {
		return nil, err
	}
	return out, nil
}

func (c *Client) DeleteSubAPIKey(ctx context.Context, master *Credentials, subUID int64, accessKey string) error {
	return c.Do(ctx, http.MethodPost, pathSubAPIKeyDel, BucketTrade, master, nil,
		map[string]any{"subUid": subUID, "accessKey": accessKey}, nil)
}

// UserUID 查询凭证所属用户的 UID（母或子均可，用于校验 Key 与 UID 的绑定关系）。
func (c *Client) UserUID(ctx context.Context, cred *Credentials) (int64, error) {
	var uid int64
	if err := c.Do(ctx, http.MethodGet, pathUserUID, BucketQuery, cred, nil, nil, &uid); err != nil {
		return 0, err
	}
	if uid <= 0 {
		return 0, fmt.Errorf("htx user uid: invalid uid %d", uid)
	}
	return uid, nil
}

// ---------------------------------------------------------------------------
// 资金划转与余额
// ---------------------------------------------------------------------------

// 母子划转类型（/v1/subuser/transfer type）。
const (
	MasterTransferOut = "master-transfer-out" // 母 -> 子（现货）
	MasterTransferIn  = "master-transfer-in"  // 子 -> 母（现货）
)

// MasterTransferRequest 母子用户之间的现货划转（母账户凭证）。
type MasterTransferRequest struct {
	SubUID        int64
	Currency      string // 小写，如 usdt
	Amount        decimal.Decimal
	ClientOrderID string // 幂等键，10 个字符
	Type          string // MasterTransferOut / MasterTransferIn
}

// SubUserTransfer 母子划转。相同 client-order-id 重复提交由火币保证幂等，结果未知时可安全重发。
func (c *Client) SubUserTransfer(ctx context.Context, master *Credentials, req MasterTransferRequest) (int64, error) {
	switch {
	case req.SubUID <= 0 || req.Currency == "" || req.Currency != strings.ToLower(req.Currency):
		return 0, fmt.Errorf("%w: sub-uid and lower-case currency required", ErrInvalidRequest)
	case !req.Amount.IsPositive():
		return 0, fmt.Errorf("%w: amount must be > 0", ErrInvalidRequest)
	case len(req.ClientOrderID) != 10:
		return 0, fmt.Errorf("%w: client-order-id must be 10 characters", ErrInvalidRequest)
	case req.Type != MasterTransferOut && req.Type != MasterTransferIn:
		return 0, fmt.Errorf("%w: type %q", ErrInvalidRequest, req.Type)
	}
	body := map[string]any{
		"sub-uid":         req.SubUID,
		"currency":        req.Currency,
		"amount":          req.Amount.String(),
		"client-order-id": req.ClientOrderID,
		"type":            req.Type,
	}
	var id int64
	if err := c.Do(ctx, http.MethodPost, pathSubUserXfer, BucketTrade, master, nil, body, &id); err != nil {
		return 0, err
	}
	return id, nil
}

// 现货-合约划转业务线（/v2/account/transfer from/to）。
const (
	LineSpot       = "spot"
	LineLinearSwap = "linear-swap"
)

// AccountTransferRequest 子账户自身现货与 U 本位合约之间的划转（子账户凭证）。
// 该接口无幂等键：调用方必须自行保证结果未知时不盲目重发。
type AccountTransferRequest struct {
	From          string // spot / linear-swap
	To            string
	Currency      string // 如 usdt
	Amount        decimal.Decimal
	MarginAccount string // 全仓 "USDT"；逐仓小写合约代码，如 "btc-usdt"
}

func (c *Client) AccountTransfer(ctx context.Context, cred *Credentials, req AccountTransferRequest) (int64, error) {
	lines := map[string]bool{LineSpot: true, LineLinearSwap: true}
	switch {
	case !lines[req.From] || !lines[req.To] || req.From == req.To:
		return 0, fmt.Errorf("%w: from/to %s->%s", ErrInvalidRequest, req.From, req.To)
	case req.Currency == "" || !req.Amount.IsPositive():
		return 0, fmt.Errorf("%w: currency and positive amount required", ErrInvalidRequest)
	case req.MarginAccount == "":
		return 0, fmt.Errorf("%w: margin-account required", ErrInvalidRequest)
	}
	body := map[string]string{
		"from":           req.From,
		"to":             req.To,
		"currency":       req.Currency,
		"amount":         req.Amount.String(),
		"margin-account": req.MarginAccount,
	}
	var id int64
	if err := c.Do(ctx, http.MethodPost, pathAccountXfer, BucketTrade, cred, nil, body, &id); err != nil {
		return 0, err
	}
	return id, nil
}

type SpotBalance struct {
	Currency string `json:"currency"`
	Type     string `json:"type"` // trade / frozen / ...
	Balance  Num    `json:"balance"`
}

type SpotAccount struct {
	ID    int64         `json:"id"`
	Type  string        `json:"type"` // spot / point / margin / super-margin
	State string        `json:"state"`
	List  []SpotBalance `json:"list"`
}

// SubUserSpotBalance 母账户查询子用户各账户余额。
func (c *Client) SubUserSpotBalance(ctx context.Context, master *Credentials, subUID int64) ([]SpotAccount, error) {
	var out []SpotAccount
	if err := c.Do(ctx, http.MethodGet, pathSubUserBalance+strconv.FormatInt(subUID, 10), BucketQuery, master, nil, nil, &out); err != nil {
		return nil, err
	}
	return out, nil
}

// SpotTradeBalance 汇总 spot 账户中某币种的可用（trade）余额。
func SpotTradeBalance(accounts []SpotAccount, currency string) decimal.Decimal {
	total := decimal.Zero
	for _, a := range accounts {
		if a.Type != "spot" {
			continue
		}
		for _, b := range a.List {
			if strings.EqualFold(b.Currency, currency) && b.Type == "trade" && b.Balance.Valid {
				total = total.Add(b.Balance.Decimal)
			}
		}
	}
	return total
}

// ---------------------------------------------------------------------------
// 合约侧开户配置（合约域名 SwapBaseURL）
// ---------------------------------------------------------------------------

// SetAssetMode 设置资产模式（子账户凭证）。文档响应示例的 asset_mode 与请求不一致，
// 因此不比对响应值，调用方应随后用 AssetMode 复核。
func (c *Client) SetAssetMode(ctx context.Context, cred *Credentials, mode int) error {
	if mode != AssetModeSingleLegacy && mode != AssetModeMulti && mode != AssetModeSingle {
		return fmt.Errorf("%w: asset_mode %d", ErrInvalidRequest, mode)
	}
	return c.Do(ctx, http.MethodPost, pathAssetMode, BucketTrade, cred, nil, map[string]int{"asset_mode": mode}, nil)
}

const pathSwapSubAuth = "/linear-swap-api/v1/swap_sub_auth"

// SwapSubAuth 为子用户开通 U 本位合约交易权限（母账户凭证）。
//
// TODO: 过渡期 V1 接口，未收录于 docs/htx_api（参数来自老项目实测：{"sub_uid": "<uid>", "sub_auth": 1}）。
// 返回结构未有文档，按 V1 通用信封解析；data 中若出现 errors 列表则视为失败。后续以官方 V5 替代接口为准替换。
func (c *Client) SwapSubAuth(ctx context.Context, master *Credentials, subUID int64) error {
	if subUID <= 0 {
		return fmt.Errorf("%w: sub_uid required", ErrInvalidRequest)
	}
	var raw json.RawMessage
	if err := c.Do(ctx, http.MethodPost, pathSwapSubAuth, BucketTrade, master, nil,
		map[string]any{"sub_uid": strconv.FormatInt(subUID, 10), "sub_auth": 1}, &raw); err != nil {
		return err
	}
	var obj struct {
		Errors []json.RawMessage `json:"errors"`
	}
	if len(raw) > 0 && raw[0] == '{' && json.Unmarshal(raw, &obj) == nil && len(obj.Errors) > 0 {
		return &ItemError{Code: "swap_sub_auth", Message: string(obj.Errors[0])}
	}
	return nil
}
