package subaccount

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/shopspring/decimal"

	"backend_v2/internal/storage"
	"backend_v2/internal/vault"
)

const accountCols = `user_id, user_name, name_attempt, COALESCE(sub_uid, 0), sub_account_type, state, step, attempts, COALESCE(last_error, '')`

func scanAccount(row interface{ Scan(...any) error }) (*Account, error) {
	var a Account
	var state, step string
	if err := row.Scan(&a.UserID, &a.UserName, &a.NameAttempt, &a.SubUID, &a.SubAccountType, &state, &step, &a.Attempts, &a.LastError); err != nil {
		return nil, err
	}
	a.State, a.Step = State(state), Step(step)
	return &a, nil
}

func getAccount(ctx context.Context, q storage.Querier, userID int64, forUpdate bool) (*Account, error) {
	query := `SELECT ` + accountCols + ` FROM subaccounts WHERE user_id = ?`
	if forUpdate {
		query += ` FOR UPDATE`
	}
	a, err := scanAccount(q.QueryRowContext(ctx, query, userID))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("get subaccount %d: %w", userID, err)
	}
	return a, nil
}

// GetAccount 读取子账户记录（不存在返回 nil）。
func GetAccount(ctx context.Context, q storage.Querier, userID int64) (*Account, error) {
	return getAccount(ctx, q, userID, false)
}

func insertAccount(ctx context.Context, tx *sql.Tx, a *Account) error {
	_, err := tx.ExecContext(ctx,
		`INSERT INTO subaccounts (user_id, user_name, name_attempt, sub_account_type, state, step) VALUES (?, ?, ?, ?, ?, ?)`,
		a.UserID, a.UserName, a.NameAttempt, a.SubAccountType, string(a.State), string(a.Step))
	if err != nil {
		return fmt.Errorf("insert subaccount %d: %w", a.UserID, err)
	}
	return nil
}

// claimLease 获取开户租约（同时将 FAILED 重置为 PROVISIONING）。
func claimLease(ctx context.Context, tx *sql.Tx, userID int64, owner string, ttl time.Duration) error {
	_, err := tx.ExecContext(ctx,
		`UPDATE subaccounts SET lease_owner = ?, lease_until = ?, attempts = attempts + 1, state = 'PROVISIONING'
		 WHERE user_id = ?`, owner, time.Now().Add(ttl).UTC(), userID)
	if err != nil {
		return fmt.Errorf("claim lease %d: %w", userID, err)
	}
	return nil
}

func leaseHeldByOther(ctx context.Context, tx *sql.Tx, userID int64, owner string) (bool, error) {
	var holder sql.NullString
	var until sql.NullTime
	if err := tx.QueryRowContext(ctx, `SELECT lease_owner, lease_until FROM subaccounts WHERE user_id = ?`, userID).
		Scan(&holder, &until); err != nil {
		return false, fmt.Errorf("read lease %d: %w", userID, err)
	}
	return holder.Valid && holder.String != owner && until.Valid && until.Time.After(time.Now()), nil
}

// advance 在持有租约的前提下推进状态机，并续租。租约丢失时返回错误（另一实例已接管）。
func advance(ctx context.Context, q storage.Querier, a *Account, owner string, ttl time.Duration) error {
	var subUID any
	if a.SubUID > 0 {
		subUID = a.SubUID
	}
	res, err := q.ExecContext(ctx,
		`UPDATE subaccounts SET step = ?, state = ?, sub_uid = ?, user_name = ?, name_attempt = ?, last_error = NULL,
		 lease_until = ? WHERE user_id = ? AND lease_owner = ?`,
		string(a.Step), string(a.State), subUID, a.UserName, a.NameAttempt, time.Now().Add(ttl).UTC(), a.UserID, owner)
	if err != nil {
		return fmt.Errorf("advance subaccount %d to %s: %w", a.UserID, a.Step, err)
	}
	if n, _ := res.RowsAffected(); n != 1 {
		return fmt.Errorf("advance subaccount %d: %w", a.UserID, ErrProvisionInProgress)
	}
	return nil
}

// release 释放租约并记录结果状态与错误。
func release(ctx context.Context, q storage.Querier, a *Account, owner string, lastErr string) error {
	if len(lastErr) > 512 {
		lastErr = lastErr[:512]
	}
	_, err := q.ExecContext(ctx,
		`UPDATE subaccounts SET state = ?, step = ?, last_error = NULLIF(?, ''), lease_owner = NULL, lease_until = NULL
		 WHERE user_id = ? AND lease_owner = ?`,
		string(a.State), string(a.Step), lastErr, a.UserID, owner)
	if err != nil {
		return fmt.Errorf("release subaccount %d: %w", a.UserID, err)
	}
	return nil
}

func setState(ctx context.Context, q storage.Querier, userID int64, from, to State) (bool, error) {
	res, err := q.ExecContext(ctx, `UPDATE subaccounts SET state = ? WHERE user_id = ? AND state = ?`, string(to), userID, string(from))
	if err != nil {
		return false, fmt.Errorf("set subaccount %d state %s->%s: %w", userID, from, to, err)
	}
	n, _ := res.RowsAffected()
	return n == 1, nil
}

func listResumable(ctx context.Context, q storage.Querier, limit int) ([]int64, error) {
	rows, err := q.QueryContext(ctx,
		`SELECT user_id FROM subaccounts WHERE state = 'PROVISIONING' AND (lease_until IS NULL OR lease_until < ?)
		 ORDER BY updated_at LIMIT ?`, time.Now().UTC(), limit)
	if err != nil {
		return nil, fmt.Errorf("list resumable subaccounts: %w", err)
	}
	defer rows.Close()
	var out []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out = append(out, id)
	}
	return out, rows.Err()
}

// ---------------------------------------------------------------------------
// API Keys
// ---------------------------------------------------------------------------

type storedKey struct {
	ID        uint64
	UserID    int64
	SubUID    int64
	AccessKey string
	Sealed    vault.Sealed
}

func insertKey(ctx context.Context, tx *sql.Tx, userID, subUID int64, accessKey string, s vault.Sealed, permission, ips, note string) error {
	_, err := tx.ExecContext(ctx,
		`INSERT INTO subaccount_api_keys (user_id, sub_uid, access_key, secret_key_id, secret_nonce, secret_ciphertext,
		 permission, ip_addresses, note, status) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, 'ACTIVE')`,
		userID, subUID, accessKey, s.KeyID, s.Nonce, s.Ciphertext, permission, ips, note)
	if err != nil {
		return fmt.Errorf("insert api key for user %d: %w", userID, err)
	}
	return nil
}

func activeKey(ctx context.Context, q storage.Querier, userID int64) (*storedKey, error) {
	var k storedKey
	err := q.QueryRowContext(ctx,
		`SELECT id, user_id, sub_uid, access_key, secret_key_id, secret_nonce, secret_ciphertext
		 FROM subaccount_api_keys WHERE user_id = ? AND status = 'ACTIVE'`, userID).
		Scan(&k.ID, &k.UserID, &k.SubUID, &k.AccessKey, &k.Sealed.KeyID, &k.Sealed.Nonce, &k.Sealed.Ciphertext)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("load active api key for user %d: %w", userID, err)
	}
	return &k, nil
}

func revokeKey(ctx context.Context, q storage.Querier, id uint64) error {
	if _, err := q.ExecContext(ctx,
		`UPDATE subaccount_api_keys SET status = 'REVOKED', revoked_at = CURRENT_TIMESTAMP(6) WHERE id = ? AND status = 'ACTIVE'`, id); err != nil {
		return fmt.Errorf("revoke api key %d: %w", id, err)
	}
	return nil
}

// ---------------------------------------------------------------------------
// Transfers
// ---------------------------------------------------------------------------

const legCols = `id, request_id, user_id, sub_uid, direction, leg, kind, currency, amount, margin_account,
	COALESCE(client_order_id, ''), status, spot_before, COALESCE(htx_transfer_id, 0), attempts, COALESCE(last_error, '')`

func scanLeg(row interface{ Scan(...any) error }) (*Leg, error) {
	var (
		l                      Leg
		dir, kind, status, amt string
		spot                   sql.NullString
	)
	if err := row.Scan(&l.ID, &l.RequestID, &l.UserID, &l.SubUID, &dir, &l.Seq, &kind, &l.Currency, &amt, &l.MarginAccount,
		&l.ClientOrderID, &status, &spot, &l.HTXTransferID, &l.Attempts, &l.LastError); err != nil {
		return nil, err
	}
	l.Direction, l.Kind, l.Status = Direction(dir), LegKind(kind), LegStatus(status)
	var err error
	if l.Amount, err = decimal.NewFromString(amt); err != nil {
		return nil, err
	}
	if spot.Valid {
		v, err := decimal.NewFromString(spot.String)
		if err != nil {
			return nil, err
		}
		l.SpotBefore = decimal.NewNullDecimal(v)
	}
	return &l, nil
}

func insertLeg(ctx context.Context, tx *sql.Tx, l *Leg) error {
	var cid any
	if l.ClientOrderID != "" {
		cid = l.ClientOrderID
	}
	res, err := tx.ExecContext(ctx,
		`INSERT INTO subaccount_transfers (request_id, user_id, sub_uid, direction, leg, kind, currency, amount,
		 margin_account, client_order_id, status) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		l.RequestID, l.UserID, l.SubUID, string(l.Direction), l.Seq, string(l.Kind), l.Currency, l.Amount.String(),
		l.MarginAccount, cid, string(l.Status))
	if err != nil {
		return err
	}
	id, err := res.LastInsertId()
	if err != nil {
		return err
	}
	l.ID = uint64(id)
	return nil
}

// setClientOrderID 母子划转的 client-order-id 由行 ID 确定性生成（10 个字符），插入后回填。
func setClientOrderID(ctx context.Context, tx *sql.Tx, l *Leg) error {
	l.ClientOrderID = clientOrderID(l.ID)
	_, err := tx.ExecContext(ctx, `UPDATE subaccount_transfers SET client_order_id = ? WHERE id = ?`, l.ClientOrderID, l.ID)
	return err
}

func listLegs(ctx context.Context, q storage.Querier, userID int64, requestID string) ([]Leg, error) {
	rows, err := q.QueryContext(ctx, `SELECT `+legCols+` FROM subaccount_transfers WHERE user_id = ? AND request_id = ? ORDER BY leg`,
		userID, requestID)
	if err != nil {
		return nil, fmt.Errorf("list transfer legs: %w", err)
	}
	defer rows.Close()
	var out []Leg
	for rows.Next() {
		l, err := scanLeg(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *l)
	}
	return out, rows.Err()
}

func getLeg(ctx context.Context, q storage.Querier, id uint64) (*Leg, error) {
	l, err := scanLeg(q.QueryRowContext(ctx, `SELECT `+legCols+` FROM subaccount_transfers WHERE id = ?`, id))
	if err != nil {
		return nil, fmt.Errorf("get transfer leg %d: %w", id, err)
	}
	return l, nil
}

// casLeg 条件更新段状态（乐观并发：只有当前状态在 from 之中才更新），返回是否成功。
func casLeg(ctx context.Context, q storage.Querier, l *Leg, from []LegStatus, to LegStatus, incAttempt bool) (bool, error) {
	args := []any{string(to), l.HTXTransferID, l.LastError}
	var spot any
	if l.SpotBefore.Valid {
		spot = l.SpotBefore.Decimal.String()
	}
	args = append(args, spot)
	inc := 0
	if incAttempt {
		inc = 1
	}
	args = append(args, inc, l.ID)
	query := `UPDATE subaccount_transfers SET status = ?, htx_transfer_id = NULLIF(?, 0), last_error = NULLIF(?, ''),
		spot_before = ?, attempts = attempts + ? WHERE id = ? AND status IN (`
	for i, f := range from {
		if i > 0 {
			query += ", "
		}
		query += "?"
		args = append(args, string(f))
	}
	res, err := q.ExecContext(ctx, query+`)`, args...)
	if err != nil {
		return false, fmt.Errorf("update transfer leg %d -> %s: %w", l.ID, to, err)
	}
	n, _ := res.RowsAffected()
	if n == 1 {
		l.Status = to
		if incAttempt {
			l.Attempts++
		}
	}
	return n == 1, nil
}

// listStaleLegs 查找需要恢复处理的段：PENDING/UNKNOWN，以及超时未完成的 IN_FLIGHT（进程崩溃遗留）。
func listStaleLegs(ctx context.Context, q storage.Querier, inFlightBefore time.Time, limit int) ([]uint64, error) {
	rows, err := q.QueryContext(ctx,
		`SELECT id FROM subaccount_transfers WHERE status IN ('PENDING', 'UNKNOWN')
		    OR (status = 'IN_FLIGHT' AND updated_at < ?) ORDER BY id LIMIT ?`, inFlightBefore.UTC(), limit)
	if err != nil {
		return nil, fmt.Errorf("list stale transfer legs: %w", err)
	}
	defer rows.Close()
	var out []uint64
	for rows.Next() {
		var id uint64
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out = append(out, id)
	}
	return out, rows.Err()
}

// clientOrderID 生成 10 个字符的母子划转幂等键：前缀 "b" + 行 ID 的 base36，左侧补 0。
func clientOrderID(id uint64) string {
	const digits = "0123456789abcdefghijklmnopqrstuvwxyz"
	buf := []byte("b000000000")
	for i := len(buf) - 1; i > 0 && id > 0; i-- {
		buf[i] = digits[id%36]
		id /= 36
	}
	return string(buf)
}
