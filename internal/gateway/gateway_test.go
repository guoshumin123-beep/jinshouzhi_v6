package gateway_test

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/shopspring/decimal"

	"backend_v2/internal/dealer"
	"backend_v2/internal/gateway"
	"backend_v2/internal/gateway/standard"
	"backend_v2/internal/gateway/synthetic"
	"backend_v2/internal/htx"
	"backend_v2/internal/storage/storagetest"
	"backend_v2/internal/subaccount"
	"backend_v2/internal/swap"
)

// ---------------------------------------------------------------------------
// 测试替身（全部本地，不访问火币）
// ---------------------------------------------------------------------------

type fakeMarket struct {
	mu     sync.Mutex
	quotes map[string]synthetic.Quote
	marks  map[string]decimal.Decimal
}

// setBook 设置单档盘口；标记价默认取中间价。
func (m *fakeMarket) setBook(contract, bid, ask, size string) {
	m.setDepth(contract,
		[]synthetic.Level{{Price: dec(bid), Size: dec(size)}},
		[]synthetic.Level{{Price: dec(ask), Size: dec(size)}})
}

func (m *fakeMarket) setDepth(contract string, bids, asks []synthetic.Level) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.quotes[contract] = synthetic.Quote{Bids: bids, Asks: asks, At: time.Now()}
	delete(m.marks, contract)
}

func (m *fakeMarket) setMark(contract, price string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.marks[contract] = dec(price)
}

func (m *fakeMarket) Quote(c string) (synthetic.Quote, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	q, ok := m.quotes[c]
	if ok {
		q.At = time.Now()
	}
	return q, ok
}

func (m *fakeMarket) Mark(c string) (decimal.Decimal, time.Time, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if p, ok := m.marks[c]; ok {
		return p, time.Now(), true
	}
	q, ok := m.quotes[c]
	if !ok || len(q.Bids) == 0 || len(q.Asks) == 0 {
		return decimal.Zero, time.Time{}, false
	}
	return q.Bids[0].Price.Add(q.Asks[0].Price).Div(decimal.NewFromInt(2)), time.Now(), true
}

var tiers = []synthetic.RiskTier{
	{MaxNotional: dec("100000"), MMR: dec("0.005"), MaxLeverage: 100},
	{MMR: dec("0.01"), MaxLeverage: 20},
}

type fakeSpecs struct{}

func (fakeSpecs) Spec(c string) (synthetic.ContractSpec, bool) {
	switch c {
	case "BTC-USDT":
		return synthetic.ContractSpec{Size: dec("0.001"), MarginCurrency: "USDT", Tiers: tiers}, true
	case "ETH-USDT":
		return synthetic.ContractSpec{Size: dec("0.01"), MarginCurrency: "USDT", Tiers: tiers}, true
	}
	return synthetic.ContractSpec{}, false
}

type fakeKeys struct{ creds map[int64]*htx.Credentials }

func (f *fakeKeys) Get(_ context.Context, userID int64) (*htx.Credentials, error) {
	c, ok := f.creds[userID]
	if !ok {
		return nil, subaccount.ErrNoSubAccount
	}
	return c, nil
}
func (f *fakeKeys) Invalidate(int64) {}

type submitCall struct {
	accessKey string
	req       htx.SwapOrderRequest
}

type fakeTrader struct {
	mu     sync.Mutex
	calls  []submitCall
	levers []string
	fail   error
}

func (f *fakeTrader) SetLeverage(_ context.Context, cred *htx.Credentials, contract, mode string, lever int) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.levers = append(f.levers, fmt.Sprintf("%s|%s|%s|%d", cred.AccessKey, contract, mode, lever))
	return nil
}

func (f *fakeTrader) SwapPlaceOrder(_ context.Context, cred *htx.Credentials, req htx.SwapOrderRequest) (*htx.SwapOrderResponse, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, submitCall{cred.AccessKey, req})
	if f.fail != nil {
		return nil, f.fail
	}
	return &htx.SwapOrderResponse{OrderID: fmt.Sprint(1000 + len(f.calls))}, nil
}

func (f *fakeTrader) snapshot() []submitCall {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]submitCall(nil), f.calls...)
}

// ---------------------------------------------------------------------------

type env struct {
	t        *testing.T
	db       *sql.DB
	dealer   *dealer.MySQLDealer
	profiles *gateway.ProfileStore
	router   *gateway.Router
	engine   *synthetic.Engine
	market   *fakeMarket
	trader   *fakeTrader
}

var engineCfg = synthetic.Config{
	MakerFeeRate:       dec("0.0002"),
	TakerFeeRate:       dec("0.0005"),
	LiquidationFeeRate: dec("0.0005"),
	SpreadBps:          dec("10"),
	MarketBand:         dec("0.05"),
	MaxQuoteAge:        5 * time.Second,
	MaxMarkAge:         5 * time.Second,
}

func setup(t *testing.T) *env {
	t.Helper()
	db := storagetest.OpenDB(t)
	log := storagetest.QuietLogger()
	dl := dealer.NewMySQLDealer(db, log)
	profiles, err := gateway.NewProfileStore(db, gateway.DefaultProfile, log)
	if err != nil {
		t.Fatal(err)
	}
	market := &fakeMarket{quotes: map[string]synthetic.Quote{}, marks: map[string]decimal.Decimal{}}
	market.setBook("BTC-USDT", "60000", "60000", "1000")
	market.setBook("ETH-USDT", "3000", "3000", "1000")
	keys := &fakeKeys{creds: map[int64]*htx.Credentials{}}
	for _, uid := range []int64{1, 2} { // 用户 3 及以上未绑定子账户
		c, _ := htx.NewCredentials(fmt.Sprintf("sub-ak-user-%d", uid), fmt.Sprintf("sub-sk-user-%d", uid))
		keys.creds[uid] = c
	}
	trader := &fakeTrader{}
	engine := synthetic.NewEngine(db, dl, market, market, fakeSpecs{}, engineCfg, log)
	router := gateway.NewRouter(db, profiles, log, engine, standard.NewExecutor(db, keys, trader, log))
	e := &env{t: t, db: db, dealer: dl, profiles: profiles, router: router, engine: engine, market: market, trader: trader}
	for _, uid := range []int64{1, 2, 3, 4} {
		e.fund(uid, "10000")
	}
	return e
}

func (e *env) fund(uid int64, amt string) {
	e.t.Helper()
	ctx := context.Background()
	a := dec(amt)
	if _, err := e.dealer.Execute(ctx, dealer.NewDeposit(uid, "USDT", a, fmt.Sprintf("dep-%d-%s", uid, amt))); err != nil {
		e.t.Fatal(err)
	}
	if _, err := e.dealer.Execute(ctx, dealer.NewTransfer(uid, dealer.AccountSpot, dealer.AccountSwap, "USDT", a, fmt.Sprintf("tr-%d-%s", uid, amt))); err != nil {
		e.t.Fatal(err)
	}
}

func (e *env) synthetic(uid int64) {
	e.t.Helper()
	if err := e.profiles.SetRoute(context.Background(), uid, swap.RouteSynthetic, "admin@test"); err != nil {
		e.t.Fatal(err)
	}
}

func (e *env) bal(uid int64, typ dealer.AccountType) dealer.BalanceSnapshot {
	e.t.Helper()
	owner := uid
	if typ.IsSystem() {
		owner = 0
	}
	b, err := e.dealer.Balance(context.Background(), dealer.AccountKey{OwnerID: owner, Type: typ, Currency: "USDT"})
	if err != nil {
		e.t.Fatal(err)
	}
	return b
}

func (e *env) assertBal(uid int64, typ dealer.AccountType, avail, frozen string) {
	e.t.Helper()
	b := e.bal(uid, typ)
	if !b.Available.Equal(dec(avail)) || !b.Frozen.Equal(dec(frozen)) {
		e.t.Fatalf("user %d %s = %s/%s, want %s/%s", uid, typ, b.Available, b.Frozen, avail, frozen)
	}
}

// assertConserved 全局守恒：所有账户余额之和为 0，且用户账户无负数。
func (e *env) assertConserved() {
	e.t.Helper()
	var total string
	if err := e.db.QueryRow(`SELECT COALESCE(SUM(available + frozen), 0) FROM accounts`).Scan(&total); err != nil {
		e.t.Fatal(err)
	}
	if !dec(total).IsZero() {
		e.t.Fatalf("ledger not conserved: %s", total)
	}
	var neg int
	if err := e.db.QueryRow(`SELECT COUNT(*) FROM accounts WHERE owner_id > 0 AND (available < 0 OR frozen < 0)`).Scan(&neg); err != nil || neg != 0 {
		e.t.Fatalf("negative user balances: %d (%v)", neg, err)
	}
}

func (e *env) count(q string, args ...any) int {
	e.t.Helper()
	var n int
	if err := e.db.QueryRow(q, args...).Scan(&n); err != nil {
		e.t.Fatal(err)
	}
	return n
}

func (e *env) place(r gateway.PlaceOrderRequest) *swap.Order {
	e.t.Helper()
	o, err := e.router.PlaceOrder(context.Background(), r)
	if err != nil {
		e.t.Fatalf("place %s: %v", r.ClientOrderID, err)
	}
	return o
}

func (e *env) openPos(uid int64, contract string, mode swap.MarginMode) *swap.Position {
	e.t.Helper()
	p, err := swap.FindOpenPosition(context.Background(), e.db, uid, contract, mode)
	if err != nil {
		e.t.Fatal(err)
	}
	return p
}

func market(uid int64, cid string, side swap.Side, vol int64, lev int) gateway.PlaceOrderRequest {
	return gateway.PlaceOrderRequest{
		UserID: uid, ClientOrderID: cid, ContractCode: "BTC-USDT",
		Side: side, OrderType: swap.OrderMarket, Volume: decimal.NewFromInt(vol), Leverage: lev,
	}
}

func limit(uid int64, cid string, side swap.Side, vol int64, price string, lev int) gateway.PlaceOrderRequest {
	r := market(uid, cid, side, vol, lev)
	r.OrderType, r.Price = swap.OrderLimit, dec(price)
	return r
}

var errHTXDown = errors.New("htx unavailable")

func dec(s string) decimal.Decimal { return decimal.RequireFromString(s) }

// ---------------------------------------------------------------------------
// 画像与路由
// ---------------------------------------------------------------------------

func TestDefaultProfileIsStandardAndRequiresSubAccount(t *testing.T) {
	e := setup(t)
	ctx := context.Background()

	p, err := e.profiles.Get(ctx, e.db, 1)
	if err != nil || p.Route != swap.RouteStandard || p.MarginMode != swap.MarginCross {
		t.Fatalf("default profile = %+v, %v", p, err)
	}
	o := e.place(market(1, "s1", swap.SideBuy, 5, 10))
	if o.Route != swap.RouteStandard || o.Status != swap.OrderSubmitted || o.MarginMode != swap.MarginCross {
		t.Fatalf("order = %+v", o)
	}
	calls := e.trader.snapshot()
	if len(calls) != 1 || calls[0].accessKey != "sub-ak-user-1" || calls[0].req.MarginMode != htx.MarginCross {
		t.Fatalf("htx calls = %+v", calls)
	}

	// 未绑定子账户的新用户：不允许交易，且没有任何落库
	_, err = e.router.PlaceOrder(ctx, market(3, "x1", swap.SideBuy, 1, 10))
	if !errors.Is(err, subaccount.ErrNoSubAccount) {
		t.Fatalf("want ErrNoSubAccount, got %v", err)
	}
	if n := e.count(`SELECT COUNT(*) FROM swap_orders WHERE user_id = 3`); n != 0 {
		t.Fatalf("orders for unbound user: %d", n)
	}
	if len(e.trader.snapshot()) != 1 {
		t.Fatal("must never fall back to another credential")
	}
}

func TestStandardPassesThroughMarginMode(t *testing.T) {
	e := setup(t)
	r := market(2, "iso", swap.SideSell, 3, 20)
	r.MarginMode = swap.MarginIsolated
	e.place(r)
	r2 := market(2, "cross", swap.SideSell, 3, 20)
	e.place(r2)
	calls := e.trader.snapshot()
	if len(calls) != 2 || calls[0].req.MarginMode != htx.MarginIsolated || calls[1].req.MarginMode != htx.MarginCross || calls[0].accessKey != "sub-ak-user-2" {
		t.Fatalf("calls = %+v", calls)
	}
}

func TestStandardSubmitFailureMarksRejected(t *testing.T) {
	e := setup(t)
	e.trader.fail = errHTXDown
	o, err := e.router.PlaceOrder(context.Background(), market(1, "f1", swap.SideBuy, 1, 10))
	if !errors.Is(err, errHTXDown) || o == nil || o.Status != swap.OrderRejected {
		t.Fatalf("got %+v, %v", o, err)
	}
	if n := e.count(`SELECT COUNT(*) FROM swap_orders WHERE id = ? AND status = 'REJECTED'`, o.ID); n != 1 {
		t.Fatal("rejected status not persisted")
	}
	// 被拒订单不再占用槽位：切换画像后新单按新画像路由
	e.synthetic(1)
	if o2 := e.place(market(1, "f2", swap.SideBuy, 1, 10)); o2.Route != swap.RouteSynthetic {
		t.Fatalf("route = %s", o2.Route)
	}
}

func TestMarginModePreferenceAndIndependentSlots(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	e.synthetic(1)
	if err := e.profiles.SetMarginMode(ctx, 1, swap.MarginIsolated); err != nil {
		t.Fatal(err)
	}
	p, _ := e.profiles.Get(ctx, e.db, 1)
	if p.Route != swap.RouteSynthetic || p.MarginMode != swap.MarginIsolated {
		t.Fatalf("profile = %+v", p)
	}

	iso := e.place(market(1, "a", swap.SideBuy, 10, 10)) // 未指定 → 取画像默认 ISOLATED
	r := market(1, "b", swap.SideSell, 10, 10)
	r.MarginMode = swap.MarginCross // 显式指定覆盖默认
	cross := e.place(r)
	if iso.MarginMode != swap.MarginIsolated || cross.MarginMode != swap.MarginCross {
		t.Fatalf("modes = %s / %s", iso.MarginMode, cross.MarginMode)
	}
	// 同一合约的逐仓多仓与全仓空仓是两个独立槽位，互不平仓
	if l := e.openPos(1, "BTC-USDT", swap.MarginIsolated); l == nil || l.Direction != swap.DirectionLong {
		t.Fatalf("isolated slot = %+v", l)
	}
	if s := e.openPos(1, "BTC-USDT", swap.MarginCross); s == nil || s.Direction != swap.DirectionShort {
		t.Fatalf("cross slot = %+v", s)
	}
	// Admin 改路由不影响用户的保证金模式偏好
	if err := e.profiles.SetRoute(ctx, 1, swap.RouteStandard, "admin@test"); err != nil {
		t.Fatal(err)
	}
	if p, _ := e.profiles.Get(ctx, e.db, 1); p.MarginMode != swap.MarginIsolated {
		t.Fatalf("margin mode lost: %+v", p)
	}
	e.assertConserved()
}

// 平反手：剩余反向开仓部分随原持仓路由；平仓且无在途订单后，新开仓才读新画像。
func TestFlipKeepsOriginalRouteThenNewProfileApplies(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	e.synthetic(1)

	long := e.place(market(1, "o1", swap.SideBuy, 100, 10))
	if long.Route != swap.RouteSynthetic {
		t.Fatalf("route = %s", long.Route)
	}
	oldPos := e.openPos(1, "BTC-USDT", swap.MarginCross)

	if err := e.profiles.SetRoute(ctx, 1, swap.RouteStandard, "admin@test"); err != nil {
		t.Fatal(err)
	}
	flip := e.place(market(1, "o2", swap.SideSell, 150, 10))
	if flip.Route != swap.RouteSynthetic || flip.Status != swap.OrderFilled || !flip.FilledVolume.Equal(dec("150")) {
		t.Fatalf("flip = %+v", flip)
	}
	if len(e.trader.snapshot()) != 0 {
		t.Fatal("flip must not reach HTX")
	}
	short := e.openPos(1, "BTC-USDT", swap.MarginCross)
	if short == nil || short.Direction != swap.DirectionShort || !short.Volume.Equal(dec("50")) || short.Route != swap.RouteSynthetic {
		t.Fatalf("new short = %+v", short)
	}
	if n := e.count(`SELECT COUNT(*) FROM swap_positions WHERE id = ? AND status = 'CLOSED' AND close_reason = 'FLIP'`, oldPos.ID); n != 1 {
		t.Fatal("old position not closed by flip")
	}

	e.place(market(1, "o3", swap.SideBuy, 50, 10)) // 平掉反手空仓（仍走 SYNTHETIC）
	if e.openPos(1, "BTC-USDT", swap.MarginCross) != nil {
		t.Fatal("position should be closed")
	}
	next := e.place(market(1, "o4", swap.SideBuy, 1, 10))
	if next.Route != swap.RouteStandard {
		t.Fatalf("new lifecycle should use new profile, got %s", next.Route)
	}
	if calls := e.trader.snapshot(); len(calls) != 1 || calls[0].accessKey != "sub-ak-user-1" {
		t.Fatalf("htx calls = %+v", calls)
	}
	e.assertConserved()
}

// 槽位内存在在途挂单时，路由同样固化（防止同一槽位出现两条通道的持仓）。
func TestRestingOrderPinsSlotRoute(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	e.synthetic(1)
	rest := e.place(limit(1, "r1", swap.SideBuy, 10, "50000", 10))
	if rest.Status != swap.OrderOpen {
		t.Fatalf("status = %s", rest.Status)
	}
	if err := e.profiles.SetRoute(ctx, 1, swap.RouteStandard, "admin@test"); err != nil {
		t.Fatal(err)
	}
	if o := e.place(limit(1, "r2", swap.SideBuy, 10, "50000", 10)); o.Route != swap.RouteSynthetic {
		t.Fatalf("pinned by resting order, got %s", o.Route)
	}
	for _, cid := range []string{"r1", "r2"} {
		if _, err := e.router.CancelOrder(ctx, 1, cid); err != nil {
			t.Fatal(err)
		}
	}
	if o := e.place(limit(1, "r3", swap.SideBuy, 10, "50000", 10)); o.Route != swap.RouteStandard {
		t.Fatalf("slot released, want STANDARD, got %s", o.Route)
	}
	e.assertBal(1, dealer.AccountSwap, "10000", "0")
}

func TestLeverageMustMatchSlot(t *testing.T) {
	e := setup(t)
	e.synthetic(1)
	e.place(market(1, "a", swap.SideBuy, 10, 10))
	if _, err := e.router.PlaceOrder(context.Background(), market(1, "b", swap.SideBuy, 1, 20)); !errors.Is(err, gateway.ErrLeverageMismatch) {
		t.Fatalf("want ErrLeverageMismatch, got %v", err)
	}
}

func TestPlaceOrderIdempotent(t *testing.T) {
	e := setup(t)
	e.synthetic(1)
	first := e.place(market(1, "same", swap.SideBuy, 100, 10))
	again := e.place(market(1, "same", swap.SideBuy, 100, 10))
	if again.ID != first.ID {
		t.Fatalf("duplicate: %d vs %d", again.ID, first.ID)
	}
	if n := e.count(`SELECT COUNT(*) FROM swap_trades WHERE order_id = ?`, first.ID); n != 1 {
		t.Fatalf("trades = %d", n)
	}
}

func TestPositionImmutableInDB(t *testing.T) {
	e := setup(t)
	e.synthetic(1)
	e.place(market(1, "i1", swap.SideBuy, 1, 10))
	p := e.openPos(1, "BTC-USDT", swap.MarginCross)
	for _, stmt := range []string{
		`UPDATE swap_positions SET route = 'STANDARD' WHERE id = ?`,
		`UPDATE swap_positions SET margin_mode = 'ISOLATED' WHERE id = ?`,
		`UPDATE swap_positions SET direction = 'SHORT' WHERE id = ?`,
	} {
		if _, err := e.db.Exec(stmt, p.ID); err == nil || !strings.Contains(err.Error(), "immutable") {
			t.Fatalf("%s: want trigger rejection, got %v", stmt, err)
		}
	}
}

// 并发下单 + Admin 并发切换画像：同一槽位只有一个 OPEN 持仓、一条路由。
func TestConcurrentOrdersAndProfileSwitch(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	e.synthetic(1)

	const n = 20
	var wg sync.WaitGroup
	routes := make(chan swap.Route, n)
	errs := make(chan error, n+1)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			o, err := e.router.PlaceOrder(ctx, market(1, fmt.Sprintf("p%d", i), swap.SideBuy, 1, 10))
			if err != nil {
				errs <- err
				return
			}
			routes <- o.Route
		}(i)
	}
	wg.Add(1)
	go func() {
		defer wg.Done()
		if err := e.profiles.SetRoute(ctx, 1, swap.RouteStandard, "admin@test"); err != nil {
			errs <- err
		}
	}()
	wg.Wait()
	close(routes)
	close(errs)
	for err := range errs {
		t.Errorf("unexpected: %v", err)
	}
	seen := map[swap.Route]int{}
	for r := range routes {
		seen[r]++
	}
	if len(seen) != 1 {
		t.Fatalf("route split within one slot: %v", seen)
	}
	if c := e.count(`SELECT COUNT(*) FROM swap_positions WHERE user_id = 1 AND status = 'OPEN'`); c > 1 {
		t.Fatalf("open positions = %d", c)
	}
	e.assertConserved()
}
