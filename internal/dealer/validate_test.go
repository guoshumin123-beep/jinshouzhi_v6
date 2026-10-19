package dealer

import (
	"errors"
	"fmt"
	"testing"

	"github.com/shopspring/decimal"
)

func d(s string) decimal.Decimal { return decimal.RequireFromString(s) }

func TestValidate(t *testing.T) {
	spot := AccountKey{OwnerID: 1, Type: AccountSpot, Currency: "USDT"}
	swap := AccountKey{OwnerID: 1, Type: AccountSwap, Currency: "USDT"}

	cases := []struct {
		name string
		ins  Instruction
		want error
	}{
		{"ok transfer", NewTransfer(1, AccountSpot, AccountSwap, "USDT", d("10"), "t1"), nil},
		{"ok freeze only", NewMarginFreeze(1, "USDT", d("5"), d("0.1"), "o1"), nil},
		{"unbalanced", Instruction{BizType: BizTransfer, BizID: "x", Postings: []Posting{
			{Account: spot, Op: OpDebit, Amount: d("10")},
			{Account: swap, Op: OpCredit, Amount: d("9.99")},
		}}, ErrUnbalanced},
		{"unbalanced per currency", Instruction{BizType: BizTransfer, BizID: "x", Postings: []Posting{
			{Account: spot, Op: OpDebit, Amount: d("10")},
			{Account: AccountKey{OwnerID: 1, Type: AccountSwap, Currency: "BTC"}, Op: OpCredit, Amount: d("10")},
		}}, ErrUnbalanced},
		{"zero amount", Instruction{BizType: BizTransfer, BizID: "x", Postings: []Posting{
			{Account: spot, Op: OpFreeze, Amount: d("0")},
		}}, ErrInvalidInstruction},
		{"too precise", Instruction{BizType: BizTransfer, BizID: "x", Postings: []Posting{
			{Account: spot, Op: OpFreeze, Amount: d("0.0000000000000000001")},
		}}, ErrInvalidInstruction},
		{"user type with system owner", Instruction{BizType: BizTransfer, BizID: "x", Postings: []Posting{
			{Account: AccountKey{OwnerID: 0, Type: AccountSpot, Currency: "USDT"}, Op: OpFreeze, Amount: d("1")},
		}}, ErrInvalidInstruction},
		{"system type with user owner", Instruction{BizType: BizTransfer, BizID: "x", Postings: []Posting{
			{Account: AccountKey{OwnerID: 1, Type: AccountSyntheticPool, Currency: "USDT"}, Op: OpFreeze, Amount: d("1")},
		}}, ErrInvalidInstruction},
		{"lower-case currency", Instruction{BizType: BizTransfer, BizID: "x", Postings: []Posting{
			{Account: AccountKey{OwnerID: 1, Type: AccountSpot, Currency: "usdt"}, Op: OpFreeze, Amount: d("1")},
		}}, ErrInvalidInstruction},
		{"unknown op", Instruction{BizType: BizTransfer, BizID: "x", Postings: []Posting{
			{Account: spot, Op: "STEAL", Amount: d("1")},
		}}, ErrInvalidInstruction},
		{"unknown biz", Instruction{BizType: "HACK", BizID: "x", Postings: []Posting{
			{Account: spot, Op: OpFreeze, Amount: d("1")},
		}}, ErrInvalidInstruction},
		{"empty biz id", Instruction{BizType: BizTransfer, Postings: []Posting{
			{Account: spot, Op: OpFreeze, Amount: d("1")},
		}}, ErrInvalidInstruction},
		{"no postings", Instruction{BizType: BizTransfer, BizID: "x"}, ErrInvalidInstruction},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := validate(tc.ins)
			if tc.want == nil && err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if tc.want != nil && !errors.Is(err, tc.want) {
				t.Fatalf("want %v, got %v", tc.want, err)
			}
		})
	}
}

func TestBuildersAreBalanced(t *testing.T) {
	var settles []Instruction
	for i, p := range []SettleParams{
		{Isolated: true, Release: d("100"), PnL: d("25.5"), Fee: d("0.3")},
		{Isolated: true, Release: d("100"), PnL: d("-60"), Fee: d("0.3")},
		{Isolated: true, Release: d("100"), PnL: d("-99.7"), Fee: d("0.3")},
		{Isolated: true, Release: d("100"), PnL: d("-150"), Fee: d("0.3")},
		{Isolated: true, Release: d("100"), PnL: d("-3"), KeepInIsolated: true},
		{Isolated: true, Release: d("0"), PnL: d("3"), KeepInIsolated: true},
		{Available: d("500"), PnL: d("12"), Fee: d("1")},
		{Available: d("500"), PnL: d("-12"), Fee: d("1")},
		{Available: d("5"), PnL: d("-12"), Fee: d("1")},
	} {
		p.UserID, p.Currency, p.BizType, p.BizID = 1, "USDT", BizSyntheticClose, fmt.Sprintf("s%d", i)
		ins, _, err := NewSyntheticSettlement(p)
		if err != nil {
			t.Fatal(err)
		}
		settles = append(settles, ins)
	}
	recUp, err := NewHTXReconcile(1, "USDT", d("12.34"), "r1")
	if err != nil {
		t.Fatal(err)
	}
	recDown, err := NewHTXReconcile(1, "USDT", d("-12.34"), "r2")
	if err != nil {
		t.Fatal(err)
	}
	all := append([]Instruction{
		NewDeposit(1, "USDT", d("1000"), "dep1"),
		NewTransfer(1, AccountSpot, AccountSwap, "USDT", d("10"), "t1"),
		NewMarginFreeze(1, "USDT", d("10"), d("0.01"), "o1"),
		NewMarginRelease(1, "USDT", d("10"), "o1-cancel"),
		NewStandardTransferOut(1, "USDT", d("10"), "so1"),
		NewSyntheticOpenFill(1, "USDT", true, d("10"), d("0.1"), "of1"),
		NewSyntheticOpenFill(1, "USDT", false, d("10"), d("0.1"), "of2"),
		recUp, recDown,
	}, settles...)
	for _, ins := range all {
		if err := validate(ins); err != nil {
			t.Errorf("%s/%s: %v", ins.BizType, ins.BizID, err)
		}
	}
}

func TestSettlementWaterfall(t *testing.T) {
	cases := []struct {
		name                                     string
		p                                        SettleParams
		payout, lossC, feeC, shortfall, feeWaive string
	}{
		{"iso profit", SettleParams{Isolated: true, Release: d("100"), PnL: d("20"), Fee: d("1")}, "119", "0", "1", "0", "0"},
		{"iso loss", SettleParams{Isolated: true, Release: d("100"), PnL: d("-30"), Fee: d("1")}, "69", "30", "1", "0", "0"},
		{"iso fee partly waived", SettleParams{Isolated: true, Release: d("100"), PnL: d("-99.5"), Fee: d("1")}, "0", "99.5", "0.5", "0", "0.5"},
		{"iso bankrupt", SettleParams{Isolated: true, Release: d("100"), PnL: d("-130"), Fee: d("1")}, "0", "100", "0", "30", "1"},
		{"cross loss within wallet", SettleParams{Available: d("50"), PnL: d("-30"), Fee: d("1")}, "19", "30", "1", "0", "0"},
		{"cross bankrupt", SettleParams{Available: d("20"), PnL: d("-30"), Fee: d("1")}, "0", "20", "0", "10", "1"},
		{"cross profit pays fee", SettleParams{Available: d("0"), PnL: d("5"), Fee: d("1")}, "4", "0", "1", "0", "0"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			tc.p.UserID, tc.p.Currency, tc.p.BizType, tc.p.BizID = 1, "USDT", BizSyntheticClose, "x"
			ins, r, err := NewSyntheticSettlement(tc.p)
			if err != nil {
				t.Fatal(err)
			}
			for _, c := range []struct {
				name      string
				got, want decimal.Decimal
			}{
				{"payout", r.Payout, d(tc.payout)}, {"lossCollected", r.LossCollected, d(tc.lossC)},
				{"feeCollected", r.FeeCollected, d(tc.feeC)}, {"shortfall", r.LossShortfall, d(tc.shortfall)},
				{"feeWaived", r.FeeWaived, d(tc.feeWaive)},
			} {
				if !c.got.Equal(c.want) {
					t.Errorf("%s = %s, want %s", c.name, c.got, c.want)
				}
			}
			if err := validate(ins); err != nil {
				t.Fatalf("unbalanced: %v", err)
			}
			// 逐仓结算绝不触碰 SWAP 的扣减
			if tc.p.Isolated {
				for _, p := range ins.Postings {
					if p.Account.Type == AccountSwap && p.Op != OpCredit {
						t.Fatalf("isolated settlement debited cross wallet: %+v", p)
					}
				}
			}
			// 全仓结算扣减不得超过可用余额
			if !tc.p.Isolated {
				balances := map[AccountKey]*BalanceSnapshot{}
				for _, k := range sortedKeys(ins) {
					balances[k] = &BalanceSnapshot{Key: k}
				}
				balances[AccountKey{1, AccountSwap, "USDT"}].Available = tc.p.Available
				if _, err := apply(ins, balances); err != nil {
					t.Fatalf("cross settlement overdraws wallet: %v", err)
				}
			}
		})
	}
}

func TestApplyRejectsNegativeUserBalance(t *testing.T) {
	ins := NewMarginFreeze(1, "USDT", d("10"), d("0"), "o1")
	key := ins.Postings[0].Account
	balances := map[AccountKey]*BalanceSnapshot{key: {Key: key, Available: d("9.99"), Frozen: d("0")}}
	if _, err := apply(ins, balances); !errors.Is(err, ErrInsufficientBalance) {
		t.Fatalf("want ErrInsufficientBalance, got %v", err)
	}
}

func TestApplyAllowsNegativeSystemBalance(t *testing.T) {
	ins := NewDeposit(1, "USDT", d("50"), "dep")
	balances := map[AccountKey]*BalanceSnapshot{}
	for _, k := range sortedKeys(ins) {
		balances[k] = &BalanceSnapshot{Key: k}
	}
	entries, err := apply(ins, balances)
	if err != nil {
		t.Fatal(err)
	}
	if got := entries[0].availableAfter; !got.Equal(d("-50")) {
		t.Fatalf("suspense after = %s, want -50", got)
	}
}

func TestPayloadHashIgnoresMemoAndTrailingZeros(t *testing.T) {
	a := NewTransfer(1, AccountSpot, AccountSwap, "USDT", d("10"), "t")
	b := NewTransfer(1, AccountSpot, AccountSwap, "USDT", d("10.000"), "t")
	b.Memo = map[string]string{"note": "x"}
	if payloadHash(a) != payloadHash(b) {
		t.Fatal("hash should be stable across memo and scale")
	}
	c := NewTransfer(1, AccountSpot, AccountSwap, "USDT", d("10.01"), "t")
	if payloadHash(a) == payloadHash(c) {
		t.Fatal("hash should differ for different amounts")
	}
}
