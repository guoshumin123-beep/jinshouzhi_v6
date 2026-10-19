package htx

import (
	"bytes"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/url"
	"strings"
	"testing"
	"time"
)

// 交叉校验向量：由 openssl 独立计算
//
//	printf '<payload>' | openssl dgst -sha256 -hmac '<secret>' -binary | base64
//
// TODO: 待 docs/htx_api/ 补充官方签名示例后增加官方向量。
func TestSignMatchesOpenSSL(t *testing.T) {
	params := url.Values{
		"order-id":         {"1234567890"},
		"AccessKeyId":      {"e2xxxxxx-99xxxxxx-84xxxxxx-7xxxx"},
		"SignatureMethod":  {"HmacSHA256"},
		"SignatureVersion": {"2"},
		"Timestamp":        {"2017-05-11T15:19:30"},
	}
	wantPayload := "GET\napi.huobi.pro\n/v1/order/orders\n" +
		"AccessKeyId=e2xxxxxx-99xxxxxx-84xxxxxx-7xxxx&SignatureMethod=HmacSHA256&SignatureVersion=2&Timestamp=2017-05-11T15%3A19%3A30&order-id=1234567890"
	if got := PreSignString("get", "API.huobi.pro", "/v1/order/orders", params); got != wantPayload {
		t.Fatalf("payload mismatch:\n got %q\nwant %q", got, wantPayload)
	}
	got := Sign([]byte("b0xxxxxx-c6xxxxxx-94xxxxxx-dxxxx"), "GET", "api.huobi.pro", "/v1/order/orders", params)
	if want := "Nmd8AU8uAe0mkFpxNbiava0aeZzBEtYjCdie1ZYZjoM="; got != want {
		t.Fatalf("signature = %s, want %s", got, want)
	}
}

func TestSignParams(t *testing.T) {
	cred, _ := NewCredentials("ak-123456789", "sk")
	in := url.Values{"symbol": {"btc usdt"}}
	now := time.Date(2026, 10, 15, 12, 30, 45, 999, time.FixedZone("CST", 8*3600))
	out := SignParams(cred, "POST", "api.hbdm.com", "/linear-swap-api/v1/swap_order", in, now)

	if len(in) != 1 {
		t.Fatal("input params mutated")
	}
	if ts := out.Get("Timestamp"); ts != "2026-10-15T04:30:45" {
		t.Fatalf("timestamp must be UTC yyyy-MM-ddTHH:mm:ss, got %s", ts)
	}
	for k, v := range map[string]string{"AccessKeyId": "ak-123456789", "SignatureMethod": "HmacSHA256", "SignatureVersion": "2"} {
		if out.Get(k) != v {
			t.Errorf("%s = %q, want %q", k, out.Get(k), v)
		}
	}
	if want := Sign([]byte("sk"), "POST", "api.hbdm.com", "/linear-swap-api/v1/swap_order", out); out.Get("Signature") != want {
		t.Fatal("signature not reproducible")
	}
	if pre := PreSignString("POST", "api.hbdm.com", "/x", out); !strings.Contains(pre, "symbol=btc%20usdt") || strings.Contains(pre, "Signature=") {
		t.Fatalf("bad canonical string: %q", pre)
	}
	if q := EncodeQuery(out); !strings.HasSuffix(q, "&Signature="+escape(out.Get("Signature"))) {
		t.Fatalf("Signature should be last: %s", q)
	}
}

func TestCredentialsNeverLeakSecret(t *testing.T) {
	const secret = "super-secret-value"
	cred, _ := NewCredentials("fake-access-key-abcdef", secret)

	var logBuf bytes.Buffer
	slog.New(slog.NewJSONHandler(&logBuf, nil)).Info("x", slog.Any("cred", cred), slog.Any("val", *cred))
	js, _ := json.Marshal(struct{ C *Credentials }{cred})

	outputs := []string{
		fmt.Sprint(cred), fmt.Sprintf("%v %+v %#v", cred, cred, cred),
		fmt.Sprintf("%v %+v %#v", *cred, *cred, *cred),
		logBuf.String(), string(js),
	}
	for _, s := range outputs {
		if strings.Contains(s, secret) {
			t.Fatalf("secret leaked: %s", s)
		}
		if strings.Contains(s, "fake-access-key-abcdef") {
			t.Fatalf("access key not masked: %s", s)
		}
	}
}

func TestCredentialsFromEnv(t *testing.T) {
	t.Setenv(EnvAccessKey, "")
	t.Setenv(EnvSecretKey, "")
	if _, err := CredentialsFromEnv(); err == nil {
		t.Fatal("expected error when env missing")
	}
	t.Setenv(EnvAccessKey, "ak")
	t.Setenv(EnvSecretKey, "sk")
	c, err := CredentialsFromEnv()
	if err != nil || c.AccessKey != "ak" {
		t.Fatalf("got %v, %v", c, err)
	}
}
