package app_test

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"backend_v2/api/middleware"
	"backend_v2/internal/app"
	"backend_v2/internal/config"
	"backend_v2/internal/htx/htxmock"
	"backend_v2/internal/storage/storagetest"
)

var (
	userSecret  = strings.Repeat("u", 40)
	adminSecret = strings.Repeat("a", 40)
	master      = htxmock.Account{UID: 63628520, AccessKey: "master-ak", Secret: "master-sk", Master: true}
)

func randKeyB64() string {
	k := make([]byte, 32)
	_, _ = rand.Read(k)
	return base64.StdEncoding.EncodeToString(k)
}

// baseEnv 返回一套最小可用配置；vault 主密钥随机生成。
func baseEnv(dsn, htxURL string) map[string]string {
	return map[string]string{
		"MYSQL_DSN":               dsn,
		"JWT_USER_SECRET":         userSecret,
		"JWT_ADMIN_SECRET":        adminSecret,
		"HUOBI_ACCESS_KEY":        master.AccessKey,
		"HUOBI_SECRET_KEY":        master.Secret,
		"HTX_MASTER_UID":          strconv.FormatInt(master.UID, 10),
		"HTX_SWAP_BASE_URL":       htxURL,
		"HTX_SPOT_BASE_URL":       htxURL,
		"HTX_SWAP_CHANNEL_CODE":   "AAe19e2c9d",
		"HTX_SUB_APIKEY_IPS":      "203.0.113.10, 203.0.113.11",
		"HTX_SUBUSER_NAME_PREFIX": "bv",
		"VAULT_ACTIVE_KEY_ID":     "k1",
		"VAULT_KEY_k1":            randKeyB64(),
	}
}

func load(t *testing.T, env map[string]string) (*config.Config, error) {
	t.Helper()
	var environ []string
	for k, v := range env {
		environ = append(environ, k+"="+v)
	}
	return config.LoadFrom(func(k string) string { return env[k] }, environ)
}

type harness struct {
	user, admin *httptest.Server
	srv         *htxmock.Server
	app         *app.App
}

func start(t *testing.T, mutate func(map[string]string)) *harness {
	t.Helper()
	_, dsn := storagetest.OpenDBWithDSN(t)
	srv := htxmock.New(master)
	t.Cleanup(srv.Close)
	env := baseEnv(dsn, srv.URL)
	if mutate != nil {
		mutate(env)
	}
	cfg, err := load(t, env)
	if err != nil {
		t.Fatal(err)
	}
	a, err := app.New(context.Background(), cfg, storagetest.QuietLogger())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = a.Close() })
	h := &harness{user: httptest.NewServer(a.UserHandler), admin: httptest.NewServer(a.AdminHandler), srv: srv, app: a}
	t.Cleanup(func() { h.user.Close(); h.admin.Close() })
	return h
}

func tok(t *testing.T, secret, sub string, role middleware.Role) string {
	t.Helper()
	s, err := middleware.IssueToken([]byte(secret), sub, role, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func do(t *testing.T, method, url, token string, body any) (int, map[string]any) {
	t.Helper()
	var r io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		r = bytes.NewReader(b)
	}
	req, _ := http.NewRequest(method, url, r)
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
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

var order = map[string]any{"client_order_id": "o1", "contract_code": "BTC-USDT", "side": "BUY",
	"order_type": "MARKET", "volume": "2", "leverage": 10}

// 全链路：用户开户 → 默认 STANDARD 下单 → 火币收到带 channel_code、由该用户子账户签名的订单。
func TestEndToEndOpenAccountAndTrade(t *testing.T) {
	h := start(t, func(e map[string]string) {
		e["HTX_PROVISIONING_ENABLED"] = "true"
		e["HTX_TRADING_ENABLED"] = "true"
	})
	if names := h.app.Tasks(); len(names) != 1 || names[0] != "subaccount_resume" {
		t.Fatalf("background tasks = %v", names)
	}
	u := tok(t, userSecret, "42", middleware.RoleUser)

	if code, body := do(t, "GET", h.user.URL+"/health", "", nil); code != 200 || body["db"] != "ok" {
		t.Fatalf("health: %d %v", code, body)
	}
	if code, body := do(t, "POST", h.user.URL+"/api/v1/swap/orders", u, order); code != 403 || body["code"] != "SUBACCOUNT_NOT_READY" {
		t.Fatalf("trade before opening account must be refused: %d %v", code, body)
	}
	code, body := do(t, "POST", h.user.URL+"/api/v1/me/subaccount", u, nil)
	if code != 200 || body["state"] != "READY" || body["ready"] != true {
		t.Fatalf("open account: %d %v", code, body)
	}
	if _, leaked := body["sub_uid"]; leaked {
		t.Fatal("user response must not expose sub uid")
	}

	code, body = do(t, "POST", h.user.URL+"/api/v1/swap/orders", u, order)
	if code != 200 || body["status"] != "SUBMITTED" {
		t.Fatalf("place order: %d %v", code, body)
	}
	orders := h.srv.Orders()
	if len(orders) != 1 || orders[0].ChannelCode != "AAe19e2c9d" || orders[0].Lever != 10 {
		t.Fatalf("htx orders = %+v", orders)
	}
	subs := h.srv.SubUsers()
	if len(subs) != 1 || orders[0].UID != subs[0] {
		t.Fatal("order must be signed by the user's own sub-account")
	}

	// Admin 冻结 → 用户无法交易
	a := tok(t, adminSecret, "ops@corp", middleware.RoleAdmin)
	if code, body := do(t, "GET", h.admin.URL+"/admin/v1/users/42/subaccount", a, nil); code != 200 || body["state"] != "READY" {
		t.Fatalf("admin status: %d %v", code, body)
	}
	if code, body := do(t, "POST", h.admin.URL+"/admin/v1/users/42/subaccount/lock", a, map[string]string{"reason": "kyc review"}); code != 200 || body["state"] != "LOCKED" {
		t.Fatalf("lock: %d %v", code, body)
	}
	o2 := map[string]any{"client_order_id": "o2", "contract_code": "BTC-USDT", "side": "BUY", "order_type": "MARKET", "volume": "1", "leverage": 10}
	if code, body := do(t, "POST", h.user.URL+"/api/v1/swap/orders", u, o2); code != 403 || body["code"] != "SUBACCOUNT_LOCKED" {
		t.Fatalf("locked user order: %d %v", code, body)
	}
	if code, _ := do(t, "POST", h.admin.URL+"/admin/v1/users/42/subaccount/unlock", a, nil); code != 200 {
		t.Fatalf("unlock: %d", code)
	}
	if code, body := do(t, "POST", h.user.URL+"/api/v1/swap/orders", u, o2); code != 200 || body["status"] != "SUBMITTED" {
		t.Fatalf("after unlock: %d %v", code, body)
	}
}

// 默认安全开关：不开启 HTX_TRADING_ENABLED 时，任何订单都不会到达火币。
func TestTradingDisabledByDefault(t *testing.T) {
	h := start(t, func(e map[string]string) { e["HTX_PROVISIONING_ENABLED"] = "true" })
	u := tok(t, userSecret, "43", middleware.RoleUser)
	if code, _ := do(t, "POST", h.user.URL+"/api/v1/me/subaccount", u, nil); code != 200 {
		t.Fatalf("open account: %d", code)
	}
	code, body := do(t, "POST", h.user.URL+"/api/v1/swap/orders", u, order)
	if code != 200 || body["status"] != "REJECTED" {
		t.Fatalf("order with trading disabled: %d %v", code, body)
	}
	if n := h.srv.CountRequests("/v5/trade/order") + h.srv.CountRequests("/v5/position/lever"); n != 0 {
		t.Fatalf("trading disabled but %d trade requests reached HTX", n)
	}
}

// 默认安全开关：不开启 HTX_PROVISIONING_ENABLED 时，不会在火币创建任何子账户。
func TestProvisioningDisabledByDefault(t *testing.T) {
	h := start(t, nil)
	if len(h.app.Tasks()) != 0 {
		t.Fatalf("no background tasks expected, got %v", h.app.Tasks())
	}
	u := tok(t, userSecret, "44", middleware.RoleUser)
	if code, body := do(t, "POST", h.user.URL+"/api/v1/me/subaccount", u, nil); code != 503 || body["code"] != "PROVISIONING_DISABLED" {
		t.Fatalf("open: %d %v", code, body)
	}
	if len(h.srv.Requests()) != 0 {
		t.Fatal("no HTX requests expected")
	}
}

func TestBackgroundRunStopsOnCancel(t *testing.T) {
	h := start(t, func(e map[string]string) { e["HTX_PROVISIONING_ENABLED"] = "true" })
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { h.app.Run(ctx); close(done) }()
	cancel()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("background tasks did not stop")
	}
}

func TestConfigValidation(t *testing.T) {
	base := baseEnv("root:pw@tcp(127.0.0.1:3306)/db", "http://127.0.0.1:1")
	cases := map[string]func(map[string]string){
		"missing dsn":         func(e map[string]string) { delete(e, "MYSQL_DSN") },
		"short jwt":           func(e map[string]string) { e["JWT_USER_SECRET"] = "short" },
		"same jwt secrets":    func(e map[string]string) { e["JWT_ADMIN_SECRET"] = e["JWT_USER_SECRET"] },
		"provisioning no ips": func(e map[string]string) { e["HTX_PROVISIONING_ENABLED"] = "true"; delete(e, "HTX_SUB_APIKEY_IPS") },
		"provisioning no master": func(e map[string]string) {
			e["HTX_PROVISIONING_ENABLED"] = "true"
			delete(e, "HUOBI_ACCESS_KEY")
			delete(e, "HUOBI_SECRET_KEY")
		},
		"bad channel code":  func(e map[string]string) { e["HTX_SWAP_CHANNEL_CODE"] = "bad code" },
		"bad bool":          func(e map[string]string) { e["HTX_TRADING_ENABLED"] = "yes please" },
		"no vault":          func(e map[string]string) { delete(e, "VAULT_ACTIVE_KEY_ID") },
		"half master creds": func(e map[string]string) { delete(e, "HUOBI_SECRET_KEY") },
	}
	for name, mut := range cases {
		env := map[string]string{}
		for k, v := range base {
			env[k] = v
		}
		mut(env)
		if _, err := load(t, env); !errors.Is(err, config.ErrConfig) {
			t.Errorf("%s: want ErrConfig, got %v", name, err)
		}
	}

	cfg, err := load(t, base)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(cfg.MySQLDSN, "parseTime=true") {
		t.Fatal("parseTime must be forced on")
	}
	if cfg.TradingEnabled || cfg.ProvisioningEnabled || cfg.TransfersEnabled {
		t.Fatal("all HTX write switches must default to false")
	}
	if cfg.AdminAddr != "127.0.0.1:8081" {
		t.Fatalf("admin must bind localhost by default, got %s", cfg.AdminAddr)
	}
	s := cfg.String()
	for _, secret := range []string{userSecret, adminSecret, master.Secret, master.AccessKey, base["VAULT_KEY_k1"], "pw@tcp"} {
		if strings.Contains(s, secret) {
			t.Fatalf("config String leaks secret material: %s", s)
		}
	}
	_ = slog.Default()
}
