package htx

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"
)

// 所有测试均使用本地 httptest 服务器，不访问真实 HTX。

func TestClientSignedRequestVerifiesServerSide(t *testing.T) {
	const secret = "test-secret"
	var gotBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		// 服务端用相同算法复算签名
		want := Sign([]byte(secret), r.Method, r.Host, r.URL.Path, q)
		if q.Get("Signature") != want {
			http.Error(w, `{"status":"error","err_code":1003,"err_msg":"bad sig"}`, http.StatusOK)
			return
		}
		if r.Method == http.MethodPost {
			if ct := r.Header.Get("Content-Type"); ct != "application/json" {
				t.Errorf("content-type = %q", ct)
			}
			b, _ := io.ReadAll(r.Body)
			_ = json.Unmarshal(b, &gotBody)
		}
		_, _ = w.Write([]byte(`{"status":"ok","data":{"echo":"` + q.Get("contract_code") + `"},"ts":1}`))
	}))
	defer srv.Close()

	fixed := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	c, err := NewClient(srv.URL, withClock(func() time.Time { return fixed }))
	if err != nil {
		t.Fatal(err)
	}
	cred, _ := NewCredentials("ak", secret)
	ctx := context.Background()

	var out struct{ Echo string }
	if err := c.Get(ctx, "/linear-swap-api/v1/swap_position_info", cred, url.Values{"contract_code": {"BTC-USDT"}}, &out); err != nil {
		t.Fatal(err)
	}
	if out.Echo != "BTC-USDT" {
		t.Fatalf("echo = %q", out.Echo)
	}

	if err := c.Post(ctx, "/linear-swap-api/v1/swap_account_info", cred, map[string]string{"margin_account": "USDT"}, nil); err != nil {
		t.Fatal(err)
	}
	if gotBody["margin_account"] != "USDT" {
		t.Fatalf("body = %v", gotBody)
	}
}

func TestClientPublicRequestIsUnsigned(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Has("Signature") || r.URL.Query().Has("AccessKeyId") {
			t.Error("public request must not carry credentials")
		}
		_, _ = w.Write([]byte(`{"status":"ok","data":[]}`))
	}))
	defer srv.Close()
	c, _ := NewClient(srv.URL)
	if err := c.Get(context.Background(), "/v1/common/symbols", nil, nil, nil); err != nil {
		t.Fatal(err)
	}
}

func TestClientErrorEnvelopes(t *testing.T) {
	cases := map[string]struct {
		body     string
		status   int
		wantCode string
	}{
		"spot":   {`{"status":"error","err-code":"order-limitorder-amount-min-error","err-msg":"min"}`, 200, "order-limitorder-amount-min-error"},
		"swap":   {`{"status":"error","err_code":1047,"err_msg":"insufficient margin","ts":1}`, 200, "1047"},
		"v2":     {`{"code":2002,"message":"invalid field","data":null}`, 200, "2002"},
		"http5x": {`{}`, 502, ""},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(tc.status)
				_, _ = w.Write([]byte(tc.body))
			}))
			defer srv.Close()
			c, _ := NewClient(srv.URL)
			err := c.Get(context.Background(), "/x", nil, nil, nil)
			apiErr, ok := IsAPIError(err)
			if !ok {
				t.Fatalf("want APIError, got %v", err)
			}
			if apiErr.Code != tc.wantCode {
				t.Fatalf("code = %q, want %q", apiErr.Code, tc.wantCode)
			}
		})
	}
}
