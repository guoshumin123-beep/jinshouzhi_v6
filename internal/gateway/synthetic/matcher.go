package synthetic

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"time"

	"backend_v2/internal/pkg/safego"
	"backend_v2/internal/storage"
	"backend_v2/internal/swap"
)

const matchBatch = 500

// MatchContract 以当前盘口撮合某合约的全部穿价挂单：挂单以自身限价、按穿越档位的数量成交（Maker）。
// 候选单在锁外筛选，逐单在独立事务（用户锁）内复核后成交；完全基于数据库状态，进程崩溃重启后可直接续跑。
func (e *Engine) MatchContract(ctx context.Context, contract string) (int, error) {
	q, err := e.quote(contract)
	if err != nil {
		return 0, err
	}
	bestAsk, okA := BestEffective(q, swap.SideBuy, e.cfg.SpreadBps)
	bestBid, okB := BestEffective(q, swap.SideSell, e.cfg.SpreadBps)
	if !okA || !okB {
		return 0, fmt.Errorf("%w: %s empty book", ErrStaleQuote, contract)
	}
	cands, err := swap.MatchCandidates(ctx, e.db, contract, bestAsk, bestBid, matchBatch)
	if err != nil {
		return 0, err
	}
	fills := 0
	for _, c := range cands {
		filled, err := e.matchOrder(ctx, c.ID, q)
		if err != nil {
			e.logger.Warn("match order failed", slog.Uint64("order_id", c.ID), slog.String("error", err.Error()))
			continue
		}
		if filled {
			fills++
		}
	}
	return fills, nil
}

func (e *Engine) matchOrder(ctx context.Context, orderID uint64, q Quote) (bool, error) {
	filled := false
	err := storage.WithTx(ctx, e.db, func(tx *sql.Tx) error {
		filled = false
		o, err := swap.GetOrder(ctx, tx, orderID)
		if err != nil {
			return err
		}
		if err := swap.LockUser(ctx, tx, o.UserID); err != nil {
			return err
		}
		if o, err = swap.GetOrder(ctx, tx, orderID); err != nil { // 加锁后重读
			return err
		}
		if o.Route != swap.RouteSynthetic || (o.Status != swap.OrderOpen && o.Status != swap.OrderPartiallyFilled) {
			return nil
		}
		vol := MakerVolume(q, o.Side, o.Price, o.Remaining(), e.cfg.SpreadBps)
		if !vol.IsPositive() {
			return nil
		}
		spec, err := e.spec(o.ContractCode)
		if err != nil {
			return err
		}
		if err := e.lockLedger(ctx, tx, o.UserID, spec.MarginCurrency); err != nil {
			return err
		}
		pos, err := swap.FindOpenPosition(ctx, tx, o.UserID, o.ContractCode, o.MarginMode)
		if err != nil {
			return err
		}
		if _, err := e.applyFill(ctx, tx, spec, o, pos, o.Price, vol, swap.RoleMaker); err != nil {
			if !isRiskReject(err) {
				return err
			}
			// 开仓保证金不足且本次无任何成交：撤销该挂单
			if err := e.cancelRemainder(ctx, tx, spec, o, err.Error()); err != nil {
				return err
			}
		}
		filled = true
		return swap.UpdateOrder(ctx, tx, o)
	})
	return filled, err
}

// RunMatcher 后台撮合循环：ticks 推送有行情更新的合约代码（来自行情订阅），
// 另按 sweepEvery 周期性全量扫描所有有挂单的合约，兜底漏掉的推送。
func (e *Engine) RunMatcher(ctx context.Context, ticks <-chan string, sweepEvery time.Duration) {
	ticker := time.NewTicker(sweepEvery)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case c := <-ticks:
			e.safeMatch(ctx, c)
		case <-ticker.C:
			contracts, err := swap.ListRestingContracts(ctx, e.db)
			if err != nil {
				e.logger.Warn("list resting contracts failed", slog.String("error", err.Error()))
				continue
			}
			for _, c := range contracts {
				e.safeMatch(ctx, c)
			}
		}
	}
}

func (e *Engine) safeMatch(ctx context.Context, contract string) {
	defer safego.Recover(e.logger, "synthetic_matcher")
	if _, err := e.MatchContract(ctx, contract); err != nil {
		e.logger.Debug("match contract skipped", slog.String("contract", contract), slog.String("error", err.Error()))
	}
}
