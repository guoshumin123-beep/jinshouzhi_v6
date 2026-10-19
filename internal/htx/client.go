// Package htx 是火币 (HTX) 的轻量级 HTTP / WebSocket 客户端。
//
// 设计约束：
//   - Client 不持有任何默认凭证。每个私有请求都必须显式传入 *Credentials，
//     从结构上杜绝 Standard 用户误用母账户下单。
//   - 私有请求按凭证所属 UID 限频（见 RateLimiter），并依据响应头主动退避。
//   - U 本位联合保证金 V5 接口见 v5.go，依据 docs/htx_api/U本位联合保证金API文档 实现。
package htx

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const (
	SpotBaseURL = "https://api.huobi.pro"
	SwapBaseURL = "https://api.hbdm.com"

	maxResponseBytes = 8 << 20
)

// Client 绑定单一域名（现货或合约）的 HTTP 客户端，可被多个 goroutine 并发使用。
type Client struct {
	baseURL *url.URL
	http    *http.Client
	limiter *RateLimiter
	logger  *slog.Logger
	now     func() time.Time

	channelCode string // 合约下单 Broker ID，见 WithChannelCode
}

type Option func(*Client)

func WithHTTPClient(h *http.Client) Option { return func(c *Client) { c.http = h } }
func WithLogger(l *slog.Logger) Option     { return func(c *Client) { c.logger = l } }

// WithChannelCode 设置合约下单的 Broker ID（channel_code），注入每一笔 SwapPlaceOrder 请求的 JSON body。
// 现货的 Broker ID 通过 client-order-id 前缀传递，逻辑不同，不使用此选项。
func WithChannelCode(code string) Option { return func(c *Client) { c.channelCode = code } }

// WithRateLimiter 替换限频器。同一进程内访问同一域名的多个 Client 应共享一个限频器。
func WithRateLimiter(l *RateLimiter) Option { return func(c *Client) { c.limiter = l } }

// withClock 仅供测试固定时间戳。
func withClock(now func() time.Time) Option { return func(c *Client) { c.now = now } }

func NewClient(baseURL string, opts ...Option) (*Client, error) {
	u, err := url.Parse(baseURL)
	if err != nil || u.Scheme == "" || u.Host == "" {
		return nil, fmt.Errorf("htx: invalid base url %q: %v", baseURL, err)
	}
	c := &Client{
		baseURL: u,
		http: &http.Client{
			Timeout: 10 * time.Second,
			Transport: &http.Transport{
				Proxy:               http.ProxyFromEnvironment,
				DialContext:         (&net.Dialer{Timeout: 5 * time.Second, KeepAlive: 30 * time.Second}).DialContext,
				MaxIdleConns:        256,
				MaxIdleConnsPerHost: 128,
				IdleConnTimeout:     90 * time.Second,
				TLSHandshakeTimeout: 5 * time.Second,
				ForceAttemptHTTP2:   true,
			},
		},
		limiter: DefaultRateLimiter(),
		logger:  slog.Default(),
		now:     time.Now,
	}
	for _, o := range opts {
		o(c)
	}
	c.logger = c.logger.With(slog.String("component", "htx"), slog.String("host", u.Host))
	return c, nil
}

// APIError HTX 返回的业务错误。
type APIError struct {
	HTTPStatus int
	Status     string
	Code       string
	Message    string
}

func (e *APIError) Error() string {
	return fmt.Sprintf("htx api error: http=%d status=%s code=%s msg=%s", e.HTTPStatus, e.Status, e.Code, e.Message)
}

// envelope 兼容现货 (err-code/err-msg)、合约 (err_code/err_msg) 与 v2 (code/message) 三种返回格式。
type envelope struct {
	Status   string          `json:"status"`
	ErrCode1 json.RawMessage `json:"err-code"`
	ErrMsg1  string          `json:"err-msg"`
	ErrCode2 json.RawMessage `json:"err_code"`
	ErrMsg2  string          `json:"err_msg"`
	Code     *int            `json:"code"`
	Message  string          `json:"message"`
	Msg      string          `json:"msg"` // 个别 V5 接口（如查询资产模式）使用 msg
	Data     json.RawMessage `json:"data"`
	NextID   *int64          `json:"nextId"` // 分页接口（如获取子用户列表）
}

func (e *envelope) toError(httpStatus int) *APIError {
	switch {
	case strings.EqualFold(e.Status, "ok"):
		return nil
	case strings.EqualFold(e.Status, "error"):
		code, msg := rawString(e.ErrCode1), e.ErrMsg1
		if code == "" {
			code, msg = rawString(e.ErrCode2), e.ErrMsg2
		}
		return &APIError{HTTPStatus: httpStatus, Status: e.Status, Code: code, Message: msg}
	case e.Code != nil && *e.Code != 200:
		msg := e.Message
		if msg == "" {
			msg = e.Msg
		}
		return &APIError{HTTPStatus: httpStatus, Code: fmt.Sprint(*e.Code), Message: msg}
	case httpStatus >= 400:
		return &APIError{HTTPStatus: httpStatus, Status: e.Status, Message: e.Message}
	}
	return nil
}

func rawString(r json.RawMessage) string {
	return strings.Trim(string(r), `"`)
}

// Get 发起 GET 请求。cred 为 nil 表示公共接口（不签名、不占用私有限频）。out 接收 data 字段。
func (c *Client) Get(ctx context.Context, path string, cred *Credentials, query url.Values, out any) error {
	return c.Do(ctx, http.MethodGet, path, BucketQuery, cred, query, nil, out)
}

// Post 发起 POST 请求，body 以 JSON 发送。签名参数位于 URL query 中，body 不参与签名。
func (c *Client) Post(ctx context.Context, path string, cred *Credentials, body any, out any) error {
	return c.Do(ctx, http.MethodPost, path, BucketTrade, cred, nil, body, out)
}

// Do 通用请求封装。私有请求（cred 非空）先在 bucket 上等待限频额度，再签名发送，并据响应头退避。
//
// 签名规则（《签名说明》）：GET 请求的业务参数参与签名；POST 请求只有
// AccessKeyId/SignatureMethod/SignatureVersion/Timestamp 参与签名，业务参数放在 JSON body。
func (c *Client) Do(ctx context.Context, method, path string, bucket Bucket, cred *Credentials, query url.Values, body any, out any) error {
	_, err := c.do(ctx, method, path, bucket, cred, query, body, out)
	return err
}

// do 同 Do，额外返回响应信封（用于读取 nextId 等 data 之外的字段）。
func (c *Client) do(ctx context.Context, method, path string, bucket Bucket, cred *Credentials, query url.Values, body any, out any) (*envelope, error) {
	if query == nil {
		query = url.Values{}
	}
	// 《签名说明》：POST 请求只有四个公共认证参数参与签名，业务参数必须放在 JSON body。
	// 从结构上禁止 POST 携带 query 业务参数，避免业务参数被误签入签名。
	if method == http.MethodPost && len(query) > 0 {
		return nil, fmt.Errorf("htx %s %s: %w: POST business params must be in JSON body, not query", method, path, ErrInvalidRequest)
	}
	if cred != nil {
		if err := c.limiter.Wait(ctx, cred.rateKey(), bucket); err != nil {
			return nil, fmt.Errorf("htx %s %s: rate limit wait: %w", method, path, err)
		}
		if lim, ok := endpointLimitFor(path); ok {
			if err := c.limiter.WaitEndpoint(ctx, cred.rateKey()+"|"+lim.name, lim.n, lim.window); err != nil {
				return nil, fmt.Errorf("htx %s %s: endpoint rate limit wait: %w", method, path, err)
			}
		}
		// 签名时间戳在拿到额度之后生成，避免排队过久导致时间戳过期（有效期 5 分钟）
		query = SignParams(cred, method, c.baseURL.Host, path, query, c.now())
	}

	u := *c.baseURL
	u.Path = path
	u.RawQuery = EncodeQuery(query)

	var reqBody io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return nil, fmt.Errorf("htx %s %s: marshal body: %w", method, path, err)
		}
		reqBody = bytes.NewReader(b)
	}

	req, err := http.NewRequestWithContext(ctx, method, u.String(), reqBody)
	if err != nil {
		return nil, fmt.Errorf("htx %s %s: build request: %w", method, path, err)
	}
	req.Header.Set("Accept", "application/json")
	if method == http.MethodPost {
		req.Header.Set("Content-Type", "application/json")
	}

	start := time.Now()
	resp, err := c.http.Do(req)
	if err != nil {
		// 注意：不要记录 u.String()，其中包含 AccessKeyId 与 Signature
		return nil, &TransportError{Op: method + " " + path, Err: err}
	}
	defer resp.Body.Close()
	if cred != nil {
		c.limiter.Observe(cred.rateKey(), bucket, resp.Header)
	}

	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes))
	if err != nil {
		return nil, &TransportError{Op: method + " " + path, Err: fmt.Errorf("read body: %w", err)}
	}
	c.logger.Debug("htx request",
		slog.String("method", method), slog.String("path", path), slog.String("bucket", bucket.String()),
		slog.Int("http_status", resp.StatusCode), slog.Duration("latency", time.Since(start)),
		slog.String("ratelimit_remaining", resp.Header.Get("ratelimit-remaining")))

	var env envelope
	if err := json.Unmarshal(raw, &env); err != nil {
		if resp.StatusCode >= 500 {
			return nil, fmt.Errorf("htx %s %s: %w", method, path, &APIError{HTTPStatus: resp.StatusCode})
		}
		return nil, fmt.Errorf("htx %s %s: decode response (http %d): %w", method, path, resp.StatusCode, err)
	}
	if apiErr := env.toError(resp.StatusCode); apiErr != nil {
		return &env, fmt.Errorf("htx %s %s: %w", method, path, apiErr)
	}
	if out == nil || len(env.Data) == 0 || string(env.Data) == "null" {
		return &env, nil
	}
	if err := json.Unmarshal(env.Data, out); err != nil {
		return &env, fmt.Errorf("htx %s %s: decode data: %w", method, path, err)
	}
	return &env, nil
}

// TransportError 网络层错误（连接失败、超时、读取响应失败）。
// 对下单这类非幂等请求，出现该错误时订单是否已被火币受理是未知的，
// 调用方必须先按 client_order_id 查询订单再决定是否重发。
type TransportError struct {
	Op  string
	Err error
}

func (e *TransportError) Error() string { return "htx " + e.Op + ": " + e.Err.Error() }
func (e *TransportError) Unwrap() error { return e.Err }

// 《错误码》中表示服务端暂时性故障、可稍后重试的错误码。
var retryableCodes = map[string]bool{
	"500":  true, // 系统错误
	"6021": true, // 接口超时，请稍后重试
}

// IsRetryable 报告错误是否为暂时性故障（网络错误、HTTP 5xx、系统错误/接口超时错误码）。
// 业务拒绝（参数不合法、余额不足、订单不存在等）返回 false。ctx 取消/超时返回 false。
func IsRetryable(err error) bool {
	if err == nil || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return false
	}
	var te *TransportError
	if errors.As(err, &te) {
		return true
	}
	if e, ok := IsAPIError(err); ok {
		return e.HTTPStatus >= 500 || retryableCodes[e.Code]
	}
	return false
}

// IsAPIError 判断 err 是否为 HTX 业务错误，并返回之。
func IsAPIError(err error) (*APIError, bool) {
	var e *APIError
	ok := errors.As(err, &e)
	return e, ok
}
