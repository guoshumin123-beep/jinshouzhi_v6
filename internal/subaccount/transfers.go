package subaccount

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"time"

	"github.com/shopspring/decimal"

	"backend_v2/internal/dealer"
	"backend_v2/internal/htx"
	"backend_v2/internal/storage"
)

const (
	maxMasterLegAttempts = 8 // 母子划转（带 client-order-id）结果未知时的重发上限，超过转人工
	maxDriveIterations   = 16
)

// Transfers 子账户资金划转服务。
//
//	TO_SUB  （本地 SWAP → 火币子账户 U 本位）：发起时账本 SWAP → HTX_CUSTODY；
//	         1) 母现货 → 子现货  2) 子现货 → 子 U 本位
//	         第 1 段失败：账本回滚；第 2 段失败：追加第 3 段 子现货 → 母现货，成功后账本回滚。
//	FROM_SUB（火币子账户 U 本位 → 本地 SWAP）：发起时冻结 HTX_CUSTODY（不足即拒绝）；
//	         1) 子 U 本位 → 子现货  2) 子现货 → 母现货，成功后账本 HTX_CUSTODY(冻结) → SWAP
//	         第 1 段失败：解冻；第 2 段失败：追加第 3 段 子现货 → 子 U 本位，成功后解冻。
//	回滚段再失败 → MANUAL，需人工处理。
//
// 幂等与结果未知：
//   - 母子划转携带 client-order-id，火币保证幂等，结果未知时用同一 client-order-id 重发；
//   - 现货-合约划转没有幂等键：发起前记录子账户现货余额快照，结果未知时绝不盲目重发，
//     先核验余额——已按预期变动则判定成功；未变动则安全重发；其他情况转人工。
//     每个用户同一时刻只有一段在途划转（数据库唯一约束），子账户现货余额只被本服务改变，核验因此可靠。
type Transfers struct {
	db      *sql.DB
	dealer  dealer.TxDealer
	spot    SpotAPI
	keys    KeyProvider
	master  *htx.Credentials
	enabled bool
	grace   time.Duration // IN_FLIGHT 超过该时长视为进程崩溃遗留
	logger  *slog.Logger
}

func NewTransfers(db *sql.DB, d dealer.TxDealer, spot SpotAPI, keys KeyProvider, master *htx.Credentials, enabled bool, logger *slog.Logger) *Transfers {
	if logger == nil {
		logger = slog.Default()
	}
	return &Transfers{db: db, dealer: d, spot: spot, keys: keys, master: master, enabled: enabled, grace: 2 * time.Minute,
		logger: logger.With(slog.String("component", "subaccount_transfers"))}
}

// ToSub 将本地 SWAP 资金划入用户火币子账户 U 本位（marginAccount：全仓 USDT / 逐仓小写合约代码）。
func (t *Transfers) ToSub(ctx context.Context, userID int64, currency string, amount decimal.Decimal, marginAccount, requestID string) (*TransferResult, error) {
	return t.start(ctx, DirToSub, userID, currency, amount, marginAccount, requestID)
}

// FromSub 将用户火币子账户 U 本位资金划回本地 SWAP。
func (t *Transfers) FromSub(ctx context.Context, userID int64, currency string, amount decimal.Decimal, marginAccount, requestID string) (*TransferResult, error) {
	return t.start(ctx, DirFromSub, userID, currency, amount, marginAccount, requestID)
}

func (t *Transfers) start(ctx context.Context, dir Direction, userID int64, currency string, amount decimal.Decimal, marginAccount, requestID string) (*TransferResult, error) {
	if !t.enabled {
		return nil, ErrTransfersDisabled
	}
	switch {
	case requestID == "" || len(requestID) > 64:
		return nil, fmt.Errorf("%w: request_id length 1..64", ErrInvalidRequest)
	case currency == "" || currency != strings.ToUpper(currency):
		return nil, fmt.Errorf("%w: currency must be upper-case", ErrInvalidRequest)
	case !amount.IsPositive() || !amount.Equal(amount.Truncate(18)):
		return nil, fmt.Errorf("%w: amount must be > 0 with at most 18 decimals", ErrInvalidRequest)
	case !validMarginAccount(marginAccount):
		return nil, fmt.Errorf("%w: margin-account %q must be USDT or a lower-case contract code", ErrInvalidRequest, marginAccount)
	}
	acc, err := GetAccount(ctx, t.db, userID)
	if err != nil {
		return nil, err
	}
	if acc == nil {
		return nil, ErrNoSubAccount
	}
	if acc.State != StateReady {
		return nil, fmt.Errorf("%w: state %s", ErrSubAccountNotReady, acc.State)
	}

	existing, err := listLegs(ctx, t.db, userID, requestID)
	if err != nil {
		return nil, err
	}
	if len(existing) > 0 {
		f := existing[0]
		if f.Direction != dir || !f.Amount.Equal(amount) || f.Currency != currency || f.MarginAccount != marginAccount {
			return nil, fmt.Errorf("%w: request_id %q reused with different parameters", ErrInvalidRequest, requestID)
		}
		return t.drive(ctx, userID, requestID)
	}

	first := &Leg{RequestID: requestID, UserID: userID, SubUID: acc.SubUID, Direction: dir, Seq: 1, Currency: currency,
		Amount: amount, MarginAccount: marginAccount, Status: LegPending, Kind: LegMasterToSubSpot}
	if dir == DirFromSub {
		first.Kind = LegSubSwapToSpot
	}
	err = storage.WithTx(ctx, t.db, func(tx *sql.Tx) error {
		if err := insertLeg(ctx, tx, first); err != nil {
			return err
		}
		if first.Kind.masterLeg() {
			if err := setClientOrderID(ctx, tx, first); err != nil {
				return err
			}
		}
		ins := dealer.NewStandardTransferOut(userID, currency, amount, ledgerBizID(first, "out"))
		if dir == DirFromSub {
			ins = dealer.NewCustodyFreeze(userID, currency, amount, ledgerBizID(first, "freeze"))
		}
		_, err := t.dealer.ExecuteInTx(ctx, tx, ins)
		return err
	})
	switch {
	case storage.IsDupEntry(err):
		if legs, lerr := listLegs(ctx, t.db, userID, requestID); lerr == nil && len(legs) > 0 {
			return t.drive(ctx, userID, requestID) // 同一 request_id 并发提交
		}
		return nil, ErrTransferInProgress
	case err != nil:
		return nil, fmt.Errorf("start %s transfer for user %d: %w", dir, userID, err)
	}
	t.logger.Info("sub-account transfer started", slog.Int64("user_id", userID), slog.String("request_id", requestID),
		slog.String("direction", string(dir)), slog.String("currency", currency), slog.String("amount", amount.String()),
		slog.String("margin_account", marginAccount))
	return t.drive(ctx, userID, requestID)
}

func ledgerBizID(l *Leg, action string) string {
	return "subxfer:" + strconv.FormatInt(l.UserID, 10) + ":" + l.RequestID + ":" + action
}

// drive 推进一个划转请求直到完成、回滚完成、需要人工，或遇到暂时无法解决的未知结果。
func (t *Transfers) drive(ctx context.Context, userID int64, requestID string) (*TransferResult, error) {
	for i := 0; i < maxDriveIterations; i++ {
		legs, err := listLegs(ctx, t.db, userID, requestID)
		if err != nil {
			return nil, err
		}
		last := legs[len(legs)-1]
		switch last.Status {
		case LegPending, LegInFlight, LegUnknown:
			progressed, err := t.processLeg(ctx, &last)
			if err != nil {
				return result(legs), err
			}
			if !progressed {
				return t.reload(ctx, userID, requestID)
			}
		case LegSucceeded:
			next, ok := nextLeg(&last)
			if !ok {
				return result(legs), nil
			}
			if err := t.createLeg(ctx, &last, next); err != nil {
				return result(legs), err
			}
		case LegFailed:
			if last.Seq != 2 {
				return result(legs), nil // 第 1 段失败已回滚；第 3 段失败已转人工
			}
			if err := t.createLeg(ctx, &last, reversalLeg(&last)); err != nil {
				return result(legs), err
			}
		case LegManual:
			return result(legs), nil
		}
	}
	return t.reload(ctx, userID, requestID)
}

func (t *Transfers) reload(ctx context.Context, userID int64, requestID string) (*TransferResult, error) {
	legs, err := listLegs(ctx, t.db, userID, requestID)
	if err != nil {
		return nil, err
	}
	return result(legs), nil
}

func nextLeg(l *Leg) (LegKind, bool) {
	switch {
	case l.Seq == 1 && l.Direction == DirToSub:
		return LegSubSpotToSwap, true
	case l.Seq == 1 && l.Direction == DirFromSub:
		return LegSubSpotToMaster, true
	}
	return "", false
}

func reversalLeg(l *Leg) LegKind {
	if l.Direction == DirToSub {
		return LegSubSpotToMaster
	}
	return LegSubSpotToSwap
}

func (t *Transfers) createLeg(ctx context.Context, prev *Leg, kind LegKind) error {
	l := &Leg{RequestID: prev.RequestID, UserID: prev.UserID, SubUID: prev.SubUID, Direction: prev.Direction, Seq: prev.Seq + 1,
		Kind: kind, Currency: prev.Currency, Amount: prev.Amount, MarginAccount: prev.MarginAccount, Status: LegPending}
	err := storage.WithTx(ctx, t.db, func(tx *sql.Tx) error {
		if err := insertLeg(ctx, tx, l); err != nil {
			return err
		}
		if kind.masterLeg() {
			return setClientOrderID(ctx, tx, l)
		}
		return nil
	})
	if storage.IsDupEntry(err) {
		return nil // 并发推进已创建
	}
	if err != nil {
		return fmt.Errorf("create transfer leg %d for %s: %w", l.Seq, l.RequestID, err)
	}
	return nil
}

func result(legs []Leg) *TransferResult {
	r := &TransferResult{Legs: legs}
	if len(legs) == 0 {
		return r
	}
	r.RequestID, r.Direction = legs[0].RequestID, legs[0].Direction
	last := legs[len(legs)-1]
	switch {
	case last.Status == LegSucceeded && last.Seq == 2:
		r.Status = LegSucceeded
	case last.Status == LegSucceeded && last.Seq == 3, last.Status == LegFailed && last.Seq == 1:
		r.Status = LegFailed // 已回滚
	case last.Status == LegFailed && last.Seq == 3, last.Status == LegManual:
		r.Status = LegManual
	default:
		r.Status = last.Status
	}
	return r
}

// processLeg 处理一段划转。返回 progressed=false 表示结果仍未知、需稍后重试（由 Resume 或再次调用推进）。
// 每次调用至多立即重发一次，避免火币持续故障时无界重试。
func (t *Transfers) processLeg(ctx context.Context, l *Leg) (bool, error) {
	switch l.Status {
	case LegInFlight:
		// 超时未完成的 IN_FLIGHT 视为崩溃遗留；未超时说明另一协程正在处理
		ok, err := staleToUnknown(ctx, t.db, l, time.Now().Add(-t.grace))
		if err != nil || !ok {
			return false, err
		}
		return t.resolveUnknown(ctx, l, true)
	case LegUnknown:
		return t.resolveUnknown(ctx, l, true)
	}
	progressed, err := t.send(ctx, l, []LegStatus{LegPending})
	if err != nil || progressed {
		return progressed, err
	}
	// 刚发生结果未知：立即核验一次（不重发现货-合约划转）
	return t.resolveUnknown(ctx, l, false)
}

// send 发起一段划转（from 为允许的当前状态）。结果未知时将段置为 UNKNOWN 并返回 progressed=false。
func (t *Transfers) send(ctx context.Context, l *Leg, from []LegStatus) (bool, error) {
	if !l.Kind.masterLeg() && containsStatus(from, LegPending) {
		// 现货-合约划转：首次发起前记录子账户现货余额快照（重发时沿用，因已核验余额未变动）
		spot, err := t.subSpot(ctx, l)
		if err != nil {
			return false, err
		}
		l.SpotBefore = decimal.NewNullDecimal(spot)
	}
	claimed, err := casLeg(ctx, t.db, l, from, LegInFlight, true)
	if err != nil || !claimed {
		return false, err
	}

	var htxID int64
	switch l.Kind {
	case LegMasterToSubSpot, LegSubSpotToMaster:
		typ := htx.MasterTransferOut
		if l.Kind == LegSubSpotToMaster {
			typ = htx.MasterTransferIn
		}
		htxID, err = t.spot.SubUserTransfer(ctx, t.master, htx.MasterTransferRequest{
			SubUID: l.SubUID, Currency: strings.ToLower(l.Currency), Amount: l.Amount, ClientOrderID: l.ClientOrderID, Type: typ,
		})
	default:
		var cred *htx.Credentials
		if cred, err = t.keys.Get(ctx, l.UserID); err == nil {
			req := htx.AccountTransferRequest{From: htx.LineSpot, To: htx.LineLinearSwap, Currency: strings.ToLower(l.Currency),
				Amount: l.Amount, MarginAccount: l.MarginAccount}
			if l.Kind == LegSubSwapToSpot {
				req.From, req.To = htx.LineLinearSwap, htx.LineSpot
			}
			htxID, err = t.spot.AccountTransfer(ctx, cred, req)
		}
	}

	if err == nil {
		l.HTXTransferID = htxID
		return true, t.finish(ctx, l, LegSucceeded, "")
	}
	if htx.IsRetryable(err) {
		l.LastError = err.Error()
		if _, uerr := casLeg(ctx, t.db, l, []LegStatus{LegInFlight}, LegUnknown, false); uerr != nil {
			return false, uerr
		}
		t.logger.Warn("transfer leg outcome unknown", slog.Int64("user_id", l.UserID), slog.String("request_id", l.RequestID),
			slog.Int("leg", l.Seq), slog.String("kind", string(l.Kind)), slog.String("error", err.Error()))
		return false, nil
	}
	return true, t.finish(ctx, l, LegFailed, err.Error())
}

// resolveUnknown 处理结果未知的一段。allowResend=false 时现货-合约划转只核验、不重发。
func (t *Transfers) resolveUnknown(ctx context.Context, l *Leg, allowResend bool) (bool, error) {
	if l.Kind.masterLeg() {
		if l.Attempts >= maxMasterLegAttempts {
			return true, t.manual(ctx, l, fmt.Sprintf("outcome unknown after %d attempts", l.Attempts))
		}
		// 同一 client-order-id 重发，火币保证幂等
		return t.send(ctx, l, []LegStatus{LegUnknown})
	}

	if !l.SpotBefore.Valid {
		return true, t.manual(ctx, l, "no spot balance snapshot")
	}
	now, err := t.subSpot(ctx, l)
	if err != nil {
		if htx.IsRetryable(err) {
			return false, nil // 余额暂时查不到：保持 UNKNOWN，稍后再核验
		}
		return false, err
	}
	before := l.SpotBefore.Decimal
	executed := before.Sub(l.Amount) // 现货 -> 合约：现货减少
	if l.Kind == LegSubSwapToSpot {
		executed = before.Add(l.Amount)
	}
	switch {
	case now.Equal(executed):
		t.logger.Info("transfer leg verified by balance", slog.Int64("user_id", l.UserID), slog.String("request_id", l.RequestID),
			slog.Int("leg", l.Seq), slog.String("spot_before", before.String()), slog.String("spot_now", now.String()))
		return true, t.finish(ctx, l, LegSucceeded, "verified by balance")
	case now.Equal(before):
		if !allowResend {
			return false, nil
		}
		t.logger.Info("transfer leg verified not executed; resending", slog.Int64("user_id", l.UserID),
			slog.String("request_id", l.RequestID), slog.Int("leg", l.Seq))
		return t.send(ctx, l, []LegStatus{LegUnknown})
	default:
		return true, t.manual(ctx, l, fmt.Sprintf("balance mismatch: before %s now %s amount %s", before, now, l.Amount))
	}
}

func (t *Transfers) subSpot(ctx context.Context, l *Leg) (decimal.Decimal, error) {
	accounts, err := t.spot.SubUserSpotBalance(ctx, t.master, l.SubUID)
	if err != nil {
		return decimal.Zero, fmt.Errorf("query sub spot balance: %w", err)
	}
	return htx.SpotTradeBalance(accounts, strings.ToLower(l.Currency)), nil
}

// finish 段落定（SUCCEEDED / FAILED），并在同一事务内完成对应的账本动作。
func (t *Transfers) finish(ctx context.Context, l *Leg, to LegStatus, msg string) error {
	l.LastError = msg
	if to == LegSucceeded {
		l.LastError = ""
	}
	err := storage.WithTx(ctx, t.db, func(tx *sql.Tx) error {
		ok, err := casLeg(ctx, tx, l, []LegStatus{LegInFlight, LegUnknown}, to, false)
		if err != nil {
			return err
		}
		if !ok {
			return fmt.Errorf("transfer leg %d changed concurrently", l.ID)
		}
		if ins, ok := ledgerOnFinish(l, to); ok {
			if _, err := t.dealer.ExecuteInTx(ctx, tx, ins); err != nil {
				return fmt.Errorf("ledger for leg %d: %w", l.ID, err)
			}
		}
		return nil
	})
	if err != nil {
		return err
	}
	level := slog.LevelInfo
	if to == LegFailed {
		level = slog.LevelWarn
	}
	t.logger.Log(ctx, level, "transfer leg finished", slog.Int64("user_id", l.UserID), slog.String("request_id", l.RequestID),
		slog.Int("leg", l.Seq), slog.String("kind", string(l.Kind)), slog.String("status", string(to)),
		slog.Int64("htx_transfer_id", l.HTXTransferID), slog.String("amount", l.Amount.String()), slog.String("detail", msg))
	return nil
}

// ledgerOnFinish 段落定时的账本动作。
func ledgerOnFinish(l *Leg, to LegStatus) (dealer.Instruction, bool) {
	switch {
	case l.Direction == DirToSub && l.Seq == 1 && to == LegFailed,
		l.Direction == DirToSub && l.Seq == 3 && to == LegSucceeded:
		// 资金未离开母账户 / 已退回母账户：HTX_CUSTODY -> SWAP
		return dealer.NewStandardTransferIn(l.UserID, l.Currency, l.Amount, ledgerBizID(l, "revert")), true
	case l.Direction == DirFromSub && l.Seq == 2 && to == LegSucceeded:
		return dealer.NewCustodySettleOut(l.UserID, l.Currency, l.Amount, ledgerBizID(l, "in")), true
	case l.Direction == DirFromSub && l.Seq == 1 && to == LegFailed,
		l.Direction == DirFromSub && l.Seq == 3 && to == LegSucceeded:
		return dealer.NewCustodyRelease(l.UserID, l.Currency, l.Amount, ledgerBizID(l, "release")), true
	}
	return dealer.Instruction{}, false
}

func (t *Transfers) manual(ctx context.Context, l *Leg, reason string) error {
	l.LastError = reason
	if _, err := casLeg(ctx, t.db, l, []LegStatus{LegInFlight, LegUnknown}, LegManual, false); err != nil {
		return err
	}
	t.logger.Error("transfer leg requires manual handling", slog.Int64("user_id", l.UserID), slog.String("request_id", l.RequestID),
		slog.Int("leg", l.Seq), slog.String("kind", string(l.Kind)), slog.String("amount", l.Amount.String()), slog.String("reason", reason))
	return nil
}

// Resume 恢复处理在途/未知的划转（后台定时调用）。
func (t *Transfers) Resume(ctx context.Context, limit int) (int, error) {
	if !t.enabled {
		return 0, nil
	}
	ids, err := listStaleLegs(ctx, t.db, time.Now().Add(-t.grace), limit)
	if err != nil {
		return 0, err
	}
	done := 0
	for _, id := range ids {
		l, err := getLeg(ctx, t.db, id)
		if err != nil {
			return done, err
		}
		r, err := t.drive(ctx, l.UserID, l.RequestID)
		if err != nil {
			t.logger.Error("resume transfer failed", slog.Uint64("leg_id", id), slog.String("error", err.Error()))
			continue
		}
		if r.Status == LegSucceeded || r.Status == LegFailed {
			done++
		}
	}
	return done, nil
}

func staleToUnknown(ctx context.Context, q storage.Querier, l *Leg, cutoff time.Time) (bool, error) {
	res, err := q.ExecContext(ctx,
		`UPDATE subaccount_transfers SET status = 'UNKNOWN' WHERE id = ? AND status = 'IN_FLIGHT' AND updated_at < ?`, l.ID, cutoff.UTC())
	if err != nil {
		return false, fmt.Errorf("mark stale leg %d unknown: %w", l.ID, err)
	}
	n, _ := res.RowsAffected()
	if n == 1 {
		l.Status = LegUnknown
	}
	return n == 1, nil
}

func containsStatus(list []LegStatus, s LegStatus) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}
