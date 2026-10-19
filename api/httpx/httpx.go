// Package httpx JSON 响应与错误映射工具。
package httpx

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"backend_v2/internal/dealer"
	"backend_v2/internal/gateway"
	"backend_v2/internal/gateway/synthetic"
	"backend_v2/internal/subaccount"
	"backend_v2/internal/swap"
)

type errorBody struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

func JSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func Error(w http.ResponseWriter, status int, code, msg string) {
	JSON(w, status, errorBody{Code: code, Message: msg})
}

// Decode 严格解析 JSON 请求体（拒绝未知字段，限制 64KB）。
func Decode(w http.ResponseWriter, r *http.Request, v any) bool {
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		Error(w, http.StatusBadRequest, "BAD_REQUEST", "invalid JSON body")
		return false
	}
	return true
}

// DomainError 把领域错误映射为 HTTP 响应；未识别的错误统一返回 500 且不泄露内部细节。
func DomainError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, subaccount.ErrNoSubAccount), errors.Is(err, subaccount.ErrSubAccountNotReady):
		// 前端据此强制引导完成子账户开通
		Error(w, http.StatusForbidden, "SUBACCOUNT_NOT_READY", "trading account is not ready")
	case errors.Is(err, subaccount.ErrSubAccountLocked):
		Error(w, http.StatusForbidden, "SUBACCOUNT_LOCKED", "trading account is locked")
	case errors.Is(err, subaccount.ErrProvisioningDisabled):
		Error(w, http.StatusServiceUnavailable, "PROVISIONING_DISABLED", "account opening is not available")
	case errors.Is(err, subaccount.ErrProvisionInProgress):
		Error(w, http.StatusConflict, "PROVISIONING_IN_PROGRESS", "account opening is in progress")
	case errors.Is(err, synthetic.ErrInsufficientMargin), errors.Is(err, dealer.ErrInsufficientBalance):
		Error(w, http.StatusBadRequest, "INSUFFICIENT_MARGIN", "insufficient available margin")
	case errors.Is(err, synthetic.ErrLeverageTooHigh):
		Error(w, http.StatusBadRequest, "LEVERAGE_TOO_HIGH", "leverage exceeds risk limit")
	case errors.Is(err, synthetic.ErrReduceOnly):
		Error(w, http.StatusBadRequest, "REDUCE_ONLY", "reduce-only order would open a position")
	case errors.Is(err, synthetic.ErrNoLiquidity):
		Error(w, http.StatusBadRequest, "NO_LIQUIDITY", "no liquidity within price band")
	case errors.Is(err, synthetic.ErrStaleQuote), errors.Is(err, synthetic.ErrStaleMark):
		Error(w, http.StatusServiceUnavailable, "MARKET_UNAVAILABLE", "market data temporarily unavailable")
	case errors.Is(err, dealer.ErrIdempotencyConflict):
		Error(w, http.StatusConflict, "IDEMPOTENCY_CONFLICT", "request id reused with different parameters")
	case errors.Is(err, swap.ErrNotFound):
		Error(w, http.StatusNotFound, "NOT_FOUND", "not found")
	case errors.Is(err, gateway.ErrLeverageMismatch):
		Error(w, http.StatusBadRequest, "LEVERAGE_MISMATCH", "leverage must match existing position or open orders")
	case errors.Is(err, gateway.ErrOrderNotCancelable):
		Error(w, http.StatusConflict, "NOT_CANCELABLE", "order is not cancelable")
	case errors.Is(err, gateway.ErrInvalidRequest), errors.Is(err, synthetic.ErrUnknownContract),
		errors.Is(err, subaccount.ErrInvalidRequest):
		Error(w, http.StatusBadRequest, "BAD_REQUEST", "invalid request")
	default:
		Error(w, http.StatusInternalServerError, "INTERNAL", "internal error")
	}
}

// Pinger 健康检查依赖（*sql.DB 实现）。
type Pinger interface {
	PingContext(ctx context.Context) error
}

// Health /health：探测数据库连通性，不需要鉴权，不暴露任何内部细节。
func Health(db Pinger) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
		defer cancel()
		if err := db.PingContext(ctx); err != nil {
			JSON(w, http.StatusServiceUnavailable, map[string]string{"status": "degraded", "db": "unreachable"})
			return
		}
		JSON(w, http.StatusOK, map[string]string{"status": "ok", "db": "ok"})
	})
}
