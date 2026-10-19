// Package synthetic 本地虚拟流动性引擎：以火币实时盘口为裁判价，在本地完成撮合、保证金、
// 强平与资金费。同时完整支持逐仓与全仓：
//
//   - 账本隔离：全仓钱包 = dealer SWAP 账户；逐仓保证金 = dealer SWAP_ISOLATED 账户。
//     逐仓亏损上限为其保证金，绝不触及 SWAP；穿仓差额由 SYNTHETIC_POOL 兜底。
//   - 全仓仓位保证金不在账本冻结，而由风控引擎按标记价格实时计算（与火币全仓口径一致），
//     挂单保证金 + 预估手续费冻结在 SWAP.frozen。
//
// 并发：所有操作都在持有 swap.LockUser 的事务内进行；随后按固定顺序锁定
// [FEE_INCOME, SYNTHETIC_POOL, SPOT, SWAP, SWAP_ISOLATED]，与 dealer 其它路径不形成锁环。
package synthetic

import (
	"errors"
	"time"

	"github.com/shopspring/decimal"
)

// Level 盘口档位。Size 单位为张。
type Level struct {
	Price decimal.Decimal
	Size  decimal.Decimal
}

// Quote 裁判盘口：Bids 价格降序，Asks 价格升序（来自火币深度行情，不含平台点差）。
type Quote struct {
	Bids []Level
	Asks []Level
	At   time.Time
}

// PriceSource 由 internal/marketdata（火币 WS 深度订阅）实现。
type PriceSource interface {
	Quote(contractCode string) (Quote, bool)
}

// MarkSource 标记价格（盯市、强平、资金费、全仓保证金计算）。
type MarkSource interface {
	Mark(contractCode string) (price decimal.Decimal, at time.Time, ok bool)
}

// FundingEvent 一次已到期的资金费结算点。Rate 为正时多头向空头支付。
type FundingEvent struct {
	Rate        decimal.Decimal
	FundingTime time.Time
}

// FundingSource 返回最近一次已到期（FundingTime <= now）的资金费结算点。
type FundingSource interface {
	LastFunding(contractCode string) (FundingEvent, bool)
}

// RiskTier 风险限额阶梯（动态维持保证金率）：仓位名义价值 <= MaxNotional 时适用。
// 最后一档 MaxNotional 为 0 表示无上限。
type RiskTier struct {
	MaxNotional decimal.Decimal
	MMR         decimal.Decimal // 维持保证金率
	MaxLeverage int
}

// ContractSpec 合约规格。
type ContractSpec struct {
	Size           decimal.Decimal // 每张合约对应的标的数量，如 BTC-USDT 为 0.001
	MarginCurrency string          // 如 USDT
	Tiers          []RiskTier      // 按 MaxNotional 升序
}

type SpecSource interface {
	Spec(contractCode string) (ContractSpec, bool)
}

// Config 引擎参数（费率均为名义价值的比例）。
type Config struct {
	MakerFeeRate       decimal.Decimal
	TakerFeeRate       decimal.Decimal
	LiquidationFeeRate decimal.Decimal
	// SpreadBps 平台点差（单边，基点）：买入价 = 卖盘价 × (1 + bps/10000)，卖出价 = 买盘价 × (1 - bps/10000)。
	SpreadBps decimal.Decimal
	// MarketBand 市价单最大偏离最优价比例（滑点保护），超出部分撤销，如 0.05。
	MarketBand  decimal.Decimal
	MaxQuoteAge time.Duration
	MaxMarkAge  time.Duration
}

var (
	ErrStaleQuote         = errors.New("synthetic: quote unavailable or stale")
	ErrStaleMark          = errors.New("synthetic: mark price unavailable or stale")
	ErrUnknownContract    = errors.New("synthetic: unknown contract")
	ErrNoLiquidity        = errors.New("synthetic: no liquidity within price band")
	ErrInsufficientMargin = errors.New("synthetic: insufficient available margin")
	ErrLeverageTooHigh    = errors.New("synthetic: leverage exceeds risk tier limit")
	ErrReduceOnly         = errors.New("synthetic: reduce-only order would open a position")
)
