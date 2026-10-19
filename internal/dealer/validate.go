package dealer

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"sort"
	"strings"

	"github.com/shopspring/decimal"
)

const (
	maxBizIDLen   = 128
	maxPostings   = 64
	amountScale   = 18 // 与 DECIMAL(36,18) 对齐
	maxCurrencyLn = 16
)

// validate 做与数据库无关的全部静态校验：字段合法性、精度、以及按币种的 Σdelta == 0。
func validate(ins Instruction) error {
	if !ins.BizType.valid() {
		return fmt.Errorf("%w: unknown biz_type %q", ErrInvalidInstruction, ins.BizType)
	}
	if ins.BizID == "" || len(ins.BizID) > maxBizIDLen {
		return fmt.Errorf("%w: biz_id length must be 1..%d", ErrInvalidInstruction, maxBizIDLen)
	}
	if len(ins.Postings) == 0 || len(ins.Postings) > maxPostings {
		return fmt.Errorf("%w: postings count must be 1..%d", ErrInvalidInstruction, maxPostings)
	}

	sums := make(map[string]decimal.Decimal)
	for i, p := range ins.Postings {
		k := p.Account
		if !k.Type.valid() {
			return fmt.Errorf("%w: posting[%d] unknown account type %q", ErrInvalidInstruction, i, k.Type)
		}
		if k.Type.IsSystem() != (k.OwnerID == SystemOwnerID) {
			return fmt.Errorf("%w: posting[%d] owner %d mismatches account type %s", ErrInvalidInstruction, i, k.OwnerID, k.Type)
		}
		if k.OwnerID < 0 {
			return fmt.Errorf("%w: posting[%d] negative owner id", ErrInvalidInstruction, i)
		}
		if k.Currency == "" || len(k.Currency) > maxCurrencyLn || k.Currency != strings.ToUpper(k.Currency) {
			return fmt.Errorf("%w: posting[%d] currency %q must be 1..%d upper-case chars", ErrInvalidInstruction, i, k.Currency, maxCurrencyLn)
		}
		if !p.Amount.IsPositive() {
			return fmt.Errorf("%w: posting[%d] amount must be > 0, got %s", ErrInvalidInstruction, i, p.Amount)
		}
		if !p.Amount.Equal(p.Amount.Truncate(amountScale)) {
			return fmt.Errorf("%w: posting[%d] amount %s exceeds %d decimal places", ErrInvalidInstruction, i, p.Amount, amountScale)
		}
		da, df, ok := p.Op.deltas(p.Amount)
		if !ok {
			return fmt.Errorf("%w: posting[%d] unknown op %q", ErrInvalidInstruction, i, p.Op)
		}
		sums[k.Currency] = sums[k.Currency].Add(da).Add(df)
	}
	for ccy, s := range sums {
		if !s.IsZero() {
			return fmt.Errorf("%w: currency %s net delta %s", ErrUnbalanced, ccy, s)
		}
	}
	return nil
}

// payloadHash 对 Postings 做规范化哈希，用于识别“同一幂等键、不同内容”的误用。
func payloadHash(ins Instruction) string {
	h := sha256.New()
	fmt.Fprintf(h, "%s\x00%s\x00", ins.BizType, ins.BizID)
	for _, p := range ins.Postings {
		fmt.Fprintf(h, "%d\x00%s\x00%s\x00%s\x00%s\x00",
			p.Account.OwnerID, p.Account.Type, p.Account.Currency, p.Op, p.Amount.StringFixed(amountScale))
	}
	return hex.EncodeToString(h.Sum(nil))
}

// sortedKeys 返回去重并按全局顺序排序的账户集合（加锁顺序）。
func sortedKeys(ins Instruction) []AccountKey {
	seen := make(map[AccountKey]struct{}, len(ins.Postings))
	keys := make([]AccountKey, 0, len(ins.Postings))
	for _, p := range ins.Postings {
		if _, ok := seen[p.Account]; ok {
			continue
		}
		seen[p.Account] = struct{}{}
		keys = append(keys, p.Account)
	}
	sort.Slice(keys, func(i, j int) bool { return keys[i].less(keys[j]) })
	return keys
}

// appliedEntry 一条分录在内存中应用后的结果。
type appliedEntry struct {
	posting        Posting
	availableDelta decimal.Decimal
	frozenDelta    decimal.Decimal
	availableAfter decimal.Decimal
	frozenAfter    decimal.Decimal
}

// apply 在已加锁的余额上逐条应用分录（按 Postings 原始顺序），并做非负校验。
// balances 会被就地修改。
func apply(ins Instruction, balances map[AccountKey]*BalanceSnapshot) ([]appliedEntry, error) {
	entries := make([]appliedEntry, 0, len(ins.Postings))
	for i, p := range ins.Postings {
		b, ok := balances[p.Account]
		if !ok {
			return nil, fmt.Errorf("dealer: internal error, account %s not locked", p.Account)
		}
		da, df, _ := p.Op.deltas(p.Amount)
		b.Available = b.Available.Add(da)
		b.Frozen = b.Frozen.Add(df)
		if !p.Account.Type.IsSystem() && (b.Available.IsNegative() || b.Frozen.IsNegative()) {
			return nil, fmt.Errorf("%w: posting[%d] %s %s %s -> available=%s frozen=%s",
				ErrInsufficientBalance, i, p.Account, p.Op, p.Amount, b.Available, b.Frozen)
		}
		entries = append(entries, appliedEntry{
			posting:        p,
			availableDelta: da,
			frozenDelta:    df,
			availableAfter: b.Available,
			frozenAfter:    b.Frozen,
		})
	}
	return entries, nil
}
