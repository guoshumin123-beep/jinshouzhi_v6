package gateway

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"

	"backend_v2/internal/storage"
	"backend_v2/internal/swap"
)

// Router 合约订单动态路由器。
type Router struct {
	db        *sql.DB
	profiles  *ProfileStore
	executors map[swap.Route]Executor
	logger    *slog.Logger
}

func NewRouter(db *sql.DB, profiles *ProfileStore, logger *slog.Logger, executors ...Executor) *Router {
	if logger == nil {
		logger = slog.Default()
	}
	m := make(map[swap.Route]Executor, len(executors))
	for _, e := range executors {
		m[e.Route()] = e
	}
	return &Router{db: db, profiles: profiles, executors: m, logger: logger.With(slog.String("component", "gateway"))}
}

// PlaceOrder 路由并受理一笔合约订单。
//
// 事务内：幂等检查 → 用户锁 → 确定保证金模式 → 查槽位持仓/在途订单 → 判定并固化路由
// → 订单落库 → executor.Accept（Synthetic：冻结/撮合/平反手/记账；Standard：校验子账户）→ 回写订单 → COMMIT。
// 事务外：executor.Submit（Standard 调火币）。
//
// 相同 (UserID, ClientOrderID) 的重复请求返回首次受理的订单。
func (r *Router) PlaceOrder(ctx context.Context, req PlaceOrderRequest) (*swap.Order, error) {
	if err := validateRequest(req); err != nil {
		return nil, err
	}

	var (
		order   *swap.Order
		created bool
	)
	err := storage.WithTx(ctx, r.db, func(tx *sql.Tx) error {
		order, created = nil, false // WithTx 死锁重试时重置

		existing, err := swap.FindOrderByClientID(ctx, tx, req.UserID, req.ClientOrderID)
		if err != nil {
			return err
		}
		if existing != nil {
			order = existing
			return nil
		}
		if err := swap.LockUser(ctx, tx, req.UserID); err != nil {
			return err
		}
		profile, err := r.profiles.Get(ctx, tx, req.UserID)
		if err != nil {
			return err
		}
		mode := req.MarginMode
		if mode == "" {
			mode = profile.MarginMode
		}

		pos, err := swap.FindOpenPosition(ctx, tx, req.UserID, req.ContractCode, mode)
		if err != nil {
			return err
		}
		active, err := swap.ListActiveOrders(ctx, tx, req.UserID, req.ContractCode, mode)
		if err != nil {
			return err
		}
		route, err := resolveRoute(req, pos, active, profile)
		if err != nil {
			return err
		}
		ex, ok := r.executors[route]
		if !ok {
			return fmt.Errorf("%w: %s", ErrNoExecutor, route)
		}

		o := &swap.Order{
			UserID:        req.UserID,
			ClientOrderID: req.ClientOrderID,
			ContractCode:  req.ContractCode,
			MarginMode:    mode,
			Route:         route,
			Side:          req.Side,
			OrderType:     req.OrderType,
			ReduceOnly:    req.ReduceOnly,
			Source:        swap.SourceUser,
			Volume:        req.Volume,
			Price:         req.Price,
			Leverage:      req.Leverage,
			Status:        swap.OrderNew,
		}
		if err := swap.InsertOrder(ctx, tx, o); err != nil {
			return err
		}
		if err := ex.Accept(ctx, tx, o, pos); err != nil {
			return fmt.Errorf("gateway accept %s order %d/%s: %w", route, req.UserID, req.ClientOrderID, err)
		}
		if o.Status == swap.OrderNew {
			return fmt.Errorf("gateway: executor %s left order %d in NEW state", route, o.ID)
		}
		if err := swap.UpdateOrder(ctx, tx, o); err != nil {
			return err
		}
		order, created = o, true
		return nil
	})
	if storage.IsDupEntry(err) {
		if existing, ferr := swap.FindOrderByClientID(ctx, r.db, req.UserID, req.ClientOrderID); ferr == nil && existing != nil {
			return existing, nil
		}
	}
	if err != nil {
		r.logger.Warn("order rejected",
			slog.Int64("user_id", req.UserID), slog.String("client_order_id", req.ClientOrderID),
			slog.String("contract", req.ContractCode), slog.String("error", err.Error()))
		return nil, err
	}
	if !created {
		return order, nil
	}

	r.logger.Info("order accepted",
		slog.Uint64("order_id", order.ID), slog.Int64("user_id", order.UserID),
		slog.String("contract", order.ContractCode), slog.String("route", string(order.Route)),
		slog.String("margin_mode", string(order.MarginMode)), slog.String("status", string(order.Status)),
		slog.String("filled", order.FilledVolume.String()), slog.String("fee", order.FeePaid.String()),
		slog.String("realized_pnl", order.RealizedPnL.String()), slog.String("frozen", order.FrozenAmount.String()))

	if order.Status != swap.OrderPendingSubmit {
		return order, nil
	}
	if err := r.executors[order.Route].Submit(ctx, order); err != nil {
		return order, fmt.Errorf("gateway submit order %d: %w", order.ID, err)
	}
	return order, nil
}

// CancelOrder 撤销用户自己的在途订单。
func (r *Router) CancelOrder(ctx context.Context, userID int64, clientOrderID string) (*swap.Order, error) {
	var order *swap.Order
	err := storage.WithTx(ctx, r.db, func(tx *sql.Tx) error {
		if err := swap.LockUser(ctx, tx, userID); err != nil {
			return err
		}
		o, err := swap.FindOrderByClientID(ctx, tx, userID, clientOrderID)
		if err != nil {
			return err
		}
		if o == nil {
			return fmt.Errorf("order %d/%s: %w", userID, clientOrderID, swap.ErrNotFound)
		}
		if !o.Status.Active() {
			return fmt.Errorf("%w: order %d status %s", ErrOrderNotCancelable, o.ID, o.Status)
		}
		ex, ok := r.executors[o.Route]
		if !ok {
			return fmt.Errorf("%w: %s", ErrNoExecutor, o.Route)
		}
		if err := ex.Cancel(ctx, tx, o); err != nil {
			return fmt.Errorf("gateway cancel order %d: %w", o.ID, err)
		}
		if err := swap.UpdateOrder(ctx, tx, o); err != nil {
			return err
		}
		order = o
		return nil
	})
	if err != nil {
		return nil, err
	}
	r.logger.Info("order canceled", slog.Uint64("order_id", order.ID), slog.Int64("user_id", userID),
		slog.String("route", string(order.Route)), slog.String("status", string(order.Status)))
	return order, nil
}

// resolveRoute 判定路由（必须在用户锁内调用）。
func resolveRoute(req PlaceOrderRequest, pos *swap.Position, active []*swap.Order, profile swap.Profile) (swap.Route, error) {
	if pos != nil {
		if req.Leverage != pos.Leverage {
			return "", fmt.Errorf("%w: position %d leverage %d, order %d", ErrLeverageMismatch, pos.ID, pos.Leverage, req.Leverage)
		}
		return pos.Route, nil
	}
	if len(active) > 0 {
		if req.Leverage != active[0].Leverage {
			return "", fmt.Errorf("%w: active order %d leverage %d, order %d", ErrLeverageMismatch, active[0].ID, active[0].Leverage, req.Leverage)
		}
		return active[0].Route, nil
	}
	return profile.Route, nil
}

func validateRequest(req PlaceOrderRequest) error {
	switch {
	case req.UserID <= 0:
		return fmt.Errorf("%w: user_id", ErrInvalidRequest)
	case req.ClientOrderID == "" || len(req.ClientOrderID) > 64:
		return fmt.Errorf("%w: client_order_id length must be 1..64", ErrInvalidRequest)
	case req.ContractCode == "" || len(req.ContractCode) > 32:
		return fmt.Errorf("%w: contract_code", ErrInvalidRequest)
	case req.MarginMode != "" && !req.MarginMode.Valid():
		return fmt.Errorf("%w: margin_mode %q", ErrInvalidRequest, req.MarginMode)
	case !req.Side.Valid():
		return fmt.Errorf("%w: side %q", ErrInvalidRequest, req.Side)
	case req.OrderType != swap.OrderMarket && req.OrderType != swap.OrderLimit:
		return fmt.Errorf("%w: order_type %q", ErrInvalidRequest, req.OrderType)
	case !req.Volume.IsPositive() || !req.Volume.IsInteger():
		return fmt.Errorf("%w: volume must be a positive integer number of contracts", ErrInvalidRequest)
	case req.OrderType == swap.OrderLimit && !req.Price.IsPositive():
		return fmt.Errorf("%w: limit order requires price", ErrInvalidRequest)
	case req.OrderType == swap.OrderMarket && !req.Price.IsZero():
		return fmt.Errorf("%w: market order must not carry price", ErrInvalidRequest)
	case req.Leverage < 1 || req.Leverage > 200:
		return fmt.Errorf("%w: leverage %d", ErrInvalidRequest, req.Leverage)
	}
	return nil
}

// IsClientError 报告 err 是否属于可直接反馈给用户的请求错误。
func IsClientError(err error) bool {
	return errors.Is(err, ErrInvalidRequest) || errors.Is(err, ErrLeverageMismatch) ||
		errors.Is(err, ErrOrderNotCancelable) || errors.Is(err, swap.ErrNotFound)
}
