package synthetic

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"strconv"
	"time"

	"backend_v2/internal/dealer"
	"backend_v2/internal/pkg/safego"
	"backend_v2/internal/storage"
	"backend_v2/internal/swap"
)

// SettleFunding 对某合约在 fundingTime 时点结算资金费（对手方为 SYNTHETIC_POOL）：
//
//	金额 = 标记价 × 张数 × 面值 × 费率；费率为正时多头支付、空头收取。
//	逐仓：从/向该仓位保证金（SWAP_ISOLATED）扣减/增加，不足部分由 POOL 兜底；
//	全仓：从/向 SWAP 钱包扣减/增加，不足部分由 POOL 兜底。
//
// 幂等：轮次首次落库时固化费率与标记价；每个仓位一条 swap_funding_payments 记录，
// 账本幂等键 funding:<position>:<unix>。崩溃后重跑只会补齐未结算的仓位。
// 结算后调用方应立即执行风控扫描（资金费可能触发强平）。
func (e *Engine) SettleFunding(ctx context.Context, contract string, ev FundingEvent) (int, error) {
	mark, err := e.mark(contract)
	if err != nil {
		return 0, err
	}
	round, err := swap.BeginFundingRound(ctx, e.db, swap.FundingRound{
		ContractCode: contract, FundingTime: ev.FundingTime, Rate: ev.Rate, MarkPrice: mark,
	})
	if err != nil {
		return 0, err
	}
	if round.Settled {
		return 0, nil
	}
	spec, err := e.spec(contract)
	if err != nil {
		return 0, err
	}
	positions, err := swap.ListOpenPositionsByContract(ctx, e.db, swap.RouteSynthetic, contract)
	if err != nil {
		return 0, err
	}
	settled := 0
	for _, p := range positions {
		ok, err := e.settlePositionFunding(ctx, spec, p.ID, round)
		if err != nil {
			return settled, fmt.Errorf("funding %s position %d: %w", contract, p.ID, err)
		}
		if ok {
			settled++
		}
	}
	if err := swap.FinishFundingRound(ctx, e.db, contract, round.FundingTime); err != nil {
		return settled, err
	}
	e.logger.Info("funding round settled", slog.String("contract", contract), slog.Time("funding_time", round.FundingTime),
		slog.String("rate", round.Rate.String()), slog.String("mark", round.MarkPrice.String()), slog.Int("positions", settled))
	return settled, nil
}

func (e *Engine) settlePositionFunding(ctx context.Context, spec ContractSpec, posID uint64, round *swap.FundingRound) (bool, error) {
	done := false
	err := storage.WithTx(ctx, e.db, func(tx *sql.Tx) error {
		done = false
		p, err := swap.GetPosition(ctx, tx, posID)
		if err != nil {
			return err
		}
		if err := swap.LockUser(ctx, tx, p.UserID); err != nil {
			return err
		}
		if p, err = swap.GetPosition(ctx, tx, posID); err != nil {
			return err
		}
		// 结算时点之后才开的仓、或已平仓的仓位不参与本轮
		if p.Status != swap.PositionOpen || p.OpenedAt.After(round.FundingTime) {
			return nil
		}
		if exists, err := swap.FundingPaymentExists(ctx, tx, p.ID, round.FundingTime); err != nil || exists {
			return err
		}
		if err := e.lockLedger(ctx, tx, p.UserID, spec.MarginCurrency); err != nil {
			return err
		}

		// payment > 0：用户支付
		payment := r18(Notional(spec, round.MarkPrice, p.Volume).Mul(round.Rate).Mul(p.Direction.Sign()))
		var res dealer.SettleResult
		if !payment.IsZero() {
			sp := dealer.SettleParams{
				UserID: p.UserID, Currency: spec.MarginCurrency, BizType: dealer.BizSyntheticFunding,
				BizID: "funding:" + strconv.FormatUint(p.ID, 10) + ":" + strconv.FormatInt(round.FundingTime.Unix(), 10),
				PnL:   payment.Neg(),
			}
			if p.MarginMode == swap.MarginIsolated {
				sp.Isolated, sp.KeepInIsolated, sp.Release = true, true, p.Margin
			} else {
				bal, err := e.swapBalance(ctx, tx, p.UserID, spec.MarginCurrency)
				if err != nil {
					return err
				}
				sp.Available = bal.Available
			}
			var ins dealer.Instruction
			if ins, res, err = dealer.NewSyntheticSettlement(sp); err != nil {
				return err
			}
			if err := e.exec(ctx, tx, ins); err != nil {
				return err
			}
			if sp.Isolated {
				p.Margin = res.Payout
			}
		}
		charged := payment
		if payment.IsPositive() {
			charged = res.LossCollected
		}
		if _, err := swap.InsertFundingPayment(ctx, tx, p.ID, round.FundingTime, p.UserID, p.ContractCode,
			round.Rate, round.MarkPrice, charged, res.LossShortfall); err != nil {
			return err
		}
		p.FundingPaid = p.FundingPaid.Add(charged)
		p.LiquidationPrice = e.liqPrice(spec, p)
		if err := swap.UpdatePosition(ctx, tx, p); err != nil {
			return err
		}
		if res.LossShortfall.IsPositive() {
			e.logger.Error("funding shortfall covered by synthetic pool", slog.Uint64("position_id", p.ID),
				slog.String("shortfall", res.LossShortfall.String()))
		}
		done = true
		return nil
	})
	return done, err
}

// RunFunding 周期性检查每个有 Synthetic 持仓的合约是否有已到期未结算的资金费，结算后立即做风控扫描。
func (e *Engine) RunFunding(ctx context.Context, src FundingSource, every time.Duration) {
	ticker := time.NewTicker(every)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			e.fundingOnce(ctx, src)
		}
	}
}

func (e *Engine) fundingOnce(ctx context.Context, src FundingSource) {
	defer safego.Recover(e.logger, "synthetic_funding")
	contracts, err := swap.ListContractsWithOpenPositions(ctx, e.db, swap.RouteSynthetic)
	if err != nil {
		e.logger.Error("list contracts for funding failed", slog.String("error", err.Error()))
		return
	}
	settledAny := false
	for _, c := range contracts {
		ev, ok := src.LastFunding(c)
		if !ok || ev.FundingTime.After(e.now()) {
			continue
		}
		n, err := e.SettleFunding(ctx, c, ev)
		if err != nil {
			e.logger.Error("funding settle failed", slog.String("contract", c), slog.String("error", err.Error()))
			continue
		}
		settledAny = settledAny || n > 0
	}
	if settledAny {
		e.ScanAll(ctx)
	}
}
