package htx

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"regexp"
	"strings"
	"testing"
	"time"
)

// 《签名说明》硬性规范回归测试。

func TestSignRulePostSignsOnlyFourAuthParams(t *testing.T) {
	var gotQuery url.Values
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotQuery = r.URL.Query()
		_, _ = w.Write([]byte(`{"code":200,"data":{"order_id":"1"}}`))
	}))
	defer srv.Close()
	c, _ := NewClient(srv.URL)
	cred, _ := NewCredentials("ak", "sk")
	if err := c.Post(context.Background(), "/v5/trade/order", cred, map[string]string{"contract_code": "BTC-USDT"}, nil); err != nil {
		t.Fatal(err)
	}
	want := map[string]bool{"AccessKeyId": true, "SignatureMethod": true, "SignatureVersion": true, "Timestamp": true, "Signature": true}
	for k := range gotQuery {
		if !want[k] {
			t.Fatalf("POST query carries non-auth param %q", k)
		}
	}
	if len(gotQuery) != len(want) {
		t.Fatalf("POST query params = %v", gotQuery)
	}
	// 签名必须只基于四个公共参数即可复算
	pre := PreSignString("POST", strings.TrimPrefix(srv.URL, "http://"), "/v5/trade/order", gotQuery)
	if strings.Contains(pre, "contract_code") {
		t.Fatalf("business param in pre-sign string: %q", pre)
	}
	if Sign([]byte("sk"), "POST", strings.TrimPrefix(srv.URL, "http://"), "/v5/trade/order", gotQuery) != gotQuery.Get("Signature") {
		t.Fatal("signature not reproducible from the four auth params")
	}

	// 结构性防护：POST 携带 query 业务参数直接拒绝，不发请求
	err := c.Do(context.Background(), http.MethodPost, "/v5/trade/order", BucketTrade, cred,
		url.Values{"contract_code": {"BTC-USDT"}}, nil, nil)
	if !errors.Is(err, ErrInvalidRequest) {
		t.Fatalf("POST with query params must be rejected, got %v", err)
	}
}

func TestSignRuleHostLowercase(t *testing.T) {
	p := url.Values{"Timestamp": {"2017-05-11T15:19:30"}}
	lines := strings.Split(PreSignString("get", "API.HBDM.COM", "/v5/trade/order", p), "\n")
	if lines[0] != "GET" || lines[1] != "api.hbdm.com" {
		t.Fatalf("lines = %q", lines)
	}
	if Sign([]byte("sk"), "GET", "API.HBDM.COM", "/x", p) != Sign([]byte("sk"), "GET", "api.hbdm.com", "/x", p) {
		t.Fatal("host case must not affect signature")
	}
}

var lowerHex = regexp.MustCompile(`%[0-9A-Fa-f]{2}`)

func TestSignRuleUppercaseHexEncoding(t *testing.T) {
	p := url.Values{
		"Timestamp": {"2017-05-11T15:19:30"},
		"note":      {"a b/c+d=é:~"},
	}
	q := canonicalQuery(p, "Signature")
	if !strings.Contains(q, "Timestamp=2017-05-11T15%3A19%3A30") {
		t.Fatalf("colon must encode as %%3A: %s", q)
	}
	if !strings.Contains(q, "a%20b") {
		t.Fatalf("space must encode as %%20: %s", q)
	}
	for _, m := range lowerHex.FindAllString(q, -1) {
		if m != strings.ToUpper(m) {
			t.Fatalf("lowercase hex escape %q in %s", m, q)
		}
	}
	// 最终 URL 中的 Signature 同样大写编码（Base64 的 + / = 会被转义）
	cred, _ := NewCredentials("ak", "sk")
	enc := EncodeQuery(SignParams(cred, "GET", "api.hbdm.com", "/x", p, time.Unix(0, 0)))
	for _, m := range lowerHex.FindAllString(enc, -1) {
		if m != strings.ToUpper(m) {
			t.Fatalf("lowercase hex escape %q in final query", m)
		}
	}
}

func TestSignRuleWebSocketSignatureNotURLEncoded(t *testing.T) {
	// 构造一个签名中含 + / = 的场景：遍历时间戳直到出现需要转义的字符
	cred, _ := NewCredentials("ak", "sk")
	for i := 0; i < 200; i++ {
		p, err := WSAuthParams(cred, "wss://api.hbdm.com/ws/v5/notification", time.Unix(int64(1700000000+i), 0))
		if err != nil {
			t.Fatal(err)
		}
		sig := p.Get("Signature")
		if strings.Contains(sig, "%") {
			t.Fatalf("ws signature must be raw base64, got %q", sig)
		}
		if strings.ContainsAny(sig, "+/=") {
			if sig != Sign([]byte("sk"), "GET", "api.hbdm.com", "/ws/v5/notification", p) {
				t.Fatal("ws signature mismatch")
			}
			return
		}
	}
	t.Fatal("no signature containing +/= generated; widen search")
}
