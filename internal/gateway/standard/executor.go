// Package standard DMA 执行器：订单透传至该用户专属的火币子账户。
//
// 铁律：每笔请求必须使用 subaccount.KeyProvider 返回的该用户子账户凭证；
// 没有绑定即拒单，绝不回退到全局母账户。
package standard

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"sync"

	"backend_v2/internal/gateway"
	"backend_v2/internal/htx"
	"backend_v2/internal/storage"
	"backend_v2/internal/subaccount"
	"backend_v2/internal/swap"
)

// Trader 火币 U 本位联合保证金 V5 交易接口，由 *htx.Client 实现。
type Trader interface {
	SetLeverage(ctx context.Context, cred *htx.Credentials, contractCode, marginMode string, lever int) error
	SwapPlaceOrder(ctx context.Context, cred *htx.Credentials, req htx.SwapOrderRequest) (*htx.SwapOrderResponse, error)
}

var _ Trader = (*htx.Client)(nil)

// ErrSubmittedCancelPending 已提交火币的订单撤单需经 outbox 调用火币撤单接口（下一阶段接入）。
var ErrSubmittedCancelPending = errors.New("standard: cancel of HTX-submitted order requires HTX cancel adapter")

type Executor struct {
	db     *sql.DB
	keys   subaccount.KeyProvider
	trader Trader
	logger *slog.Logger

	// levers 已在火币侧生效的杠杆缓存：(子账户, 合约, 保证金模式) -> 杠杆。
	// V5 下单参数不含杠杆，必须先调用设置杠杆接口；缓存命中时跳过，节省限频额度。
	// 子账户由平台托管、杠杆只经由本系统修改，因此缓存与火币侧保持一致；进程重启后首次下单会重新设置。
	levers sync.Map
}

func NewExecutor(db *sql.DB, keys subaccount.KeyProvider, trader Trader, logger *slog.Logger) *Executor {
	if logger == nil {
		logger = slog.Default()
	}
	return &Executor{db: db, keys: keys, trader: trader, logger: logger.With(slog.String("component", "gateway_standard"))}
}

var _ gateway.Executor = (*Executor)(nil)

func (e *Executor) Route() swap.Route { return swap.RouteStandard }

// Accept 用户必须有可用的子账户绑定（未绑定不允许交易），否则整个事务回滚。
// Standard 资金常驻火币子账户（本地 HTX_CUSTODY 为镜像），保证金由火币冻结，此处不调用 dealer；
// 持仓在火币成交回报同步时建立/更新（平反手由火币单向持仓原生完成，本地镜像随之关闭旧仓、新建反向仓）。
func (e *Executor) Accept(ctx context.Context, _ *sql.Tx, o *swap.Order, _ *swap.Position) error {
	if _, err := e.keys.Get(ctx, o.UserID); err != nil {
		return fmt.Errorf("resolve sub-account for user %d: %w", o.UserID, err)
	}
	o.Status = swap.OrderPendingSubmit
	return nil
}

// Cancel 尚未提交火币的订单可直接本地撤销。
func (e *Executor) Cancel(_ context.Context, _ *sql.Tx, o *swap.Order) error {
	if o.Status == swap.OrderPendingSubmit {
		o.Status = swap.OrderCanceled
		return nil
	}
	return ErrSubmittedCancelPending
}

// Submit 事务提交后，提取该用户专属子账户凭证：先确保杠杆，再提交至火币，随后回写订单状态。
func (e *Executor) Submit(ctx context.Context, o *swap.Order) error {
	cred, err := e.keys.Get(ctx, o.UserID)
	if err == nil {
		err = e.ensureLeverage(ctx, cred, o)
	}
	if err == nil {
		var resp *htx.SwapOrderResponse
		resp, err = e.trader.SwapPlaceOrder(ctx, cred, BuildHTXOrder(o))
		if err == nil {
			e.logger.Info("htx order submitted",
				slog.Uint64("order_id", o.ID), slog.Int64("user_id", o.UserID),
				slog.Any("credential", cred), slog.String("htx_order_id", resp.OrderID))
			return e.setStatus(ctx, o, swap.OrderSubmitted, "")
		}
		e.logger.Warn("htx submit failed",
			slog.Uint64("order_id", o.ID), slog.Int64("user_id", o.UserID),
			slog.Any("credential", cred), slog.String("error", err.Error())) // credential 的 LogValue 已脱敏
	}
	if serr := e.setStatus(ctx, o, swap.OrderRejected, err.Error()); serr != nil {
		e.logger.Error("mark order rejected failed", slog.Uint64("order_id", o.ID), slog.String("error", serr.Error()))
	}
	return err
}

func (e *Executor) ensureLeverage(ctx context.Context, cred *htx.Credentials, o *swap.Order) error {
	mode := htxMarginMode(o.MarginMode)
	key := cred.AccessKey + "|" + o.ContractCode + "|" + mode
	if v, ok := e.levers.Load(key); ok && v.(int) == o.Leverage {
		return nil
	}
	if err := e.trader.SetLeverage(ctx, cred, o.ContractCode, mode, o.Leverage); err != nil {
		e.levers.Delete(key)
		return fmt.Errorf("set leverage %s/%s/%d: %w", o.ContractCode, mode, o.Leverage, err)
	}
	e.levers.Store(key, o.Leverage)
	return nil
}

func htxMarginMode(m swap.MarginMode) string {
	if m == swap.MarginIsolated {
		return htx.MarginIsolated
	}
	return htx.MarginCross
}

func (e *Executor) setStatus(ctx context.Context, o *swap.Order, st swap.OrderStatus, msg string) error {
	return storage.WithTx(ctx, e.db, func(tx *sql.Tx) error {
		if err := swap.LockUser(ctx, tx, o.UserID); err != nil {
			return err
		}
		o.Status, o.ErrMsg = st, msg
		return swap.UpdateOrder(ctx, tx, o)
	})
}

// BuildHTXOrder 映射为 V5 下单请求（文档《下单》）。保证金模式完全透传；
// 单向持仓不填 position_side（火币默认 both）；client_order_id 使用本地订单 ID，用于崩溃恢复时按其查单去重。
// 单向持仓下反向单数量大于持仓时，由火币原生完成“平仓 + 反向开仓”。
func BuildHTXOrder(o *swap.Order) htx.SwapOrderRequest {
	req := htx.SwapOrderRequest{
		ContractCode:  o.ContractCode,
		MarginMode:    htxMarginMode(o.MarginMode),
		Side:          htx.SideBuy,
		Type:          htx.OrderTypeMarket,
		Volume:        o.Volume.String(),
		ClientOrderID: strconv.FormatUint(o.ID, 10),
	}
	if o.Side == swap.SideSell {
		req.Side = htx.SideSell
	}
	if o.ReduceOnly {
		req.ReduceOnly = 1
	}
	if o.OrderType == swap.OrderLimit {
		req.Type = htx.OrderTypeLimit
		req.Price = o.Price.String()
	}
	return req
}
