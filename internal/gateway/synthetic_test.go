package gateway_test

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"backend_v2/internal/dealer"
	"backend_v2/internal/gateway/synthetic"
	"backend_v2/internal/swap"
)

// 下列金额均为手算期望值。公共参数：盘口 60000/60000，点差 10bps（买 60060 / 卖 59940），
// taker 0.05%，maker 0.02%，强平费 0.05%，BTC 面值 0.001，MMR 0.5%（名义价值 <= 10 万）。

func TestCrossMarketOrderSpreadAndFee(t *testing.T) {
	e := setup(t)
	e.synthetic(1)
	o := e.place(market(1, "m1", swap.SideBuy, 100, 10))
	// 名义 6006，手续费 3.003；全仓保证金不冻结
	if !o.AvgFillPrice.Equal(dec("60060")) || !o.FeePaid.Equal(dec("3.003")) || o.Status != swap.OrderFilled {
		t.Fatalf("order = %+v", o)
	}
	e.assertBal(1, dealer.AccountSwap, "9996.997", "0")
	e.assertBal(0, dealer.AccountFeeIncome, "3.003", "0")
	p := e.openPos(1, "BTC-USDT", swap.MarginCross)
	if !p.Margin.IsZero() || !p.LiquidationPrice.IsZero() || !p.AvgPrice.Equal(dec("60060")) {
		t.Fatalf("cross position = %+v", p)
	}
	e.assertConserved()
}

func TestIsolatedOpenAndCloseLedgerIsolation(t *testing.T) {
	e := setup(t)
	e.synthetic(1)
	r := market(1, "i1", swap.SideBuy, 100, 10)
	r.MarginMode = swap.MarginIsolated
	e.place(r)
	// IM 600.6 划入 SWAP_ISOLATED，手续费 3.003
	e.assertBal(1, dealer.AccountSwap, "9396.397", "0")
	e.assertBal(1, dealer.AccountSwapIsolated, "600.6", "0")
	p := e.openPos(1, "BTC-USDT", swap.MarginIsolated)
	wantLiq := dec("5405.4").DivRound(dec("0.0995"), 18)
	if !p.Margin.Equal(dec("600.6")) || !p.LiquidationPrice.Equal(wantLiq) {
		t.Fatalf("isolated position = %+v (want liq %s)", p, wantLiq)
	}

	e.market.setBook("BTC-USDT", "61000", "61000", "1000")
	r2 := market(1, "i2", swap.SideSell, 100, 10)
	r2.MarginMode = swap.MarginIsolated
	c := e.place(r2)
	// 卖价 61000*0.999 = 60939；盈亏 87.9；手续费 3.04695；返还 600.6+87.9-3.04695
	if !c.RealizedPnL.Equal(dec("87.9")) || !c.FeePaid.Equal(dec("3.04695")) {
		t.Fatalf("close = %+v", c)
	}
	e.assertBal(1, dealer.AccountSwap, "10081.85005", "0")
	e.assertBal(1, dealer.AccountSwapIsolated, "0", "0")
	e.assertBal(0, dealer.AccountSyntheticPool, "-87.9", "0")
	e.assertBal(0, dealer.AccountFeeIncome, "6.04995", "0")
	e.assertConserved()
}

func TestFlipSettlesThenOpensReverse(t *testing.T) {
	e := setup(t)
	e.synthetic(1)
	e.place(market(1, "l", swap.SideBuy, 100, 10))
	f := e.place(market(1, "f", swap.SideSell, 150, 10))
	// 平 100：盈亏 (59940-60060)*0.1 = -12，费 2.997；反手开空 50：费 1.4985
	if !f.RealizedPnL.Equal(dec("-12")) || !f.FeePaid.Equal(dec("4.4955")) || !f.FilledVolume.Equal(dec("150")) {
		t.Fatalf("flip order = %+v", f)
	}
	trades, err := swap.ListTrades(context.Background(), e.db, f.ID)
	if err != nil || len(trades) != 2 || trades[0].Kind != swap.TradeClose || trades[1].Kind != swap.TradeOpen ||
		trades[0].PositionID == trades[1].PositionID {
		t.Fatalf("flip trades = %+v, %v", trades, err)
	}
	s := e.openPos(1, "BTC-USDT", swap.MarginCross)
	if s.Direction != swap.DirectionShort || !s.Volume.Equal(dec("50")) || !s.AvgPrice.Equal(dec("59940")) {
		t.Fatalf("short = %+v", s)
	}
	e.assertBal(1, dealer.AccountSwap, "9980.5015", "0")
	e.assertConserved()
}

func TestIsolatedFlipMovesMarginToNewPosition(t *testing.T) {
	e := setup(t)
	e.synthetic(1)
	r := market(1, "a", swap.SideBuy, 100, 10)
	r.MarginMode = swap.MarginIsolated
	e.place(r)
	r2 := market(1, "b", swap.SideSell, 130, 10)
	r2.MarginMode = swap.MarginIsolated
	e.place(r2)
	// 旧仓保证金 600.6 全部释放结算；新空仓 30 张 @59940：IM = 59940*30*0.001/10 = 179.82
	s := e.openPos(1, "BTC-USDT", swap.MarginIsolated)
	if s.Direction != swap.DirectionShort || !s.Margin.Equal(dec("179.82")) {
		t.Fatalf("new isolated short = %+v", s)
	}
	e.assertBal(1, dealer.AccountSwapIsolated, "179.82", "0")
	e.assertConserved()
}

func TestLimitOrderRestsMatchesAsMakerAndCancels(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	e.synthetic(1)
	o := e.place(limit(1, "L1", swap.SideBuy, 100, "59000", 10))
	// 冻结 IM 590 + 预估 taker 费 2.95
	if o.Status != swap.OrderOpen || !o.FrozenAmount.Equal(dec("592.95")) {
		t.Fatalf("resting = %+v", o)
	}
	e.assertBal(1, dealer.AccountSwap, "9407.05", "592.95")

	// 卖一 58900 × 1.001 = 58958.9 <= 59000，可成交 60 张，以限价 59000 成交（maker 费 0.708）
	e.market.setBook("BTC-USDT", "58800", "58900", "60")
	n, err := e.engine.MatchContract(ctx, "BTC-USDT")
	if err != nil || n != 1 {
		t.Fatalf("match = %d, %v", n, err)
	}
	got, _ := swap.GetOrder(ctx, e.db, o.ID)
	if got.Status != swap.OrderPartiallyFilled || !got.FilledVolume.Equal(dec("60")) || !got.AvgFillPrice.Equal(dec("59000")) ||
		!got.FeePaid.Equal(dec("0.708")) || !got.FrozenAmount.Equal(dec("237.18")) {
		t.Fatalf("after match = %+v", got)
	}
	trades, _ := swap.ListTrades(ctx, e.db, o.ID)
	if len(trades) != 1 || trades[0].Role != swap.RoleMaker {
		t.Fatalf("trades = %+v", trades)
	}
	e.assertBal(1, dealer.AccountSwap, "9762.112", "237.18")

	c, err := e.router.CancelOrder(ctx, 1, "L1")
	if err != nil || c.Status != swap.OrderCanceled {
		t.Fatalf("cancel = %+v, %v", c, err)
	}
	e.assertBal(1, dealer.AccountSwap, "9999.292", "0")
	if p := e.openPos(1, "BTC-USDT", swap.MarginCross); !p.Volume.Equal(dec("60")) {
		t.Fatalf("position = %+v", p)
	}
	if _, err := e.router.CancelOrder(ctx, 1, "L1"); err == nil {
		t.Fatal("double cancel must fail")
	}
	e.assertConserved()
}

func TestMarketableLimitTakesThenRests(t *testing.T) {
	e := setup(t)
	e.synthetic(1)
	e.market.setDepth("BTC-USDT",
		[]synthetic.Level{{Price: dec("59990"), Size: dec("1000")}},
		[]synthetic.Level{{Price: dec("60000"), Size: dec("30")}, {Price: dec("60050"), Size: dec("1000")}})
	// 限价 60100：第一档 60060 可吃 30 张，第二档 60110.05 超出限价
	o := e.place(limit(1, "T", swap.SideBuy, 100, "60100", 10))
	if o.Status != swap.OrderPartiallyFilled || !o.FilledVolume.Equal(dec("30")) || !o.AvgFillPrice.Equal(dec("60060")) {
		t.Fatalf("order = %+v", o)
	}
	// 剩余 70 张按限价冻结：IM 420.7 + 费 2.1035
	if !o.FrozenAmount.Equal(dec("422.8035")) {
		t.Fatalf("frozen = %s", o.FrozenAmount)
	}
	e.assertConserved()
}

func TestMarketSweepRespectsBandAndCancelsRemainder(t *testing.T) {
	e := setup(t)
	e.synthetic(1)
	e.market.setDepth("BTC-USDT",
		[]synthetic.Level{{Price: dec("59990"), Size: dec("1000")}},
		[]synthetic.Level{{Price: dec("60000"), Size: dec("10")}, {Price: dec("60100"), Size: dec("10")}, {Price: dec("70000"), Size: dec("100")}})
	o := e.place(market(1, "S", swap.SideBuy, 50, 10))
	vwap := dec("60060").Mul(dec("10")).Add(dec("60160.1").Mul(dec("10"))).DivRound(dec("20"), 18)
	if o.Status != swap.OrderCanceled || !o.FilledVolume.Equal(dec("20")) || !o.AvgFillPrice.Equal(vwap) {
		t.Fatalf("order = %+v", o)
	}
}

func TestReduceOnly(t *testing.T) {
	e := setup(t)
	e.synthetic(1)
	r := market(1, "ro0", swap.SideSell, 5, 10)
	r.ReduceOnly = true
	if _, err := e.router.PlaceOrder(context.Background(), r); !errors.Is(err, synthetic.ErrReduceOnly) {
		t.Fatalf("want ErrReduceOnly, got %v", err)
	}
	e.place(market(1, "l", swap.SideBuy, 10, 10))
	r2 := market(1, "ro1", swap.SideSell, 15, 10)
	r2.ReduceOnly = true
	o := e.place(r2)
	if !o.FilledVolume.Equal(dec("10")) || o.Status != swap.OrderCanceled {
		t.Fatalf("reduce-only = %+v", o)
	}
	if e.openPos(1, "BTC-USDT", swap.MarginCross) != nil {
		t.Fatal("reduce-only must not open reverse position")
	}
}

func TestInsufficientMarginRejectsWholeOrder(t *testing.T) {
	e := setup(t)
	e.synthetic(1)
	e.market.setBook("BTC-USDT", "60000", "60000", "100000") // 盘口足够深，确保失败原因是保证金而非流动性
	// 2000 张 IM 12012 > 10000
	_, err := e.router.PlaceOrder(context.Background(), market(1, "big", swap.SideBuy, 2000, 10))
	if !errors.Is(err, synthetic.ErrInsufficientMargin) {
		t.Fatalf("want ErrInsufficientMargin, got %v", err)
	}
	if n := e.count(`SELECT COUNT(*) FROM swap_orders WHERE user_id = 1`); n != 0 {
		t.Fatal("rejected order persisted")
	}
	// 杠杆超出阶梯上限（名义 > 10 万只允许 20x）
	if _, err := e.router.PlaceOrder(context.Background(), market(1, "lev", swap.SideBuy, 1700, 100)); !errors.Is(err, synthetic.ErrLeverageTooHigh) {
		t.Fatalf("want ErrLeverageTooHigh, got %v", err)
	}
	e.assertBal(1, dealer.AccountSwap, "10000", "0")
}

func TestIsolatedLiquidationBankruptcyCoveredByPool(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	e.synthetic(1)
	r := market(1, "x", swap.SideBuy, 100, 50)
	r.MarginMode = swap.MarginIsolated
	e.place(r)
	// IM 120.12，费 3.003
	e.assertBal(1, dealer.AccountSwap, "9876.877", "0")
	p := e.openPos(1, "BTC-USDT", swap.MarginIsolated)

	if rep, err := e.engine.CheckUser(ctx, 1); err != nil || len(rep.Liquidated) != 0 {
		t.Fatalf("healthy position liquidated: %+v, %v", rep, err)
	}

	e.market.setMark("BTC-USDT", "50000") // 跳空：亏损 1006 远超保证金
	rep, err := e.engine.CheckUser(ctx, 1)
	if err != nil || len(rep.Liquidated) != 1 || rep.Liquidated[0] != p.ID {
		t.Fatalf("report = %+v, %v", rep, err)
	}
	closed, _ := swap.GetPosition(ctx, e.db, p.ID)
	if closed.Status != swap.PositionClosed || closed.CloseReason != swap.CloseLiquidation {
		t.Fatalf("position = %+v", closed)
	}
	// 逐仓亏损以保证金为限：SWAP 不受影响；POOL 收到 120.12，差额 885.88 记为穿仓
	e.assertBal(1, dealer.AccountSwap, "9876.877", "0")
	e.assertBal(1, dealer.AccountSwapIsolated, "0", "0")
	e.assertBal(0, dealer.AccountSyntheticPool, "120.12", "0")
	if n := e.count(`SELECT COUNT(*) FROM swap_trades WHERE position_id = ? AND role = 'LIQUIDATION' AND loss_shortfall = 885.88`, p.ID); n != 1 {
		t.Fatal("shortfall not recorded on liquidation trade")
	}
	if n := e.count(`SELECT COUNT(*) FROM swap_orders WHERE source = 'LIQUIDATION' AND status = 'FILLED'`); n != 1 {
		t.Fatal("liquidation order missing")
	}
	e.assertConserved()
}

func setupCrossTwoPositions(t *testing.T) *env {
	e := setup(t)
	e.synthetic(5)
	e.fund(5, "1000")
	e.place(market(5, "btc", swap.SideBuy, 100, 20)) // 名义 6006，费 3.003
	r := market(5, "eth", swap.SideBuy, 100, 20)     // 名义 3003，费 1.5015
	r.ContractCode = "ETH-USDT"
	e.place(r)
	// 一笔挂单，验证强平时被撤销并释放冻结（IM 10 + 费 0.1）
	lr := limit(5, "rest", swap.SideBuy, 10, "2000", 20)
	lr.ContractCode = "ETH-USDT"
	if o := e.place(lr); !o.FrozenAmount.Equal(dec("10.1")) {
		t.Fatalf("resting frozen = %s", o.FrozenAmount)
	}
	e.assertBal(5, dealer.AccountSwap, "985.3955", "10.1")
	return e
}

// 全仓连环强平：先强平维持保证金最大的仓位，风险恢复即停止。
func TestCrossLiquidationStopsWhenHealthy(t *testing.T) {
	e := setupCrossTwoPositions(t)
	ctx := context.Background()
	btcPos := e.openPos(5, "BTC-USDT", swap.MarginCross)

	// 权益 995.4955 - 906 - 53 = 36.4955 <= MM 25.5 + 14.75
	e.market.setMark("BTC-USDT", "51000")
	e.market.setMark("ETH-USDT", "2950")
	rep, err := e.engine.CheckUser(ctx, 5)
	if err != nil || len(rep.Liquidated) != 1 || rep.Liquidated[0] != btcPos.ID {
		t.Fatalf("report = %+v, %v", rep, err)
	}
	if e.openPos(5, "ETH-USDT", swap.MarginCross) == nil {
		t.Fatal("ETH position should survive once account is healthy")
	}
	// BTC 平仓：亏损 906 + 强平费 2.55；挂单已撤
	e.assertBal(5, dealer.AccountSwap, "86.9455", "0")
	if n := e.count(`SELECT COUNT(*) FROM swap_orders WHERE client_order_id = 'rest' AND status = 'CANCELED'`); n != 1 {
		t.Fatal("resting order not canceled by liquidation")
	}
	e.assertConserved()
}

func TestCrossChainLiquidationWithBankruptcy(t *testing.T) {
	e := setupCrossTwoPositions(t)
	ctx := context.Background()
	e.market.setMark("BTC-USDT", "50000")
	e.market.setMark("ETH-USDT", "2900")
	rep, err := e.engine.CheckUser(ctx, 5)
	if err != nil || len(rep.Liquidated) != 2 {
		t.Fatalf("report = %+v, %v", rep, err)
	}
	// 钱包 995.4955 全部被 BTC 亏损吸收，剩余 10.5045 与 ETH 亏损 103 由 POOL 兜底；钱包不为负
	e.assertBal(5, dealer.AccountSwap, "0", "0")
	var shortfall string
	if err := e.db.QueryRow(`SELECT SUM(loss_shortfall) FROM swap_trades WHERE user_id = 5 AND role = 'LIQUIDATION'`).Scan(&shortfall); err != nil {
		t.Fatal(err)
	}
	if !dec(shortfall).Equal(dec("113.5045")) {
		t.Fatalf("shortfall = %s", shortfall)
	}
	if n := e.count(`SELECT COUNT(*) FROM swap_positions WHERE user_id = 5 AND status = 'OPEN'`); n != 0 {
		t.Fatalf("open positions left: %d", n)
	}
	e.assertConserved()
}

func TestFundingIsolatedAndCrossIdempotent(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	e.synthetic(1)
	e.synthetic(2)
	e.synthetic(4)
	r := market(1, "isoLong", swap.SideBuy, 100, 10)
	r.MarginMode = swap.MarginIsolated
	e.place(r)                                               // 逐仓多 100，保证金 600.6
	e.place(market(2, "crossShort", swap.SideSell, 100, 10)) // 全仓空 100
	e.market.setMark("BTC-USDT", "60000")
	fundingTime := time.Now()
	e.place(market(4, "late", swap.SideBuy, 100, 10)) // 结算时点之后开仓，不参与本轮

	swap2Before := e.bal(2, dealer.AccountSwap).Available
	swap4Before := e.bal(4, dealer.AccountSwap).Available
	n, err := e.engine.SettleFunding(ctx, "BTC-USDT", synthetic.FundingEvent{Rate: dec("0.0001"), FundingTime: fundingTime})
	if err != nil || n != 2 {
		t.Fatalf("settled = %d, %v", n, err)
	}
	// 金额 = 60000 × 100 × 0.001 × 0.0001 = 0.6：多头付、空头收
	iso := e.openPos(1, "BTC-USDT", swap.MarginIsolated)
	if !iso.Margin.Equal(dec("600")) || !iso.FundingPaid.Equal(dec("0.6")) {
		t.Fatalf("isolated after funding = %+v", iso)
	}
	e.assertBal(1, dealer.AccountSwapIsolated, "600", "0")
	if got := e.bal(2, dealer.AccountSwap).Available; !got.Equal(swap2Before.Add(dec("0.6"))) {
		t.Fatalf("cross short wallet = %s", got)
	}
	if got := e.bal(4, dealer.AccountSwap).Available; !got.Equal(swap4Before) {
		t.Fatal("late position must not be charged")
	}

	again, err := e.engine.SettleFunding(ctx, "BTC-USDT", synthetic.FundingEvent{Rate: dec("0.0001"), FundingTime: fundingTime})
	if err != nil || again != 0 {
		t.Fatalf("re-run settled %d, %v", again, err)
	}
	e.assertConserved()
}

func TestTransferOutGuardsCrossMargin(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	e.synthetic(1)
	e.place(market(1, "l", swap.SideBuy, 100, 10))
	// Free = 9996.997 + (-6) - 600 = 9390.997
	free, err := e.engine.MaxTransferOut(ctx, 1, "USDT")
	if err != nil || !free.Equal(dec("9390.997")) {
		t.Fatalf("max transfer = %s, %v", free, err)
	}
	if err := e.engine.TransferOut(ctx, 1, "USDT", dec("9391"), "out-1"); !errors.Is(err, synthetic.ErrInsufficientMargin) {
		t.Fatalf("want ErrInsufficientMargin, got %v", err)
	}
	if err := e.engine.TransferOut(ctx, 1, "USDT", free, "out-2"); err != nil {
		t.Fatal(err)
	}
	e.assertBal(1, dealer.AccountSpot, "9390.997", "0")
	e.assertConserved()
}

// 并发：多用户、多合约下单 + 撮合 + 风控扫描 + 资金费同时进行，账本始终守恒。
func TestSyntheticConcurrentEngineLoops(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	for _, u := range []int64{1, 2, 3, 4} {
		e.synthetic(u)
	}
	var wg sync.WaitGroup
	errs := make(chan error, 200)
	for _, u := range []int64{1, 2, 3, 4} {
		wg.Add(1)
		go func(u int64) {
			defer wg.Done()
			for i := 0; i < 10; i++ {
				side := swap.SideBuy
				if i%3 == 0 {
					side = swap.SideSell
				}
				r := market(u, fmt.Sprintf("c%d", i), side, int64(5+i), 10)
				if i%2 == 0 {
					r.ContractCode = "ETH-USDT"
				}
				if i%4 == 1 {
					r.MarginMode = swap.MarginIsolated
				}
				if i%5 == 2 {
					r = limit(u, fmt.Sprintf("c%d", i), side, 5, "60000", 10)
				}
				if _, err := e.router.PlaceOrder(ctx, r); err != nil && !errors.Is(err, synthetic.ErrInsufficientMargin) {
					errs <- err
				}
			}
		}(u)
	}
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < 10; i++ {
			if _, err := e.engine.MatchContract(ctx, "BTC-USDT"); err != nil {
				errs <- err
			}
			e.engine.ScanAll(ctx)
		}
	}()
	wg.Add(1)
	go func() {
		defer wg.Done()
		if _, err := e.engine.SettleFunding(ctx, "ETH-USDT", synthetic.FundingEvent{Rate: dec("0.0003"), FundingTime: time.Now()}); err != nil {
			errs <- err
		}
	}()
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Errorf("unexpected: %v", err)
	}
	e.assertConserved()
	// 每个逐仓账户余额 = 其全部 OPEN 逐仓仓位保证金之和
	rows, err := e.db.Query(`SELECT a.owner_id, a.available, COALESCE((SELECT SUM(margin) FROM swap_positions p
		WHERE p.user_id = a.owner_id AND p.status = 'OPEN' AND p.margin_mode = 'ISOLATED'), 0)
		FROM accounts a WHERE a.account_type = 'SWAP_ISOLATED'`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	for rows.Next() {
		var uid int64
		var bal, sum string
		if err := rows.Scan(&uid, &bal, &sum); err != nil {
			t.Fatal(err)
		}
		if !dec(bal).Equal(dec(sum)) {
			t.Errorf("user %d SWAP_ISOLATED %s != Σ position margin %s", uid, bal, sum)
		}
	}
}
