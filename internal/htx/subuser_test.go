package htx

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"testing"

	"github.com/shopspring/decimal"
)

// 子用户余额响应示例逐字取自 docs/htx_api/子用户管理API文档/子用户余额.md
const docSubUserBalance = `{
"status":"ok",
"data":[
{
"id":13704588,
"type":"spot",
"state":"working",
"list":[
{
"currency":"usdt",
"type":"trade",
"balance":"8.0855916572",
"debt":"invalid",
"available":"invalid"
}
],
"symbol":""
},
{
"id":24994285,
"type":"point",
"state":"working",
"list":[
{
"currency":"hbpoint",
"type":"trade",
"balance":"10"
}
],
"symbol":""
}
]
}`

func TestParseDocSubUserBalance(t *testing.T) {
	accs, err := serve(t, 200, docSubUserBalance).SubUserSpotBalance(context.Background(), testCred(), 1)
	if err != nil {
		t.Fatal(err)
	}
	if got := SpotTradeBalance(accs, "usdt"); !got.Equal(decimal.RequireFromString("8.0855916572")) {
		t.Fatalf("usdt trade balance = %s", got)
	}
	if got := SpotTradeBalance(accs, "hbpoint"); !got.IsZero() {
		t.Fatal("point account must not count as spot")
	}
}

func TestParseDocCreateSubUserPartialFailure(t *testing.T) {
	// 文档示例中第二个条目以 errCode 表示重名
	body := `{"code":200,"data":[{"userName":"test456","note":"x","errCode":"2002","errMessage":"valueinusernameduplicatedwithexistingrecord"}]}`
	_, err := serve(t, 200, body).CreateSubUser(context.Background(), testCred(), "test456", "x", "")
	var item *ItemError
	if !errors.As(err, &item) || item.Code != "2002" {
		t.Fatalf("want ItemError 2002, got %v", err)
	}
}

func TestSubUserNameRules(t *testing.T) {
	for name, ok := range map[string]bool{
		"test123": true, "abcdef": true, "a12345": true, "abc": false, "1abcdef": false,
		"abc_def": false, "abcdefghijklmnopqrstu": false, "Abcdef1": true,
	} {
		if validSubUserName(name) != ok {
			t.Errorf("%q: want %v", name, ok)
		}
	}
}

func TestSubUserRequestGuards(t *testing.T) {
	c, _ := NewClient("http://127.0.0.1:1")
	ctx := context.Background()
	if _, err := c.GenerateSubAPIKey(ctx, testCred(), SubAPIKeyRequest{SubUID: 1, Note: "n", Permission: "readOnly,trade"}); !errors.Is(err, ErrIPBindingRequired) {
		t.Fatalf("missing IP binding: %v", err)
	}
	for _, cid := range []string{"", "123456789", "12345678901"} {
		_, err := c.SubUserTransfer(ctx, testCred(), MasterTransferRequest{SubUID: 1, Currency: "usdt",
			Amount: decimal.NewFromInt(1), ClientOrderID: cid, Type: MasterTransferOut})
		if !errors.Is(err, ErrInvalidRequest) {
			t.Errorf("client-order-id %q accepted: %v", cid, err)
		}
	}
	if _, err := c.SubUserTransfer(ctx, testCred(), MasterTransferRequest{SubUID: 1, Currency: "USDT",
		Amount: decimal.NewFromInt(1), ClientOrderID: "b000000001", Type: MasterTransferOut}); !errors.Is(err, ErrInvalidRequest) {
		t.Fatal("upper-case currency accepted")
	}
	if _, err := c.AccountTransfer(ctx, testCred(), AccountTransferRequest{From: LineSpot, To: LineSpot, Currency: "usdt",
		Amount: decimal.NewFromInt(1), MarginAccount: "USDT"}); !errors.Is(err, ErrInvalidRequest) {
		t.Fatal("same from/to accepted")
	}
}

func TestOTPErrorClassified(t *testing.T) {
	c := serve(t, 200, `{"code":1003,"message":"otp token is required"}`)
	_, err := c.GenerateSubAPIKey(context.Background(), testCred(), SubAPIKeyRequest{SubUID: 1, Note: "n",
		Permission: "readOnly,trade", IPAddresses: []string{"1.2.3.4"}})
	if !errors.Is(err, ErrOTPRequired) || IsRetryable(err) {
		t.Fatalf("want non-retryable ErrOTPRequired, got %v", err)
	}
}

func TestSubAPIKeyRedaction(t *testing.T) {
	const secret = "fake-generated-secret-value"
	c := serve(t, 200, `{"code":200,"data":{"accessKey":"fake-access-key-123456","secretKey":"`+secret+`","permission":"readOnly,trade"}}`)
	k, err := c.GenerateSubAPIKey(context.Background(), testCred(), SubAPIKeyRequest{SubUID: 1, Note: "n",
		Permission: "readOnly,trade", IPAddresses: []string{"1.2.3.4"}})
	if err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	slog.New(slog.NewJSONHandler(&buf, nil)).Info("k", slog.Any("key", k), slog.Any("val", *k))
	js, _ := json.Marshal(k)
	for _, out := range []string{fmt.Sprint(k), fmt.Sprintf("%+v %#v", k, *k), buf.String(), string(js)} {
		if strings.Contains(out, secret) {
			t.Fatal("secret leaked")
		}
	}
	got := k.Secret()
	k.Wipe()
	if string(got) != secret || string(k.Secret()) == secret {
		t.Fatal("Secret copy / Wipe semantics broken")
	}
}
