package subaccount_test

import (
	"bytes"
	"context"
	"crypto/rand"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"testing"

	"github.com/shopspring/decimal"

	"backend_v2/internal/dealer"
	"backend_v2/internal/htx"
	"backend_v2/internal/htx/htxmock"
	"backend_v2/internal/storage/storagetest"
	"backend_v2/internal/subaccount"
	"backend_v2/internal/vault"
)

var master = htxmock.Account{UID: 63628520, AccessKey: "master-ak", Secret: "master-sk", Master: true}

type env struct {
	t      *testing.T
	db     *sql.DB
	srv    *htxmock.Server
	prov   *subaccount.Provisioner
	keys   *subaccount.DBKeyProvider
	xfer   *subaccount.Transfers
	dealer *dealer.MySQLDealer
	logs   *syncBuffer
}

type syncBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}
func (s *syncBuffer) String() string { s.mu.Lock(); defer s.mu.Unlock(); return s.b.String() }

func setup(t *testing.T) *env {
	t.Helper()
	db := storagetest.OpenDB(t)
	srv := htxmock.New(master)
	t.Cleanup(srv.Close)
	logs := &syncBuffer{}
	log := slog.New(slog.NewJSONHandler(logs, &slog.HandlerOptions{Level: slog.LevelDebug}))

	spot, _ := htx.NewClient(srv.URL, htx.WithLogger(log))
	swapC, _ := htx.NewClient(srv.URL, htx.WithLogger(log))
	k := make([]byte, vault.KeySize)
	_, _ = rand.Read(k)
	v, err := vault.New(map[string][]byte{"t1": k}, "t1")
	if err != nil {
		t.Fatal(err)
	}
	keys := subaccount.NewDBKeyProvider(db, v, 0)
	prov, err := subaccount.NewProvisioner(db, spot, swapC, v, keys, subaccount.Config{
		Master: master.Credentials(), NamePrefix: "bv", KeyIPs: []string{"203.0.113.10", "203.0.113.11"},
	}, log)
	if err != nil {
		t.Fatal(err)
	}
	dl := dealer.NewMySQLDealer(db, log)
	xfer := subaccount.NewTransfers(db, dl, spot, keys, master.Credentials(), true, log)
	return &env{t: t, db: db, srv: srv, prov: prov, keys: keys, xfer: xfer, dealer: dl, logs: logs}
}

func (e *env) provision(uid int64) *subaccount.Account {
	e.t.Helper()
	a, err := e.prov.Provision(context.Background(), uid)
	if err != nil {
		e.t.Fatalf("provision %d: %v", uid, err)
	}
	if a.State != subaccount.StateReady {
		e.t.Fatalf("state = %s", a.State)
	}
	return a
}

func (e *env) count(q string, args ...any) int {
	e.t.Helper()
	var n int
	if err := e.db.QueryRow(q, args...).Scan(&n); err != nil {
		e.t.Fatal(err)
	}
	return n
}

// assertNoSecretLeak 确认 mock 生成的所有 Secret 都没有出现在日志和数据库明文中。
func (e *env) assertNoSecretLeak() {
	e.t.Helper()
	if strings.Contains(e.logs.String(), "mock-sk-") {
		e.t.Fatal("api secret leaked into logs")
	}
	if n := e.count(`SELECT COUNT(*) FROM subaccount_api_keys WHERE INSTR(secret_ciphertext, 'mock-sk-') > 0`); n != 0 {
		e.t.Fatal("plaintext secret stored in database")
	}
}

func dec(s string) decimal.Decimal { return decimal.RequireFromString(s) }

// ---------------------------------------------------------------------------
// 开户状态机
// ---------------------------------------------------------------------------

func TestProvisionHappyPath(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	a := e.provision(7)

	sub, ok := e.srv.SubUser(a.SubUID)
	if !ok || !sub.ContractAuth || sub.DeductMode != "sub" || sub.AssetMode != htx.AssetModeMulti ||
		e.srv.PositionMode(a.SubUID) != htx.PositionModeSingle || sub.Type != htx.SubAccountGeneral {
		t.Fatalf("htx sub user = %+v mode=%s", sub, e.srv.PositionMode(a.SubUID))
	}
	if len(sub.Keys) != 1 {
		t.Fatalf("htx keys = %d", len(sub.Keys))
	}
	for _, k := range sub.Keys {
		if k.IPs != "203.0.113.10,203.0.113.11" || k.Permission != "readOnly,trade" {
			t.Fatalf("key must be IP-bound with trade permission: %+v", k)
		}
	}

	cred, err := e.keys.Get(ctx, 7)
	if err != nil || cred.UID != a.SubUID {
		t.Fatalf("key provider = %v, %v", cred, err)
	}
	// 解密出的凭证可直接用于火币请求（签名正确）
	c, _ := htx.NewClient(e.srv.URL)
	if uid, err := c.UserUID(ctx, cred); err != nil || uid != a.SubUID {
		t.Fatalf("decrypted credential unusable: %d %v", uid, err)
	}

	// 幂等：再次调用不产生任何火币请求
	before := len(e.srv.Requests())
	if again := e.provision(7); again.SubUID != a.SubUID {
		t.Fatal("sub uid changed")
	}
	if len(e.srv.Requests()) != before {
		t.Fatal("idempotent provision issued HTX requests")
	}
	e.assertNoSecretLeak()
}

func TestProvisionRecoversLostCreateResponse(t *testing.T) {
	e := setup(t)
	e.srv.DropResponseAfterApply("/v2/sub-user/creation", 1)
	if _, err := e.prov.Provision(context.Background(), 8); !htx.IsRetryable(err) {
		t.Fatalf("lost response should be retryable, got %v", err)
	}
	a, _ := subaccount.GetAccount(context.Background(), e.db, 8)
	if a.State != subaccount.StateProvisioning || a.Step != subaccount.StepReserved {
		t.Fatalf("after lost response = %+v", a)
	}
	ready := e.provision(8)
	if subs := e.srv.SubUsers(); len(subs) != 1 || subs[0] != ready.SubUID {
		t.Fatalf("duplicate sub users created: %v", subs)
	}
}

func TestProvisionRenamesWhenNameTakenByOthers(t *testing.T) {
	e := setup(t)
	e.srv.TakeName("bv0009") // base36(9) = "9"，补齐到 6 位
	a := e.provision(9)
	if a.UserName != "bv9x1" && !strings.HasSuffix(a.UserName, "x1") {
		t.Fatalf("user name = %s", a.UserName)
	}
	if a.NameAttempt != 1 {
		t.Fatalf("name attempt = %d", a.NameAttempt)
	}
}

func TestProvisionSweepsOrphanKeyAfterLostResponse(t *testing.T) {
	e := setup(t)
	e.srv.DropResponseAfterApply("/v2/sub-user/api-key-generation", 1)
	if _, err := e.prov.Provision(context.Background(), 10); err == nil {
		t.Fatal("expected failure on lost key response")
	}
	a, _ := subaccount.GetAccount(context.Background(), e.db, 10)
	sub, _ := e.srv.SubUser(a.SubUID)
	if len(sub.Keys) != 1 || e.count(`SELECT COUNT(*) FROM subaccount_api_keys WHERE user_id = 10`) != 0 {
		t.Fatalf("expected one orphan key at HTX and none locally")
	}
	var orphan string
	for ak := range sub.Keys {
		orphan = ak
	}
	e.provision(10)
	sub, _ = e.srv.SubUser(a.SubUID)
	if _, still := sub.Keys[orphan]; still || len(sub.Keys) != 1 {
		t.Fatalf("orphan key not swept: %d keys", len(sub.Keys))
	}
	var local string
	if err := e.db.QueryRow(`SELECT access_key FROM subaccount_api_keys WHERE user_id = 10 AND status = 'ACTIVE'`).Scan(&local); err != nil {
		t.Fatal(err)
	}
	if _, ok := sub.Keys[local]; !ok {
		t.Fatal("local active key does not match HTX")
	}
	e.assertNoSecretLeak()
}

func TestProvisionOTPRequiredFailsCleanly(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	e.srv.RequireOTP(true)
	a, err := e.prov.Provision(ctx, 11)
	if !errors.Is(err, htx.ErrOTPRequired) || a.State != subaccount.StateFailed {
		t.Fatalf("got %+v, %v", a, err)
	}
	sub, _ := e.srv.SubUser(a.SubUID)
	if len(sub.Keys) != 0 || e.count(`SELECT COUNT(*) FROM subaccount_api_keys WHERE user_id = 11`) != 0 {
		t.Fatal("dirty key left after OTP failure")
	}
	if _, err := e.keys.Get(ctx, 11); !errors.Is(err, subaccount.ErrSubAccountNotReady) {
		t.Fatalf("key provider must refuse: %v", err)
	}
	// 后台续跑不会自动重试 FAILED；人工重试（再次 Provision）在问题解除后成功
	if n, _ := e.prov.ResumePending(ctx, 10); n != 0 {
		t.Fatal("FAILED must not be auto-resumed")
	}
	e.srv.RequireOTP(false)
	if ready := e.provision(11); ready.SubUID != a.SubUID {
		t.Fatal("retry must reuse existing sub user")
	}
}

func TestProvisionTerminalFailureAfterKeyRevokesKey(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	e.srv.FailNext("/v5/account/asset_mode", 0, 6014, "当前不允许切换至联合保证金模式")
	a, err := e.prov.Provision(ctx, 12)
	if err == nil || a.State != subaccount.StateFailed || a.Step != subaccount.StepConfigured {
		t.Fatalf("got %+v, %v", a, err)
	}
	sub, _ := e.srv.SubUser(a.SubUID)
	if len(sub.Keys) != 0 {
		t.Fatal("issued key must be deleted at HTX on terminal failure")
	}
	if e.count(`SELECT COUNT(*) FROM subaccount_api_keys WHERE user_id = 12 AND status = 'ACTIVE'`) != 0 ||
		e.count(`SELECT COUNT(*) FROM subaccount_api_keys WHERE user_id = 12 AND status = 'REVOKED'`) != 1 {
		t.Fatal("local key must be revoked")
	}
	e.provision(12)
	sub, _ = e.srv.SubUser(a.SubUID)
	if len(sub.Keys) != 1 {
		t.Fatalf("retry keys = %d", len(sub.Keys))
	}
}

func TestProvisionRetryableFailureResumes(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	e.srv.FailNext("/linear-swap-api/v1/swap_sub_auth", 0, 6021, "接口超时，请稍后重试")
	a, err := e.prov.Provision(ctx, 13)
	if !htx.IsRetryable(err) || a.State != subaccount.StateProvisioning || a.Step != subaccount.StepCreated {
		t.Fatalf("got %+v, %v", a, err)
	}
	if n, err := e.prov.ResumePending(ctx, 10); err != nil || n != 1 {
		t.Fatalf("resume = %d, %v", n, err)
	}
	if got, _ := subaccount.GetAccount(ctx, e.db, 13); got.State != subaccount.StateReady {
		t.Fatalf("after resume = %+v", got)
	}
}

func TestProvisionConcurrentSameUser(t *testing.T) {
	e := setup(t)
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := e.prov.Provision(context.Background(), 14)
			if err != nil && !errors.Is(err, subaccount.ErrProvisionInProgress) {
				t.Errorf("unexpected: %v", err)
			}
		}()
	}
	wg.Wait()
	e.provision(14)
	if subs := e.srv.SubUsers(); len(subs) != 1 {
		t.Fatalf("sub users = %v", subs)
	}
}

func TestConfigRequiresIPBinding(t *testing.T) {
	_, err := subaccount.NewProvisioner(nil, nil, nil, nil, nil, subaccount.Config{
		Master: master.Credentials(), NamePrefix: "bv",
	}, nil)
	if !errors.Is(err, subaccount.ErrInvalidConfig) || !strings.Contains(err.Error(), "egress") {
		t.Fatalf("missing IP binding accepted: %v", err)
	}
}

func TestLockUnlock(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	a := e.provision(15)
	if _, err := e.keys.Get(ctx, 15); err != nil {
		t.Fatal(err)
	}
	if err := e.prov.Lock(ctx, 15, "ops@corp", "risk review"); err != nil {
		t.Fatal(err)
	}
	if _, err := e.keys.Get(ctx, 15); !errors.Is(err, subaccount.ErrSubAccountLocked) {
		t.Fatalf("locked account must not yield credentials (cache invalidated): %v", err)
	}
	if sub, _ := e.srv.SubUser(a.SubUID); !sub.Locked {
		t.Fatal("HTX sub user not locked")
	}
	if err := e.prov.Unlock(ctx, 15, "ops@corp"); err != nil {
		t.Fatal(err)
	}
	if _, err := e.keys.Get(ctx, 15); err != nil {
		t.Fatalf("unlocked: %v", err)
	}
	if _, err := e.keys.Get(ctx, 999); !errors.Is(err, subaccount.ErrNoSubAccount) {
		t.Fatalf("unknown user: %v", err)
	}
}

// ---------------------------------------------------------------------------
// 划转
// ---------------------------------------------------------------------------

// readyWithSwap 开户并在本地 SWAP 入金。
func (e *env) readyWithSwap(uid int64, amt string) *subaccount.Account {
	e.t.Helper()
	a := e.provision(uid)
	ctx := context.Background()
	for _, ins := range []dealer.Instruction{
		dealer.NewDeposit(uid, "USDT", dec(amt), fmt.Sprintf("dep-%d", uid)),
		dealer.NewTransfer(uid, dealer.AccountSpot, dealer.AccountSwap, "USDT", dec(amt), fmt.Sprintf("tr-%d", uid)),
	} {
		if _, err := e.dealer.Execute(ctx, ins); err != nil {
			e.t.Fatal(err)
		}
	}
	return a
}

func (e *env) bal(uid int64, typ dealer.AccountType) dealer.BalanceSnapshot {
	e.t.Helper()
	b, err := e.dealer.Balance(context.Background(), dealer.AccountKey{OwnerID: uid, Type: typ, Currency: "USDT"})
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

func TestTransferToSubAndBack(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	a := e.readyWithSwap(20, "1000")
	masterBefore := e.srv.MasterSpot()

	r, err := e.xfer.ToSub(ctx, 20, "USDT", dec("300.123456789"), "USDT", "req-1")
	if err != nil || r.Status != subaccount.LegSucceeded || len(r.Legs) != 2 {
		t.Fatalf("to sub = %+v, %v", r, err)
	}
	sub, _ := e.srv.SubUser(a.SubUID)
	if !sub.Swap["USDT"].Equal(dec("300.123456789")) || !sub.Spot.IsZero() ||
		!e.srv.MasterSpot().Equal(masterBefore.Sub(dec("300.123456789"))) {
		t.Fatalf("htx balances: swap=%s spot=%s", sub.Swap["USDT"], sub.Spot)
	}
	e.assertBal(20, dealer.AccountSwap, "699.876543211", "0")
	e.assertBal(20, dealer.AccountHTXCustody, "300.123456789", "0")
	// 母子划转携带 10 位 client-order-id；现货-合约划转请求中没有 client-order-id
	for _, req := range e.srv.Requests() {
		switch req.Path {
		case "/v1/subuser/transfer":
			if s, _ := req.Body["client-order-id"].(string); len(s) != 10 {
				t.Fatalf("client-order-id = %v", req.Body["client-order-id"])
			}
			if _, isStr := req.Body["amount"].(string); !isStr {
				t.Fatal("amount must be sent as string")
			}
		case "/v2/account/transfer":
			if _, has := req.Body["client-order-id"]; has || req.Body["margin-account"] != "USDT" {
				t.Fatalf("account transfer body = %v", req.Body)
			}
		}
	}

	// 幂等：同一 request_id 不重复执行
	n := len(e.srv.Requests())
	if r2, err := e.xfer.ToSub(ctx, 20, "USDT", dec("300.123456789"), "USDT", "req-1"); err != nil || r2.Status != subaccount.LegSucceeded {
		t.Fatalf("replay = %+v, %v", r2, err)
	}
	if len(e.srv.Requests()) != n {
		t.Fatal("replayed request hit HTX")
	}
	if _, err := e.xfer.ToSub(ctx, 20, "USDT", dec("1"), "USDT", "req-1"); !errors.Is(err, subaccount.ErrInvalidRequest) {
		t.Fatalf("request_id reuse with different amount: %v", err)
	}

	r, err = e.xfer.FromSub(ctx, 20, "USDT", dec("100"), "USDT", "req-2")
	if err != nil || r.Status != subaccount.LegSucceeded {
		t.Fatalf("from sub = %+v, %v", r, err)
	}
	e.assertBal(20, dealer.AccountSwap, "799.876543211", "0")
	e.assertBal(20, dealer.AccountHTXCustody, "200.123456789", "0")
}

func TestTransferDisabledByDefaultFlag(t *testing.T) {
	e := setup(t)
	e.readyWithSwap(21, "100")
	off := subaccount.NewTransfers(e.db, e.dealer, nil, e.keys, master.Credentials(), false, nil)
	n := len(e.srv.Requests())
	if _, err := off.ToSub(context.Background(), 21, "USDT", dec("10"), "USDT", "r"); !errors.Is(err, subaccount.ErrTransfersDisabled) {
		t.Fatalf("want ErrTransfersDisabled, got %v", err)
	}
	if len(e.srv.Requests()) != n {
		t.Fatal("disabled transfers must not call HTX")
	}
	e.assertBal(21, dealer.AccountSwap, "100", "0")
}

// 现货-合约划转响应丢失：通过余额核验判定成功，绝不重发。
func TestTransferUnknownOutcomeVerifiedByBalance(t *testing.T) {
	e := setup(t)
	a := e.readyWithSwap(22, "500")
	e.srv.DropResponseAfterApply("/v2/account/transfer", 1)
	r, err := e.xfer.ToSub(context.Background(), 22, "USDT", dec("200"), "btc-usdt", "req-u")
	if err != nil || r.Status != subaccount.LegSucceeded {
		t.Fatalf("result = %+v, %v", r, err)
	}
	if n := e.srv.CountRequests("/v2/account/transfer"); n != 1 {
		t.Fatalf("account transfer sent %d times; must not resend after it executed", n)
	}
	sub, _ := e.srv.SubUser(a.SubUID)
	if !sub.Swap["btc-usdt"].Equal(dec("200")) || !sub.Spot.IsZero() {
		t.Fatalf("htx: swap=%v spot=%s", sub.Swap, sub.Spot)
	}
	if !strings.Contains(r.Legs[1].LastError, "") || r.Legs[1].Status != subaccount.LegSucceeded {
		t.Fatalf("leg = %+v", r.Legs[1])
	}
}

// 服务端未执行（HTTP 500）：核验余额未变动后才重发。
func TestTransferNotExecutedThenResent(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	a := e.readyWithSwap(23, "500")
	e.srv.FailNext("/v2/account/transfer", 500, 500, "系统错误")
	r, err := e.xfer.ToSub(ctx, 23, "USDT", dec("50"), "USDT", "req-n")
	if err != nil || r.Status != subaccount.LegUnknown {
		t.Fatalf("first call = %+v, %v", r, err)
	}
	if n := e.srv.CountRequests("/v2/account/transfer"); n != 1 {
		t.Fatalf("must not blindly resend within the same call: %d", n)
	}
	if done, err := e.xfer.Resume(ctx, 10); err != nil || done != 1 {
		t.Fatalf("resume = %d, %v", done, err)
	}
	if n := e.srv.CountRequests("/v2/account/transfer"); n != 2 {
		t.Fatalf("resend count = %d", n)
	}
	if sub, _ := e.srv.SubUser(a.SubUID); !sub.Swap["USDT"].Equal(dec("50")) {
		t.Fatalf("swap = %s", sub.Swap["USDT"])
	}
}

// 母子划转响应丢失：用同一 client-order-id 重发，火币幂等，只扣一次。
func TestMasterLegLostResponseResendIsIdempotent(t *testing.T) {
	e := setup(t)
	e.readyWithSwap(24, "500")
	before := e.srv.MasterSpot()
	e.srv.DropResponseAfterApply("/v1/subuser/transfer", 1)
	r, err := e.xfer.ToSub(context.Background(), 24, "USDT", dec("80"), "USDT", "req-m")
	if err != nil || r.Status != subaccount.LegSucceeded {
		t.Fatalf("result = %+v, %v", r, err)
	}
	if n := e.srv.CountRequests("/v1/subuser/transfer"); n != 2 {
		t.Fatalf("master leg requests = %d", n)
	}
	var ids []string
	for _, req := range e.srv.Requests() {
		if req.Path == "/v1/subuser/transfer" {
			ids = append(ids, req.Body["client-order-id"].(string))
		}
	}
	if ids[0] != ids[1] {
		t.Fatal("resend must reuse client-order-id")
	}
	if !e.srv.MasterSpot().Equal(before.Sub(dec("80"))) {
		t.Fatal("master debited more than once")
	}
}

func TestTransferFirstLegFailureRollsBackLedger(t *testing.T) {
	e := setup(t)
	e.readyWithSwap(25, "500")
	// 母子划转被火币业务拒绝（不可重试）：第 1 段 FAILED，账本回滚
	e.srv.FailNext("/v1/subuser/transfer", 0, 1002, "forbidden")
	r, err := e.xfer.ToSub(context.Background(), 25, "USDT", dec("100"), "USDT", "req-f")
	if err != nil || r.Status != subaccount.LegFailed || len(r.Legs) != 1 {
		t.Fatalf("result = %+v, %v", r, err)
	}
	e.assertBal(25, dealer.AccountSwap, "500", "0")
	e.assertBal(25, dealer.AccountHTXCustody, "0", "0")
	if n := e.srv.CountRequests("/v2/account/transfer"); n != 0 {
		t.Fatal("second leg must not run after first leg failed")
	}
}

func TestTransferSecondLegFailureReversesToMaster(t *testing.T) {
	e := setup(t)
	a := e.readyWithSwap(26, "500")
	before := e.srv.MasterSpot()
	e.srv.FailNext("/v2/account/transfer", 0, 1067, "字段不合法")
	r, err := e.xfer.ToSub(context.Background(), 26, "USDT", dec("120"), "USDT", "req-r")
	if err != nil || r.Status != subaccount.LegFailed || len(r.Legs) != 3 || r.Legs[2].Kind != subaccount.LegSubSpotToMaster {
		t.Fatalf("result = %+v, %v", r, err)
	}
	if !e.srv.MasterSpot().Equal(before) {
		t.Fatal("funds not returned to master")
	}
	if sub, _ := e.srv.SubUser(a.SubUID); !sub.Spot.IsZero() {
		t.Fatal("funds stuck in sub spot")
	}
	e.assertBal(26, dealer.AccountSwap, "500", "0")
	e.assertBal(26, dealer.AccountHTXCustody, "0", "0")
}

func TestFromSubInsufficientCustodyRejectedUpfront(t *testing.T) {
	e := setup(t)
	e.readyWithSwap(27, "100")
	n := len(e.srv.Requests())
	if _, err := e.xfer.FromSub(context.Background(), 27, "USDT", dec("1"), "USDT", "req-x"); !errors.Is(err, dealer.ErrInsufficientBalance) {
		t.Fatalf("want ErrInsufficientBalance, got %v", err)
	}
	if len(e.srv.Requests()) != n || e.count(`SELECT COUNT(*) FROM subaccount_transfers WHERE user_id = 27`) != 0 {
		t.Fatal("rejected request left HTX calls or rows")
	}
}

func TestBalanceMismatchGoesManual(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	a := e.readyWithSwap(28, "500")
	e.srv.FailNext("/v2/account/transfer", 500, 500, "系统错误")
	r, err := e.xfer.ToSub(ctx, 28, "USDT", dec("10"), "USDT", "req-mm")
	if err != nil || r.Status != subaccount.LegUnknown {
		t.Fatalf("status = %+v, %v", r, err)
	}
	// 外部因素改变了子账户现货余额：既不等于发起前，也不等于预期结果，核验无法判定
	if _, err := htxTransferFromMaster(e, a.SubUID, "3"); err != nil {
		t.Fatal(err)
	}
	r, err = e.xfer.ToSub(ctx, 28, "USDT", dec("10"), "USDT", "req-mm")
	if err != nil || r.Status != subaccount.LegManual {
		t.Fatalf("want MANUAL, got %+v, %v", r, err)
	}
	if n := e.srv.CountRequests("/v2/account/transfer"); n != 1 {
		t.Fatalf("must not resend when verification is inconclusive: %d", n)
	}
	if n := e.count(`SELECT COUNT(*) FROM subaccount_transfers WHERE user_id = 28 AND status = 'MANUAL'`); n != 1 {
		t.Fatal("manual leg not recorded")
	}
	// MANUAL 段不再占用在途槽位，但该请求不会被自动推进
	if done, _ := e.xfer.Resume(ctx, 10); done != 0 {
		t.Fatal("manual leg must not be auto-resumed")
	}
}

func htxTransferFromMaster(e *env, subUID int64, amt string) (int64, error) {
	c, _ := htx.NewClient(e.srv.URL)
	return c.SubUserTransfer(context.Background(), master.Credentials(), htx.MasterTransferRequest{
		SubUID: subUID, Currency: "usdt", Amount: dec(amt), ClientOrderID: "ext0000001", Type: htx.MasterTransferOut,
	})
}

func TestConcurrentTransfersSerializedPerUser(t *testing.T) {
	e := setup(t)
	e.readyWithSwap(29, "1000")
	var wg sync.WaitGroup
	var mu sync.Mutex
	results := map[string]int{}
	for i := 0; i < 6; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_, err := e.xfer.ToSub(context.Background(), 29, "USDT", dec("10"), "USDT", fmt.Sprintf("c-%d", i))
			mu.Lock()
			defer mu.Unlock()
			switch {
			case err == nil:
				results["ok"]++
			case errors.Is(err, subaccount.ErrTransferInProgress):
				results["busy"]++
			default:
				t.Errorf("unexpected: %v", err)
			}
		}(i)
	}
	wg.Wait()
	if results["ok"] == 0 {
		t.Fatal("no transfer succeeded")
	}
	ok := decimal.NewFromInt(int64(results["ok"] * 10))
	e.assertBal(29, dealer.AccountHTXCustody, ok.String(), "0")
	e.assertBal(29, dealer.AccountSwap, decimal.NewFromInt(1000).Sub(ok).String(), "0")
}

func TestTransferInputValidation(t *testing.T) {
	e := setup(t)
	e.readyWithSwap(30, "100")
	ctx := context.Background()
	for name, call := range map[string]func() error{
		"lower currency": func() error { _, err := e.xfer.ToSub(ctx, 30, "usdt", dec("1"), "USDT", "a"); return err },
		"zero amount":    func() error { _, err := e.xfer.ToSub(ctx, 30, "USDT", dec("0"), "USDT", "b"); return err },
		"too precise": func() error {
			_, err := e.xfer.ToSub(ctx, 30, "USDT", dec("0.0000000000000000001"), "USDT", "c")
			return err
		},
		"bad margin": func() error { _, err := e.xfer.ToSub(ctx, 30, "USDT", dec("1"), "BTC-USDT", "d"); return err },
	} {
		if err := call(); !errors.Is(err, subaccount.ErrInvalidRequest) {
			t.Errorf("%s: %v", name, err)
		}
	}
	if _, err := e.xfer.ToSub(ctx, 31, "USDT", dec("1"), "USDT", "e"); !errors.Is(err, subaccount.ErrNoSubAccount) {
		t.Fatalf("no account: %v", err)
	}
}
