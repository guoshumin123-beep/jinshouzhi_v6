// Package dealer 是整个系统唯一的资产扣减与账本引擎。
// 所有余额变动必须通过 Dealer.Execute 下发原子事务，禁止任何其它代码直接修改 accounts 表。
package dealer

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/shopspring/decimal"
)

// AccountType 账户类型。用户账户 owner_id > 0，系统账户 owner_id == 0。
type AccountType string

const (
	// 用户账户
	AccountSpot       AccountType = "SPOT"
	AccountSwap       AccountType = "SWAP"
	AccountHTXCustody AccountType = "HTX_CUSTODY" // Standard 用户常驻火币子账户资金的本地镜像
	// Synthetic 逐仓保证金。与 SWAP（全仓钱包）在账本层面严格隔离：
	// 逐仓亏损上限为其已划入的保证金，绝不触及 SWAP。
	AccountSwapIsolated AccountType = "SWAP_ISOLATED"

	// 系统账户（允许为负）
	AccountSyntheticPool    AccountType = "SYNTHETIC_POOL"    // Synthetic 用户的对手方
	AccountFeeIncome        AccountType = "FEE_INCOME"        // 手续费收入
	AccountDepositSuspense  AccountType = "DEPOSIT_SUSPENSE"  // 充值在途
	AccountWithdrawSuspense AccountType = "WITHDRAW_SUSPENSE" // 提现在途
	AccountHTXPnL           AccountType = "HTX_PNL"           // 子账户对账差额的对手方
)

// SystemOwnerID 系统账户的 owner_id。
const SystemOwnerID int64 = 0

// IsSystem 报告该类型是否为系统账户。须与 migration 中 chk_accounts_non_negative 保持一致。
func (t AccountType) IsSystem() bool {
	switch t {
	case AccountSyntheticPool, AccountFeeIncome, AccountDepositSuspense, AccountWithdrawSuspense, AccountHTXPnL:
		return true
	}
	return false
}

func (t AccountType) valid() bool {
	switch t {
	case AccountSpot, AccountSwap, AccountHTXCustody, AccountSwapIsolated:
		return true
	}
	return t.IsSystem()
}

// AccountKey 唯一定位一个余额账户。
type AccountKey struct {
	OwnerID  int64
	Type     AccountType
	Currency string
}

func (k AccountKey) String() string {
	return fmt.Sprintf("%d/%s/%s", k.OwnerID, k.Type, k.Currency)
}

// less 定义全局固定的加锁顺序，防止死锁。
func (k AccountKey) less(o AccountKey) bool {
	if k.OwnerID != o.OwnerID {
		return k.OwnerID < o.OwnerID
	}
	if k.Type != o.Type {
		return k.Type < o.Type
	}
	return k.Currency < o.Currency
}

// OpKind 单条分录的操作类型。
type OpKind string

const (
	OpCredit      OpKind = "CREDIT"       // available += amt
	OpDebit       OpKind = "DEBIT"        // available -= amt
	OpFreeze      OpKind = "FREEZE"       // available -> frozen
	OpUnfreeze    OpKind = "UNFREEZE"     // frozen -> available
	OpDebitFrozen OpKind = "DEBIT_FROZEN" // frozen -= amt
)

// deltas 返回该操作对 (available, frozen) 的影响。
func (op OpKind) deltas(amt decimal.Decimal) (avail, frozen decimal.Decimal, ok bool) {
	switch op {
	case OpCredit:
		return amt, decimal.Zero, true
	case OpDebit:
		return amt.Neg(), decimal.Zero, true
	case OpFreeze:
		return amt.Neg(), amt, true
	case OpUnfreeze:
		return amt, amt.Neg(), true
	case OpDebitFrozen:
		return decimal.Zero, amt.Neg(), true
	}
	return decimal.Zero, decimal.Zero, false
}

// BizType 业务类型，与 BizID 共同构成幂等键。
type BizType string

const (
	BizDeposit             BizType = "DEPOSIT"
	BizWithdraw            BizType = "WITHDRAW"
	BizTransfer            BizType = "TRANSFER"
	BizSwapMarginFreeze    BizType = "SWAP_MARGIN_FREEZE"
	BizSwapMarginRelease   BizType = "SWAP_MARGIN_RELEASE"
	BizSyntheticOpen       BizType = "SYNTHETIC_OPEN"      // 开仓成交：手续费 + 逐仓保证金划转
	BizSyntheticClose      BizType = "SYNTHETIC_CLOSE"     // 平仓成交：盈亏 + 手续费
	BizSyntheticLiquidate  BizType = "SYNTHETIC_LIQUIDATE" // 强平成交
	BizSyntheticFunding    BizType = "SYNTHETIC_FUNDING"   // 资金费
	BizStandardTransferOut BizType = "STANDARD_TRANSFER_OUT"
	BizHTXReconcile        BizType = "HTX_RECONCILE"
	BizStandardTransferIn  BizType = "STANDARD_TRANSFER_IN" // HTX_CUSTODY -> SWAP（转出子账户完成 / 转入回滚）
	BizCustodyFreeze       BizType = "CUSTODY_FREEZE"       // 转出子账户前冻结镜像余额
	BizCustodyRelease      BizType = "CUSTODY_RELEASE"      // 转出子账户失败回滚，解冻镜像余额
)

func (b BizType) valid() bool {
	switch b {
	case BizDeposit, BizWithdraw, BizTransfer, BizSwapMarginFreeze, BizSwapMarginRelease,
		BizSyntheticOpen, BizSyntheticClose, BizSyntheticLiquidate, BizSyntheticFunding,
		BizStandardTransferOut, BizHTXReconcile, BizStandardTransferIn, BizCustodyFreeze, BizCustodyRelease:
		return true
	}
	return false
}

// Posting 一条分录。Amount 必须 > 0。
type Posting struct {
	Account AccountKey
	Op      OpKind
	Amount  decimal.Decimal
}

// Instruction 一个原子账本事务。
type Instruction struct {
	BizType  BizType
	BizID    string
	Postings []Posting
	Memo     map[string]string // 仅作审计元数据，不参与幂等比对
}

// Empty 报告指令是否不含任何分录（例如手续费为 0 的全仓开仓），此类指令无需执行。
func (ins Instruction) Empty() bool { return len(ins.Postings) == 0 }

// BalanceSnapshot 账户余额快照。
type BalanceSnapshot struct {
	Key       AccountKey
	Available decimal.Decimal
	Frozen    decimal.Decimal
	Version   uint64
}

// Receipt Execute 的执行回执。
type Receipt struct {
	TxnID    uint64
	Replayed bool // true 表示命中幂等，返回的是首次执行的结果
	Balances map[AccountKey]BalanceSnapshot
}

// Dealer 全系统唯一可修改余额的组件。
type Dealer interface {
	Execute(ctx context.Context, ins Instruction) (*Receipt, error)
	Balance(ctx context.Context, key AccountKey) (BalanceSnapshot, error)
}

// TxDealer 额外支持在调用方事务中执行，供 gateway 等需要跨表原子性的模块使用。
type TxDealer interface {
	Dealer
	ExecuteInTx(ctx context.Context, tx *sql.Tx, ins Instruction) (*Receipt, error)
	// LockBalancesInTx 在调用方事务内对账户加排他锁并返回余额（账户不存在时创建）。
	// 用于“先读余额再决定记账金额”的场景（如穿仓兜底），读取与后续记账处于同一把锁内。
	LockBalancesInTx(ctx context.Context, tx *sql.Tx, keys ...AccountKey) (map[AccountKey]BalanceSnapshot, error)
}

var (
	ErrInvalidInstruction  = errors.New("dealer: invalid instruction")
	ErrUnbalanced          = errors.New("dealer: postings do not sum to zero")
	ErrInsufficientBalance = errors.New("dealer: insufficient balance")
	ErrIdempotencyConflict = errors.New("dealer: biz key reused with different payload")
)
