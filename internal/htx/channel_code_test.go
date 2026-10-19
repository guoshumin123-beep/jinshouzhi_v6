package htx_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"backend_v2/internal/htx"
	"backend_v2/internal/htx/htxmock"
)

const testBrokerID = "AAe19e2c9d"

func placeReq() htx.SwapOrderRequest {
	return htx.SwapOrderRequest{ContractCode: "BTC-USDT", MarginMode: htx.MarginCross, Side: htx.SideBuy,
		Type: htx.OrderTypeMarket, Volume: "1", ClientOrderID: "42"}
}

func TestChannelCodeInjectedIntoBody(t *testing.T) {
	srv := htxmock.New(subA)
	defer srv.Close()
	c, _ := htx.NewClient(srv.URL, htx.WithChannelCode(testBrokerID))
	if _, err := c.SwapPlaceOrder(context.Background(), subA.Credentials(), placeReq()); err != nil {
		t.Fatalf("mock (strict body) must accept whitelisted channel_code: %v", err)
	}
	reqs := srv.Requests()
	r := reqs[len(reqs)-1]
	// 与 contract_code / side / type 平级位于 JSON body
	if r.Body["channel_code"] != testBrokerID || r.Body["contract_code"] != "BTC-USDT" || r.Body["side"] != "buy" {
		t.Fatalf("body = %v", r.Body)
	}
	// 不在 URL 参数中，也不参与签名（mock 已按四个公共参数验签通过）
	for k := range r.Query {
		if strings.EqualFold(k, "channel_code") {
			t.Fatal("channel_code must not be a URL/signature parameter")
		}
	}
	if o := srv.Orders(); len(o) != 1 || o[0].ChannelCode != testBrokerID {
		t.Fatalf("mock order = %+v", o)
	}
}

func TestChannelCodeOmittedWhenNotConfigured(t *testing.T) {
	srv := htxmock.New(subA)
	defer srv.Close()
	c, _ := htx.NewClient(srv.URL)
	if _, err := c.SwapPlaceOrder(context.Background(), subA.Credentials(), placeReq()); err != nil {
		t.Fatal(err)
	}
	reqs := srv.Requests()
	if _, has := reqs[len(reqs)-1].Body["channel_code"]; has {
		t.Fatal("empty channel_code must be omitted")
	}
}

func TestChannelCodeExplicitValueWinsAndValidated(t *testing.T) {
	srv := htxmock.New(subA)
	defer srv.Close()
	c, _ := htx.NewClient(srv.URL, htx.WithChannelCode(testBrokerID))
	r := placeReq()
	r.ChannelCode = "OtherBroker1"
	if _, err := c.SwapPlaceOrder(context.Background(), subA.Credentials(), r); err != nil {
		t.Fatal(err)
	}
	reqs := srv.Requests()
	if reqs[len(reqs)-1].Body["channel_code"] != "OtherBroker1" {
		t.Fatal("explicit channel_code overridden")
	}
	bad, _ := htx.NewClient(srv.URL, htx.WithChannelCode("bad code!"))
	if _, err := bad.SwapPlaceOrder(context.Background(), subA.Credentials(), placeReq()); !errors.Is(err, htx.ErrInvalidRequest) {
		t.Fatalf("invalid channel_code must be rejected locally: %v", err)
	}
}

func TestSwapChannelCodeFromEnv(t *testing.T) {
	t.Setenv(htx.EnvSwapChannelCode, testBrokerID)
	if v, err := htx.SwapChannelCodeFromEnv(); err != nil || v != testBrokerID {
		t.Fatalf("got %q, %v", v, err)
	}
	t.Setenv(htx.EnvSwapChannelCode, "")
	if v, err := htx.SwapChannelCodeFromEnv(); err != nil || v != "" {
		t.Fatalf("empty: %q, %v", v, err)
	}
	t.Setenv(htx.EnvSwapChannelCode, "no spaces allowed")
	if _, err := htx.SwapChannelCodeFromEnv(); !errors.Is(err, htx.ErrInvalidRequest) {
		t.Fatalf("invalid env accepted: %v", err)
	}
}
