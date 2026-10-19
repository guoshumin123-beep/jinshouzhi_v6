package synthetic

import (
	"fmt"

	"github.com/shopspring/decimal"

	"backend_v2/internal/swap"
)

// 本文件为纯函数风控计算，所有金额保留 18 位小数（与账本精度一致）。

func r18(d decimal.Decimal) decimal.Decimal { return d.Round(18) }

var one = decimal.NewFromInt(1)

// Tier 返回名义价值所处的风险阶梯。
func (s ContractSpec) Tier(notional decimal.Decimal) RiskTier {
	for _, t := range s.Tiers {
		if t.MaxNotional.IsZero() || notional.LessThanOrEqual(t.MaxNotional) {
			return t
		}
	}
	return s.Tiers[len(s.Tiers)-1]
}

func (s ContractSpec) validate() error {
	if !s.Size.IsPositive() || s.MarginCurrency == "" || len(s.Tiers) == 0 {
		return fmt.Errorf("%w: incomplete spec", ErrUnknownContract)
	}
	return nil
}

// Notional 名义价值 = 价格 × 张数 × 合约面值。
func Notional(spec ContractSpec, price, volume decimal.Decimal) decimal.Decimal {
	return r18(price.Mul(volume).Mul(spec.Size))
}

// InitialMargin 初始保证金 = 名义价值 / 杠杆。
func InitialMargin(notional decimal.Decimal, leverage int) decimal.Decimal {
	return notional.DivRound(decimal.NewFromInt(int64(leverage)), 18)
}

// MaintenanceMargin 维持保证金 = 名义价值 × 所在阶梯 MMR（动态维持保证金率）。
func MaintenanceMargin(spec ContractSpec, notional decimal.Decimal) decimal.Decimal {
	return r18(notional.Mul(spec.Tier(notional).MMR))
}

// UnrealizedPnL 未实现盈亏（按标记价）。
func UnrealizedPnL(spec ContractSpec, p *swap.Position, mark decimal.Decimal) decimal.Decimal {
	return r18(mark.Sub(p.AvgPrice).Mul(p.Volume).Mul(spec.Size).Mul(p.Direction.Sign()))
}

// RealizedPnL 平掉 volume 张的已实现盈亏。
func RealizedPnL(spec ContractSpec, p *swap.Position, price, volume decimal.Decimal) decimal.Decimal {
	return r18(price.Sub(p.AvgPrice).Mul(volume).Mul(spec.Size).Mul(p.Direction.Sign()))
}

// IsolatedLiquidationPrice 逐仓预估强平价：权益(保证金 + 未实现盈亏) = 维持保证金 时的价格。
//
//	多：P = (E·Q − M) / (Q·(1 − mmr))
//	空：P = (E·Q + M) / (Q·(1 + mmr))
//
// 其中 Q = 张数 × 面值，mmr 取当前名义价值所在阶梯。结果小于 0 时返回 0（不会被强平）。
func IsolatedLiquidationPrice(spec ContractSpec, p *swap.Position) decimal.Decimal {
	if !p.Volume.IsPositive() {
		return decimal.Zero
	}
	q := p.Volume.Mul(spec.Size)
	mmr := spec.Tier(Notional(spec, p.AvgPrice, p.Volume)).MMR
	var price decimal.Decimal
	if p.Direction == swap.DirectionLong {
		denom := q.Mul(one.Sub(mmr))
		if !denom.IsPositive() {
			return decimal.Zero
		}
		price = p.AvgPrice.Mul(q).Sub(p.Margin).DivRound(denom, 18)
	} else {
		price = p.AvgPrice.Mul(q).Add(p.Margin).DivRound(q.Mul(one.Add(mmr)), 18)
	}
	return decimal.Max(price, decimal.Zero)
}

// IsolatedRisk 逐仓仓位风险。
type IsolatedRisk struct {
	Equity      decimal.Decimal // 保证金 + 未实现盈亏
	Maintenance decimal.Decimal
}

func (r IsolatedRisk) Liquidatable() bool { return r.Equity.LessThanOrEqual(r.Maintenance) }

func ComputeIsolated(spec ContractSpec, p *swap.Position, mark decimal.Decimal) IsolatedRisk {
	return IsolatedRisk{
		Equity:      p.Margin.Add(UnrealizedPnL(spec, p, mark)),
		Maintenance: MaintenanceMargin(spec, Notional(spec, mark, p.Volume)),
	}
}

// CrossPosition 参与全仓计算的仓位及其规格、标记价。
type CrossPosition struct {
	Position *swap.Position
	Spec     ContractSpec
	Mark     decimal.Decimal
}

// CrossRisk 全仓账户风险（单一保证金币种）。
type CrossRisk struct {
	WalletAvailable decimal.Decimal // SWAP.available
	WalletFrozen    decimal.Decimal // SWAP.frozen（挂单冻结）
	UnrealizedPnL   decimal.Decimal
	InitialMargin   decimal.Decimal // Σ 标记价名义价值 / 杠杆
	Maintenance     decimal.Decimal // Σ 动态维持保证金
	Equity          decimal.Decimal // 钱包 + 未实现盈亏
	// Free 可用于开新仓（全仓或逐仓）与划出的金额：
	//   available + min(0, 未实现盈亏) − 全仓初始保证金占用（浮盈不可用于开仓/划出）
	Free decimal.Decimal
}

// RiskRatio 风险率 = 权益 / 维持保证金；无全仓仓位时返回 (0, false)。
func (r CrossRisk) RiskRatio() (decimal.Decimal, bool) {
	if !r.Maintenance.IsPositive() {
		return decimal.Zero, false
	}
	return r.Equity.DivRound(r.Maintenance, 8), true
}

// Liquidatable 全仓权益 <= 全部维持保证金时触发强平。
func (r CrossRisk) Liquidatable() bool {
	return r.Maintenance.IsPositive() && r.Equity.LessThanOrEqual(r.Maintenance)
}

func ComputeCross(available, frozen decimal.Decimal, positions []CrossPosition) CrossRisk {
	r := CrossRisk{WalletAvailable: available, WalletFrozen: frozen}
	for _, cp := range positions {
		n := Notional(cp.Spec, cp.Mark, cp.Position.Volume)
		r.UnrealizedPnL = r.UnrealizedPnL.Add(UnrealizedPnL(cp.Spec, cp.Position, cp.Mark))
		r.InitialMargin = r.InitialMargin.Add(InitialMargin(n, cp.Position.Leverage))
		r.Maintenance = r.Maintenance.Add(MaintenanceMargin(cp.Spec, n))
	}
	r.Equity = available.Add(frozen).Add(r.UnrealizedPnL)
	r.Free = available.Add(decimal.Min(r.UnrealizedPnL, decimal.Zero)).Sub(r.InitialMargin)
	return r
}
