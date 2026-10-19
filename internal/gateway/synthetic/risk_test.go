package synthetic

import (
	"testing"
	"time"

	"github.com/shopspring/decimal"

	"backend_v2/internal/swap"
)

func d(s string) decimal.Decimal { return decimal.RequireFromString(s) }

var btc = ContractSpec{
	Size: d("0.001"), MarginCurrency: "USDT",
	Tiers: []RiskTier{
		{MaxNotional: d("100000"), MMR: d("0.005"), MaxLeverage: 100},
		{MaxNotional: d("500000"), MMR: d("0.01"), MaxLeverage: 50},
		{MMR: d("0.025"), MaxLeverage: 20},
	},
}

func TestTierSelection(t *testing.T) {
	for _, tc := range []struct {
		notional string
		mmr      string
		lev      int
	}{{"1", "0.005", 100}, {"100000", "0.005", 100}, {"100000.01", "0.01", 50}, {"9999999", "0.025", 20}} {
		tier := btc.Tier(d(tc.notional))
		if !tier.MMR.Equal(d(tc.mmr)) || tier.MaxLeverage != tc.lev {
			t.Errorf("notional %s -> %+v", tc.notional, tier)
		}
	}
}

// 在预估强平价处，逐仓权益应恰好等于维持保证金。
func TestIsolatedLiquidationPriceIsBreakEven(t *testing.T) {
	for _, dir := range []swap.Direction{swap.DirectionLong, swap.DirectionShort} {
		p := &swap.Position{Direction: dir, Volume: d("100"), AvgPrice: d("60060"), Margin: d("600.6"), MarginMode: swap.MarginIsolated}
		liq := IsolatedLiquidationPrice(btc, p)
		r := ComputeIsolated(btc, p, liq)
		if r.Equity.Sub(r.Maintenance).Abs().GreaterThan(d("0.000001")) {
			t.Errorf("%s liq %s: equity %s != mm %s", dir, liq, r.Equity, r.Maintenance)
		}
		// 越过强平价一点点即可强平；回到开仓价则安全
		worse := liq.Sub(d("1"))
		if dir == swap.DirectionShort {
			worse = liq.Add(d("1"))
		}
		if !ComputeIsolated(btc, p, worse).Liquidatable() || ComputeIsolated(btc, p, p.AvgPrice).Liquidatable() {
			t.Errorf("%s liquidation boundary wrong", dir)
		}
	}
	// 长仓：(60060*0.1 - 600.6) / (0.1*0.995)
	long := &swap.Position{Direction: swap.DirectionLong, Volume: d("100"), AvgPrice: d("60060"), Margin: d("600.6")}
	if got, want := IsolatedLiquidationPrice(btc, long), d("5405.4").DivRound(d("0.0995"), 18); !got.Equal(want) {
		t.Fatalf("long liq = %s, want %s", got, want)
	}
}

func TestComputeCross(t *testing.T) {
	long := &swap.Position{Direction: swap.DirectionLong, Volume: d("100"), AvgPrice: d("60060"), Leverage: 10}
	r := ComputeCross(d("9996.997"), d("10"), []CrossPosition{{Position: long, Spec: btc, Mark: d("60000")}})
	// uPnL = -6；IM = 6000/10 = 600；MM = 6000*0.005 = 30
	checks := map[string][2]decimal.Decimal{
		"upnl":   {r.UnrealizedPnL, d("-6")},
		"im":     {r.InitialMargin, d("600")},
		"mm":     {r.Maintenance, d("30")},
		"equity": {r.Equity, d("10000.997")},
		"free":   {r.Free, d("9390.997")},
	}
	for name, c := range checks {
		if !c[0].Equal(c[1]) {
			t.Errorf("%s = %s, want %s", name, c[0], c[1])
		}
	}
	if r.Liquidatable() {
		t.Fatal("healthy account flagged")
	}
	// 浮盈不可用于开仓
	r2 := ComputeCross(d("100"), d("0"), []CrossPosition{{Position: long, Spec: btc, Mark: d("70000")}})
	if !r2.Free.Equal(d("100").Sub(d("700"))) {
		t.Fatalf("free with profit = %s", r2.Free)
	}
}

func TestSweepSpreadBandAndLimit(t *testing.T) {
	q := Quote{
		Asks: []Level{{d("60000"), d("10")}, {d("60100"), d("10")}, {d("70000"), d("100")}},
		Bids: []Level{{d("59990"), d("5")}, {d("59000"), d("50")}},
		At:   time.Now(),
	}
	spread := d("10") // 10bps
	// 买：吃前两档（含点差 60060、60160.1），第三档超出 5% 价格带
	r := Sweep(q, swap.SideBuy, d("100"), decimal.Zero, d("0.05"), spread)
	wantVWAP := d("60060").Mul(d("10")).Add(d("60160.1").Mul(d("10"))).DivRound(d("20"), 18)
	if !r.Volume.Equal(d("20")) || !r.VWAP.Equal(wantVWAP) {
		t.Fatalf("sweep = %+v, want 20 @ %s", r, wantVWAP)
	}
	// 限价 60100：只有第一档（60060）满足
	if r := Sweep(q, swap.SideBuy, d("100"), d("60100"), decimal.Zero, spread); !r.Volume.Equal(d("10")) || !r.VWAP.Equal(d("60060")) {
		t.Fatalf("limit sweep = %+v", r)
	}
	// 卖：买一 59990 × 0.999 = 59930.01
	if p, _ := BestEffective(q, swap.SideSell, spread); !p.Equal(d("59930.01")) {
		t.Fatalf("best sell = %s", p)
	}
	// 挂单可成交量：卖限价 59000 时两档买盘（59930.01、58941）中仅第一档穿越
	if v := MakerVolume(q, swap.SideSell, d("59000"), d("100"), spread); !v.Equal(d("5")) {
		t.Fatalf("maker volume = %s", v)
	}
}
