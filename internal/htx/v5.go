package htx

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/shopspring/decimal"
)

// U 本位联合保证金 V5 接口。依据 docs/htx_api/U本位联合保证金API文档（文档来源 HTX 开放平台）。
// 限频类别取自各接口文档的“接口权限”字段（交易 → BucketTrade，读取 → BucketQuery）。

const (
	pathOrder         = "/v5/trade/order"          // POST 下单（交易）/ GET 查询订单信息（读取）
	pathCancelOrder   = "/v5/trade/cancel_order"   // POST 撤单（交易）
	pathOpenPositions = "/v5/trade/position/opens" // GET 查询当前持仓（读取）
	pathBalance       = "/v5/account/balance"      // GET 查询账户余额（读取）
	pathAssetMode     = "/v5/account/asset_mode"   // GET 查询资产模式（读取）
	pathPositionMode  = "/v5/position/mode"        // GET 查询（读取）/ POST 设置持仓模式（交易）
	pathLever         = "/v5/position/lever"       // POST 设置杠杆等级（文档标注为“读取”）
)

// ---------------------------------------------------------------------------
// 宽松数值类型：火币 V5 返回中同一类字段可能是字符串、数字、空串或 null。
// ---------------------------------------------------------------------------

// Num 十进制数值。Valid=false 表示 null 或空串。
type Num struct {
	decimal.Decimal
	Valid bool
}

func (n *Num) UnmarshalJSON(b []byte) error {
	b = bytes.TrimSpace(b)
	if string(b) == "null" || string(b) == `""` {
		*n = Num{}
		return nil
	}
	if len(b) > 0 && b[0] == '"' {
		var s string
		if err := json.Unmarshal(b, &s); err != nil {
			return err
		}
		b = []byte(s)
	}
	d, err := decimal.NewFromString(string(b))
	if err != nil {
		return fmt.Errorf("htx: invalid number %q: %w", b, err)
	}
	*n = Num{Decimal: d, Valid: true}
	return nil
}

// Millis 毫秒时间戳（字符串或数字）。
type Millis struct{ time.Time }

func (m *Millis) UnmarshalJSON(b []byte) error {
	var n Num
	if err := n.UnmarshalJSON(b); err != nil {
		return err
	}
	if !n.Valid {
		m.Time = time.Time{}
		return nil
	}
	m.Time = time.UnixMilli(n.IntPart())
	return nil
}

// ---------------------------------------------------------------------------
// 枚举
// ---------------------------------------------------------------------------

const (
	MarginCross    = "cross"
	MarginIsolated = "isolated"

	SideBuy  = "buy"
	SideSell = "sell"

	OrderTypeMarket   = "market"
	OrderTypeLimit    = "limit"
	OrderTypePostOnly = "post_only"

	PositionModeSingle = "single_side"
	PositionModeDual   = "dual_side"

	// 订单状态（查询订单信息 state）
	OrderStateNew               = "new"
	OrderStatePartiallyFilled   = "partially_filled"
	OrderStateFilled            = "filled"
	OrderStatePartiallyCanceled = "partially_canceled"
	OrderStateCanceled          = "canceled"

	// 资产模式（查询资产模式 asset_mode）
	AssetModeSingleLegacy = 0 // 单币种保证金模式（老）
	AssetModeMulti        = 1 // 联合保证金模式
	AssetModeSingle       = 2 // 单币种保证金模式（新）
)

var ErrInvalidRequest = errors.New("htx: invalid request")

var channelCodePattern = regexp.MustCompile(`^[A-Za-z0-9]{1,32}$`)

// EnvSwapChannelCode 合约下单 Broker ID 的环境变量名。
const EnvSwapChannelCode = "HTX_SWAP_CHANNEL_CODE"

// SwapChannelCodeFromEnv 读取合约 Broker ID；格式非法时返回错误（不回显取值以外的信息）。
func SwapChannelCodeFromEnv() (string, error) {
	v := strings.TrimSpace(os.Getenv(EnvSwapChannelCode))
	if v != "" && !channelCodePattern.MatchString(v) {
		return "", fmt.Errorf("%w: %s must be 1-32 letters/digits", ErrInvalidRequest, EnvSwapChannelCode)
	}
	return v, nil
}

// ---------------------------------------------------------------------------
// 下单 POST /v5/trade/order
// ---------------------------------------------------------------------------

// SwapOrderRequest 下单请求（字段与文档《下单》一致）。
// 杠杆不在下单参数中，需事先通过 SetLeverage 设置。止盈止损参数本系统暂不使用，未列出。
type SwapOrderRequest struct {
	ContractCode     string `json:"contract_code"`
	MarginMode       string `json:"margin_mode"`             // cross / isolated
	PositionSide     string `json:"position_side,omitempty"` // 单向持仓下可不填（默认 both）
	Side             string `json:"side"`                    // buy / sell
	Type             string `json:"type"`                    // market / limit / post_only
	PriceMatch       string `json:"price_match,omitempty"`   // opponent / optimal_5 / optimal_10 / optimal_20，与 price 互斥
	ClientOrderID    string `json:"client_order_id,omitempty"`
	Price            string `json:"price,omitempty"`
	Volume           string `json:"volume"` // 张数
	ReduceOnly       int    `json:"reduce_only,omitempty"`
	TimeInForce      string `json:"time_in_force,omitempty"` // fok / ioc / gtc / rpi，默认 gtc
	SelfMatchPrevent string `json:"self_match_prevent,omitempty"`
	// ChannelCode 火币 Broker ID（返佣）。由 Client 依据 WithChannelCode 统一注入，业务层无需填写。
	// 位于 JSON body，POST 业务参数不参与签名。未收录于 docs/htx_api（老项目实测参数）。
	ChannelCode string `json:"channel_code,omitempty"`
}

// Validate 按文档约束做本地校验，避免无效请求消耗交易限频额度。
func (r SwapOrderRequest) Validate() error {
	fail := func(f string, a ...any) error { return fmt.Errorf("%w: %s", ErrInvalidRequest, fmt.Sprintf(f, a...)) }
	switch {
	case r.ContractCode == "":
		return fail("contract_code required")
	case r.MarginMode != MarginCross && r.MarginMode != MarginIsolated:
		return fail("margin_mode %q", r.MarginMode)
	case r.Side != SideBuy && r.Side != SideSell:
		return fail("side %q", r.Side)
	case r.Type != OrderTypeMarket && r.Type != OrderTypeLimit && r.Type != OrderTypePostOnly:
		return fail("type %q", r.Type)
	case r.PositionSide != "" && r.PositionSide != "both" && r.PositionSide != "long" && r.PositionSide != "short":
		return fail("position_side %q", r.PositionSide)
	case r.Price != "" && r.PriceMatch != "":
		return fail("price and price_match are mutually exclusive")
	case r.Type == OrderTypeMarket && r.Price != "":
		return fail("market order must not carry price")
	case r.Type != OrderTypeMarket && r.Price == "" && r.PriceMatch == "":
		return fail("%s order requires price or price_match", r.Type)
	case r.ReduceOnly != 0 && r.ReduceOnly != 1:
		return fail("reduce_only must be 0 or 1")
	case r.ChannelCode != "" && !channelCodePattern.MatchString(r.ChannelCode):
		return fail("channel_code %q must be 1-32 letters/digits", r.ChannelCode)
	}
	if v, err := decimal.NewFromString(r.Volume); err != nil || !v.IsPositive() || !v.IsInteger() {
		return fail("volume %q must be a positive integer", r.Volume)
	}
	if r.Price != "" {
		if p, err := decimal.NewFromString(r.Price); err != nil || !p.IsPositive() {
			return fail("price %q", r.Price)
		}
	}
	if r.ClientOrderID != "" {
		// 文档：用户自定义订单ID [1-9223372036854775807]
		if id, err := strconv.ParseInt(r.ClientOrderID, 10, 64); err != nil || id < 1 {
			return fail("client_order_id %q out of range [1, 9223372036854775807]", r.ClientOrderID)
		}
	}
	return nil
}

// SwapOrderResponse 下单响应。
type SwapOrderResponse struct {
	OrderID       string `json:"order_id"`
	ClientOrderID string `json:"client_order_id"` // 下单时未填写则不返回
}

// SwapPlaceOrder 下单。cred 必须是该用户专属子账户的凭证。
func (c *Client) SwapPlaceOrder(ctx context.Context, cred *Credentials, req SwapOrderRequest) (*SwapOrderResponse, error) {
	if req.ChannelCode == "" {
		req.ChannelCode = c.channelCode
	}
	if err := req.Validate(); err != nil {
		return nil, err
	}
	var out SwapOrderResponse
	if err := c.Do(ctx, http.MethodPost, pathOrder, BucketTrade, cred, nil, req, &out); err != nil {
		return nil, err
	}
	if out.OrderID == "" {
		return nil, fmt.Errorf("htx place order: empty order_id in response")
	}
	return &out, nil
}

// ---------------------------------------------------------------------------
// 撤单 POST /v5/trade/cancel_order
// ---------------------------------------------------------------------------

type SwapCancelRequest struct {
	ContractCode  string `json:"contract_code"`
	OrderID       string `json:"order_id,omitempty"`
	ClientOrderID string `json:"client_order_id,omitempty"`
}

// SwapCancelOrder 撤单。order_id 与 client_order_id 至少填一个（都填时以 order_id 为准）。
// 订单不存在时火币返回错误码 1061。
func (c *Client) SwapCancelOrder(ctx context.Context, cred *Credentials, req SwapCancelRequest) (*SwapOrderResponse, error) {
	if req.ContractCode == "" || (req.OrderID == "" && req.ClientOrderID == "") {
		return nil, fmt.Errorf("%w: contract_code and one of order_id/client_order_id required", ErrInvalidRequest)
	}
	var out SwapOrderResponse
	if err := c.Do(ctx, http.MethodPost, pathCancelOrder, BucketTrade, cred, nil, req, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// ---------------------------------------------------------------------------
// 查询订单信息 GET /v5/trade/order
// ---------------------------------------------------------------------------

// SwapOrder 订单信息（主要字段）。
type SwapOrder struct {
	ID               string `json:"id"`
	ContractCode     string `json:"contract_code"`
	Side             string `json:"side"`
	PositionSide     string `json:"position_side"`
	Type             string `json:"type"`
	PriceMatch       string `json:"price_match"`
	OrderID          string `json:"order_id"`
	ClientOrderID    string `json:"client_order_id"`
	MarginMode       string `json:"margin_mode"`
	Price            Num    `json:"price"`
	Volume           Num    `json:"volume"`
	LeverRate        Num    `json:"lever_rate"`
	State            string `json:"state"`
	OrderSource      string `json:"order_source"`
	ReduceOnly       bool   `json:"reduce_only"`
	TimeInForce      string `json:"time_in_force"`
	TradeAvgPrice    Num    `json:"trade_avg_price"`
	TradeVolume      Num    `json:"trade_volume"`
	TradeTurnover    Num    `json:"trade_turnover"`
	FeeCurrency      string `json:"fee_currency"`
	Fee              Num    `json:"fee"`
	Profit           Num    `json:"profit"`
	RealProfit       Num    `json:"real_profit"`
	ContractType     string `json:"contract_type"`
	CancelReason     string `json:"cancel_reason"`
	CancelVolume     Num    `json:"cancel_volume"`
	SelfMatchPrevent string `json:"self_match_prevent"`
	CreatedTime      Millis `json:"created_time"`
	UpdatedTime      Millis `json:"updated_time"`
}

// Final 订单是否已终结（不会再有成交）。
func (o *SwapOrder) Final() bool {
	switch o.State {
	case OrderStateFilled, OrderStateCanceled, OrderStatePartiallyCanceled:
		return true
	}
	return false
}

// SwapOrderInfo 查询订单。marginMode 为空时火币默认 cross。
func (c *Client) SwapOrderInfo(ctx context.Context, cred *Credentials, contractCode, marginMode, orderID, clientOrderID string) (*SwapOrder, error) {
	if contractCode == "" || (orderID == "" && clientOrderID == "") {
		return nil, fmt.Errorf("%w: contract_code and one of order_id/client_order_id required", ErrInvalidRequest)
	}
	q := url.Values{"contract_code": {contractCode}}
	if marginMode != "" {
		q.Set("margin_mode", marginMode)
	}
	if orderID != "" {
		q.Set("order_id", orderID)
	}
	if clientOrderID != "" {
		q.Set("client_order_id", clientOrderID)
	}
	var out SwapOrder
	if err := c.Do(ctx, http.MethodGet, pathOrder, BucketQuery, cred, q, nil, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// ---------------------------------------------------------------------------
// 查询当前持仓 GET /v5/trade/position/opens
// ---------------------------------------------------------------------------

type SwapPosition struct {
	ContractCode      string `json:"contract_code"`
	PositionSide      string `json:"position_side"`
	Direction         string `json:"direction"` // 单向持仓下：多仓 buy，空仓 sell
	MarginMode        string `json:"margin_mode"`
	OpenAvgPrice      Num    `json:"open_avg_price"`
	Volume            Num    `json:"volume"`
	Available         Num    `json:"available"`
	LeverRate         Num    `json:"lever_rate"`
	ADLRiskPercent    Num    `json:"adl_risk_percent"`
	LiquidationPrice  Num    `json:"liquidation_price"`
	InitialMargin     Num    `json:"initial_margin"`
	MaintenanceMargin Num    `json:"maintenance_margin"`
	Margin            Num    `json:"margin"` // 全仓：初始保证金；逐仓：仓位保证金
	ProfitUnreal      Num    `json:"profit_unreal"`
	ProfitRate        Num    `json:"profit_rate"`
	MarginRate        Num    `json:"margin_rate"`
	MarginCurrency    string `json:"margin_currency"`
	LastPrice         Num    `json:"last_price"`
	MarkPrice         Num    `json:"mark_price"`
	ContractType      string `json:"contract_type"`
	CreatedTime       Millis `json:"created_time"`
	UpdatedTime       Millis `json:"updated_time"`
}

// SwapOpenPositions 查询当前持仓；contractCode 为空表示全部合约。
func (c *Client) SwapOpenPositions(ctx context.Context, cred *Credentials, contractCode string) ([]SwapPosition, error) {
	q := url.Values{}
	if contractCode != "" {
		q.Set("contract_code", contractCode)
	}
	var out []SwapPosition
	if err := c.Do(ctx, http.MethodGet, pathOpenPositions, BucketQuery, cred, q, nil, &out); err != nil {
		return nil, err
	}
	return out, nil
}

// ---------------------------------------------------------------------------
// 账户 GET /v5/account/balance, GET /v5/account/asset_mode
// ---------------------------------------------------------------------------

type BalanceDetail struct {
	Currency              string `json:"currency"`
	Equity                Num    `json:"equity"`          // 全仓权益 = available + profit_unreal
	IsolatedEquity        Num    `json:"isolated_equity"` // 逐仓权益
	Available             Num    `json:"available"`       // 全仓余额
	IsolatedAvailable     Num    `json:"isolated_available"`
	WithdrawAvailable     Num    `json:"withdraw_available"`
	ProfitUnreal          Num    `json:"profit_unreal"`
	IsolatedProfitUnreal  Num    `json:"isolated_profit_unreal"`
	InitialMargin         Num    `json:"initial_margin"`
	MaintenanceMargin     Num    `json:"maintenance_margin"`
	MaintenanceMarginRate Num    `json:"maintenance_margin_rate"`
	InitialMarginRate     Num    `json:"initial_margin_rate"`
	Voucher               Num    `json:"voucher"`
	VoucherValue          Num    `json:"voucher_value"`
	CreatedTime           Millis `json:"created_time"`
	UpdatedTime           Millis `json:"updated_time"`
}

// AccountBalance 账户余额。账户级金额单位为 USD，适用于联合保证金模式。
type AccountBalance struct {
	State                 string          `json:"state"` // normal / liquidating / adl / open_limit
	Equity                Num             `json:"equity"`
	InitialMargin         Num             `json:"initial_margin"`
	MaintenanceMargin     Num             `json:"maintenance_margin"`
	MaintenanceMarginRate Num             `json:"maintenance_margin_rate"`
	ProfitUnreal          Num             `json:"profit_unreal"`
	AvailableMargin       Num             `json:"available_margin"`
	VoucherValue          Num             `json:"voucher_value"`
	CreatedTime           Millis          `json:"created_time"`
	UpdatedTime           Millis          `json:"updated_time"`
	Details               []BalanceDetail `json:"details"`
}

// Detail 返回某币种明细。
func (a *AccountBalance) Detail(currency string) (BalanceDetail, bool) {
	for _, d := range a.Details {
		if d.Currency == currency {
			return d, true
		}
	}
	return BalanceDetail{}, false
}

func (c *Client) AccountBalance(ctx context.Context, cred *Credentials) (*AccountBalance, error) {
	var out AccountBalance
	if err := c.Do(ctx, http.MethodGet, pathBalance, BucketQuery, cred, nil, nil, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// AssetMode 查询资产模式（0 单币种老 / 1 联合保证金 / 2 单币种新）。V5 接口适用于联合保证金账户。
func (c *Client) AssetMode(ctx context.Context, cred *Credentials) (int, error) {
	var out struct {
		AssetMode int `json:"asset_mode"`
	}
	if err := c.Do(ctx, http.MethodGet, pathAssetMode, BucketQuery, cred, nil, nil, &out); err != nil {
		return 0, err
	}
	return out.AssetMode, nil
}

// ---------------------------------------------------------------------------
// 持仓模式与杠杆 /v5/position/mode, /v5/position/lever
// ---------------------------------------------------------------------------

func (c *Client) PositionMode(ctx context.Context, cred *Credentials) (string, error) {
	var out struct {
		PositionMode string `json:"position_mode"`
	}
	if err := c.Do(ctx, http.MethodGet, pathPositionMode, BucketQuery, cred, nil, nil, &out); err != nil {
		return "", err
	}
	return out.PositionMode, nil
}

// SetPositionMode 设置持仓模式（本系统子账户统一为 single_side）。
func (c *Client) SetPositionMode(ctx context.Context, cred *Credentials, mode string) error {
	if mode != PositionModeSingle && mode != PositionModeDual {
		return fmt.Errorf("%w: position_mode %q", ErrInvalidRequest, mode)
	}
	var out struct {
		PositionMode string `json:"position_mode"`
	}
	if err := c.Do(ctx, http.MethodPost, pathPositionMode, BucketTrade, cred, nil,
		map[string]string{"position_mode": mode}, &out); err != nil {
		return err
	}
	if out.PositionMode != mode {
		return fmt.Errorf("htx set position mode: requested %s, got %q", mode, out.PositionMode)
	}
	return nil
}

// SetLeverage 设置合约杠杆。单向持仓模式下无需 position_side。
// 文档将该接口“接口权限”标注为读取，因此计入查询桶。
func (c *Client) SetLeverage(ctx context.Context, cred *Credentials, contractCode, marginMode string, lever int) error {
	if contractCode == "" || (marginMode != MarginCross && marginMode != MarginIsolated) || lever < 1 {
		return fmt.Errorf("%w: set lever %s/%s/%d", ErrInvalidRequest, contractCode, marginMode, lever)
	}
	body := map[string]string{
		"contract_code": contractCode,
		"margin_mode":   marginMode,
		"lever_rate":    strconv.Itoa(lever),
	}
	var out struct {
		LeverRate Num `json:"lever_rate"`
	}
	if err := c.Do(ctx, http.MethodPost, pathLever, BucketQuery, cred, nil, body, &out); err != nil {
		return err
	}
	if !out.LeverRate.Valid || out.LeverRate.IntPart() != int64(lever) {
		return fmt.Errorf("htx set lever: requested %d, got %s", lever, out.LeverRate.String())
	}
	return nil
}
