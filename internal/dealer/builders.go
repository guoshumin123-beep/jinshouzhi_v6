package dealer

import (
	"fmt"

	"github.com/shopspring/decimal"
)

// 业务层不直接拼装 Posting，统一使用下列语义化构造器。

func userKey(userID int64, t AccountType, ccy string) AccountKey {
	return AccountKey{OwnerID: userID, Type: t, Currency: ccy}
}

func systemKey(t AccountType, ccy string) AccountKey {
	return AccountKey{OwnerID: SystemOwnerID, Type: t, Currency: ccy}
}

// NewDeposit 充值入账：DEPOSIT_SUSPENSE -> 用户 SPOT。
func NewDeposit(userID int64, ccy string, amt decimal.Decimal, depositID string) Instruction {
	return Instruction{
		BizType: BizDeposit,
		BizID:   depositID,
		Postings: []Posting{
			{Account: systemKey(AccountDepositSuspense, ccy), Op: OpDebit, Amount: amt},
			{Account: userKey(userID, AccountSpot, ccy), Op: OpCredit, Amount: amt},
		},
	}
}

// NewTransfer 用户在自有账户间划转（如 SPOT <-> SWAP）。
func NewTransfer(userID int64, from, to AccountType, ccy string, amt decimal.Decimal, bizID string) Instruction {
	return Instruction{
		BizType: BizTransfer,
		BizID:   bizID,
		Postings: []Posting{
			{Account: userKey(userID, from, ccy), Op: OpDebit, Amount: amt},
			{Account: userKey(userID, to, ccy), Op: OpCredit, Amount: amt},
		},
	}
}

// NewMarginFreeze Synthetic 开仓：冻结保证金 + 预估手续费。
func NewMarginFreeze(userID int64, ccy string, margin, fee decimal.Decimal, orderID string) Instruction {
	return Instruction{
		BizType: BizSwapMarginFreeze,
		BizID:   orderID,
		Postings: []Posting{
			{Account: userKey(userID, AccountSwap, ccy), Op: OpFreeze, Amount: margin.Add(fee)},
		},
	}
}

// NewMarginRelease 撤单/未成交部分解冻。
func NewMarginRelease(userID int64, ccy string, amt decimal.Decimal, bizID string) Instruction {
	return Instruction{
		BizType: BizSwapMarginRelease,
		BizID:   bizID,
		Postings: []Posting{
			{Account: userKey(userID, AccountSwap, ccy), Op: OpUnfreeze, Amount: amt},
		},
	}
}

// NewSyntheticOpenFill Synthetic 开仓成交的资金划转（在订单冻结已解冻回 available 之后调用）：
//
//	全仓：仅扣手续费（SWAP -> FEE_INCOME），仓位保证金由风控引擎按标记价格实时计算，不在账本冻结；
//	逐仓：手续费 + 初始保证金，保证金 SWAP -> SWAP_ISOLATED。
//
// 手续费为 0 的全仓开仓返回空指令（Empty() == true）。
func NewSyntheticOpenFill(userID int64, ccy string, isolated bool, margin, fee decimal.Decimal, bizID string) Instruction {
	swap := userKey(userID, AccountSwap, ccy)
	ins := Instruction{BizType: BizSyntheticOpen, BizID: bizID}
	if fee.IsPositive() {
		ins.Postings = append(ins.Postings,
			Posting{Account: swap, Op: OpDebit, Amount: fee},
			Posting{Account: systemKey(AccountFeeIncome, ccy), Op: OpCredit, Amount: fee})
	}
	if isolated && margin.IsPositive() {
		ins.Postings = append(ins.Postings,
			Posting{Account: swap, Op: OpDebit, Amount: margin},
			Posting{Account: userKey(userID, AccountSwapIsolated, ccy), Op: OpCredit, Amount: margin})
	}
	return ins
}

// SettleParams Synthetic 结算参数（平仓、强平、资金费通用）。
type SettleParams struct {
	UserID   int64
	Currency string
	BizType  BizType // BizSyntheticClose / BizSyntheticLiquidate / BizSyntheticFunding
	BizID    string

	// Isolated=true：结算资金来源为 SWAP_ISOLATED 中释放的 Release（该仓位保证金的对应部分），
	// 亏损上限即 Release，绝不触及 SWAP。
	// Isolated=false（全仓）：结算资金来源为 SWAP 的 Available（调用方须在同一事务内通过 LockBalancesInTx 读取）。
	Isolated  bool
	Release   decimal.Decimal
	Available decimal.Decimal

	// KeepInIsolated 仅逐仓有效：结算剩余留在 SWAP_ISOLATED（资金费场景），否则返还 SWAP（平仓场景）。
	KeepInIsolated bool

	PnL decimal.Decimal // 用户视角已实现盈亏，正为盈利（由 SYNTHETIC_POOL 支付）
	Fee decimal.Decimal // >= 0，计入 FEE_INCOME
}

// SettleResult 结算结果。LossShortfall > 0 表示穿仓：用户资金不足以覆盖亏损，
// 差额由 SYNTHETIC_POOL 兜底（即 POOL 少收）。FeeWaived 为因资金不足而免收的手续费。
type SettleResult struct {
	Payout        decimal.Decimal // 逐仓：结算后返还/留存的保证金
	LossCollected decimal.Decimal
	FeeCollected  decimal.Decimal
	LossShortfall decimal.Decimal
	FeeWaived     decimal.Decimal
}

// NewSyntheticSettlement 生成带穿仓兜底的结算指令。清偿顺序：先亏损（付给 POOL），再手续费，剩余为 Payout。
func NewSyntheticSettlement(p SettleParams) (Instruction, SettleResult, error) {
	var res SettleResult
	if p.Release.IsNegative() || p.Available.IsNegative() || p.Fee.IsNegative() {
		return Instruction{}, res, fmt.Errorf("%w: settle %s negative input", ErrInvalidInstruction, p.BizID)
	}
	switch p.BizType {
	case BizSyntheticClose, BizSyntheticLiquidate, BizSyntheticFunding:
	default:
		return Instruction{}, res, fmt.Errorf("%w: settle biz_type %s", ErrInvalidInstruction, p.BizType)
	}

	gain := decimal.Max(p.PnL, decimal.Zero)
	loss := decimal.Max(p.PnL.Neg(), decimal.Zero)
	funds := p.Available
	if p.Isolated {
		funds = p.Release
	}
	total := funds.Add(gain)
	res.LossCollected = decimal.Min(loss, total)
	res.FeeCollected = decimal.Min(p.Fee, total.Sub(res.LossCollected))
	res.Payout = total.Sub(res.LossCollected).Sub(res.FeeCollected)
	res.LossShortfall = loss.Sub(res.LossCollected)
	res.FeeWaived = p.Fee.Sub(res.FeeCollected)

	swap := userKey(p.UserID, AccountSwap, p.Currency)
	iso := userKey(p.UserID, AccountSwapIsolated, p.Currency)
	pool := systemKey(AccountSyntheticPool, p.Currency)
	feeAcc := systemKey(AccountFeeIncome, p.Currency)

	ins := Instruction{BizType: p.BizType, BizID: p.BizID}
	add := func(k AccountKey, op OpKind, amt decimal.Decimal) {
		if amt.IsPositive() {
			ins.Postings = append(ins.Postings, Posting{Account: k, Op: op, Amount: amt})
		}
	}
	add(pool, OpDebit, gain)
	switch {
	case !p.Isolated:
		// 全仓：SWAP 净变动 = gain - lossCollected - feeCollected
		net := gain.Sub(res.LossCollected).Sub(res.FeeCollected)
		if net.IsPositive() {
			add(swap, OpCredit, net)
		} else {
			add(swap, OpDebit, net.Neg())
		}
	case p.KeepInIsolated:
		net := res.Payout.Sub(p.Release)
		if net.IsPositive() {
			add(iso, OpCredit, net)
		} else {
			add(iso, OpDebit, net.Neg())
		}
	default:
		add(iso, OpDebit, p.Release)
		add(swap, OpCredit, res.Payout)
	}
	add(pool, OpCredit, res.LossCollected)
	add(feeAcc, OpCredit, res.FeeCollected)
	return ins, res, nil
}

// NewStandardTransferOut Standard 用户资金由本地 SWAP 转入火币子账户托管镜像。
// 真实的母子账户划转由 outbox worker 异步执行（不在 dealer 事务内调用外部 API）。
func NewStandardTransferOut(userID int64, ccy string, amt decimal.Decimal, bizID string) Instruction {
	return Instruction{
		BizType: BizStandardTransferOut,
		BizID:   bizID,
		Postings: []Posting{
			{Account: userKey(userID, AccountSwap, ccy), Op: OpDebit, Amount: amt},
			{Account: userKey(userID, AccountHTXCustody, ccy), Op: OpCredit, Amount: amt},
		},
	}
}

// NewStandardTransferIn 资金由火币子账户托管镜像回到本地 SWAP（转入子账户的回滚）。
func NewStandardTransferIn(userID int64, ccy string, amt decimal.Decimal, bizID string) Instruction {
	return Instruction{
		BizType: BizStandardTransferIn,
		BizID:   bizID,
		Postings: []Posting{
			{Account: userKey(userID, AccountHTXCustody, ccy), Op: OpDebit, Amount: amt},
			{Account: userKey(userID, AccountSwap, ccy), Op: OpCredit, Amount: amt},
		},
	}
}

// NewCustodyFreeze 从子账户转出前冻结镜像余额（余额不足即拒绝发起）。
func NewCustodyFreeze(userID int64, ccy string, amt decimal.Decimal, bizID string) Instruction {
	return Instruction{BizType: BizCustodyFreeze, BizID: bizID, Postings: []Posting{
		{Account: userKey(userID, AccountHTXCustody, ccy), Op: OpFreeze, Amount: amt},
	}}
}

// NewCustodyRelease 转出失败回滚：解冻镜像余额。
func NewCustodyRelease(userID int64, ccy string, amt decimal.Decimal, bizID string) Instruction {
	return Instruction{BizType: BizCustodyRelease, BizID: bizID, Postings: []Posting{
		{Account: userKey(userID, AccountHTXCustody, ccy), Op: OpUnfreeze, Amount: amt},
	}}
}

// NewCustodySettleOut 转出完成（资金已回到母账户）：扣除冻结的镜像余额，计入本地 SWAP。
func NewCustodySettleOut(userID int64, ccy string, amt decimal.Decimal, bizID string) Instruction {
	return Instruction{BizType: BizStandardTransferIn, BizID: bizID, Postings: []Posting{
		{Account: userKey(userID, AccountHTXCustody, ccy), Op: OpDebitFrozen, Amount: amt},
		{Account: userKey(userID, AccountSwap, ccy), Op: OpCredit, Amount: amt},
	}}
}

// NewHTXReconcile 将子账户真实权益与本地镜像的差额入账，对手方为 HTX_PNL。
func NewHTXReconcile(userID int64, ccy string, delta decimal.Decimal, snapshotID string) (Instruction, error) {
	if delta.IsZero() {
		return Instruction{}, fmt.Errorf("%w: reconcile %s zero delta", ErrInvalidInstruction, snapshotID)
	}
	custody := userKey(userID, AccountHTXCustody, ccy)
	pnl := systemKey(AccountHTXPnL, ccy)
	amt := delta.Abs()
	ps := []Posting{
		{Account: pnl, Op: OpDebit, Amount: amt},
		{Account: custody, Op: OpCredit, Amount: amt},
	}
	if delta.IsNegative() {
		ps = []Posting{
			{Account: custody, Op: OpDebit, Amount: amt},
			{Account: pnl, Op: OpCredit, Amount: amt},
		}
	}
	return Instruction{BizType: BizHTXReconcile, BizID: snapshotID, Postings: ps}, nil
}
