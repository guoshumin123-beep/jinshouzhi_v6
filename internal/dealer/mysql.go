package dealer

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"hash/fnv"
	"log/slog"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/shopspring/decimal"

	"backend_v2/internal/storage"
)

const (
	stripeCount        = 1024
	maxDeadlockRetries = 3
)

// MySQLDealer 基于 MySQL InnoDB 的 Dealer 实现。
//
// 并发控制分两层：
//  1. 进程内条带锁：同一账户的请求在本进程内排队，降低 DB 行锁竞争（仅为优化）。
//  2. DB 行锁：按全局固定顺序 INSERT..ON DUPLICATE KEY UPDATE + SELECT..FOR UPDATE，
//     多实例部署下的正确性由它保证。
type MySQLDealer struct {
	db      *sql.DB
	logger  *slog.Logger
	stripes [stripeCount]sync.Mutex
}

var _ TxDealer = (*MySQLDealer)(nil)

func NewMySQLDealer(db *sql.DB, logger *slog.Logger) *MySQLDealer {
	if logger == nil {
		logger = slog.Default()
	}
	return &MySQLDealer{db: db, logger: logger.With(slog.String("component", "dealer"))}
}

// Execute 原子地执行一个账本事务。相同 (BizType, BizID) 重复提交时返回首次结果且 Replayed=true。
func (d *MySQLDealer) Execute(ctx context.Context, ins Instruction) (*Receipt, error) {
	if err := validate(ins); err != nil {
		return nil, err
	}
	keys := sortedKeys(ins)
	hash := payloadHash(ins)

	unlock := d.lockStripes(keys)
	defer unlock()

	var (
		rcpt *Receipt
		err  error
	)
	for attempt := 1; attempt <= maxDeadlockRetries; attempt++ {
		rcpt, err = d.executeOnce(ctx, ins, keys, hash)
		if !storage.IsDeadlock(err) || attempt == maxDeadlockRetries {
			break
		}
		d.logger.Warn("dealer deadlock, retrying",
			slog.String("biz_type", string(ins.BizType)), slog.String("biz_id", ins.BizID), slog.Int("attempt", attempt))
		select {
		case <-ctx.Done():
			return nil, fmt.Errorf("dealer execute %s/%s: %w", ins.BizType, ins.BizID, ctx.Err())
		case <-time.After(time.Duration(attempt*10) * time.Millisecond):
		}
	}
	if err != nil {
		d.logger.Warn("dealer txn rejected",
			slog.String("biz_type", string(ins.BizType)), slog.String("biz_id", ins.BizID), slog.String("error", err.Error()))
		return nil, err
	}
	d.logReceipt("dealer txn committed", ins, rcpt)
	return rcpt, nil
}

func (d *MySQLDealer) executeOnce(ctx context.Context, ins Instruction, keys []AccountKey, hash string) (_ *Receipt, err error) {
	tx, err := d.db.BeginTx(ctx, storage.TxOptions)
	if err != nil {
		return nil, fmt.Errorf("dealer execute %s/%s: begin tx: %w", ins.BizType, ins.BizID, err)
	}
	defer func() {
		if err != nil {
			_ = tx.Rollback()
		}
	}()

	rcpt, err := d.stage(ctx, tx, ins, keys, hash)
	if err != nil {
		return nil, err
	}
	if rcpt.Replayed {
		_ = tx.Rollback() // 重放路径未写入任何数据
		return rcpt, nil
	}
	if err = tx.Commit(); err != nil {
		return nil, fmt.Errorf("dealer execute %s/%s: commit: %w", ins.BizType, ins.BizID, err)
	}
	return rcpt, nil
}

// ExecuteInTx 在调用方的事务中执行账本指令，使“创建持仓 + 冻结保证金”等跨表操作原子化。
//
// 调用方契约：
//   - tx 应以 storage.TxOptions（READ COMMITTED）开启，推荐使用 storage.WithTx。
//   - 返回任何 error 时，调用方必须回滚整个事务（此时 tx 中可能已有部分写入）。
//   - 本方法不加进程内条带锁、不做死锁重试；死锁由 storage.WithTx 整体重试。
//   - Receipt.Replayed=true 表示该 (BizType, BizID) 已在其它事务中提交，本次未写入任何数据。
//   - 资金变动在调用方 Commit 之后才生效；本方法记录的是“staged”日志。
func (d *MySQLDealer) ExecuteInTx(ctx context.Context, tx *sql.Tx, ins Instruction) (*Receipt, error) {
	if err := validate(ins); err != nil {
		return nil, err
	}
	rcpt, err := d.stage(ctx, tx, ins, sortedKeys(ins), payloadHash(ins))
	if err != nil {
		d.logger.Warn("dealer txn rejected in outer tx",
			slog.String("biz_type", string(ins.BizType)), slog.String("biz_id", ins.BizID), slog.String("error", err.Error()))
		return nil, err
	}
	d.logReceipt("dealer txn staged in outer tx", ins, rcpt)
	return rcpt, nil
}

// stage 在给定事务内完成幂等抢占、加锁、记账，但不提交。
func (d *MySQLDealer) stage(ctx context.Context, tx *sql.Tx, ins Instruction, keys []AccountKey, hash string) (*Receipt, error) {
	// 1. 幂等：抢占 (biz_type, biz_id)。并发重复请求会在唯一索引上等待首个事务结束。
	//    InnoDB 的唯一键冲突只回滚该语句，事务仍可继续用于读取已提交的原始结果。
	txnID, err := insertTxn(ctx, tx, ins, hash)
	if storage.IsDupEntry(err) {
		return loadReplay(ctx, tx, ins, hash)
	}
	if err != nil {
		return nil, fmt.Errorf("dealer execute %s/%s: insert txn: %w", ins.BizType, ins.BizID, err)
	}

	// 2. 按固定顺序确保账户存在并加锁，然后读取余额。
	balances, ids, err := lockAccounts(ctx, tx, keys)
	if err != nil {
		return nil, fmt.Errorf("dealer execute %s/%s: lock accounts: %w", ins.BizType, ins.BizID, err)
	}

	// 3. 内存中应用分录并校验非负。
	entries, err := apply(ins, balances)
	if err != nil {
		return nil, err
	}

	// 4. 回写余额与分录。
	for _, k := range keys {
		b := balances[k]
		if _, err := tx.ExecContext(ctx,
			`UPDATE accounts SET available = ?, frozen = ?, version = version + 1 WHERE id = ?`,
			b.Available.String(), b.Frozen.String(), ids[k]); err != nil {
			return nil, fmt.Errorf("dealer execute %s/%s: update account %s: %w", ins.BizType, ins.BizID, k, err)
		}
		b.Version++
	}
	if err := insertEntries(ctx, tx, txnID, entries, ids); err != nil {
		return nil, fmt.Errorf("dealer execute %s/%s: insert entries: %w", ins.BizType, ins.BizID, err)
	}

	out := make(map[AccountKey]BalanceSnapshot, len(balances))
	for k, b := range balances {
		out[k] = *b
	}
	return &Receipt{TxnID: txnID, Balances: out}, nil
}

func insertTxn(ctx context.Context, tx *sql.Tx, ins Instruction, hash string) (uint64, error) {
	var memo any
	if len(ins.Memo) > 0 {
		b, err := json.Marshal(ins.Memo)
		if err != nil {
			return 0, fmt.Errorf("marshal memo: %w", err)
		}
		memo = string(b)
	}
	res, err := tx.ExecContext(ctx,
		`INSERT INTO ledger_txns (biz_type, biz_id, payload_hash, memo) VALUES (?, ?, ?, ?)`,
		string(ins.BizType), ins.BizID, hash, memo)
	if err != nil {
		return 0, err
	}
	id, err := res.LastInsertId()
	if err != nil {
		return 0, err
	}
	return uint64(id), nil
}

// lockAccounts 按 keys 顺序（全局固定顺序）确保账户行存在并加排他锁。
func lockAccounts(ctx context.Context, tx *sql.Tx, keys []AccountKey) (map[AccountKey]*BalanceSnapshot, map[AccountKey]uint64, error) {
	placeholders := strings.TrimSuffix(strings.Repeat("(?, ?, ?),", len(keys)), ",")
	args := make([]any, 0, len(keys)*3)
	for _, k := range keys {
		args = append(args, k.OwnerID, string(k.Type), k.Currency)
	}

	// 对已存在的行，ON DUPLICATE KEY UPDATE 会按 VALUES 顺序加 X 锁；不存在的行被创建并持有锁。
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO accounts (owner_id, account_type, currency) VALUES `+placeholders+
			` ON DUPLICATE KEY UPDATE id = id`, args...); err != nil {
		return nil, nil, fmt.Errorf("ensure accounts: %w", err)
	}

	rows, err := tx.QueryContext(ctx,
		`SELECT id, owner_id, account_type, currency, available, frozen, version FROM accounts
		 WHERE (owner_id, account_type, currency) IN (`+placeholders+`)
		 ORDER BY owner_id, account_type, currency FOR UPDATE`, args...)
	if err != nil {
		return nil, nil, fmt.Errorf("select for update: %w", err)
	}
	defer rows.Close()

	balances := make(map[AccountKey]*BalanceSnapshot, len(keys))
	ids := make(map[AccountKey]uint64, len(keys))
	for rows.Next() {
		var (
			id     uint64
			k      AccountKey
			typ    string
			avail  string
			frozen string
			ver    uint64
		)
		if err := rows.Scan(&id, &k.OwnerID, &typ, &k.Currency, &avail, &frozen, &ver); err != nil {
			return nil, nil, fmt.Errorf("scan account: %w", err)
		}
		k.Type = AccountType(typ)
		b, err := newSnapshot(k, avail, frozen, ver)
		if err != nil {
			return nil, nil, err
		}
		balances[k] = b
		ids[k] = id
	}
	if err := rows.Err(); err != nil {
		return nil, nil, fmt.Errorf("iterate accounts: %w", err)
	}
	if len(balances) != len(keys) {
		return nil, nil, fmt.Errorf("expected %d locked accounts, got %d", len(keys), len(balances))
	}
	return balances, ids, nil
}

func insertEntries(ctx context.Context, tx *sql.Tx, txnID uint64, entries []appliedEntry, ids map[AccountKey]uint64) error {
	var sb strings.Builder
	sb.WriteString(`INSERT INTO ledger_entries (txn_id, seq, account_id, op, amount,
		available_delta, frozen_delta, available_after, frozen_after) VALUES `)
	args := make([]any, 0, len(entries)*9)
	for i, e := range entries {
		if i > 0 {
			sb.WriteString(",")
		}
		sb.WriteString("(?, ?, ?, ?, ?, ?, ?, ?, ?)")
		args = append(args, txnID, i, ids[e.posting.Account], string(e.posting.Op), e.posting.Amount.String(),
			e.availableDelta.String(), e.frozenDelta.String(), e.availableAfter.String(), e.frozenAfter.String())
	}
	_, err := tx.ExecContext(ctx, sb.String(), args...)
	return err
}

// loadReplay 返回已提交事务的回执。回执余额取自该事务分录的 *_after 字段；Version 不回填。
func loadReplay(ctx context.Context, q storage.Querier, ins Instruction, hash string) (*Receipt, error) {
	var (
		txnID      uint64
		storedHash string
	)
	err := q.QueryRowContext(ctx,
		`SELECT id, payload_hash FROM ledger_txns WHERE biz_type = ? AND biz_id = ?`,
		string(ins.BizType), ins.BizID).Scan(&txnID, &storedHash)
	if err != nil {
		return nil, fmt.Errorf("dealer replay %s/%s: load txn: %w", ins.BizType, ins.BizID, err)
	}
	if storedHash != hash {
		return nil, fmt.Errorf("%w: %s/%s (txn %d)", ErrIdempotencyConflict, ins.BizType, ins.BizID, txnID)
	}

	rows, err := q.QueryContext(ctx,
		`SELECT a.owner_id, a.account_type, a.currency, e.available_after, e.frozen_after
		 FROM ledger_entries e JOIN accounts a ON a.id = e.account_id
		 WHERE e.txn_id = ? ORDER BY e.seq`, txnID)
	if err != nil {
		return nil, fmt.Errorf("dealer replay %s/%s: load entries: %w", ins.BizType, ins.BizID, err)
	}
	defer rows.Close()

	out := make(map[AccountKey]BalanceSnapshot)
	for rows.Next() {
		var (
			k             AccountKey
			typ           string
			avail, frozen string
		)
		if err := rows.Scan(&k.OwnerID, &typ, &k.Currency, &avail, &frozen); err != nil {
			return nil, fmt.Errorf("dealer replay %s/%s: scan entry: %w", ins.BizType, ins.BizID, err)
		}
		k.Type = AccountType(typ)
		b, err := newSnapshot(k, avail, frozen, 0)
		if err != nil {
			return nil, err
		}
		out[k] = *b // 后面的分录覆盖前面的，得到事务结束时的余额
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("dealer replay %s/%s: iterate entries: %w", ins.BizType, ins.BizID, err)
	}
	return &Receipt{TxnID: txnID, Replayed: true, Balances: out}, nil
}

// LockBalancesInTx 见 TxDealer。按全局固定顺序加锁，与 Execute/ExecuteInTx 不会形成锁环。
func (d *MySQLDealer) LockBalancesInTx(ctx context.Context, tx *sql.Tx, keys ...AccountKey) (map[AccountKey]BalanceSnapshot, error) {
	if len(keys) == 0 {
		return map[AccountKey]BalanceSnapshot{}, nil
	}
	ins := Instruction{}
	for _, k := range keys {
		if !k.Type.valid() || k.Type.IsSystem() != (k.OwnerID == SystemOwnerID) {
			return nil, fmt.Errorf("%w: lock balance %s", ErrInvalidInstruction, k)
		}
		ins.Postings = append(ins.Postings, Posting{Account: k})
	}
	balances, _, err := lockAccounts(ctx, tx, sortedKeys(ins))
	if err != nil {
		return nil, fmt.Errorf("dealer lock balances: %w", err)
	}
	out := make(map[AccountKey]BalanceSnapshot, len(balances))
	for k, b := range balances {
		out[k] = *b
	}
	return out, nil
}

// Balance 读取账户余额（不加锁）。账户不存在时返回零余额。
func (d *MySQLDealer) Balance(ctx context.Context, key AccountKey) (BalanceSnapshot, error) {
	var (
		avail, frozen string
		ver           uint64
	)
	err := d.db.QueryRowContext(ctx,
		`SELECT available, frozen, version FROM accounts WHERE owner_id = ? AND account_type = ? AND currency = ?`,
		key.OwnerID, string(key.Type), key.Currency).Scan(&avail, &frozen, &ver)
	if errors.Is(err, sql.ErrNoRows) {
		return BalanceSnapshot{Key: key, Available: decimal.Zero, Frozen: decimal.Zero}, nil
	}
	if err != nil {
		return BalanceSnapshot{}, fmt.Errorf("dealer balance %s: %w", key, err)
	}
	b, err := newSnapshot(key, avail, frozen, ver)
	if err != nil {
		return BalanceSnapshot{}, err
	}
	return *b, nil
}

func newSnapshot(k AccountKey, avail, frozen string, ver uint64) (*BalanceSnapshot, error) {
	a, err := decimal.NewFromString(avail)
	if err != nil {
		return nil, fmt.Errorf("dealer: parse available of %s: %w", k, err)
	}
	f, err := decimal.NewFromString(frozen)
	if err != nil {
		return nil, fmt.Errorf("dealer: parse frozen of %s: %w", k, err)
	}
	return &BalanceSnapshot{Key: k, Available: a, Frozen: f, Version: ver}, nil
}

// lockStripes 按升序获取去重后的条带锁，返回解锁函数。
func (d *MySQLDealer) lockStripes(keys []AccountKey) func() {
	idx := make([]int, 0, len(keys))
	seen := make(map[int]struct{}, len(keys))
	for _, k := range keys {
		h := fnv.New32a()
		_, _ = h.Write([]byte(k.String()))
		i := int(h.Sum32() % stripeCount)
		if _, ok := seen[i]; !ok {
			seen[i] = struct{}{}
			idx = append(idx, i)
		}
	}
	sort.Ints(idx)
	for _, i := range idx {
		d.stripes[i].Lock()
	}
	return func() {
		for j := len(idx) - 1; j >= 0; j-- {
			d.stripes[idx[j]].Unlock()
		}
	}
}

func (d *MySQLDealer) logReceipt(msg string, ins Instruction, r *Receipt) {
	attrs := []any{
		slog.String("biz_type", string(ins.BizType)),
		slog.String("biz_id", ins.BizID),
		slog.Uint64("txn_id", r.TxnID),
		slog.Bool("replayed", r.Replayed),
	}
	for i, p := range ins.Postings {
		attrs = append(attrs, slog.Group(fmt.Sprintf("posting_%d", i),
			slog.String("account", p.Account.String()),
			slog.String("op", string(p.Op)),
			slog.String("amount", p.Amount.String()),
		))
	}
	for k, b := range r.Balances {
		attrs = append(attrs, slog.Group("after_"+k.String(),
			slog.String("available", b.Available.String()),
			slog.String("frozen", b.Frozen.String()),
		))
	}
	d.logger.Info(msg, attrs...)
}
