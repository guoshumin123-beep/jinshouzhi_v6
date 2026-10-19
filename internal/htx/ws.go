package htx

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/url"
	"sync"
	"time"

	"github.com/gorilla/websocket"

	"backend_v2/internal/pkg/safego"
)

const (
	wsReadTimeout  = 30 * time.Second // HTX 约每 5s 发一次 ping，超时视为断线
	wsWriteTimeout = 5 * time.Second
	wsMaxBackoff   = 30 * time.Second
	wsMaxMsgBytes  = 4 << 20
)

// WSClient 带自动重连、gzip 解压、ping/pong 应答和断线重订阅的 WebSocket 客户端骨架。
// 行情 (market) 与私有推送 (notification) 共用，具体频道协议待 docs/htx_api/ 确认。
type WSClient struct {
	url     string
	handler func(msg []byte)
	// onConnect 在每次（重）连接成功后、重订阅前调用，用于私有频道鉴权。
	onConnect func(ctx context.Context, c *WSClient) error
	logger    *slog.Logger
	dialer    *websocket.Dialer

	mu     sync.Mutex
	conn   *websocket.Conn
	topics map[string]struct{}
}

type WSOption func(*WSClient)

func WithWSLogger(l *slog.Logger) WSOption { return func(c *WSClient) { c.logger = l } }
func WithWSOnConnect(f func(ctx context.Context, c *WSClient) error) WSOption {
	return func(c *WSClient) { c.onConnect = f }
}

// NewWSClient handler 收到的是解压后、已剔除心跳的业务消息；handler 内 panic 会被捕获。
func NewWSClient(wsURL string, handler func(msg []byte), opts ...WSOption) *WSClient {
	c := &WSClient{
		url:     wsURL,
		handler: handler,
		logger:  slog.Default(),
		dialer:  &websocket.Dialer{HandshakeTimeout: 10 * time.Second},
		topics:  make(map[string]struct{}),
	}
	for _, o := range opts {
		o(c)
	}
	c.logger = c.logger.With(slog.String("component", "htx_ws"), slog.String("url", wsURL))
	return c
}

// Run 阻塞运行直到 ctx 取消，断线后指数退避重连。调用方应通过 safego.Go 启动。
func (c *WSClient) Run(ctx context.Context) error {
	backoff := time.Second
	for {
		err := c.runOnce(ctx)
		if ctx.Err() != nil {
			return ctx.Err()
		}
		c.logger.Warn("htx ws disconnected, reconnecting", slog.String("error", errString(err)), slog.Duration("backoff", backoff))
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(backoff):
		}
		backoff = min(backoff*2, wsMaxBackoff)
	}
}

func (c *WSClient) runOnce(ctx context.Context) error {
	conn, _, err := c.dialer.DialContext(ctx, c.url, nil)
	if err != nil {
		return fmt.Errorf("dial: %w", err)
	}
	conn.SetReadLimit(wsMaxMsgBytes)
	c.mu.Lock()
	c.conn = conn
	c.mu.Unlock()

	done := make(chan struct{})
	defer func() {
		close(done)
		c.mu.Lock()
		c.conn = nil
		c.mu.Unlock()
		_ = conn.Close()
	}()
	// ctx 取消时主动关闭连接，打断阻塞的 ReadMessage
	safego.Go(c.logger, "htx_ws_ctx_watch", func() {
		select {
		case <-ctx.Done():
			_ = conn.Close()
		case <-done:
		}
	})

	if c.onConnect != nil {
		if err := c.onConnect(ctx, c); err != nil {
			return fmt.Errorf("on connect: %w", err)
		}
	}
	if err := c.resubscribe(); err != nil {
		return fmt.Errorf("resubscribe: %w", err)
	}
	c.logger.Info("htx ws connected")

	for {
		_ = conn.SetReadDeadline(time.Now().Add(wsReadTimeout))
		typ, data, err := conn.ReadMessage()
		if err != nil {
			return fmt.Errorf("read: %w", err)
		}
		if typ == websocket.BinaryMessage {
			if data, err = gunzip(data); err != nil {
				return fmt.Errorf("gunzip: %w", err)
			}
		}
		if handled, err := c.handleHeartbeat(data); handled {
			if err != nil {
				return fmt.Errorf("pong: %w", err)
			}
			continue
		}
		c.dispatch(data)
	}
}

// heartbeat 覆盖两种已知格式（待 docs/htx_api/ 确认）：
//
//	行情：{"ping": 1492420473027}            -> {"pong": 1492420473027}
//	私有：{"op": "ping", "ts": "1492420473027"} -> {"op": "pong", "ts": "1492420473027"}
type heartbeat struct {
	Ping json.RawMessage `json:"ping"`
	Op   string          `json:"op"`
	TS   json.RawMessage `json:"ts"`
}

func (c *WSClient) handleHeartbeat(data []byte) (bool, error) {
	if !bytes.Contains(data, []byte("ping")) {
		return false, nil
	}
	var hb heartbeat
	if err := json.Unmarshal(data, &hb); err != nil {
		return false, nil
	}
	switch {
	case len(hb.Ping) > 0:
		return true, c.Send(map[string]json.RawMessage{"pong": hb.Ping})
	case hb.Op == "ping":
		return true, c.Send(map[string]any{"op": "pong", "ts": hb.TS})
	}
	return false, nil
}

func (c *WSClient) dispatch(data []byte) {
	defer safego.Recover(c.logger, "htx_ws_handler")
	if c.handler != nil {
		c.handler(data)
	}
}

// Send 线程安全地写入一条 JSON 消息。
func (c *WSClient) Send(v any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return fmt.Errorf("htx ws marshal: %w", err)
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.conn == nil {
		return errors.New("htx ws: not connected")
	}
	_ = c.conn.SetWriteDeadline(time.Now().Add(wsWriteTimeout))
	return c.conn.WriteMessage(websocket.TextMessage, b)
}

// Subscribe 记录频道（重连后自动重订阅），已连接时立即发送 {"sub": topic, "id": topic}。
func (c *WSClient) Subscribe(topic string) error {
	c.mu.Lock()
	c.topics[topic] = struct{}{}
	connected := c.conn != nil
	c.mu.Unlock()
	if !connected {
		return nil
	}
	return c.Send(map[string]string{"sub": topic, "id": topic})
}

func (c *WSClient) resubscribe() error {
	c.mu.Lock()
	topics := make([]string, 0, len(c.topics))
	for t := range c.topics {
		topics = append(topics, t)
	}
	c.mu.Unlock()
	for _, t := range topics {
		if err := c.Send(map[string]string{"sub": t, "id": t}); err != nil {
			return err
		}
	}
	return nil
}

// WSAuthParams 生成私有 WS 鉴权所需的签名参数（GET + host + path 签名）。
// TODO: 鉴权消息的外层格式（合约 {"op":"auth","type":"api",...} / 现货 v2 {"action":"req","ch":"auth",...}）
// 以 docs/htx_api/ 中的官方示例为准后再封装。
func WSAuthParams(cred *Credentials, wsURL string, now time.Time) (url.Values, error) {
	u, err := url.Parse(wsURL)
	if err != nil {
		return nil, fmt.Errorf("htx ws auth: parse url: %w", err)
	}
	return SignParams(cred, "GET", u.Host, u.Path, url.Values{}, now), nil
}

func gunzip(b []byte) ([]byte, error) {
	r, err := gzip.NewReader(bytes.NewReader(b))
	if err != nil {
		return nil, err
	}
	defer r.Close()
	return io.ReadAll(io.LimitReader(r, wsMaxMsgBytes))
}

func errString(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}
