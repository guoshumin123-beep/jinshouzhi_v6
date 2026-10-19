package synthetic

import (
	"github.com/shopspring/decimal"

	"backend_v2/internal/swap"
)

var tenThousand = decimal.NewFromInt(10000)

// effective 返回含平台点差的可成交档位：买单吃卖盘（加价），卖单吃买盘（减价）。
func effective(q Quote, side swap.Side, spreadBps decimal.Decimal) []Level {
	src, factor := q.Asks, one.Add(spreadBps.Div(tenThousand))
	if side == swap.SideSell {
		src, factor = q.Bids, one.Sub(spreadBps.Div(tenThousand))
	}
	out := make([]Level, 0, len(src))
	for _, l := range src {
		if l.Price.IsPositive() && l.Size.IsPositive() {
			out = append(out, Level{Price: r18(l.Price.Mul(factor)), Size: l.Size.Floor()})
		}
	}
	return out
}

// crosses 报告含点差的档位价格是否满足限价（买：价格 <= limit；卖：价格 >= limit）。
func crosses(side swap.Side, price, limit decimal.Decimal) bool {
	if side == swap.SideBuy {
		return price.LessThanOrEqual(limit)
	}
	return price.GreaterThanOrEqual(limit)
}

// SweepResult 一次吃单的聚合结果。
type SweepResult struct {
	Volume decimal.Decimal // 整数张
	VWAP   decimal.Decimal
}

// Sweep 以吃单方式按档位扫盘：limit 非零时只吃满足限价的档位；band 非零时只吃偏离最优价不超过 band 的档位。
func Sweep(q Quote, side swap.Side, maxVol, limit, band, spreadBps decimal.Decimal) SweepResult {
	levels := effective(q, side, spreadBps)
	var (
		vol, notional decimal.Decimal
		best          decimal.Decimal
	)
	for i, l := range levels {
		if i == 0 {
			best = l.Price
		}
		if !limit.IsZero() && !crosses(side, l.Price, limit) {
			break
		}
		if band.IsPositive() && l.Price.Sub(best).Abs().GreaterThan(best.Mul(band)) {
			break
		}
		take := decimal.Min(l.Size, maxVol.Sub(vol))
		if !take.IsPositive() {
			break
		}
		vol = vol.Add(take)
		notional = notional.Add(l.Price.Mul(take))
	}
	if !vol.IsPositive() {
		return SweepResult{}
	}
	return SweepResult{Volume: vol, VWAP: notional.DivRound(vol, 18)}
}

// MakerVolume 挂单（限价 limit）在当前盘口可成交的张数：所有穿越限价的对手档位数量之和。
// 挂单以自身限价成交。
func MakerVolume(q Quote, side swap.Side, limit, maxVol, spreadBps decimal.Decimal) decimal.Decimal {
	var vol decimal.Decimal
	for _, l := range effective(q, side, spreadBps) {
		if !crosses(side, l.Price, limit) {
			break
		}
		vol = vol.Add(l.Size)
	}
	return decimal.Min(vol, maxVol)
}

// BestEffective 含点差的最优可成交价（买单看卖一、卖单看买一）。
func BestEffective(q Quote, side swap.Side, spreadBps decimal.Decimal) (decimal.Decimal, bool) {
	l := effective(q, side, spreadBps)
	if len(l) == 0 {
		return decimal.Zero, false
	}
	return l[0].Price, true
}
