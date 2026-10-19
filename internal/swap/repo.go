package swap

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/shopspring/decimal"

	"backend_v2/internal/storage"
)

// 本文件所有写操作都要求调用方已持有 LockUser。

// LockUser 获取用户级排他锁（行不存在时创建）。必须是事务内的第一把锁。
func LockUser(ctx context.Context, tx *sql.Tx, userID int64) error {
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO user_locks (user_id) VALUES (?) ON DUPLICATE KEY UPDATE user_id = user_id`, userID); err != nil {
		return fmt.Errorf("lock user %d: %w", userID, err)
	}
	var id int64
	if err := tx.QueryRowContext(ctx, `SELECT user_id FROM user_locks WHERE user_id = ? FOR UPDATE`, userID).Scan(&id); err != nil {
		return fmt.Errorf("lock user %d: %w", userID, err)
	}
	return nil
}

// ---------------------------------------------------------------------------
// Profiles
// ---------------------------------------------------------------------------

// GetProfile 读取用户画像；不存在时返回 defaults。
func GetProfile(ctx context.Context, q storage.Querier, userID int64, defaults Profile) (Profile, error) {
	p := Profile{UserID: userID}
	var route, mode string
	err := q.QueryRowContext(ctx, `SELECT route, margin_mode FROM user_trade_profiles WHERE user_id = ?`, userID).Scan(&route, &mode)
	if errors.Is(err, sql.ErrNoRows) {
		defaults.UserID = userID
		return defaults, nil
	}
	if err != nil {
		return p, fmt.Errorf("get profile %d: %w", userID, err)
	}
	p.Route, p.MarginMode = Route(route), MarginMode(mode)
	return p, nil
}

// SetProfileRoute 仅供 Admin。插入新行时 margin_mode 取 defaultMode。
func SetProfileRoute(ctx context.Context, q storage.Querier, userID int64, r Route, defaultMode MarginMode, operator string) error {
	_, err := q.ExecContext(ctx,
		`INSERT INTO user_trade_profiles (user_id, route, margin_mode, route_updated_by, route_updated_at)
		 VALUES (?, ?, ?, ?, CURRENT_TIMESTAMP(6))
		 ON DUPLICATE KEY UPDATE route = VALUES(route), route_updated_by = VALUES(route_updated_by),
		   route_updated_at = VALUES(route_updated_at)`,
		userID, string(r), string(defaultMode), operator)
	if err != nil {
		return fmt.Errorf("set route %d: %w", userID, err)
	}
	return nil
}

// SetProfileMarginMode 用户自选默认保证金模式。插入新行时 route 取 defaultRoute。
func SetProfileMarginMode(ctx context.Context, q storage.Querier, userID int64, m MarginMode, defaultRoute Route) error {
	_, err := q.ExecContext(ctx,
		`INSERT INTO user_trade_profiles (user_id, route, margin_mode) VALUES (?, ?, ?)
		 ON DUPLICATE KEY UPDATE margin_mode = VALUES(margin_mode)`,
		userID, string(defaultRoute), string(m))
	if err != nil {
		return fmt.Errorf("set margin mode %d: %w", userID, err)
	}
	return nil
}

// ---------------------------------------------------------------------------
// Positions
// ---------------------------------------------------------------------------

const positionCols = `id, user_id, contract_code, margin_mode, route, direction, volume, avg_price, margin, leverage,
	liquidation_price, realized_pnl, fee_paid, funding_paid, status, COALESCE(close_reason, ''), opened_at, closed_at`

func scanPosition(row interface{ Scan(...any) error }) (*Position, error) {
	var (
		p                                                Position
		mode, route, dir, status, reason                 string
		volume, avg, margin, liq, realized, fee, funding string
		closedAt                                         sql.NullTime
	)
	if err := row.Scan(&p.ID, &p.UserID, &p.ContractCode, &mode, &route, &dir, &volume, &avg, &margin, &p.Leverage,
		&liq, &realized, &fee, &funding, &status, &reason, &p.OpenedAt, &closedAt); err != nil {
		return nil, err
	}
	p.MarginMode, p.Route, p.Direction, p.Status, p.CloseReason =
		MarginMode(mode), Route(route), Direction(dir), PositionStatus(status), CloseReason(reason)
	if err := parseDecimals(map[*decimal.Decimal]string{
		&p.Volume: volume, &p.AvgPrice: avg, &p.Margin: margin, &p.LiquidationPrice: liq,
		&p.RealizedPnL: realized, &p.FeePaid: fee, &p.FundingPaid: funding,
	}); err != nil {
		return nil, fmt.Errorf("position %d: %w", p.ID, err)
	}
	if closedAt.Valid {
		p.ClosedAt = &closedAt.Time
	}
	return &p, nil
}

// FindOpenPosition 返回槽位 (user, contract, mode) 当前 OPEN 持仓，不存在返回 nil。
func FindOpenPosition(ctx context.Context, q storage.Querier, userID int64, contract string, mode MarginMode) (*Position, error) {
	p, err := scanPosition(q.QueryRowContext(ctx,
		`SELECT `+positionCols+` FROM swap_positions
		 WHERE user_id = ? AND contract_code = ? AND margin_mode = ? AND status = 'OPEN'`, userID, contract, string(mode)))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("find open position %d/%s/%s: %w", userID, contract, mode, err)
	}
	return p, nil
}

func GetPosition(ctx context.Context, q storage.Querier, id uint64) (*Position, error) {
	p, err := scanPosition(q.QueryRowContext(ctx, `SELECT `+positionCols+` FROM swap_positions WHERE id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("position %d: %w", id, ErrNotFound)
	}
	if err != nil {
		return nil, fmt.Errorf("get position %d: %w", id, err)
	}
	return p, nil
}

// ListOpenPositions 列出用户在某路由下的全部 OPEN 持仓；mode 为空表示不限。
func ListOpenPositions(ctx context.Context, q storage.Querier, userID int64, route Route, mode MarginMode) ([]*Position, error) {
	query := `SELECT ` + positionCols + ` FROM swap_positions WHERE user_id = ? AND route = ? AND status = 'OPEN'`
	args := []any{userID, string(route)}
	if mode != "" {
		query += ` AND margin_mode = ?`
		args = append(args, string(mode))
	}
	return queryPositions(ctx, q, query+` ORDER BY id`, args...)
}

// ListOpenPositionsByContract 列出某合约在某路由下的全部 OPEN 持仓（资金费结算用）。
func ListOpenPositionsByContract(ctx context.Context, q storage.Querier, route Route, contract string) ([]*Position, error) {
	return queryPositions(ctx, q,
		`SELECT `+positionCols+` FROM swap_positions WHERE route = ? AND status = 'OPEN' AND contract_code = ? ORDER BY id`,
		string(route), contract)
}

// ListUsersWithOpenPositions 返回某路由下持有 OPEN 持仓的用户（风控扫描用）。
func ListUsersWithOpenPositions(ctx context.Context, q storage.Querier, route Route) ([]int64, error) {
	rows, err := q.QueryContext(ctx,
		`SELECT DISTINCT user_id FROM swap_positions WHERE route = ? AND status = 'OPEN' ORDER BY user_id`, string(route))
	if err != nil {
		return nil, fmt.Errorf("list users with positions: %w", err)
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

// ListContractsWithOpenPositions 返回某路由下有 OPEN 持仓的合约。
func ListContractsWithOpenPositions(ctx context.Context, q storage.Querier, route Route) ([]string, error) {
	rows, err := q.QueryContext(ctx,
		`SELECT DISTINCT contract_code FROM swap_positions WHERE route = ? AND status = 'OPEN' ORDER BY contract_code`, string(route))
	if err != nil {
		return nil, fmt.Errorf("list contracts with positions: %w", err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var c string
		if err := rows.Scan(&c); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

func queryPositions(ctx context.Context, q storage.Querier, query string, args ...any) ([]*Position, error) {
	rows, err := q.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("query positions: %w", err)
	}
	defer rows.Close()
	var out []*Position
	for rows.Next() {
		p, err := scanPosition(rows)
		if err != nil {
			return nil, fmt.Errorf("scan position: %w", err)
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

func InsertPosition(ctx context.Context, tx *sql.Tx, p *Position) error {
	res, err := tx.ExecContext(ctx,
		`INSERT INTO swap_positions (user_id, contract_code, margin_mode, route, direction, volume, avg_price, margin,
		 leverage, liquidation_price, realized_pnl, fee_paid, funding_paid, status)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 'OPEN')`,
		p.UserID, p.ContractCode, string(p.MarginMode), string(p.Route), string(p.Direction),
		p.Volume.String(), p.AvgPrice.String(), p.Margin.String(), p.Leverage, p.LiquidationPrice.String(),
		p.RealizedPnL.String(), p.FeePaid.String(), p.FundingPaid.String())
	if err != nil {
		return fmt.Errorf("insert position %d/%s/%s: %w", p.UserID, p.ContractCode, p.MarginMode, err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		return err
	}
	p.ID, p.Status = uint64(id), PositionOpen
	return nil
}

// UpdatePosition 回写可变字段；route/margin_mode/direction 等固化字段从不出现在 UPDATE 中（触发器亦拦截）。
func UpdatePosition(ctx context.Context, tx *sql.Tx, p *Position) error {
	_, err := tx.ExecContext(ctx,
		`UPDATE swap_positions SET volume = ?, avg_price = ?, margin = ?, liquidation_price = ?, realized_pnl = ?,
		 fee_paid = ?, funding_paid = ?, status = ?, close_reason = NULLIF(?, ''),
		 closed_at = IF(? = 'CLOSED', COALESCE(closed_at, CURRENT_TIMESTAMP(6)), NULL)
		 WHERE id = ?`,
		p.Volume.String(), p.AvgPrice.String(), p.Margin.String(), p.LiquidationPrice.String(), p.RealizedPnL.String(),
		p.FeePaid.String(), p.FundingPaid.String(), string(p.Status), string(p.CloseReason), string(p.Status), p.ID)
	if err != nil {
		return fmt.Errorf("update position %d: %w", p.ID, err)
	}
	if p.Status == PositionClosed && p.ClosedAt == nil {
		now := time.Now()
		p.ClosedAt = &now
	}
	return nil
}

// ---------------------------------------------------------------------------
// Orders
// ---------------------------------------------------------------------------

const orderCols = `id, user_id, client_order_id, contract_code, margin_mode, route, side, order_type, reduce_only, source,
	volume, price, leverage, filled_volume, avg_fill_price, fee_paid, realized_pnl, frozen_amount, fill_seq, status,
	COALESCE(err_msg, ''), created_at`

func scanOrder(row interface{ Scan(...any) error }) (*Order, error) {
	var (
		o                                            Order
		mode, route, side, typ, source, status       string
		volume, price, filled, avg, fee, pnl, frozen string
	)
	if err := row.Scan(&o.ID, &o.UserID, &o.ClientOrderID, &o.ContractCode, &mode, &route, &side, &typ, &o.ReduceOnly, &source,
		&volume, &price, &o.Leverage, &filled, &avg, &fee, &pnl, &frozen, &o.FillSeq, &status, &o.ErrMsg, &o.CreatedAt); err != nil {
		return nil, err
	}
	o.MarginMode, o.Route, o.Side, o.OrderType, o.Source, o.Status =
		MarginMode(mode), Route(route), Side(side), OrderType(typ), OrderSource(source), OrderStatus(status)
	if err := parseDecimals(map[*decimal.Decimal]string{
		&o.Volume: volume, &o.Price: price, &o.FilledVolume: filled, &o.AvgFillPrice: avg,
		&o.FeePaid: fee, &o.RealizedPnL: pnl, &o.FrozenAmount: frozen,
	}); err != nil {
		return nil, fmt.Errorf("order %d: %w", o.ID, err)
	}
	return &o, nil
}

func FindOrderByClientID(ctx context.Context, q storage.Querier, userID int64, clientOrderID string) (*Order, error) {
	o, err := scanOrder(q.QueryRowContext(ctx,
		`SELECT `+orderCols+` FROM swap_orders WHERE user_id = ? AND client_order_id = ?`, userID, clientOrderID))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("find order %d/%s: %w", userID, clientOrderID, err)
	}
	return o, nil
}

func GetOrder(ctx context.Context, q storage.Querier, id uint64) (*Order, error) {
	o, err := scanOrder(q.QueryRowContext(ctx, `SELECT `+orderCols+` FROM swap_orders WHERE id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("order %d: %w", id, ErrNotFound)
	}
	if err != nil {
		return nil, fmt.Errorf("get order %d: %w", id, err)
	}
	return o, nil
}

// ListActiveOrders 用户在槽位中的在途订单；contract/mode 为空表示不限。
func ListActiveOrders(ctx context.Context, q storage.Querier, userID int64, contract string, mode MarginMode) ([]*Order, error) {
	query := `SELECT ` + orderCols + ` FROM swap_orders WHERE user_id = ? AND status IN (` + activeIn() + `)`
	args := []any{userID}
	if contract != "" {
		query += ` AND contract_code = ?`
		args = append(args, contract)
	}
	if mode != "" {
		query += ` AND margin_mode = ?`
		args = append(args, string(mode))
	}
	return queryOrders(ctx, q, query+` ORDER BY id`, args...)
}

// MatchCandidates Synthetic 撮合候选：价格穿越的挂单（买单 price >= bestAsk，卖单 price <= bestBid）。
func MatchCandidates(ctx context.Context, q storage.Querier, contract string, bestAsk, bestBid decimal.Decimal, limit int) ([]*Order, error) {
	return queryOrders(ctx, q,
		`SELECT `+orderCols+` FROM swap_orders
		 WHERE route = 'SYNTHETIC' AND contract_code = ? AND status IN ('OPEN', 'PARTIALLY_FILLED')
		   AND ((side = 'BUY' AND price >= ?) OR (side = 'SELL' AND price <= ?))
		 ORDER BY id LIMIT ?`,
		contract, bestAsk.String(), bestBid.String(), limit)
}

// ListRestingContracts 有 Synthetic 挂单的合约。
func ListRestingContracts(ctx context.Context, q storage.Querier) ([]string, error) {
	rows, err := q.QueryContext(ctx,
		`SELECT DISTINCT contract_code FROM swap_orders WHERE route = 'SYNTHETIC' AND status IN ('OPEN', 'PARTIALLY_FILLED')`)
	if err != nil {
		return nil, fmt.Errorf("list resting contracts: %w", err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var c string
		if err := rows.Scan(&c); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

func queryOrders(ctx context.Context, q storage.Querier, query string, args ...any) ([]*Order, error) {
	rows, err := q.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("query orders: %w", err)
	}
	defer rows.Close()
	var out []*Order
	for rows.Next() {
		o, err := scanOrder(rows)
		if err != nil {
			return nil, fmt.Errorf("scan order: %w", err)
		}
		out = append(out, o)
	}
	return out, rows.Err()
}

func activeIn() string {
	parts := make([]string, len(ActiveOrderStatuses))
	for i, s := range ActiveOrderStatuses {
		parts[i] = "'" + string(s) + "'"
	}
	return strings.Join(parts, ", ")
}

func InsertOrder(ctx context.Context, tx *sql.Tx, o *Order) error {
	if o.Source == "" {
		o.Source = SourceUser
	}
	res, err := tx.ExecContext(ctx,
		`INSERT INTO swap_orders (user_id, client_order_id, contract_code, margin_mode, route, side, order_type, reduce_only,
		 source, volume, price, leverage, status) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		o.UserID, o.ClientOrderID, o.ContractCode, string(o.MarginMode), string(o.Route), string(o.Side), string(o.OrderType),
		o.ReduceOnly, string(o.Source), o.Volume.String(), o.Price.String(), o.Leverage, string(o.Status))
	if err != nil {
		return fmt.Errorf("insert order %d/%s: %w", o.UserID, o.ClientOrderID, err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		return err
	}
	o.ID = uint64(id)
	return nil
}

// UpdateOrder 回写订单可变字段。
func UpdateOrder(ctx context.Context, tx *sql.Tx, o *Order) error {
	msg := o.ErrMsg
	if len(msg) > 255 {
		msg = msg[:255]
	}
	_, err := tx.ExecContext(ctx,
		`UPDATE swap_orders SET filled_volume = ?, avg_fill_price = ?, fee_paid = ?, realized_pnl = ?, frozen_amount = ?,
		 fill_seq = ?, status = ?, err_msg = NULLIF(?, '') WHERE id = ?`,
		o.FilledVolume.String(), o.AvgFillPrice.String(), o.FeePaid.String(), o.RealizedPnL.String(), o.FrozenAmount.String(),
		o.FillSeq, string(o.Status), msg, o.ID)
	if err != nil {
		return fmt.Errorf("update order %d: %w", o.ID, err)
	}
	return nil
}

// ---------------------------------------------------------------------------
// Trades & funding
// ---------------------------------------------------------------------------

func InsertTrade(ctx context.Context, tx *sql.Tx, t *Trade) error {
	res, err := tx.ExecContext(ctx,
		`INSERT INTO swap_trades (order_id, user_id, position_id, contract_code, margin_mode, route, side, kind, role,
		 price, volume, fee, realized_pnl, loss_shortfall) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		t.OrderID, t.UserID, t.PositionID, t.ContractCode, string(t.MarginMode), string(t.Route), string(t.Side),
		string(t.Kind), string(t.Role), t.Price.String(), t.Volume.String(), t.Fee.String(), t.RealizedPnL.String(),
		t.LossShortfall.String())
	if err != nil {
		return fmt.Errorf("insert trade order %d: %w", t.OrderID, err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		return err
	}
	t.ID = uint64(id)
	return nil
}

func ListTrades(ctx context.Context, q storage.Querier, orderID uint64) ([]*Trade, error) {
	rows, err := q.QueryContext(ctx,
		`SELECT id, order_id, user_id, position_id, contract_code, margin_mode, route, side, kind, role, price, volume,
		 fee, realized_pnl, loss_shortfall FROM swap_trades WHERE order_id = ? ORDER BY id`, orderID)
	if err != nil {
		return nil, fmt.Errorf("list trades %d: %w", orderID, err)
	}
	defer rows.Close()
	var out []*Trade
	for rows.Next() {
		var (
			t                             Trade
			mode, route, side, kind, role string
			price, vol, fee, pnl, short   string
		)
		if err := rows.Scan(&t.ID, &t.OrderID, &t.UserID, &t.PositionID, &t.ContractCode, &mode, &route, &side, &kind, &role,
			&price, &vol, &fee, &pnl, &short); err != nil {
			return nil, err
		}
		t.MarginMode, t.Route, t.Side, t.Kind, t.Role = MarginMode(mode), Route(route), Side(side), TradeKind(kind), Role(role)
		if err := parseDecimals(map[*decimal.Decimal]string{
			&t.Price: price, &t.Volume: vol, &t.Fee: fee, &t.RealizedPnL: pnl, &t.LossShortfall: short,
		}); err != nil {
			return nil, err
		}
		out = append(out, &t)
	}
	return out, rows.Err()
}

// FundingRound 资金费结算轮次。
type FundingRound struct {
	ContractCode string
	FundingTime  time.Time
	Rate         decimal.Decimal
	MarkPrice    decimal.Decimal
	Settled      bool
}

// BeginFundingRound 创建或读取轮次（幂等）。已 SETTLED 时 Settled=true。
func BeginFundingRound(ctx context.Context, db *sql.DB, r FundingRound) (*FundingRound, error) {
	if _, err := db.ExecContext(ctx,
		`INSERT INTO synthetic_funding_rounds (contract_code, funding_time, rate, mark_price, status)
		 VALUES (?, ?, ?, ?, 'SETTLING') ON DUPLICATE KEY UPDATE contract_code = contract_code`,
		r.ContractCode, r.FundingTime.UTC(), r.Rate.String(), r.MarkPrice.String()); err != nil {
		return nil, fmt.Errorf("begin funding round %s@%s: %w", r.ContractCode, r.FundingTime, err)
	}
	var (
		rate, mark, status string
		out                = FundingRound{ContractCode: r.ContractCode, FundingTime: r.FundingTime.UTC()}
	)
	if err := db.QueryRowContext(ctx,
		`SELECT rate, mark_price, status FROM synthetic_funding_rounds WHERE contract_code = ? AND funding_time = ?`,
		r.ContractCode, r.FundingTime.UTC()).Scan(&rate, &mark, &status); err != nil {
		return nil, fmt.Errorf("load funding round: %w", err)
	}
	// 以首次落库的费率与标记价为准，保证崩溃重启后重放结果一致
	if err := parseDecimals(map[*decimal.Decimal]string{&out.Rate: rate, &out.MarkPrice: mark}); err != nil {
		return nil, err
	}
	out.Settled = status == "SETTLED"
	return &out, nil
}

func FinishFundingRound(ctx context.Context, db *sql.DB, contract string, fundingTime time.Time) error {
	if _, err := db.ExecContext(ctx,
		`UPDATE synthetic_funding_rounds SET status = 'SETTLED', settled_at = CURRENT_TIMESTAMP(6)
		 WHERE contract_code = ? AND funding_time = ?`, contract, fundingTime.UTC()); err != nil {
		return fmt.Errorf("finish funding round %s@%s: %w", contract, fundingTime, err)
	}
	return nil
}

// InsertFundingPayment 幂等写入；已存在时返回 inserted=false。
func InsertFundingPayment(ctx context.Context, tx *sql.Tx, posID uint64, fundingTime time.Time, userID int64, contract string,
	rate, mark, amount, shortfall decimal.Decimal) (bool, error) {
	_, err := tx.ExecContext(ctx,
		`INSERT INTO swap_funding_payments (position_id, funding_time, user_id, contract_code, rate, mark_price, amount, shortfall)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		posID, fundingTime.UTC(), userID, contract, rate.String(), mark.String(), amount.String(), shortfall.String())
	if storage.IsDupEntry(err) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("insert funding payment pos %d: %w", posID, err)
	}
	return true, nil
}

func FundingPaymentExists(ctx context.Context, q storage.Querier, posID uint64, fundingTime time.Time) (bool, error) {
	var n int
	if err := q.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM swap_funding_payments WHERE position_id = ? AND funding_time = ?`,
		posID, fundingTime.UTC()).Scan(&n); err != nil {
		return false, fmt.Errorf("check funding payment: %w", err)
	}
	return n > 0, nil
}

func parseDecimals(m map[*decimal.Decimal]string) error {
	for dst, src := range m {
		v, err := decimal.NewFromString(src)
		if err != nil {
			return fmt.Errorf("parse decimal %q: %w", src, err)
		}
		*dst = v
	}
	return nil
}
