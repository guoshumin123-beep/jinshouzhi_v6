package dealer

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"testing"

	"github.com/shopspring/decimal"

	"backend_v2/internal/storage"
	"backend_v2/internal/storage/storagetest"
)

// 集成测试：设置 TEST_MYSQL_DSN（见 storagetest.EnvDSN）后运行；
// 使用派生的 <dbname>_test_dealer 库，绝不触碰开发库本身。
func openTestDB(t *testing.T) *sql.DB {
	t.Helper()
	return storagetest.OpenDB(t)
}

func quietLogger() *slog.Logger { return storagetest.QuietLogger() }

func mustBalance(t *testing.T, dl Dealer, k AccountKey) BalanceSnapshot {
	t.Helper()
	b, err := dl.Balance(context.Background(), k)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func assertBal(t *testing.T, dl Dealer, k AccountKey, avail, frozen string) {
	t.Helper()
	b := mustBalance(t, dl, k)
	if !b.Available.Equal(d(avail)) || !b.Frozen.Equal(d(frozen)) {
		t.Fatalf("%s: got available=%s frozen=%s, want %s/%s", k, b.Available, b.Frozen, avail, frozen)
	}
}

func TestMySQLLifecycle(t *testing.T) {
	db := openTestDB(t)
	dl := NewMySQLDealer(db, quietLogger())
	ctx := context.Background()

	spot := AccountKey{1, AccountSpot, "USDT"}
	swap := AccountKey{1, AccountSwap, "USDT"}
	pool := AccountKey{0, AccountSyntheticPool, "USDT"}
	fee := AccountKey{0, AccountFeeIncome, "USDT"}

	steps := []Instruction{
		NewDeposit(1, "USDT", d("1000"), "dep-1"),
		NewTransfer(1, AccountSpot, AccountSwap, "USDT", d("300"), "tr-1"),
		NewSyntheticOpenFill(1, "USDT", true, d("100"), d("0.5"), "open-1"),
	}
	for _, ins := range steps {
		if _, err := dl.Execute(ctx, ins); err != nil {
			t.Fatalf("%s: %v", ins.BizID, err)
		}
	}
	settle, res, err := NewSyntheticSettlement(SettleParams{
		UserID: 1, Currency: "USDT", BizType: BizSyntheticClose, BizID: "close-1",
		Isolated: true, Release: d("100"), PnL: d("40"), Fee: d("0.5"),
	})
	if err != nil {
		t.Fatal(err)
	}
	if !res.Payout.Equal(d("139.5")) || !res.LossShortfall.IsZero() {
		t.Fatalf("settle result = %+v", res)
	}
	rcpt, err := dl.Execute(ctx, settle)
	if err != nil {
		t.Fatal(err)
	}
	if got := rcpt.Balances[swap]; !got.Available.Equal(d("339")) || !got.Frozen.IsZero() {
		t.Fatalf("receipt swap = %+v", got)
	}

	assertBal(t, dl, spot, "700", "0")
	assertBal(t, dl, swap, "339", "0")
	assertBal(t, dl, AccountKey{1, AccountSwapIsolated, "USDT"}, "0", "0")
	assertBal(t, dl, pool, "-40", "0")
	assertBal(t, dl, fee, "1", "0")
	if v := mustBalance(t, dl, swap).Version; v != 3 {
		t.Fatalf("swap version = %d, want 3", v)
	}

	// 全局守恒：所有账户 available+frozen 之和为 0
	var total string
	if err := db.QueryRow(`SELECT COALESCE(SUM(available + frozen), 0) FROM accounts`).Scan(&total); err != nil {
		t.Fatal(err)
	}
	if !d(total).IsZero() {
		t.Fatalf("ledger not conserved: %s", total)
	}
}

func TestMySQLIdempotency(t *testing.T) {
	db := openTestDB(t)
	dl := NewMySQLDealer(db, quietLogger())
	ctx := context.Background()

	ins := NewDeposit(7, "USDT", d("50"), "dep-x")
	first, err := dl.Execute(ctx, ins)
	if err != nil {
		t.Fatal(err)
	}
	// 中间插入另一笔变动，确认重放返回的是首笔事务结束时的余额
	if _, err := dl.Execute(ctx, NewDeposit(7, "USDT", d("1"), "dep-y")); err != nil {
		t.Fatal(err)
	}
	again, err := dl.Execute(ctx, ins)
	if err != nil {
		t.Fatal(err)
	}
	if !again.Replayed || again.TxnID != first.TxnID {
		t.Fatalf("expected replay of txn %d, got %+v", first.TxnID, again)
	}
	spot := AccountKey{7, AccountSpot, "USDT"}
	if !again.Balances[spot].Available.Equal(d("50")) {
		t.Fatalf("replay balance = %s, want 50", again.Balances[spot].Available)
	}
	assertBal(t, dl, spot, "51", "0")

	conflict := NewDeposit(7, "USDT", d("51"), "dep-x")
	if _, err := dl.Execute(ctx, conflict); !errors.Is(err, ErrIdempotencyConflict) {
		t.Fatalf("want ErrIdempotencyConflict, got %v", err)
	}
}

func TestMySQLInsufficientRollsBack(t *testing.T) {
	db := openTestDB(t)
	dl := NewMySQLDealer(db, quietLogger())
	ctx := context.Background()

	if _, err := dl.Execute(ctx, NewDeposit(2, "USDT", d("10"), "dep")); err != nil {
		t.Fatal(err)
	}
	ins := NewTransfer(2, AccountSpot, AccountSwap, "USDT", d("10.000000000000000001"), "tr-over")
	if _, err := dl.Execute(ctx, ins); !errors.Is(err, ErrInsufficientBalance) {
		t.Fatalf("want ErrInsufficientBalance, got %v", err)
	}
	assertBal(t, dl, AccountKey{2, AccountSpot, "USDT"}, "10", "0")

	// 失败事务不得占用幂等键：余额补足后同一 biz_id 可以成功执行
	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM ledger_txns WHERE biz_id = 'tr-over'`).Scan(&n); err != nil || n != 0 {
		t.Fatalf("failed txn left %d rows (err=%v)", n, err)
	}
	if _, err := dl.Execute(ctx, NewDeposit(2, "USDT", d("1"), "dep-2")); err != nil {
		t.Fatal(err)
	}
	if _, err := dl.Execute(ctx, ins); err != nil {
		t.Fatalf("retry after top-up: %v", err)
	}
}

// 两个 Dealer 实例模拟多进程部署（条带锁不共享），双向对冲划转，验证 DB 行锁下无丢失更新、无死锁失败。
func TestMySQLConcurrentCrossTransfers(t *testing.T) {
	db := openTestDB(t)
	a, b := NewMySQLDealer(db, quietLogger()), NewMySQLDealer(db, quietLogger())
	ctx := context.Background()

	for _, uid := range []int64{10, 11} {
		if _, err := a.Execute(ctx, NewDeposit(uid, "USDT", d("1000"), fmt.Sprintf("seed-%d", uid))); err != nil {
			t.Fatal(err)
		}
	}

	const n = 100
	var wg sync.WaitGroup
	errs := make(chan error, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			dl := a
			from, to := AccountSpot, AccountSwap
			if i%2 == 1 {
				dl, from, to = b, AccountSwap, AccountSpot
			}
			uid := int64(10 + i%4/2)
			// SPOT->SWAP 与 SWAP->SPOT 交错；SWAP 初始为 0，失败的余额不足是合法结果
			_, err := dl.Execute(ctx, NewTransfer(uid, from, to, "USDT", d("1.5"), fmt.Sprintf("x-%d", i)))
			if err != nil && !errors.Is(err, ErrInsufficientBalance) {
				errs <- err
			}
		}(i)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Errorf("unexpected error: %v", err)
	}

	for _, uid := range []int64{10, 11} {
		s := mustBalance(t, a, AccountKey{uid, AccountSpot, "USDT"})
		w := mustBalance(t, a, AccountKey{uid, AccountSwap, "USDT"})
		if !s.Available.Add(w.Available).Equal(d("1000")) {
			t.Errorf("user %d: spot %s + swap %s != 1000", uid, s.Available, w.Available)
		}
		if s.Available.IsNegative() || w.Available.IsNegative() {
			t.Errorf("user %d negative balance", uid)
		}
	}

	// 分录与余额一致：每个账户的 Σdelta 等于当前余额
	rows, err := db.Query(`SELECT a.id, a.available, COALESCE(SUM(e.available_delta), 0)
		FROM accounts a LEFT JOIN ledger_entries e ON e.account_id = a.id GROUP BY a.id, a.available`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	for rows.Next() {
		var id int64
		var bal, sum string
		if err := rows.Scan(&id, &bal, &sum); err != nil {
			t.Fatal(err)
		}
		if !d(bal).Equal(d(sum)) {
			t.Errorf("account %d: balance %s != entries sum %s", id, bal, sum)
		}
	}
}

// 同一幂等键并发提交：只有一次真正执行，其余全部为重放。
func TestMySQLConcurrentSameBizID(t *testing.T) {
	db := openTestDB(t)
	dealers := []*MySQLDealer{NewMySQLDealer(db, quietLogger()), NewMySQLDealer(db, quietLogger())}
	ctx := context.Background()

	const n = 20
	var (
		wg       sync.WaitGroup
		mu       sync.Mutex
		executed int
	)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			r, err := dealers[i%2].Execute(ctx, NewDeposit(20, "USDT", decimal.NewFromInt(5), "same"))
			if err != nil {
				t.Errorf("execute: %v", err)
				return
			}
			if !r.Replayed {
				mu.Lock()
				executed++
				mu.Unlock()
			}
		}(i)
	}
	wg.Wait()
	if executed != 1 {
		t.Fatalf("executed %d times, want 1", executed)
	}
	assertBal(t, dealers[0], AccountKey{20, AccountSpot, "USDT"}, "5", "0")
}

// ---------------------------------------------------------------------------
// ExecuteInTx
// ---------------------------------------------------------------------------

func createProbeTable(t *testing.T, db *sql.DB) {
	t.Helper()
	if _, err := db.Exec(`CREATE TABLE tx_probe (id VARCHAR(32) PRIMARY KEY)`); err != nil {
		t.Fatal(err)
	}
}

func probeCount(t *testing.T, db *sql.DB, q string, args ...any) int {
	t.Helper()
	var n int
	if err := db.QueryRow(q, args...).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func TestExecuteInTxCommitsWithCallerWrites(t *testing.T) {
	db := openTestDB(t)
	createProbeTable(t, db)
	dl := NewMySQLDealer(db, quietLogger())
	ctx := context.Background()
	if _, err := dl.Execute(ctx, NewDeposit(30, "USDT", d("100"), "dep")); err != nil {
		t.Fatal(err)
	}
	if _, err := dl.Execute(ctx, NewTransfer(30, AccountSpot, AccountSwap, "USDT", d("100"), "tr")); err != nil {
		t.Fatal(err)
	}

	err := storage.WithTx(ctx, db, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, `INSERT INTO tx_probe (id) VALUES ('pos-1')`); err != nil {
			return err
		}
		r, err := dl.ExecuteInTx(ctx, tx, NewMarginFreeze(30, "USDT", d("40"), d("0"), "ord-1"))
		if err != nil {
			return err
		}
		if r.Replayed {
			t.Error("unexpected replay")
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	assertBal(t, dl, AccountKey{30, AccountSwap, "USDT"}, "60", "40")
	if probeCount(t, db, `SELECT COUNT(*) FROM tx_probe`) != 1 {
		t.Fatal("caller write missing")
	}
}

func TestExecuteInTxRollsBackWithCaller(t *testing.T) {
	db := openTestDB(t)
	createProbeTable(t, db)
	dl := NewMySQLDealer(db, quietLogger())
	ctx := context.Background()
	if _, err := dl.Execute(ctx, NewDeposit(31, "USDT", d("100"), "dep")); err != nil {
		t.Fatal(err)
	}
	swapKey := AccountKey{31, AccountSpot, "USDT"}

	// 情形 1：dealer 成功，但调用方后续步骤失败 → 账本变动必须一并回滚
	callerErr := errors.New("position insert failed")
	err := storage.WithTx(ctx, db, func(tx *sql.Tx) error {
		if _, err := dl.ExecuteInTx(ctx, tx, NewTransfer(31, AccountSpot, AccountSwap, "USDT", d("30"), "tr-a")); err != nil {
			return err
		}
		return callerErr
	})
	if !errors.Is(err, callerErr) {
		t.Fatalf("got %v", err)
	}
	assertBal(t, dl, swapKey, "100", "0")
	if probeCount(t, db, `SELECT COUNT(*) FROM ledger_txns WHERE biz_id = 'tr-a'`) != 0 {
		t.Fatal("ledger txn survived caller rollback")
	}

	// 情形 2：调用方先写入，dealer 余额不足 → 调用方写入也必须回滚
	err = storage.WithTx(ctx, db, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, `INSERT INTO tx_probe (id) VALUES ('pos-x')`); err != nil {
			return err
		}
		_, err := dl.ExecuteInTx(ctx, tx, NewTransfer(31, AccountSpot, AccountSwap, "USDT", d("101"), "tr-b"))
		return err
	})
	if !errors.Is(err, ErrInsufficientBalance) {
		t.Fatalf("want ErrInsufficientBalance, got %v", err)
	}
	if probeCount(t, db, `SELECT COUNT(*) FROM tx_probe`) != 0 {
		t.Fatal("caller write survived dealer failure")
	}
	assertBal(t, dl, swapKey, "100", "0")
}

func TestExecuteInTxReplay(t *testing.T) {
	db := openTestDB(t)
	dl := NewMySQLDealer(db, quietLogger())
	ctx := context.Background()
	ins := NewDeposit(32, "USDT", d("10"), "dep-r")
	first, err := dl.Execute(ctx, ins)
	if err != nil {
		t.Fatal(err)
	}
	err = storage.WithTx(ctx, db, func(tx *sql.Tx) error {
		r, err := dl.ExecuteInTx(ctx, tx, ins)
		if err != nil {
			return err
		}
		if !r.Replayed || r.TxnID != first.TxnID {
			t.Errorf("expected replay of %d, got %+v", first.TxnID, r)
		}
		// 冲突的幂等键在事务内同样被拒绝
		_, err = dl.ExecuteInTx(ctx, tx, NewDeposit(32, "USDT", d("11"), "dep-r"))
		if !errors.Is(err, ErrIdempotencyConflict) {
			t.Errorf("want ErrIdempotencyConflict, got %v", err)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	assertBal(t, dl, AccountKey{32, AccountSpot, "USDT"}, "10", "0")
}

// Execute 与 ExecuteInTx 混合并发，验证两条路径共享同一套 DB 行锁语义。
func TestExecuteAndExecuteInTxConcurrent(t *testing.T) {
	db := openTestDB(t)
	dl := NewMySQLDealer(db, quietLogger())
	ctx := context.Background()
	if _, err := dl.Execute(ctx, NewDeposit(33, "USDT", d("1000"), "seed")); err != nil {
		t.Fatal(err)
	}

	const n = 60
	var wg sync.WaitGroup
	errs := make(chan error, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			ins := NewTransfer(33, AccountSpot, AccountSwap, "USDT", d("10"), fmt.Sprintf("mix-%d", i))
			if i%2 == 1 {
				ins = NewTransfer(33, AccountSwap, AccountSpot, "USDT", d("5"), fmt.Sprintf("mix-%d", i))
			}
			var err error
			if i%3 == 0 {
				_, err = dl.Execute(ctx, ins)
			} else {
				err = storage.WithTx(ctx, db, func(tx *sql.Tx) error {
					_, err := dl.ExecuteInTx(ctx, tx, ins)
					return err
				})
			}
			if err != nil && !errors.Is(err, ErrInsufficientBalance) {
				errs <- err
			}
		}(i)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Errorf("unexpected: %v", err)
	}
	s := mustBalance(t, dl, AccountKey{33, AccountSpot, "USDT"})
	w := mustBalance(t, dl, AccountKey{33, AccountSwap, "USDT"})
	if !s.Available.Add(w.Available).Equal(d("1000")) || w.Available.IsNegative() {
		t.Fatalf("spot %s swap %s", s.Available, w.Available)
	}
}
