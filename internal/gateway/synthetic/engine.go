package synthetic

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"time"

	"github.com/shopspring/decimal"

	"backend_v2/internal/dealer"
	"backend_v2/internal/gateway"
	"backend_v2/internal/storage"
	"backend_v2/internal/swap"
)

// Engine Synthetic 通道执行器 + 撮合/风控/资金费核心。
type Engine struct {
	db     *sql.DB
	dealer dealer.TxDealer
	prices PriceSource
	marks  MarkSource
	specs  SpecSource
	cfg    Config
	now    func() time.Time
	logger *slog.Logger
}

func NewEngine(db *sql.DB, d dealer.TxDealer, prices PriceSource, marks MarkSource, specs SpecSource, cfg Config, logger *slog.Logger) *Engine {
	if logger == nil {
		logger = slog.Default()
	}
	return &Engine{db: db, dealer: d, prices: prices, marks: marks, specs: specs, cfg: cfg, now: time.Now,
		logger: logger.With(slog.String("component", "synthetic"))}
}

var _ gateway.Executor = (*Engine)(nil)

func (e *Engine) Route() swap.Route { return swap.RouteSynthetic }

// ---------------------------------------------------------------------------
// 行情与规格
// ---------------------------------------------------------------------------

func (e *Engine) spec(contract string) (ContractSpec, error) {
	s, ok := e.specs.Spec(contract)
	if !ok {
		return ContractSpec{}, fmt.Errorf("%w: %s", ErrUnknownContract, contract)
	}
	if err := s.validate(); err != nil {
		return ContractSpec{}, fmt.Errorf("%s: %w", contract, err)
	}
	return s, nil
}

func (e *Engine) quote(contract string) (Quote, error) {
	q, ok := e.prices.Quote(contract)
	if !ok || e.now().Sub(q.At) > e.cfg.MaxQuoteAge || len(q.Bids) == 0 || len(q.Asks) == 0 {
		return Quote{}, fmt.Errorf("%w: %s", ErrStaleQuote, contract)
	}
	return q, nil
}

func (e *Engine) mark(contract string) (decimal.Decimal, error) {
	p, at, ok := e.marks.Mark(contract)
	if !ok || !p.IsPositive() || e.now().Sub(at) > e.cfg.MaxMarkAge {
		return decimal.Zero, fmt.Errorf("%w: %s", ErrStaleMark, contract)
	}
	return p, nil
}

func (e *Engine) feeRate(role swap.Role) decimal.Decimal {
	switch role {
	case swap.RoleMaker:
		return e.cfg.MakerFeeRate
	case swap.RoleLiquidation:
		return e.cfg.LiquidationFeeRate
	}
	return e.cfg.TakerFeeRate
}

// ---------------------------------------------------------------------------
// 账本加锁与全仓风险
// ---------------------------------------------------------------------------

func walletKeys(userID int64, ccy string) (swapKey, isoKey dealer.AccountKey, all []dealer.AccountKey) {
	swapKey = dealer.AccountKey{OwnerID: userID, Type: dealer.AccountSwap, Currency: ccy}
	isoKey = dealer.AccountKey{OwnerID: userID, Type: dealer.AccountSwapIsolated, Currency: ccy}
	all = []dealer.AccountKey{
		{OwnerID: dealer.SystemOwnerID, Type: dealer.AccountFeeIncome, Currency: ccy},
		{OwnerID: dealer.SystemOwnerID, Type: dealer.AccountSyntheticPool, Currency: ccy},
		{OwnerID: userID, Type: dealer.AccountSpot, Currency: ccy},
		swapKey, isoKey,
	}
	return
}

// lockLedger 在 LockUser 之后、任何记账之前调用，按固定顺序锁定本次操作可能触及的全部账户。
func (e *Engine) lockLedger(ctx context.Context, tx *sql.Tx, userID int64, ccy string) error {
	_, _, keys := walletKeys(userID, ccy)
	if _, err := e.dealer.LockBalancesInTx(ctx, tx, keys...); err != nil {
		return fmt.Errorf("lock ledger user %d %s: %w", userID, ccy, err)
	}
	return nil
}

func (e *Engine) swapBalance(ctx context.Context, tx *sql.Tx, userID int64, ccy string) (dealer.BalanceSnapshot, error) {
	k, _, _ := walletKeys(userID, ccy)
	m, err := e.dealer.LockBalancesInTx(ctx, tx, k)
	if err != nil {
		return dealer.BalanceSnapshot{}, err
	}
	return m[k], nil
}

// crossRisk 计算用户在某保证金币种下的全仓风险（读取的是本事务内的最新余额与持仓）。
func (e *Engine) crossRisk(ctx context.Context, tx *sql.Tx, userID int64, ccy string) (CrossRisk, []CrossPosition, error) {
	bal, err := e.swapBalance(ctx, tx, userID, ccy)
	if err != nil {
		return CrossRisk{}, nil, err
	}
	positions, err := swap.ListOpenPositions(ctx, tx, userID, swap.RouteSynthetic, swap.MarginCross)
	if err != nil {
		return CrossRisk{}, nil, err
	}
	var cps []CrossPosition
	for _, p := range positions {
		spec, err := e.spec(p.ContractCode)
		if err != nil {
			return CrossRisk{}, nil, err
		}
		if spec.MarginCurrency != ccy {
			continue
		}
		m, err := e.mark(p.ContractCode)
		if err != nil {
			return CrossRisk{}, nil, err
		}
		cps = append(cps, CrossPosition{Position: p, Spec: spec, Mark: m})
	}
	return ComputeCross(bal.Available, bal.Frozen, cps), cps, nil
}

func (e *Engine) exec(ctx context.Context, tx *sql.Tx, ins dealer.Instruction) error {
	if ins.Empty() {
		return nil
	}
	_, err := e.dealer.ExecuteInTx(ctx, tx, ins)
	return err
}

// ---------------------------------------------------------------------------
// gateway.Executor
// ---------------------------------------------------------------------------

// Accept 受理订单（调用方已持有用户锁）。
//
//	市价单：按深度扫盘（含点差、滑点保护）即时成交，剩余撤销（IOC）。
//	限价单：可立即成交部分以吃单成交，剩余部分冻结保证金 + 预估手续费后挂单，由 Matcher 按盘口撮合。
func (e *Engine) Accept(ctx context.Context, tx *sql.Tx, o *swap.Order, pos *swap.Position) error {
	spec, err := e.spec(o.ContractCode)
	if err != nil {
		return err
	}
	q, err := e.quote(o.ContractCode)
	if err != nil {
		return err
	}
	if err := e.lockLedger(ctx, tx, o.UserID, spec.MarginCurrency); err != nil {
		return err
	}
	if o.ReduceOnly && (pos == nil || pos.Direction == swap.DirectionOf(o.Side)) {
		return fmt.Errorf("%w: no opposite position", ErrReduceOnly)
	}

	if o.OrderType == swap.OrderMarket {
		sw := Sweep(q, o.Side, o.Volume, decimal.Zero, e.cfg.MarketBand, e.cfg.SpreadBps)
		if !sw.Volume.IsPositive() {
			return ErrNoLiquidity
		}
		if _, err := e.applyFill(ctx, tx, spec, o, pos, sw.VWAP, sw.Volume, swap.RoleTaker); err != nil {
			return err
		}
		if o.Status.Active() || o.Status == swap.OrderNew {
			e.closeOrder(o, "market order remainder canceled (IOC)")
		}
		return e.requireFilled(o)
	}

	// 限价：先吃单
	sw := Sweep(q, o.Side, o.Volume, o.Price, decimal.Zero, e.cfg.SpreadBps)
	if sw.Volume.IsPositive() {
		if pos, err = e.applyFill(ctx, tx, spec, o, pos, sw.VWAP, sw.Volume, swap.RoleTaker); err != nil {
			return err
		}
	}
	if !o.Remaining().IsPositive() || o.Status == swap.OrderCanceled {
		return e.requireFilled(o)
	}

	// 剩余挂单：冻结开仓部分的保证金 + 预估手续费
	ref := o.Price
	if o.Side == swap.SideSell {
		if best, ok := BestEffective(q, swap.SideSell, e.cfg.SpreadBps); ok && best.GreaterThan(ref) {
			ref = best
		}
	}
	freeze, err := e.restingFreeze(ctx, tx, spec, o, pos, ref)
	if err != nil {
		return err
	}
	if freeze.IsPositive() {
		if err := e.exec(ctx, tx, dealer.NewMarginFreeze(o.UserID, spec.MarginCurrency, freeze, decimal.Zero, o.BizID("freeze"))); err != nil {
			return fmt.Errorf("freeze resting order: %w", err)
		}
	}
	o.FrozenAmount = freeze
	if o.FilledVolume.IsPositive() {
		o.Status = swap.OrderPartiallyFilled
	} else {
		o.Status = swap.OrderOpen
	}
	return nil
}

// requireFilled 受理时一张都没成交且已被撤销的订单整体拒绝（事务回滚），避免产生空订单。
func (e *Engine) requireFilled(o *swap.Order) error {
	if o.Status == swap.OrderCanceled && o.FilledVolume.IsZero() {
		return fmt.Errorf("%w: %s", ErrInsufficientMargin, o.ErrMsg)
	}
	return nil
}

// restingFreeze 计算挂单需冻结金额（开仓部分初始保证金 + 开仓部分吃单费率手续费），并做可用保证金检查。
// 平仓部分不冻结：其手续费在平仓结算时从释放的保证金/盈亏中收取。
func (e *Engine) restingFreeze(ctx context.Context, tx *sql.Tx, spec ContractSpec, o *swap.Order, pos *swap.Position, ref decimal.Decimal) (decimal.Decimal, error) {
	openVol := o.Remaining()
	if pos != nil && pos.Status == swap.PositionOpen && pos.Direction != swap.DirectionOf(o.Side) {
		openVol = decimal.Max(openVol.Sub(pos.Volume), decimal.Zero)
	}
	if o.ReduceOnly || !openVol.IsPositive() {
		return decimal.Zero, nil
	}
	n := Notional(spec, ref, openVol)
	if err := e.checkLeverage(spec, o.Leverage, pos, o.Side, ref, openVol); err != nil {
		return decimal.Zero, err
	}
	need := InitialMargin(n, o.Leverage).Add(r18(n.Mul(e.cfg.TakerFeeRate)))
	risk, _, err := e.crossRisk(ctx, tx, o.UserID, spec.MarginCurrency)
	if err != nil {
		return decimal.Zero, err
	}
	if need.GreaterThan(risk.Free) {
		return decimal.Zero, fmt.Errorf("%w: need %s, free %s", ErrInsufficientMargin, need, risk.Free)
	}
	return need, nil
}

// checkLeverage 开仓后仓位名义价值所在阶梯必须允许该杠杆。
func (e *Engine) checkLeverage(spec ContractSpec, lev int, pos *swap.Position, side swap.Side, price, openVol decimal.Decimal) error {
	vol := openVol
	if pos != nil && pos.Status == swap.PositionOpen && pos.Direction == swap.DirectionOf(side) {
		vol = vol.Add(pos.Volume)
	}
	tier := spec.Tier(Notional(spec, price, vol))
	if lev > tier.MaxLeverage {
		return fmt.Errorf("%w: leverage %d > %d for notional tier", ErrLeverageTooHigh, lev, tier.MaxLeverage)
	}
	return nil
}

// Cancel 撤单：释放剩余冻结。
func (e *Engine) Cancel(ctx context.Context, tx *sql.Tx, o *swap.Order) error {
	spec, err := e.spec(o.ContractCode)
	if err != nil {
		return err
	}
	if err := e.lockLedger(ctx, tx, o.UserID, spec.MarginCurrency); err != nil {
		return err
	}
	return e.cancelRemainder(ctx, tx, spec, o, "canceled by user")
}

// Submit Synthetic 在 Accept 中已完成全部处理，无外部动作。
func (e *Engine) Submit(context.Context, *swap.Order) error { return nil }

func (e *Engine) cancelRemainder(ctx context.Context, tx *sql.Tx, spec ContractSpec, o *swap.Order, reason string) error {
	if o.FrozenAmount.IsPositive() {
		if err := e.exec(ctx, tx, dealer.NewMarginRelease(o.UserID, spec.MarginCurrency, o.FrozenAmount, o.BizID("cancel"))); err != nil {
			return fmt.Errorf("release order %d frozen: %w", o.ID, err)
		}
		o.FrozenAmount = decimal.Zero
	}
	e.closeOrder(o, reason)
	return nil
}

func (e *Engine) closeOrder(o *swap.Order, reason string) {
	if o.FilledVolume.Equal(o.Volume) {
		o.Status = swap.OrderFilled
		return
	}
	o.Status, o.ErrMsg = swap.OrderCanceled, reason
}

// cancelAllResting 撤销用户全部 Synthetic 挂单（全仓与逐仓挂单的冻结都来自共享的 SWAP 钱包），except 除外。
func (e *Engine) cancelAllResting(ctx context.Context, tx *sql.Tx, userID int64, except uint64, reason string) error {
	orders, err := swap.ListActiveOrders(ctx, tx, userID, "", "")
	if err != nil {
		return err
	}
	for _, o := range orders {
		if o.ID == except || o.Route != swap.RouteSynthetic {
			continue
		}
		spec, err := e.spec(o.ContractCode)
		if err != nil {
			return err
		}
		if err := e.cancelRemainder(ctx, tx, spec, o, reason); err != nil {
			return err
		}
		if err := swap.UpdateOrder(ctx, tx, o); err != nil {
			return err
		}
	}
	return nil
}

// ---------------------------------------------------------------------------
// 成交（含平反手）
// ---------------------------------------------------------------------------

// applyFill 以 price 成交 vol 张，返回槽位成交后的 OPEN 持仓（可能为 nil）。
//
// 单向持仓“平反手”：若订单方向与持仓相反，先平掉 min(vol, 持仓) 张（结算盈亏、释放保证金、收手续费），
// 剩余部分以同一路由、同一保证金模式反向开新仓（重新占用保证金）。
// 开仓部分可用保证金不足时，只成交平仓部分，订单剩余撤销。
func (e *Engine) applyFill(ctx context.Context, tx *sql.Tx, spec ContractSpec, o *swap.Order, pos *swap.Position,
	price, vol decimal.Decimal, role swap.Role) (*swap.Position, error) {
	if !vol.IsPositive() || vol.GreaterThan(o.Remaining()) {
		return pos, fmt.Errorf("synthetic: invalid fill volume %s for order %d remaining %s", vol, o.ID, o.Remaining())
	}
	o.FillSeq++
	seq := "f" + strconv.Itoa(o.FillSeq)

	// 1. 按比例释放本次成交对应的挂单冻结
	if o.FrozenAmount.IsPositive() {
		rel := o.FrozenAmount
		if vol.LessThan(o.Remaining()) {
			rel = o.FrozenAmount.Mul(vol).DivRound(o.Remaining(), 18)
		}
		if rel.IsPositive() {
			if err := e.exec(ctx, tx, dealer.NewMarginRelease(o.UserID, spec.MarginCurrency, rel, o.BizID(seq, "unfreeze"))); err != nil {
				return pos, fmt.Errorf("release fill frozen: %w", err)
			}
			o.FrozenAmount = o.FrozenAmount.Sub(rel)
		}
	}

	closeVol := decimal.Zero
	if pos != nil && pos.Status == swap.PositionOpen && pos.Direction != swap.DirectionOf(o.Side) {
		closeVol = decimal.Min(vol, pos.Volume)
	}
	openVol := vol.Sub(closeVol)
	if o.ReduceOnly {
		openVol = decimal.Zero
	}
	rate := e.feeRate(role)
	filled := decimal.Zero

	// 2. 平仓部分
	if closeVol.IsPositive() {
		reason := swap.CloseNormal
		if openVol.IsPositive() {
			reason = swap.CloseFlip
		}
		if o.Source == swap.SourceLiquidation {
			reason = swap.CloseLiquidation
		}
		if err := e.settleClose(ctx, tx, spec, o, pos, price, closeVol, rate, role, seq, reason); err != nil {
			return pos, err
		}
		filled = filled.Add(closeVol)
		if pos.Status == swap.PositionClosed {
			pos = nil
		}
	}

	// 3. 开仓部分（含反手）
	var cancelReason string
	if openVol.IsPositive() {
		newPos, err := e.openFill(ctx, tx, spec, o, pos, price, openVol, rate, role, seq)
		switch {
		case err == nil:
			pos = newPos
			filled = filled.Add(openVol)
		case isRiskReject(err) && filled.IsPositive():
			cancelReason = err.Error() // 平仓部分已成交，开仓部分放弃
		case isRiskReject(err) && o.FilledVolume.IsPositive():
			cancelReason = err.Error() // 之前已有成交（挂单后续撮合）
		default:
			return pos, err
		}
	} else if vol.GreaterThan(closeVol) {
		cancelReason = "reduce-only remainder canceled"
	}

	// 4. 订单汇总
	if filled.IsPositive() {
		totalFilled := o.FilledVolume.Add(filled)
		o.AvgFillPrice = o.AvgFillPrice.Mul(o.FilledVolume).Add(price.Mul(filled)).DivRound(totalFilled, 18)
		o.FilledVolume = totalFilled
	}
	switch {
	case cancelReason != "":
		if err := e.cancelRemainder(ctx, tx, spec, o, cancelReason); err != nil {
			return pos, err
		}
	case !o.Remaining().IsPositive():
		o.Status = swap.OrderFilled
	default:
		o.Status = swap.OrderPartiallyFilled
	}
	return pos, nil
}

func isRiskReject(err error) bool {
	return errors.Is(err, ErrInsufficientMargin) || errors.Is(err, ErrLeverageTooHigh) || errors.Is(err, dealer.ErrInsufficientBalance)
}

// openFill 开仓/加仓/反手开仓。
func (e *Engine) openFill(ctx context.Context, tx *sql.Tx, spec ContractSpec, o *swap.Order, pos *swap.Position,
	price, vol, rate decimal.Decimal, role swap.Role, seq string) (*swap.Position, error) {
	if err := e.checkLeverage(spec, o.Leverage, pos, o.Side, price, vol); err != nil {
		return nil, err
	}
	n := Notional(spec, price, vol)
	im := InitialMargin(n, o.Leverage)
	fee := r18(n.Mul(rate))
	risk, _, err := e.crossRisk(ctx, tx, o.UserID, spec.MarginCurrency)
	if err != nil {
		return nil, err
	}
	if im.Add(fee).GreaterThan(risk.Free) {
		return nil, fmt.Errorf("%w: need %s, free %s", ErrInsufficientMargin, im.Add(fee), risk.Free)
	}
	isolated := o.MarginMode == swap.MarginIsolated
	if err := e.exec(ctx, tx, dealer.NewSyntheticOpenFill(o.UserID, spec.MarginCurrency, isolated, im, fee, o.BizID(seq, "open"))); err != nil {
		return nil, fmt.Errorf("open fill ledger: %w", err)
	}

	if pos == nil {
		// 新仓（含反手）：路由与保证金模式继承自订单（订单受理时已按固化规则确定）
		pos = &swap.Position{
			UserID: o.UserID, ContractCode: o.ContractCode, MarginMode: o.MarginMode, Route: o.Route,
			Direction: swap.DirectionOf(o.Side), Leverage: o.Leverage,
		}
		pos.AvgPrice, pos.Volume = price, vol
		if isolated {
			pos.Margin = im
		}
		pos.FeePaid = fee
		pos.LiquidationPrice = e.liqPrice(spec, pos)
		if err := swap.InsertPosition(ctx, tx, pos); err != nil {
			return nil, err
		}
	} else {
		newVol := pos.Volume.Add(vol)
		pos.AvgPrice = pos.AvgPrice.Mul(pos.Volume).Add(price.Mul(vol)).DivRound(newVol, 18)
		pos.Volume = newVol
		if isolated {
			pos.Margin = pos.Margin.Add(im)
		}
		pos.FeePaid = pos.FeePaid.Add(fee)
		pos.LiquidationPrice = e.liqPrice(spec, pos)
		if err := swap.UpdatePosition(ctx, tx, pos); err != nil {
			return nil, err
		}
	}
	o.FeePaid = o.FeePaid.Add(fee)
	if err := swap.InsertTrade(ctx, tx, &swap.Trade{
		OrderID: o.ID, UserID: o.UserID, PositionID: pos.ID, ContractCode: o.ContractCode, MarginMode: o.MarginMode,
		Route: o.Route, Side: o.Side, Kind: swap.TradeOpen, Role: role, Price: price, Volume: vol, Fee: fee,
	}); err != nil {
		return nil, err
	}
	return pos, nil
}

func (e *Engine) liqPrice(spec ContractSpec, p *swap.Position) decimal.Decimal {
	if p.MarginMode != swap.MarginIsolated {
		return decimal.Zero // 全仓强平价取决于整个账户，由风控实时判断
	}
	return IsolatedLiquidationPrice(spec, p)
}

// settleClose 平掉 pos 的 vol 张：结算盈亏与手续费，逐仓按比例释放保证金。穿仓差额由 SYNTHETIC_POOL 兜底。
func (e *Engine) settleClose(ctx context.Context, tx *sql.Tx, spec ContractSpec, o *swap.Order, pos *swap.Position,
	price, vol, rate decimal.Decimal, role swap.Role, seq string, reason swap.CloseReason) error {
	pnl := RealizedPnL(spec, pos, price, vol)
	fee := r18(Notional(spec, price, vol).Mul(rate))
	bizType := dealer.BizSyntheticClose
	if role == swap.RoleLiquidation {
		bizType = dealer.BizSyntheticLiquidate
	}
	p := dealer.SettleParams{
		UserID: o.UserID, Currency: spec.MarginCurrency, BizType: bizType, BizID: o.BizID(seq, "close"),
		PnL: pnl, Fee: fee,
	}
	if pos.MarginMode == swap.MarginIsolated {
		p.Isolated = true
		p.Release = pos.Margin
		if vol.LessThan(pos.Volume) {
			p.Release = pos.Margin.Mul(vol).DivRound(pos.Volume, 18)
		}
	} else {
		bal, err := e.swapBalance(ctx, tx, o.UserID, spec.MarginCurrency)
		if err != nil {
			return err
		}
		// 全仓亏损超过可用余额且仍有挂单冻结：先撤销其它挂单释放资金，再结算
		if pnl.Neg().Add(fee).GreaterThan(bal.Available) && bal.Frozen.IsPositive() {
			if err := e.cancelAllResting(ctx, tx, o.UserID, o.ID, "canceled to cover cross margin loss"); err != nil {
				return err
			}
			if bal, err = e.swapBalance(ctx, tx, o.UserID, spec.MarginCurrency); err != nil {
				return err
			}
		}
		p.Available = bal.Available
	}
	ins, res, err := dealer.NewSyntheticSettlement(p)
	if err != nil {
		return err
	}
	if err := e.exec(ctx, tx, ins); err != nil {
		return fmt.Errorf("close settlement: %w", err)
	}
	if res.LossShortfall.IsPositive() {
		e.logger.Error("bankruptcy covered by synthetic pool",
			slog.Int64("user_id", o.UserID), slog.Uint64("position_id", pos.ID), slog.Uint64("order_id", o.ID),
			slog.String("margin_mode", string(pos.MarginMode)), slog.String("shortfall", res.LossShortfall.String()),
			slog.String("fee_waived", res.FeeWaived.String()))
	}

	pos.Volume = pos.Volume.Sub(vol)
	if p.Isolated {
		pos.Margin = pos.Margin.Sub(p.Release)
	}
	pos.RealizedPnL = pos.RealizedPnL.Add(pnl)
	pos.FeePaid = pos.FeePaid.Add(res.FeeCollected)
	if pos.Volume.IsZero() {
		pos.Status, pos.CloseReason, pos.LiquidationPrice = swap.PositionClosed, reason, decimal.Zero
	} else {
		pos.LiquidationPrice = e.liqPrice(spec, pos)
	}
	if err := swap.UpdatePosition(ctx, tx, pos); err != nil {
		return err
	}
	o.FeePaid = o.FeePaid.Add(res.FeeCollected)
	o.RealizedPnL = o.RealizedPnL.Add(pnl)
	return swap.InsertTrade(ctx, tx, &swap.Trade{
		OrderID: o.ID, UserID: o.UserID, PositionID: pos.ID, ContractCode: o.ContractCode, MarginMode: pos.MarginMode,
		Route: o.Route, Side: o.Side, Kind: swap.TradeClose, Role: role, Price: price, Volume: vol,
		Fee: res.FeeCollected, RealizedPnL: pnl, LossShortfall: res.LossShortfall,
	})
}

// ---------------------------------------------------------------------------
// 资金划出风控
// ---------------------------------------------------------------------------

// MaxTransferOut 返回 SWAP 钱包当前可划出金额（扣除全仓保证金占用与浮亏）。
func (e *Engine) MaxTransferOut(ctx context.Context, userID int64, ccy string) (decimal.Decimal, error) {
	var free decimal.Decimal
	err := storage.WithTx(ctx, e.db, func(tx *sql.Tx) error {
		if err := swap.LockUser(ctx, tx, userID); err != nil {
			return err
		}
		if err := e.lockLedger(ctx, tx, userID, ccy); err != nil {
			return err
		}
		risk, _, err := e.crossRisk(ctx, tx, userID, ccy)
		free = decimal.Max(risk.Free, decimal.Zero)
		return err
	})
	return free, err
}

// TransferOut SWAP -> SPOT 划转。所有从合约钱包划出的资金必须经由此方法（而非直接调用 dealer），
// 以保证全仓仓位的保证金不被划走。
func (e *Engine) TransferOut(ctx context.Context, userID int64, ccy string, amount decimal.Decimal, bizID string) error {
	if !amount.IsPositive() {
		return fmt.Errorf("%w: transfer amount must be > 0", gateway.ErrInvalidRequest)
	}
	return storage.WithTx(ctx, e.db, func(tx *sql.Tx) error {
		if err := swap.LockUser(ctx, tx, userID); err != nil {
			return err
		}
		if err := e.lockLedger(ctx, tx, userID, ccy); err != nil {
			return err
		}
		risk, _, err := e.crossRisk(ctx, tx, userID, ccy)
		if err != nil {
			return err
		}
		if amount.GreaterThan(risk.Free) {
			return fmt.Errorf("%w: transfer %s exceeds free %s", ErrInsufficientMargin, amount, risk.Free)
		}
		return e.exec(ctx, tx, dealer.NewTransfer(userID, dealer.AccountSwap, dealer.AccountSpot, ccy, amount, bizID))
	})
}
