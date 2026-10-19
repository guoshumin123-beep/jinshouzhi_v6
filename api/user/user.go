// Package user 用户前台路由组。UserID 一律取自 JWT，绝不取自请求参数；
// 响应中不包含任何子账户凭证或内部路由信息。
package user

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"strconv"
	"time"

	"github.com/shopspring/decimal"

	"backend_v2/api/httpx"
	"backend_v2/api/middleware"
	"backend_v2/internal/gateway"
	"backend_v2/internal/subaccount"
	"backend_v2/internal/swap"
)

// SwapWallet 合约钱包划出（由 synthetic.Engine 实现，带全仓保证金风控）。
type SwapWallet interface {
	MaxTransferOut(ctx context.Context, userID int64, ccy string) (decimal.Decimal, error)
	TransferOut(ctx context.Context, userID int64, ccy string, amount decimal.Decimal, bizID string) error
}

// SubAccounts 子账户开户（由 subaccount.Service 实现）。
type SubAccounts interface {
	Status(ctx context.Context, userID int64) (*subaccount.Account, error)
	Provision(ctx context.Context, userID int64) (*subaccount.Account, error)
}

// Deps User 服务依赖。
type Deps struct {
	Secret      []byte
	Router      *gateway.Router
	Profiles    *gateway.ProfileStore
	Wallet      SwapWallet
	SubAccounts SubAccounts
	Health      http.Handler       // /health（免鉴权）
	Limiter     middleware.Limiter // 接口限流（预留，nil 为放行）
	Logger      *slog.Logger
}

type Handler struct {
	router   *gateway.Router
	profiles *gateway.ProfileStore
	wallet   SwapWallet
	subs     SubAccounts
	logger   *slog.Logger
}

// NewServer 构建 User HTTP 服务：Recover → /health → JWT(role=user) → 限流 → 路由。
func NewServer(d Deps) http.Handler {
	h := &Handler{router: d.Router, profiles: d.Profiles, wallet: d.Wallet, subs: d.SubAccounts,
		logger: d.Logger.With(slog.String("component", "user_api"))}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/v1/me/trade-profile", h.getProfile)
	mux.HandleFunc("PUT /api/v1/me/trade-profile/margin-mode", h.setMarginMode)
	mux.HandleFunc("GET /api/v1/me/subaccount", h.subAccountStatus)
	mux.HandleFunc("POST /api/v1/me/subaccount", h.openSubAccount)
	mux.HandleFunc("POST /api/v1/swap/orders", h.placeOrder)
	mux.HandleFunc("DELETE /api/v1/swap/orders/{client_order_id}", h.cancelOrder)
	mux.HandleFunc("GET /api/v1/swap/wallet/{currency}/transferable", h.transferable)
	mux.HandleFunc("POST /api/v1/swap/wallet/transfer-out", h.transferOut)
	return middleware.Server(d.Logger, d.Health, d.Secret, middleware.RoleUser, d.Limiter, mux)
}

// subAccountDTO 用户可见的开户状态（不含子账户 UID、用户名、Key 等任何内部信息）。
type subAccountDTO struct {
	State string `json:"state"` // NONE / PROVISIONING / READY / LOCKED / FAILED
	Ready bool   `json:"ready"`
}

func (h *Handler) subAccountStatus(w http.ResponseWriter, r *http.Request) {
	a, err := h.subs.Status(r.Context(), me(r))
	if errors.Is(err, subaccount.ErrNoSubAccount) {
		httpx.JSON(w, http.StatusOK, subAccountDTO{State: "NONE"})
		return
	}
	if err != nil {
		httpx.DomainError(w, err)
		return
	}
	httpx.JSON(w, http.StatusOK, subAccountDTO{State: string(a.State), Ready: a.State == subaccount.StateReady})
}

// openSubAccount 开户（幂等）：200 已就绪；202 处理中（稍后查询）；502 开户失败（需客服处理）。
func (h *Handler) openSubAccount(w http.ResponseWriter, r *http.Request) {
	a, err := h.subs.Provision(r.Context(), me(r))
	switch {
	case a != nil && a.State == subaccount.StateReady:
		httpx.JSON(w, http.StatusOK, subAccountDTO{State: string(a.State), Ready: true})
	case a != nil && a.State == subaccount.StateProvisioning:
		httpx.JSON(w, http.StatusAccepted, subAccountDTO{State: string(a.State)})
	case a != nil && a.State == subaccount.StateFailed:
		h.logger.Error("sub-account opening failed", slog.Int64("user_id", me(r)), slog.String("error", errString(err)))
		httpx.Error(w, http.StatusBadGateway, "SUBACCOUNT_PROVISION_FAILED", "account opening failed, please contact support")
	case a != nil && a.State == subaccount.StateLocked:
		httpx.DomainError(w, subaccount.ErrSubAccountLocked)
	default:
		httpx.DomainError(w, err)
	}
}

func errString(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

func me(r *http.Request) int64 {
	p, _ := middleware.PrincipalFrom(r.Context())
	return p.UserID
}

func (h *Handler) getProfile(w http.ResponseWriter, r *http.Request) {
	p, err := h.profiles.Lookup(r.Context(), me(r))
	if err != nil {
		httpx.DomainError(w, err)
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]string{"margin_mode": string(p.MarginMode)})
}

func (h *Handler) setMarginMode(w http.ResponseWriter, r *http.Request) {
	var body struct {
		MarginMode string `json:"margin_mode"`
	}
	if !httpx.Decode(w, r, &body) {
		return
	}
	if err := h.profiles.SetMarginMode(r.Context(), me(r), swap.MarginMode(body.MarginMode)); err != nil {
		httpx.DomainError(w, err)
		return
	}
	h.getProfile(w, r)
}

type placeOrderBody struct {
	ClientOrderID string          `json:"client_order_id"`
	ContractCode  string          `json:"contract_code"`
	MarginMode    string          `json:"margin_mode,omitempty"`
	Side          string          `json:"side"`
	OrderType     string          `json:"order_type"`
	Volume        decimal.Decimal `json:"volume"`
	Price         decimal.Decimal `json:"price,omitempty"`
	Leverage      int             `json:"leverage"`
	ReduceOnly    bool            `json:"reduce_only,omitempty"`
}

// OrderDTO 对用户可见的订单字段。
type OrderDTO struct {
	ID            uint64    `json:"id"`
	ClientOrderID string    `json:"client_order_id"`
	ContractCode  string    `json:"contract_code"`
	MarginMode    string    `json:"margin_mode"`
	Side          string    `json:"side"`
	OrderType     string    `json:"order_type"`
	ReduceOnly    bool      `json:"reduce_only"`
	Volume        string    `json:"volume"`
	Price         string    `json:"price"`
	Leverage      int       `json:"leverage"`
	FilledVolume  string    `json:"filled_volume"`
	AvgFillPrice  string    `json:"avg_fill_price"`
	FeePaid       string    `json:"fee_paid"`
	RealizedPnL   string    `json:"realized_pnl"`
	Status        string    `json:"status"`
	CreatedAt     time.Time `json:"created_at"`
}

func toDTO(o *swap.Order) OrderDTO {
	return OrderDTO{
		ID: o.ID, ClientOrderID: o.ClientOrderID, ContractCode: o.ContractCode, MarginMode: string(o.MarginMode),
		Side: string(o.Side), OrderType: string(o.OrderType), ReduceOnly: o.ReduceOnly, Volume: o.Volume.String(),
		Price: o.Price.String(), Leverage: o.Leverage, FilledVolume: o.FilledVolume.String(),
		AvgFillPrice: o.AvgFillPrice.String(), FeePaid: o.FeePaid.String(), RealizedPnL: o.RealizedPnL.String(),
		Status: string(o.Status), CreatedAt: o.CreatedAt,
	}
}

func (h *Handler) placeOrder(w http.ResponseWriter, r *http.Request) {
	var b placeOrderBody
	if !httpx.Decode(w, r, &b) {
		return
	}
	o, err := h.router.PlaceOrder(r.Context(), gateway.PlaceOrderRequest{
		UserID: me(r), ClientOrderID: b.ClientOrderID, ContractCode: b.ContractCode,
		MarginMode: swap.MarginMode(b.MarginMode), Side: swap.Side(b.Side), OrderType: swap.OrderType(b.OrderType),
		Volume: b.Volume, Price: b.Price, Leverage: b.Leverage, ReduceOnly: b.ReduceOnly,
	})
	if err != nil {
		if o != nil { // 已受理但提交通道失败（订单已标记为 REJECTED）
			httpx.JSON(w, http.StatusOK, toDTO(o))
			return
		}
		httpx.DomainError(w, err)
		return
	}
	httpx.JSON(w, http.StatusOK, toDTO(o))
}

func (h *Handler) cancelOrder(w http.ResponseWriter, r *http.Request) {
	o, err := h.router.CancelOrder(r.Context(), me(r), r.PathValue("client_order_id"))
	if err != nil {
		httpx.DomainError(w, err)
		return
	}
	httpx.JSON(w, http.StatusOK, toDTO(o))
}

func (h *Handler) transferable(w http.ResponseWriter, r *http.Request) {
	amt, err := h.wallet.MaxTransferOut(r.Context(), me(r), r.PathValue("currency"))
	if err != nil {
		httpx.DomainError(w, err)
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]string{"currency": r.PathValue("currency"), "amount": amt.String()})
}

func (h *Handler) transferOut(w http.ResponseWriter, r *http.Request) {
	var b struct {
		RequestID string          `json:"request_id"`
		Currency  string          `json:"currency"`
		Amount    decimal.Decimal `json:"amount"`
	}
	if !httpx.Decode(w, r, &b) {
		return
	}
	if b.RequestID == "" || len(b.RequestID) > 64 || b.Currency == "" {
		httpx.Error(w, http.StatusBadRequest, "BAD_REQUEST", "request_id and currency are required")
		return
	}
	bizID := "user-transfer-out:" + itoa(me(r)) + ":" + b.RequestID
	if err := h.wallet.TransferOut(r.Context(), me(r), b.Currency, b.Amount, bizID); err != nil {
		httpx.DomainError(w, err)
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]string{"status": "OK"})
}

func itoa(v int64) string { return strconv.FormatInt(v, 10) }
