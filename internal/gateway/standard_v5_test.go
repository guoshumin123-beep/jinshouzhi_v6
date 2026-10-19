package gateway_test

import (
	"context"
	"errors"
	"strconv"
	"testing"

	"backend_v2/internal/gateway"
	"backend_v2/internal/gateway/standard"
	"backend_v2/internal/htx"
	"backend_v2/internal/htx/htxmock"
	"backend_v2/internal/storage/storagetest"
	"backend_v2/internal/subaccount"
	"backend_v2/internal/swap"
)

// mockKeys 用户 -> 其专属模拟子账户。
type mockKeys map[int64]htxmock.Account

func (m mockKeys) Get(_ context.Context, userID int64) (*htx.Credentials, error) {
	a, ok := m[userID]
	if !ok {
		return nil, subaccount.ErrNoSubAccount
	}
	return a.Credentials(), nil
}
func (mockKeys) Invalidate(int64) {}

type v5env struct {
	router *gateway.Router
	srv    *htxmock.Server
	keys   mockKeys
}

// setupV5 Standard 通道全链路：Router → standard.Executor → 真实 htx.Client → htxmock（校验签名与 V5 字段）。
func setupV5(t *testing.T) *v5env {
	t.Helper()
	db := storagetest.OpenDB(t)
	log := storagetest.QuietLogger()
	keys := mockKeys{
		1: {UID: 90001, AccessKey: "u1-sub-ak", Secret: "u1-sub-sk"},
		2: {UID: 90002, AccessKey: "u2-sub-ak", Secret: "u2-sub-sk"},
	}
	srv := htxmock.New(keys[1], keys[2])
	t.Cleanup(srv.Close)
	client, err := htx.NewClient(srv.URL, htx.WithLogger(log))
	if err != nil {
		t.Fatal(err)
	}
	profiles, _ := gateway.NewProfileStore(db, gateway.DefaultProfile, log)
	router := gateway.NewRouter(db, profiles, log, standard.NewExecutor(db, keys, client, log))
	return &v5env{router: router, srv: srv, keys: keys}
}

func TestStandardV5EndToEnd(t *testing.T) {
	e := setupV5(t)
	ctx := context.Background()

	o, err := e.router.PlaceOrder(ctx, market(1, "e2e-1", swap.SideBuy, 3, 10))
	if err != nil || o.Status != swap.OrderSubmitted || o.Route != swap.RouteStandard {
		t.Fatalf("order = %+v, %v", o, err)
	}
	reqs := e.srv.Requests()
	if len(reqs) != 2 || reqs[0].Path != "/v5/position/lever" || reqs[1].Path != "/v5/trade/order" {
		t.Fatalf("expected lever then order, got %+v", reqs)
	}
	for _, r := range reqs {
		if r.UID != e.keys[1].UID {
			t.Fatalf("request signed by uid %d, want user 1's sub-account %d", r.UID, e.keys[1].UID)
		}
	}
	body := reqs[1].Body
	if body["margin_mode"] != "cross" || body["side"] != "buy" || body["type"] != "market" || body["volume"] != "3" ||
		body["client_order_id"] != strconv.FormatUint(o.ID, 10) {
		t.Fatalf("order body = %v", body)
	}
	if _, ok := body["price"]; ok {
		t.Fatal("market order must not send price")
	}
	if e.srv.Lever(e.keys[1].UID, "BTC-USDT", "cross") != 10 {
		t.Fatal("leverage not set before order")
	}

	// 同杠杆再次下单：不重复设置杠杆
	if _, err := e.router.PlaceOrder(ctx, market(1, "e2e-2", swap.SideBuy, 1, 10)); err != nil {
		t.Fatal(err)
	}
	if n := len(e.srv.Requests()); n != 3 {
		t.Fatalf("leverage re-sent: %d requests", n)
	}

	// 逐仓限价只减仓：独立槽位，独立杠杆
	r := limit(1, "e2e-3", swap.SideSell, 2, "65000.5", 5)
	r.MarginMode, r.ReduceOnly = swap.MarginIsolated, true
	if _, err := e.router.PlaceOrder(ctx, r); err != nil {
		t.Fatal(err)
	}
	last := e.srv.Requests()
	b := last[len(last)-1].Body
	if b["margin_mode"] != "isolated" || b["type"] != "limit" || b["price"] != "65000.5" || b["reduce_only"] != float64(1) {
		t.Fatalf("isolated limit body = %v", b)
	}
	if e.srv.Lever(e.keys[1].UID, "BTC-USDT", "isolated") != 5 {
		t.Fatal("isolated leverage not set")
	}

	// 另一用户使用其自己的子账户
	if _, err := e.router.PlaceOrder(ctx, market(2, "e2e-4", swap.SideSell, 1, 20)); err != nil {
		t.Fatal(err)
	}
	for _, o := range e.srv.Orders() {
		if o.ClientOrderID == "" {
			t.Fatal("client_order_id missing (needed for crash recovery)")
		}
	}
	uids := map[int64]int{}
	for _, o := range e.srv.Orders() {
		uids[o.UID]++
	}
	if uids[e.keys[1].UID] != 3 || uids[e.keys[2].UID] != 1 {
		t.Fatalf("orders per sub-account = %v", uids)
	}
}

func TestStandardV5RejectionsMarkOrderRejected(t *testing.T) {
	e := setupV5(t)
	ctx := context.Background()

	// 火币业务拒绝（字段不合法）
	e.srv.FailNext("/v5/trade/order", 0, 1067, "字段不合法")
	o, err := e.router.PlaceOrder(ctx, market(1, "rej-1", swap.SideBuy, 1, 10))
	if apiErr, ok := htx.IsAPIError(err); !ok || apiErr.Code != "1067" || o.Status != swap.OrderRejected {
		t.Fatalf("got %+v, %v", o, err)
	}

	// 设置杠杆失败：订单不得发送
	e.srv.FailNext("/v5/position/lever", 0, 6013, "仓位处于爆仓或者ADL中，请稍后再试")
	before := len(e.srv.Orders())
	o, err = e.router.PlaceOrder(ctx, market(1, "rej-2", swap.SideBuy, 1, 25))
	if err == nil || o.Status != swap.OrderRejected || len(e.srv.Orders()) != before {
		t.Fatalf("got %+v, %v (orders %d -> %d)", o, err, before, len(e.srv.Orders()))
	}
	// 失败后缓存被清除，下次重新设置杠杆
	if _, err := e.router.PlaceOrder(ctx, market(1, "ok-3", swap.SideBuy, 1, 25)); err != nil {
		t.Fatalf("retry after lever failure: %v", err)
	}
	if e.srv.Lever(e.keys[1].UID, "BTC-USDT", "cross") != 25 {
		t.Fatal("lever not retried")
	}

	// 系统错误：可重试类错误同样先标记 REJECTED（outbox 自动重试将在下一阶段接入）
	e.srv.FailNext("/v5/trade/order", 0, 6021, "接口超时，请稍后重试")
	_, err = e.router.PlaceOrder(ctx, market(1, "rej-4", swap.SideBuy, 1, 25))
	if !htx.IsRetryable(err) {
		t.Fatalf("6021 should be classified retryable: %v", err)
	}

	// 未绑定子账户的用户：不发出任何请求
	n := len(e.srv.Requests())
	if _, err := e.router.PlaceOrder(ctx, market(3, "x", swap.SideBuy, 1, 10)); !errors.Is(err, subaccount.ErrNoSubAccount) {
		t.Fatalf("want ErrNoSubAccount, got %v", err)
	}
	if len(e.srv.Requests()) != n {
		t.Fatal("request sent for unbound user")
	}
}
