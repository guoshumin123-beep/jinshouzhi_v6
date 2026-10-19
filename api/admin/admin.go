// Package admin Admin 后台路由组。与 User 路由组使用不同的 ServeMux、不同的 JWT 密钥，部署在不同端口。
package admin

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"strconv"

	"backend_v2/api/httpx"
	"backend_v2/api/middleware"
	"backend_v2/internal/gateway"
	"backend_v2/internal/subaccount"
	"backend_v2/internal/swap"
)

// SubAccounts 子账户管理（由 subaccount.Service 实现）。
type SubAccounts interface {
	Status(ctx context.Context, userID int64) (*subaccount.Account, error)
	Provision(ctx context.Context, userID int64) (*subaccount.Account, error)
	Lock(ctx context.Context, userID int64, operator, reason string) error
	Unlock(ctx context.Context, userID int64, operator string) error
}

// Deps Admin 服务依赖。
type Deps struct {
	Secret      []byte
	Profiles    *gateway.ProfileStore
	SubAccounts SubAccounts
	Health      http.Handler
	Limiter     middleware.Limiter
	Logger      *slog.Logger
}

type Handler struct {
	profiles *gateway.ProfileStore
	subs     SubAccounts
	logger   *slog.Logger
}

// NewServer 构建 Admin HTTP 服务：Recover → /health → JWT(role=admin) → 限流 → 路由。
func NewServer(d Deps) http.Handler {
	h := &Handler{profiles: d.Profiles, subs: d.SubAccounts, logger: d.Logger.With(slog.String("component", "admin_api"))}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /admin/v1/users/{id}/trade-profile", h.getProfile)
	mux.HandleFunc("PUT /admin/v1/users/{id}/trade-profile/route", h.setRoute)
	mux.HandleFunc("GET /admin/v1/users/{id}/subaccount", h.subAccount)
	mux.HandleFunc("POST /admin/v1/users/{id}/subaccount/provision", h.provision)
	mux.HandleFunc("POST /admin/v1/users/{id}/subaccount/lock", h.lock)
	mux.HandleFunc("POST /admin/v1/users/{id}/subaccount/unlock", h.unlock)
	return middleware.Server(d.Logger, d.Health, d.Secret, middleware.RoleAdmin, d.Limiter, mux)
}

// subAccountDTO Admin 可见的子账户状态（不含任何 API Key 信息）。
type subAccountDTO struct {
	UserID    int64  `json:"user_id"`
	SubUID    int64  `json:"sub_uid,omitempty"`
	UserName  string `json:"user_name"`
	State     string `json:"state"`
	Step      string `json:"step"`
	Attempts  int    `json:"attempts"`
	LastError string `json:"last_error,omitempty"`
}

func toSubDTO(a *subaccount.Account) subAccountDTO {
	return subAccountDTO{UserID: a.UserID, SubUID: a.SubUID, UserName: a.UserName, State: string(a.State),
		Step: string(a.Step), Attempts: a.Attempts, LastError: a.LastError}
}

func (h *Handler) subAccount(w http.ResponseWriter, r *http.Request) {
	id, ok := userID(w, r)
	if !ok {
		return
	}
	a, err := h.subs.Status(r.Context(), id)
	if errors.Is(err, subaccount.ErrNoSubAccount) {
		httpx.Error(w, http.StatusNotFound, "NOT_FOUND", "user has no sub-account")
		return
	}
	if err != nil {
		httpx.DomainError(w, err)
		return
	}
	httpx.JSON(w, http.StatusOK, toSubDTO(a))
}

// provision 人工触发开户或重试（含 FAILED 账户）。
func (h *Handler) provision(w http.ResponseWriter, r *http.Request) {
	id, ok := userID(w, r)
	if !ok {
		return
	}
	operator, _ := middleware.PrincipalFrom(r.Context())
	a, err := h.subs.Provision(r.Context(), id)
	h.logger.Info("admin triggered sub-account provisioning", slog.Int64("user_id", id), slog.String("operator", operator.Subject),
		slog.Bool("ok", err == nil))
	if a != nil {
		status := http.StatusOK
		if a.State == subaccount.StateProvisioning {
			status = http.StatusAccepted
		} else if a.State == subaccount.StateFailed {
			status = http.StatusBadGateway
		}
		httpx.JSON(w, status, toSubDTO(a))
		return
	}
	httpx.DomainError(w, err)
}

func (h *Handler) lock(w http.ResponseWriter, r *http.Request) {
	id, ok := userID(w, r)
	if !ok {
		return
	}
	var body struct {
		Reason string `json:"reason"`
	}
	if !httpx.Decode(w, r, &body) {
		return
	}
	operator, _ := middleware.PrincipalFrom(r.Context())
	if err := h.subs.Lock(r.Context(), id, operator.Subject, body.Reason); err != nil {
		httpx.DomainError(w, err)
		return
	}
	h.subAccount(w, r)
}

func (h *Handler) unlock(w http.ResponseWriter, r *http.Request) {
	id, ok := userID(w, r)
	if !ok {
		return
	}
	operator, _ := middleware.PrincipalFrom(r.Context())
	if err := h.subs.Unlock(r.Context(), id, operator.Subject); err != nil {
		httpx.DomainError(w, err)
		return
	}
	h.subAccount(w, r)
}

type profileDTO struct {
	UserID     int64  `json:"user_id"`
	Route      string `json:"route"`
	MarginMode string `json:"margin_mode"`
}

func userID(w http.ResponseWriter, r *http.Request) (int64, bool) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id <= 0 {
		httpx.Error(w, http.StatusBadRequest, "BAD_REQUEST", "invalid user id")
		return 0, false
	}
	return id, true
}

func (h *Handler) getProfile(w http.ResponseWriter, r *http.Request) {
	id, ok := userID(w, r)
	if !ok {
		return
	}
	p, err := h.profiles.Lookup(r.Context(), id)
	if err != nil {
		httpx.DomainError(w, err)
		return
	}
	httpx.JSON(w, http.StatusOK, profileDTO{UserID: id, Route: string(p.Route), MarginMode: string(p.MarginMode)})
}

// setRoute 切换用户路由画像（STANDARD / SYNTHETIC）。只影响未来新开仓；
// 已有 OPEN 持仓及其在途订单在生命周期内保持原路由。
func (h *Handler) setRoute(w http.ResponseWriter, r *http.Request) {
	id, ok := userID(w, r)
	if !ok {
		return
	}
	var body struct {
		Route string `json:"route"`
	}
	if !httpx.Decode(w, r, &body) {
		return
	}
	route := swap.Route(body.Route)
	if !route.Valid() {
		httpx.Error(w, http.StatusBadRequest, "BAD_REQUEST", "route must be STANDARD or SYNTHETIC")
		return
	}
	operator, _ := middleware.PrincipalFrom(r.Context())
	if err := h.profiles.SetRoute(r.Context(), id, route, operator.Subject); err != nil {
		httpx.DomainError(w, err)
		return
	}
	h.getProfile(w, r)
}
