package htx

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

func gz(t *testing.T, s string) []byte {
	t.Helper()
	var buf bytes.Buffer
	w := gzip.NewWriter(&buf)
	_, _ = w.Write([]byte(s))
	_ = w.Close()
	return buf.Bytes()
}

func TestWSClientHeartbeatSubscribeAndDispatch(t *testing.T) {
	serverGot := make(chan string, 10)
	upgrader := websocket.Upgrader{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer conn.Close()
		// 等待订阅消息
		_, sub, err := conn.ReadMessage()
		if err != nil {
			return
		}
		serverGot <- string(sub)

		_ = conn.WriteMessage(websocket.BinaryMessage, gz(t, `{"ping":1700000000000}`))
		_, pong, _ := conn.ReadMessage()
		serverGot <- string(pong)

		_ = conn.WriteMessage(websocket.BinaryMessage, gz(t, `{"op":"ping","ts":"1700000000001"}`))
		_, pong2, _ := conn.ReadMessage()
		serverGot <- string(pong2)

		_ = conn.WriteMessage(websocket.BinaryMessage, gz(t, `{"ch":"market.BTC-USDT.bbo","tick":{"bid":[1,2]}}`))
		_, _, _ = conn.ReadMessage() // 保持连接直到客户端关闭
	}))
	defer srv.Close()

	got := make(chan string, 1)
	panicked := false
	c := NewWSClient("ws"+strings.TrimPrefix(srv.URL, "http"), func(msg []byte) {
		got <- string(msg)
		if !panicked {
			panicked = true
			panic("handler bug must not kill the read loop")
		}
	})
	if err := c.Subscribe("market.BTC-USDT.bbo"); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	runErr := make(chan error, 1)
	go func() { runErr <- c.Run(ctx) }()

	expectJSON := func(want map[string]any) {
		t.Helper()
		select {
		case s := <-serverGot:
			var m map[string]any
			if err := json.Unmarshal([]byte(s), &m); err != nil {
				t.Fatalf("bad json %q", s)
			}
			for k, v := range want {
				if m[k] != v {
					t.Fatalf("server got %s, want %s=%v", s, k, v)
				}
			}
		case <-ctx.Done():
			t.Fatal("timeout")
		}
	}
	expectJSON(map[string]any{"sub": "market.BTC-USDT.bbo"})
	expectJSON(map[string]any{"pong": float64(1700000000000)})
	expectJSON(map[string]any{"op": "pong", "ts": "1700000000001"})

	select {
	case msg := <-got:
		if !strings.Contains(msg, "market.BTC-USDT.bbo") {
			t.Fatalf("handler got %q", msg)
		}
	case <-ctx.Done():
		t.Fatal("handler not called")
	}

	cancel()
	select {
	case <-runErr:
	case <-time.After(3 * time.Second):
		t.Fatal("Run did not exit after cancel")
	}
}

func TestWSAuthParamsSignsHostAndPath(t *testing.T) {
	cred, _ := NewCredentials("ak", "sk")
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	p, err := WSAuthParams(cred, "wss://api.hbdm.com/linear-swap-notification", now)
	if err != nil {
		t.Fatal(err)
	}
	if want := Sign([]byte("sk"), "GET", "api.hbdm.com", "/linear-swap-notification", p); p.Get("Signature") != want {
		t.Fatal("ws auth signature mismatch")
	}
}
