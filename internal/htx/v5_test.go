package htx

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/shopspring/decimal"
)

// 以下响应体逐字取自 docs/htx_api/U本位联合保证金API文档 中的响应示例。

const docOrderInfo = `{
    "code": 200,
    "message": "Success",
    "data": {
        "id": "1463958523020918784",
        "side": "buy",
        "type": "limit",
        "price": "5000",
        "volume": "1",
        "volume": "0",
        "state": "new",
        "profit": null,
        "contract_code": "BTC-USDT",
        "position_side": "long",
        "price_match": null,
        "order_id": "1463958523020918784",
        "client_order_id": "5",
        "margin_mode": "cross",
        "lever_rate": 10,
        "order_source": "api",
        "reduce_only": false,
        "time_in_force": "gtc",
        "tp_trigger_price": "",
        "tp_order_price": "",
        "tp_type": "0",
        "tp_trigger_price_type": "",
        "sl_trigger_price": "",
        "sl_order_price": "",
        "sl_type": "0",
        "sl_trigger_price_type": "",
        "trade_avg_price": "0",
        "trade_volume": "0",
        "trade_turnover": "0",
        "fee_currency": "",
        "fee": "0",
        "price_protect": true,
        "real_profit": null,
        "contract_type": "swap",
        "created_time": "1769076510922",
        "updated_time": "1769076510922",
        "cancel_reason": null,
        "self_match_prevent": "cancel_taker"
    },
    "ts": 1769076533192
}`

const docPositions = `{
	"code": 200,
	"message": "Success",
	"data": [{
		"contract_code": "ETH-USDT",
		"position_side": "both",
		"direction": "buy",
		"margin_mode": "cross",
		"open_avg_price": "1672.36",
		"volume": "10",
		"available": "10",
		"lever_rate": 5,
		"adl_risk_percent": null,
		"liquidation_price": "-6615323.17169213120630934",
		"initial_margin": "23.9516",
		"maintenance_margin": "0.4490925",
		"margin": "0.913265012017327136",
		"profit_unreal": "-47.478",
		"profit_rate": "-1.9822",
		"margin_rate": "0.000000681298594547",
		"mark_price": "1197.58",
		"margin_currency": "USDT",
		"contract_type": "swap",
		"created_time": 1740451087505,
		"updated_time": 1740451087505
	}],
	"ts": 1740451897695
}`

const docBalance = `{
    "code": 200,
    "message": "Success",
    "data": {
        "state": "normal",
        "equity": "0",
        "details": [
            {
                "currency": "USDT",
                "equity": "113.540541233146082524",
                "available": "113.540541233146082524",
                "isolated_available": "30.00",
                "profit_unreal": "0",
                "initial_margin": "0",
                "maintenance_margin": "0",
                "maintenance_margin_rate": "0",
                "initial_margin_rate": "0",
                "available_margin": "113.540541233146082524",
                "voucher": "0",
                "voucher_value": "0",
                "withdraw_available": "113.540541233146082524",
                "created_time": 1755261041303,
                "updated_time": 1767771949096,
                "isolated_equity": "0.5",
                "isolated_profit_unreal": "0"
            }
        ],
        "initial_margin": "0",
        "maintenance_margin": "0",
        "maintenance_margin_rate": "0",
        "profit_unreal": "0",
        "available_margin": "0",
        "voucher_value": "0",
        "created_time": 1755246156916,
        "updated_time": 1766390998717
    },
    "ts": 1767771989216
}`

func serve(t *testing.T, status int, body string) *Client {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	c, err := NewClient(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func testCred() *Credentials {
	c, _ := NewCredentials("ak", "sk")
	c.UID = 42
	return c
}

func TestParseDocOrderInfo(t *testing.T) {
	o, err := serve(t, 200, docOrderInfo).SwapOrderInfo(context.Background(), testCred(), "BTC-USDT", MarginCross, "1463958523020918784", "")
	if err != nil {
		t.Fatal(err)
	}
	if o.OrderID != "1463958523020918784" || o.ClientOrderID != "5" || o.State != OrderStateNew || o.Final() {
		t.Fatalf("order = %+v", o)
	}
	if !o.LeverRate.Valid || o.LeverRate.IntPart() != 10 || o.Profit.Valid || o.RealProfit.Valid || !o.Price.Equal(decimal.NewFromInt(5000)) {
		t.Fatalf("numeric fields = lever %+v profit %+v price %s", o.LeverRate, o.Profit, o.Price)
	}
	if o.CreatedTime.UnixMilli() != 1769076510922 {
		t.Fatalf("created_time = %v", o.CreatedTime)
	}
}

func TestParseDocPositions(t *testing.T) {
	ps, err := serve(t, 200, docPositions).SwapOpenPositions(context.Background(), testCred(), "ETH-USDT")
	if err != nil || len(ps) != 1 {
		t.Fatalf("positions = %+v, %v", ps, err)
	}
	p := ps[0]
	if p.Direction != SideBuy || p.MarginMode != MarginCross || !p.Volume.Equal(decimal.NewFromInt(10)) ||
		p.ADLRiskPercent.Valid || p.LiquidationPrice.String() != "-6615323.17169213120630934" ||
		p.UpdatedTime.UnixMilli() != 1740451087505 || p.LeverRate.IntPart() != 5 {
		t.Fatalf("position = %+v", p)
	}
}

func TestParseDocBalance(t *testing.T) {
	b, err := serve(t, 200, docBalance).AccountBalance(context.Background(), testCred())
	if err != nil {
		t.Fatal(err)
	}
	d, ok := b.Detail("USDT")
	if !ok || b.State != "normal" || d.Available.String() != "113.540541233146082524" || d.IsolatedAvailable.String() != "30" ||
		d.IsolatedEquity.String() != "0.5" {
		t.Fatalf("balance = %+v", b)
	}
}

func TestAssetModeUsesMsgEnvelope(t *testing.T) {
	m, err := serve(t, 200, `{"code":200,"msg":"操作成功","data":{"asset_mode":1},"ts":1547521135713}`).AssetMode(context.Background(), testCred())
	if err != nil || m != AssetModeMulti {
		t.Fatalf("asset mode = %d, %v", m, err)
	}
}

func TestErrorClassification(t *testing.T) {
	ctx := context.Background()
	req := SwapCancelRequest{ContractCode: "BTC-USDT", OrderID: "1"}
	// 文档《撤单》失败示例
	_, err := serve(t, 200, `{"code": 1061, "message": "The order does not exist.", "data": null, "ts": 1782119905956}`).SwapCancelOrder(ctx, testCred(), req)
	if e, ok := IsAPIError(err); !ok || e.Code != "1061" || IsRetryable(err) {
		t.Fatalf("1061: %v", err)
	}
	for _, body := range []string{`{"code":6021,"message":"接口超时，请稍后重试","data":null}`, `{"code":500,"message":"系统错误","data":null}`} {
		if _, err := serve(t, 200, body).SwapCancelOrder(ctx, testCred(), req); !IsRetryable(err) {
			t.Fatalf("%s should be retryable: %v", body, err)
		}
	}
	if _, err := serve(t, 502, `<html>bad gateway</html>`).SwapCancelOrder(ctx, testCred(), req); !IsRetryable(err) {
		t.Fatalf("502 should be retryable: %v", err)
	}
	// 网络错误：连接被拒绝
	c, _ := NewClient("http://127.0.0.1:1")
	_, err = c.SwapCancelOrder(ctx, testCred(), req)
	var te *TransportError
	if !errors.As(err, &te) || !IsRetryable(err) {
		t.Fatalf("transport error: %v", err)
	}
}

func TestOrderRequestValidation(t *testing.T) {
	ok := SwapOrderRequest{ContractCode: "BTC-USDT", MarginMode: MarginCross, Side: SideBuy, Type: OrderTypeMarket, Volume: "1", ClientOrderID: "9223372036854775807"}
	if err := ok.Validate(); err != nil {
		t.Fatal(err)
	}
	bad := map[string]func(*SwapOrderRequest){
		"margin mode":        func(r *SwapOrderRequest) { r.MarginMode = "both" },
		"side":               func(r *SwapOrderRequest) { r.Side = "long" },
		"type":               func(r *SwapOrderRequest) { r.Type = "opponent" },
		"market with price":  func(r *SwapOrderRequest) { r.Price = "1" },
		"limit no price":     func(r *SwapOrderRequest) { r.Type = OrderTypeLimit },
		"price + match":      func(r *SwapOrderRequest) { r.Type, r.Price, r.PriceMatch = OrderTypeLimit, "1", "opponent" },
		"fractional volume":  func(r *SwapOrderRequest) { r.Volume = "1.5" },
		"client id zero":     func(r *SwapOrderRequest) { r.ClientOrderID = "0" },
		"client id overflow": func(r *SwapOrderRequest) { r.ClientOrderID = "9223372036854775808" },
		"position side":      func(r *SwapOrderRequest) { r.PositionSide = "up" },
	}
	for name, mut := range bad {
		r := ok
		mut(&r)
		if err := r.Validate(); !errors.Is(err, ErrInvalidRequest) {
			t.Errorf("%s: want ErrInvalidRequest, got %v", name, err)
		}
	}
}

func TestNumAcceptsAllShapes(t *testing.T) {
	for in, want := range map[string]string{`"1.50"`: "1.5", `12`: "12", `-3.25`: "-3.25"} {
		var n Num
		if err := n.UnmarshalJSON([]byte(in)); err != nil || !n.Valid || n.String() != want {
			t.Errorf("%s -> %+v %v", in, n, err)
		}
	}
	for _, in := range []string{`null`, `""`} {
		var n Num
		if err := n.UnmarshalJSON([]byte(in)); err != nil || n.Valid {
			t.Errorf("%s should be invalid: %+v %v", in, n, err)
		}
	}
	var n Num
	if err := n.UnmarshalJSON([]byte(`"abc"`)); err == nil || !strings.Contains(err.Error(), "invalid number") {
		t.Fatalf("garbage accepted: %v", err)
	}
	var m Millis
	if err := m.UnmarshalJSON([]byte(`"1740451087505"`)); err != nil || !m.Equal(time.UnixMilli(1740451087505)) {
		t.Fatalf("millis = %v %v", m, err)
	}
}
