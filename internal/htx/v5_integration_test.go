package htx_test

import (
	"context"
	"errors"
	"sort"
	"strconv"
	"sync"
	"testing"
	"time"

	"backend_v2/internal/htx"
	"backend_v2/internal/htx/htxmock"
)

var (
	subA = htxmock.Account{UID: 1001, AccessKey: "sub-a-ak", Secret: "sub-a-sk"}
	subB = htxmock.Account{UID: 1002, AccessKey: "sub-b-ak", Secret: "sub-b-sk"}
)

func TestV5OrderLifecycleAgainstMock(t *testing.T) {
	srv := htxmock.New(subA, subB)
	defer srv.Close()
	c, _ := htx.NewClient(srv.URL)
	ctx := context.Background()
	cred := subA.Credentials()

	if m, err := c.AssetMode(ctx, cred); err != nil || m != htx.AssetModeMulti {
		t.Fatalf("asset mode = %d, %v", m, err)
	}
	if err := c.SetPositionMode(ctx, cred, htx.PositionModeSingle); err != nil {
		t.Fatal(err)
	}
	if err := c.SetLeverage(ctx, cred, "BTC-USDT", htx.MarginIsolated, 20); err != nil {
		t.Fatal(err)
	}
	if srv.Lever(subA.UID, "BTC-USDT", htx.MarginIsolated) != 20 {
		t.Fatal("lever not applied")
	}

	resp, err := c.SwapPlaceOrder(ctx, cred, htx.SwapOrderRequest{
		ContractCode: "BTC-USDT", MarginMode: htx.MarginIsolated, Side: htx.SideSell, Type: htx.OrderTypeLimit,
		Price: "65000.5", Volume: "3", ClientOrderID: "77", ReduceOnly: 1,
	})
	if err != nil || resp.ClientOrderID != "77" || resp.OrderID == "" {
		t.Fatalf("place = %+v, %v", resp, err)
	}
	orders := srv.Orders()
	if len(orders) != 1 || orders[0].UID != subA.UID || orders[0].MarginMode != "isolated" || !orders[0].ReduceOnly || orders[0].Lever != 20 {
		t.Fatalf("mock orders = %+v", orders)
	}

	// 按 client_order_id 查单（GET：业务参数参与签名）
	o, err := c.SwapOrderInfo(ctx, cred, "BTC-USDT", htx.MarginIsolated, "", "77")
	if err != nil || o.OrderID != resp.OrderID || o.State != htx.OrderStateNew {
		t.Fatalf("order info = %+v, %v", o, err)
	}
	// 其他子账户看不到该订单
	if _, err := c.SwapOrderInfo(ctx, subB.Credentials(), "BTC-USDT", "", "", "77"); err == nil {
		t.Fatal("cross-account order visibility")
	}
	// 重复 client_order_id 被拒
	if _, err := c.SwapPlaceOrder(ctx, cred, htx.SwapOrderRequest{
		ContractCode: "BTC-USDT", MarginMode: htx.MarginCross, Side: htx.SideBuy, Type: htx.OrderTypeMarket, Volume: "1", ClientOrderID: "77",
	}); err == nil {
		t.Fatal("duplicate client_order_id accepted")
	}

	if _, err := c.SwapCancelOrder(ctx, cred, htx.SwapCancelRequest{ContractCode: "BTC-USDT", ClientOrderID: "77"}); err != nil {
		t.Fatal(err)
	}
	if o, _ := c.SwapOrderInfo(ctx, cred, "BTC-USDT", htx.MarginIsolated, resp.OrderID, ""); o.State != htx.OrderStateCanceled || !o.Final() {
		t.Fatalf("after cancel = %+v", o)
	}
	_, err = c.SwapCancelOrder(ctx, cred, htx.SwapCancelRequest{ContractCode: "BTC-USDT", OrderID: resp.OrderID})
	if e, ok := htx.IsAPIError(err); !ok || e.Code != "1061" {
		t.Fatalf("second cancel = %v", err)
	}

	// 伪造签名（错误密钥）被拒
	bad, _ := htx.NewCredentials(subA.AccessKey, "wrong-secret")
	if _, err := c.AccountBalance(ctx, bad); err == nil {
		t.Fatal("bad signature accepted")
	}
}

func TestV5PostBodyNotSignedGetParamsSigned(t *testing.T) {
	srv := htxmock.New(subA)
	defer srv.Close()
	c, _ := htx.NewClient(srv.URL)
	ctx := context.Background()
	if _, err := c.SwapOpenPositions(ctx, subA.Credentials(), "ETH-USDT"); err != nil {
		t.Fatal(err)
	}
	if _, err := c.SwapPlaceOrder(ctx, subA.Credentials(), htx.SwapOrderRequest{
		ContractCode: "ETH-USDT", MarginMode: htx.MarginCross, Side: htx.SideBuy, Type: htx.OrderTypeMarket, Volume: "2",
	}); err != nil {
		t.Fatal(err)
	}
	reqs := srv.Requests()
	if reqs[0].Query.Get("contract_code") != "ETH-USDT" {
		t.Fatal("GET business params must be in signed query")
	}
	if reqs[1].Query.Has("contract_code") || reqs[1].Body["contract_code"] != "ETH-USDT" {
		t.Fatal("POST business params must be in body only")
	}
	for _, k := range []string{"lever_rate", "direction", "offset", "order_price_type"} {
		if _, ok := reqs[1].Body[k]; ok {
			t.Fatalf("legacy field %q sent to V5 order API", k)
		}
	}
}

// 按 UID 限频：同一子账户的交易请求在任一窗口内不超过额度；不同子账户互不影响。
func TestV5RateLimitPerUIDAgainstMock(t *testing.T) {
	srv := htxmock.New(subA, subB)
	defer srv.Close()
	const window, guard, limit = 300 * time.Millisecond, 60 * time.Millisecond, 5
	c, _ := htx.NewClient(srv.URL, htx.WithRateLimiter(htx.NewRateLimiter(window, limit, limit).WithGuard(guard)))
	ctx := context.Background()

	var wg sync.WaitGroup
	errs := make(chan error, 40)
	for _, acc := range []htxmock.Account{subA, subB} {
		for i := 0; i < 12; i++ {
			wg.Add(1)
			go func(acc htxmock.Account) {
				defer wg.Done()
				_, err := c.SwapPlaceOrder(ctx, acc.Credentials(), htx.SwapOrderRequest{
					ContractCode: "BTC-USDT", MarginMode: htx.MarginCross, Side: htx.SideBuy, Type: htx.OrderTypeMarket, Volume: "1",
				})
				if err != nil {
					errs <- err
				}
			}(acc)
		}
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatal(err)
	}
	byUID := map[int64][]time.Time{}
	for _, r := range srv.Requests() {
		byUID[r.UID] = append(byUID[r.UID], r.At)
	}
	for uid, ts := range byUID {
		if len(ts) != 12 {
			t.Fatalf("uid %d requests = %d", uid, len(ts))
		}
		sort.Slice(ts, func(i, j int) bool { return ts[i].Before(ts[j]) })
		for i := limit; i < len(ts); i++ {
			// 以服务端到达时间计：第 i 个与第 i-limit 个请求的间隔必须严格 >= 窗口（安全余量吸收传输抖动）
			if gap := ts[i].Sub(ts[i-limit]); gap < window {
				t.Fatalf("uid %d exceeded %d req / %s: gap %s at #%d", uid, limit, window, gap, i)
			}
		}
	}
	// 两个 UID 并行，总耗时约 2 个窗口而不是 4 个
	all := append(byUID[subA.UID], byUID[subB.UID]...)
	sort.Slice(all, func(i, j int) bool { return all[i].Before(all[j]) })
	if span := all[len(all)-1].Sub(all[0]); span > 3*(window+guard) {
		t.Fatalf("UIDs throttled together: span %s", span)
	}
}

func TestV5RecoveryTimeHeaderPausesUID(t *testing.T) {
	srv := htxmock.New(subA)
	defer srv.Close()
	c, _ := htx.NewClient(srv.URL)
	ctx := context.Background()
	srv.SetHeader("recovery-time", formatMillis(time.Now().Add(300*time.Millisecond)))
	if _, err := c.SwapOpenPositions(ctx, subA.Credentials(), ""); err != nil {
		t.Fatal(err)
	}
	srv.SetHeader("recovery-time", "")
	start := time.Now()
	if _, err := c.SwapOpenPositions(ctx, subA.Credentials(), ""); err != nil {
		t.Fatal(err)
	}
	if waited := time.Since(start); waited < 200*time.Millisecond {
		t.Fatalf("did not back off for recovery-time (waited %s)", waited)
	}
	// ctx 先于恢复时间到期时立即返回
	srv.SetHeader("recovery-time", formatMillis(time.Now().Add(time.Hour)))
	_, _ = c.SwapOpenPositions(ctx, subA.Credentials(), "")
	short, cancel := context.WithTimeout(ctx, 50*time.Millisecond)
	defer cancel()
	if _, err := c.SwapOpenPositions(short, subA.Credentials(), ""); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("want deadline exceeded, got %v", err)
	}
}

func formatMillis(t time.Time) string { return strconv.FormatInt(t.UnixMilli(), 10) }
