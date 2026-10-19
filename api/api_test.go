package api_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/shopspring/decimal"

	"backend_v2/api/admin"
	"backend_v2/api/httpx"
	"backend_v2/api/middleware"
	"backend_v2/api/user"
	"backend_v2/internal/dealer"
	"backend_v2/internal/gateway"
	"backend_v2/internal/gateway/standard"
	"backend_v2/internal/gateway/synthetic"
	"backend_v2/internal/htx"
	"backend_v2/internal/storage/storagetest"
	"backend_v2/internal/subaccount"
)

var (
	userSecret  = []byte("user-secret-for-tests")
	adminSecret = []byte("admin-secret-for-tests")
)

type market struct{}

func (market) Quote(string) (synthetic.Quote, bool) {
	l := []synthetic.Level{{Price: decimal.NewFromInt(60000), Size: decimal.NewFromInt(1000)}}
	return synthetic.Quote{Bids: l, Asks: l, At: time.Now()}, true
}
func (market) Mark(string) (decimal.Decimal, time.Time, bool) {
	return decimal.NewFromInt(60000), time.Now(), true
}

type specs struct{}

func (specs) Spec(c string) (synthetic.ContractSpec, bool) {
	return synthetic.ContractSpec{Size: decimal.RequireFromString("0.001"), MarginCurrency: "USDT",
		Tiers: []synthetic.RiskTier{{MMR: decimal.RequireFromString("0.005"), MaxLeverage: 100}}}, c == "BTC-USDT"
}

type noKeys struct{}

func (noKeys) Get(context.Context, int64) (*htx.Credentials, error) {
	return nil, subaccount.ErrNoSubAccount
}
func (noKeys) Invalidate(int64) {}

type servers struct {
	user, admin *httptest.Server
}

func setup(t *testing.T) servers {
	t.Helper()
	db := storagetest.OpenDB(t)
	log := storagetest.QuietLogger()
	dl := dealer.NewMySQLDealer(db, log)
	ctx := context.Background()
	for _, ins := range []dealer.Instruction{
		dealer.NewDeposit(7, "USDT", decimal.NewFromInt(10000), "d7"),
		dealer.NewTransfer(7, dealer.AccountSpot, dealer.AccountSwap, "USDT", decimal.NewFromInt(10000), "t7"),
	} {
		if _, err := dl.Execute(ctx, ins); err != nil {
			t.Fatal(err)
		}
	}
	profiles, _ := gateway.NewProfileStore(db, gateway.DefaultProfile, log)
	engine := synthetic.NewEngine(db, dl, market{}, market{}, specs{}, synthetic.Config{
		TakerFeeRate: decimal.Zero, MakerFeeRate: decimal.Zero, LiquidationFeeRate: decimal.Zero, SpreadBps: decimal.Zero,
		MarketBand: decimal.RequireFromString("0.05"), MaxQuoteAge: time.Minute, MaxMarkAge: time.Minute,
	}, log)
	router := gateway.NewRouter(db, profiles, log, engine, standard.NewExecutor(db, noKeys{}, htxStub{}, log))
	s := servers{
		user: httptest.NewServer(user.NewServer(user.Deps{Secret: userSecret, Router: router, Profiles: profiles,
			Wallet: engine, SubAccounts: subaccount.NewService(db, nil), Health: httpx.Health(db), Logger: log})),
		admin: httptest.NewServer(admin.NewServer(admin.Deps{Secret: adminSecret, Profiles: profiles,
			SubAccounts: subaccount.NewService(db, nil), Health: httpx.Health(db), Logger: log})),
	}
	t.Cleanup(func() { s.user.Close(); s.admin.Close() })
	return s
}

type htxStub struct{}

func (htxStub) SetLeverage(context.Context, *htx.Credentials, string, string, int) error { return nil }
func (htxStub) SwapPlaceOrder(context.Context, *htx.Credentials, htx.SwapOrderRequest) (*htx.SwapOrderResponse, error) {
	return nil, fmt.Errorf("htx stub: no sub-account in this test")
}

func token(t *testing.T, secret []byte, sub string, role middleware.Role, ttl time.Duration) string {
	t.Helper()
	tok, err := middleware.IssueToken(secret, sub, role, ttl)
	if err != nil {
		t.Fatal(err)
	}
	return tok
}

func call(t *testing.T, method, url, tok string, body any) (int, map[string]any) {
	t.Helper()
	var rdr *bytes.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rdr = bytes.NewReader(b)
	} else {
		rdr = bytes.NewReader(nil)
	}
	req, _ := http.NewRequest(method, url, rdr)
	if tok != "" {
		req.Header.Set("Authorization", "Bearer "+tok)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	out := map[string]any{}
	_ = json.NewDecoder(resp.Body).Decode(&out)
	return resp.StatusCode, out
}

func TestAuthIsolationBetweenUserAndAdmin(t *testing.T) {
	s := setup(t)
	userTok := token(t, userSecret, "7", middleware.RoleUser, time.Hour)
	adminTok := token(t, adminSecret, "ops@corp", middleware.RoleAdmin, time.Hour)
	forged := token(t, userSecret, "7", middleware.RoleAdmin, time.Hour) // 用 User 密钥伪造 admin 角色
	expired := token(t, userSecret, "7", middleware.RoleUser, -time.Minute)

	cases := []struct {
		name, method, url, tok string
		want                   int
	}{
		{"no token", "GET", s.user.URL + "/api/v1/me/trade-profile", "", 401},
		{"expired", "GET", s.user.URL + "/api/v1/me/trade-profile", expired, 401},
		{"admin token on user api", "GET", s.user.URL + "/api/v1/me/trade-profile", adminTok, 401},
		{"user token on admin api", "GET", s.admin.URL + "/admin/v1/users/7/trade-profile", userTok, 401},
		{"forged admin role", "GET", s.admin.URL + "/admin/v1/users/7/trade-profile", forged, 401},
		{"admin path not on user server", "GET", s.user.URL + "/admin/v1/users/7/trade-profile", userTok, 404},
		{"ok user", "GET", s.user.URL + "/api/v1/me/trade-profile", userTok, 200},
		{"ok admin", "GET", s.admin.URL + "/admin/v1/users/7/trade-profile", adminTok, 200},
	}
	for _, tc := range cases {
		if got, body := call(t, tc.method, tc.url, tc.tok, nil); got != tc.want {
			t.Errorf("%s: status %d, want %d (%v)", tc.name, got, tc.want, body)
		}
	}
}

func TestAdminSwitchesRouteAndUserTrades(t *testing.T) {
	s := setup(t)
	userTok := token(t, userSecret, "7", middleware.RoleUser, time.Hour)
	adminTok := token(t, adminSecret, "ops@corp", middleware.RoleAdmin, time.Hour)
	order := map[string]any{"client_order_id": "c1", "contract_code": "BTC-USDT", "side": "BUY",
		"order_type": "MARKET", "volume": "10", "leverage": 10}

	// 默认 STANDARD 且未开通子账户 → 引导开通
	code, body := call(t, "POST", s.user.URL+"/api/v1/swap/orders", userTok, order)
	if code != 403 || body["code"] != "SUBACCOUNT_NOT_READY" {
		t.Fatalf("got %d %v", code, body)
	}

	code, body = call(t, "PUT", s.admin.URL+"/admin/v1/users/7/trade-profile/route", adminTok, map[string]string{"route": "SYNTHETIC"})
	if code != 200 || body["route"] != "SYNTHETIC" {
		t.Fatalf("set route: %d %v", code, body)
	}
	if code, _ := call(t, "PUT", s.admin.URL+"/admin/v1/users/7/trade-profile/route", adminTok, map[string]string{"route": "BOGUS"}); code != 400 {
		t.Fatalf("invalid route accepted: %d", code)
	}

	code, body = call(t, "PUT", s.user.URL+"/api/v1/me/trade-profile/margin-mode", userTok, map[string]string{"margin_mode": "ISOLATED"})
	if code != 200 || body["margin_mode"] != "ISOLATED" {
		t.Fatalf("set margin mode: %d %v", code, body)
	}

	code, body = call(t, "POST", s.user.URL+"/api/v1/swap/orders", userTok, order)
	if code != 200 || body["status"] != "FILLED" || body["margin_mode"] != "ISOLATED" {
		t.Fatalf("place: %d %v", code, body)
	}
	if _, leaked := body["route"]; leaked {
		t.Fatal("internal route must not be exposed to users")
	}
	raw, _ := json.Marshal(body)
	if strings.Contains(string(raw), "sub-ak") || strings.Contains(strings.ToLower(string(raw)), "secret") {
		t.Fatal("credential leaked in user response")
	}

	code, body = call(t, "GET", s.user.URL+"/api/v1/swap/wallet/USDT/transferable", userTok, nil)
	// 逐仓保证金 60 已划入 SWAP_ISOLATED：10000 - 60
	if code != 200 || body["amount"] != "9940" {
		t.Fatalf("transferable: %d %v", code, body)
	}
	code, body = call(t, "POST", s.user.URL+"/api/v1/swap/wallet/transfer-out", userTok,
		map[string]string{"request_id": "r1", "currency": "USDT", "amount": "9940.01"})
	if code != 400 || body["code"] != "INSUFFICIENT_MARGIN" {
		t.Fatalf("over-withdraw: %d %v", code, body)
	}
}

func TestUnknownFieldsAndBadInputRejected(t *testing.T) {
	s := setup(t)
	userTok := token(t, userSecret, "7", middleware.RoleUser, time.Hour)
	// 请求体里伪造 user_id 字段 → 拒绝（UserID 只能来自 JWT）
	code, _ := call(t, "POST", s.user.URL+"/api/v1/swap/orders", userTok, map[string]any{"user_id": 8, "client_order_id": "x"})
	if code != 400 {
		t.Fatalf("unknown field accepted: %d", code)
	}
	code, body := call(t, "DELETE", s.user.URL+"/api/v1/swap/orders/nope", userTok, nil)
	if code != 404 {
		t.Fatalf("cancel unknown: %d %v", code, body)
	}
}

func TestRecoverMiddleware(t *testing.T) {
	h := middleware.Recover(slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil)), http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		panic(fmt.Sprintf("boom %d", 1))
	}))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/", nil))
	if rec.Code != 500 || !strings.Contains(rec.Body.String(), "INTERNAL") {
		t.Fatalf("recover: %d %s", rec.Code, rec.Body.String())
	}
}

func TestHealthIsPublicAndRateLimitHookWorks(t *testing.T) {
	s := setup(t)
	for _, u := range []string{s.user.URL, s.admin.URL} {
		code, body := call(t, "GET", u+"/health", "", nil)
		if code != 200 || body["status"] != "ok" || body["db"] != "ok" {
			t.Fatalf("%s/health = %d %v", u, code, body)
		}
	}
	// 限流钩子：拒绝时返回 429，且位于鉴权之后
	h := middleware.Server(slog.Default(), nil, userSecret, middleware.RoleUser, denyAll{},
		http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(200) }))
	srv := httptest.NewServer(h)
	defer srv.Close()
	if code, _ := call(t, "GET", srv.URL+"/x", "", nil); code != 401 {
		t.Fatalf("unauthenticated must hit auth first: %d", code)
	}
	if code, body := call(t, "GET", srv.URL+"/x", token(t, userSecret, "7", middleware.RoleUser, time.Hour), nil); code != 429 || body["code"] != "RATE_LIMITED" {
		t.Fatalf("limiter: %d %v", code, body)
	}
}

type denyAll struct{}

func (denyAll) Allow(*http.Request) bool { return false }

func TestSubAccountEndpointsWhenProvisioningDisabled(t *testing.T) {
	s := setup(t)
	userTok := token(t, userSecret, "7", middleware.RoleUser, time.Hour)
	adminTok := token(t, adminSecret, "ops@corp", middleware.RoleAdmin, time.Hour)
	if code, body := call(t, "GET", s.user.URL+"/api/v1/me/subaccount", userTok, nil); code != 200 || body["state"] != "NONE" {
		t.Fatalf("status: %d %v", code, body)
	}
	if code, body := call(t, "POST", s.user.URL+"/api/v1/me/subaccount", userTok, nil); code != 503 || body["code"] != "PROVISIONING_DISABLED" {
		t.Fatalf("open: %d %v", code, body)
	}
	if code, body := call(t, "GET", s.admin.URL+"/admin/v1/users/7/subaccount", adminTok, nil); code != 404 || body["code"] != "NOT_FOUND" {
		t.Fatalf("admin status: %d %v", code, body)
	}
	if code, _ := call(t, "POST", s.admin.URL+"/admin/v1/users/7/subaccount/lock", userTok, map[string]string{"reason": "x"}); code != 401 {
		t.Fatalf("user token on admin lock: %d", code)
	}
}
