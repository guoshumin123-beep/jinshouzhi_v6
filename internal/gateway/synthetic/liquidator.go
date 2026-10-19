package synthetic

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"strconv"
	"time"

	"github.com/shopspring/decimal"

	"backend_v2/internal/pkg/safego"
	"backend_v2/internal/storage"
	"backend_v2/internal/swap"
)

// LiquidationReport 一次风控检查的结果。
type LiquidationReport struct {
	UserID     int64
	Liquidated []uint64 // 被强平的持仓 ID（按执行顺序）
}

// CheckUser 对单个用户执行强平检查（单事务、持有用户锁）：
//
//	逐仓：每个仓位独立判断 权益(保证金 + 浮动盈亏) <= 维持保证金 → 以标记价全部强平，亏损上限为该仓保证金。
//	全仓：账户权益 <= Σ 动态维持保证金 → 先撤销全部挂单释放冻结，再按维持保证金占用从大到小
//	      逐个强平（连环强平），每平一个重新计算风险，直到账户恢复安全或无仓可平；
//	      钱包不足以覆盖的亏损由 SYNTHETIC_POOL 兜底。
func (e *Engine) CheckUser(ctx context.Context, userID int64) (*LiquidationReport, error) {
	rep := &LiquidationReport{UserID: userID}
	err := storage.WithTx(ctx, e.db, func(tx *sql.Tx) error {
		rep.Liquidated = nil
		if err := swap.LockUser(ctx, tx, userID); err != nil {
			return err
		}
		positions, err := swap.ListOpenPositions(ctx, tx, userID, swap.RouteSynthetic, "")
		if err != nil {
			return err
		}
		currencies := map[string]bool{}
		for _, p := range positions {
			spec, err := e.spec(p.ContractCode)
			if err != nil {
				return err
			}
			if !currencies[spec.MarginCurrency] {
				currencies[spec.MarginCurrency] = true
				if err := e.lockLedger(ctx, tx, userID, spec.MarginCurrency); err != nil {
					return err
				}
			}
		}

		// 逐仓
		for _, p := range positions {
			if p.MarginMode != swap.MarginIsolated {
				continue
			}
			spec, _ := e.spec(p.ContractCode)
			mark, err := e.mark(p.ContractCode)
			if err != nil {
				return err
			}
			if r := ComputeIsolated(spec, p, mark); r.Liquidatable() {
				e.logger.Warn("isolated position liquidating",
					slog.Int64("user_id", userID), slog.Uint64("position_id", p.ID), slog.String("mark", mark.String()),
					slog.String("equity", r.Equity.String()), slog.String("maintenance", r.Maintenance.String()))
				if err := e.cancelSlotOrders(ctx, tx, p); err != nil {
					return err
				}
				if err := e.liquidate(ctx, tx, spec, p, mark); err != nil {
					return err
				}
				rep.Liquidated = append(rep.Liquidated, p.ID)
			}
		}

		// 全仓（连环强平）
		for ccy := range currencies {
			ids, err := e.liquidateCross(ctx, tx, userID, ccy)
			if err != nil {
				return err
			}
			rep.Liquidated = append(rep.Liquidated, ids...)
		}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("risk check user %d: %w", userID, err)
	}
	return rep, nil
}

func (e *Engine) liquidateCross(ctx context.Context, tx *sql.Tx, userID int64, ccy string) ([]uint64, error) {
	risk, cps, err := e.crossRisk(ctx, tx, userID, ccy)
	if err != nil || !risk.Liquidatable() {
		return nil, err
	}
	ratio, _ := risk.RiskRatio()
	e.logger.Warn("cross account liquidating",
		slog.Int64("user_id", userID), slog.String("currency", ccy), slog.String("equity", risk.Equity.String()),
		slog.String("maintenance", risk.Maintenance.String()), slog.String("risk_ratio", ratio.String()))

	// 挂单冻结也属于钱包权益，撤单不改变权益但把资金释放回 available 以便结算
	if err := e.cancelAllResting(ctx, tx, userID, 0, "canceled by cross liquidation"); err != nil {
		return nil, err
	}
	var liquidated []uint64
	for risk.Liquidatable() && len(cps) > 0 {
		// 选维持保证金占用最大的仓位（对风险率改善最大）
		victim := cps[0]
		for _, cp := range cps[1:] {
			if MaintenanceMargin(cp.Spec, Notional(cp.Spec, cp.Mark, cp.Position.Volume)).GreaterThan(
				MaintenanceMargin(victim.Spec, Notional(victim.Spec, victim.Mark, victim.Position.Volume))) {
				victim = cp
			}
		}
		if err := e.liquidate(ctx, tx, victim.Spec, victim.Position, victim.Mark); err != nil {
			return liquidated, err
		}
		liquidated = append(liquidated, victim.Position.ID)
		if risk, cps, err = e.crossRisk(ctx, tx, userID, ccy); err != nil {
			return liquidated, err
		}
	}
	return liquidated, nil
}

// cancelSlotOrders 撤销该仓位所在槽位的全部挂单（强平前清理）。
func (e *Engine) cancelSlotOrders(ctx context.Context, tx *sql.Tx, p *swap.Position) error {
	orders, err := swap.ListActiveOrders(ctx, tx, p.UserID, p.ContractCode, p.MarginMode)
	if err != nil {
		return err
	}
	for _, o := range orders {
		if o.Route != swap.RouteSynthetic {
			continue
		}
		spec, err := e.spec(o.ContractCode)
		if err != nil {
			return err
		}
		if err := e.cancelRemainder(ctx, tx, spec, o, "canceled by liquidation"); err != nil {
			return err
		}
		if err := swap.UpdateOrder(ctx, tx, o); err != nil {
			return err
		}
	}
	return nil
}

// liquidate 以标记价全部平掉 p（生成 LIQUIDATION 来源的减仓订单与成交，留痕可审计）。
func (e *Engine) liquidate(ctx context.Context, tx *sql.Tx, spec ContractSpec, p *swap.Position, mark decimal.Decimal) error {
	o := &swap.Order{
		UserID: p.UserID, ClientOrderID: "liq-" + strconv.FormatUint(p.ID, 10), ContractCode: p.ContractCode,
		MarginMode: p.MarginMode, Route: p.Route, Side: p.Direction.ClosingSide(), OrderType: swap.OrderMarket,
		ReduceOnly: true, Source: swap.SourceLiquidation, Volume: p.Volume, Leverage: p.Leverage, Status: swap.OrderNew,
	}
	if err := swap.InsertOrder(ctx, tx, o); err != nil {
		return err
	}
	if _, err := e.applyFill(ctx, tx, spec, o, p, mark, p.Volume, swap.RoleLiquidation); err != nil {
		return fmt.Errorf("liquidate position %d: %w", p.ID, err)
	}
	if err := swap.UpdateOrder(ctx, tx, o); err != nil {
		return err
	}
	e.logger.Warn("position liquidated",
		slog.Int64("user_id", p.UserID), slog.Uint64("position_id", p.ID), slog.Uint64("order_id", o.ID),
		slog.String("margin_mode", string(p.MarginMode)), slog.String("mark", mark.String()),
		slog.String("realized_pnl", o.RealizedPnL.String()))
	return nil
}

// RunLiquidator 周期性扫描所有持有 Synthetic 仓位的用户。
func (e *Engine) RunLiquidator(ctx context.Context, every time.Duration) {
	ticker := time.NewTicker(every)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			e.ScanAll(ctx)
		}
	}
}

// ScanAll 对所有持仓用户执行一次风控检查；单个用户失败不影响其他用户。
func (e *Engine) ScanAll(ctx context.Context) {
	defer safego.Recover(e.logger, "synthetic_liquidator")
	users, err := swap.ListUsersWithOpenPositions(ctx, e.db, swap.RouteSynthetic)
	if err != nil {
		e.logger.Error("list users for risk scan failed", slog.String("error", err.Error()))
		return
	}
	for _, uid := range users {
		if _, err := e.CheckUser(ctx, uid); err != nil {
			e.logger.Error("risk check failed", slog.Int64("user_id", uid), slog.String("error", err.Error()))
		}
	}
}
