package htx

import (
	"context"
	"net/http"
	"strconv"
	"sync"
	"time"
)

// Bucket 火币私有接口的限频类别（文档“接口权限”：交易 / 读取）。
type Bucket int

const (
	BucketQuery Bucket = iota // 读取
	BucketTrade               // 交易
)

func (b Bucket) String() string {
	if b == BucketTrade {
		return "trade"
	}
	return "query"
}

// 文档《访问次数限制》：每个 UID 3 秒最多 144 次私有请求，其中交易接口 72 次、查询接口 72 次；
// 该 UID 下所有合约共享额度。
const (
	DefaultRateWindow = 3 * time.Second
	DefaultTradeLimit = 72
	DefaultQueryLimit = 72
	// DefaultRateGuard 窗口安全余量：火币按请求到达服务端的时间计算限频，而客户端排队时刻与到达时刻之间
	// 存在调度与网络抖动。客户端按 窗口+余量 计算额度，避免抖动导致服务端判定超频。
	DefaultRateGuard = 150 * time.Millisecond
)

// RateLimiter 按 UID 隔离的滑动窗口限频器（交易/查询分桶）。
// 另外根据响应头主动退避：ratelimit-remaining 为 0 时等待到 ratelimit-reset；
// 出现 recovery-time（撤单率限制禁用）时，该 UID 全部请求暂停到恢复时间。
type RateLimiter struct {
	window time.Duration
	guard  time.Duration
	limits [2]int
	now    func() time.Time

	mu        sync.Mutex
	users     map[string]*uidState
	endpoints map[string][]time.Time // 按接口单独限频（母账户接口等），键为 rateKey|接口名
}

type uidState struct {
	hits         [2][]time.Time // 各桶窗口内的请求时间（升序）
	blockedUntil [2]time.Time
}

func NewRateLimiter(window time.Duration, tradeLimit, queryLimit int) *RateLimiter {
	l := &RateLimiter{window: window, guard: DefaultRateGuard, now: time.Now, users: map[string]*uidState{},
		endpoints: map[string][]time.Time{}}
	l.limits[BucketTrade], l.limits[BucketQuery] = tradeLimit, queryLimit
	return l
}

// WithGuard 设置窗口安全余量（见 DefaultRateGuard），返回自身便于链式调用。
func (l *RateLimiter) WithGuard(d time.Duration) *RateLimiter {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.guard = d
	return l
}

func DefaultRateLimiter() *RateLimiter {
	return NewRateLimiter(DefaultRateWindow, DefaultTradeLimit, DefaultQueryLimit)
}

// Wait 阻塞直到 key 的 b 桶有额度，并占用一次额度；ctx 取消时返回 ctx.Err()。
func (l *RateLimiter) Wait(ctx context.Context, key string, b Bucket) error {
	for {
		delay := l.reserve(key, b)
		if delay <= 0 {
			return nil
		}
		t := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			t.Stop()
			return ctx.Err()
		case <-t.C:
		}
	}
}

// reserve 有额度则占用并返回 0，否则返回需要等待的时长。
func (l *RateLimiter) reserve(key string, b Bucket) time.Duration {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.now()
	st := l.users[key]
	if st == nil {
		st = &uidState{}
		l.users[key] = st
	}
	if until := st.blockedUntil[b]; now.Before(until) {
		return until.Sub(now)
	}
	span := l.window + l.guard
	hits := st.hits[b]
	cut := 0
	for cut < len(hits) && !hits[cut].After(now.Add(-span)) {
		cut++
	}
	hits = hits[cut:]
	if len(hits) >= l.limits[b] {
		st.hits[b] = hits
		return hits[0].Add(span).Sub(now)
	}
	st.hits[b] = append(hits, now)
	return 0
}

// Observe 依据响应头调整退避。
func (l *RateLimiter) Observe(key string, b Bucket, h http.Header) {
	now := l.now()
	var until [2]time.Time
	if ms, ok := headerInt(h, "recovery-time"); ok {
		// 撤单率限制禁用：恢复时间戳（毫秒），对该 UID 全部请求生效
		t := time.UnixMilli(ms)
		until[BucketTrade], until[BucketQuery] = t, t
	}
	if rem, ok := headerInt(h, "ratelimit-remaining"); ok && rem <= 0 {
		if reset, ok := headerInt(h, "ratelimit-reset"); ok {
			// 文档未明确 ratelimit-reset 是时间戳还是时长（单位均为毫秒），按量级区分
			t := now.Add(time.Duration(reset) * time.Millisecond)
			if reset > 1e12 {
				t = time.UnixMilli(reset)
			}
			if t.After(until[b]) {
				until[b] = t
			}
		}
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	st := l.users[key]
	if st == nil {
		st = &uidState{}
		l.users[key] = st
	}
	for i := range until {
		if until[i].After(st.blockedUntil[i]) {
			st.blockedUntil[i] = until[i]
		}
	}
}

func headerInt(h http.Header, k string) (int64, bool) {
	v := h.Get(k)
	if v == "" {
		return 0, false
	}
	n, err := strconv.ParseInt(v, 10, 64)
	return n, err == nil
}

// WaitEndpoint 按接口单独限频：key 在 window（加安全余量）内最多 n 次。
func (l *RateLimiter) WaitEndpoint(ctx context.Context, key string, n int, window time.Duration) error {
	for {
		delay := l.reserveEndpoint(key, n, window)
		if delay <= 0 {
			return nil
		}
		t := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			t.Stop()
			return ctx.Err()
		case <-t.C:
		}
	}
}

func (l *RateLimiter) reserveEndpoint(key string, n int, window time.Duration) time.Duration {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.now()
	span := window + l.guard
	hits := l.endpoints[key]
	cut := 0
	for cut < len(hits) && !hits[cut].After(now.Add(-span)) {
		cut++
	}
	hits = hits[cut:]
	if len(hits) >= n {
		l.endpoints[key] = hits
		return hits[0].Add(span).Sub(now)
	}
	l.endpoints[key] = append(hits, now)
	return 0
}

type endpointLimit struct {
	name   string
	n      int
	window time.Duration
}

// 子用户管理文档中单独声明限频的接口（按调用方 UID 计）。
var endpointLimits = []struct {
	prefix string
	limit  endpointLimit
}{
	{"/v1/subuser/transfer", endpointLimit{"subuser_transfer", 2, 2 * time.Second}},
	{"/v1/subuser/aggregate-balance", endpointLimit{"subuser_aggregate_balance", 2, 2 * time.Second}},
	{"/v1/account/accounts/", endpointLimit{"subuser_balance", 20, 2 * time.Second}},
	{"/v2/sub-user/management", endpointLimit{"subuser_management", 20, 2 * time.Second}},
}

func endpointLimitFor(path string) (endpointLimit, bool) {
	for _, e := range endpointLimits {
		if path == e.prefix || (e.prefix[len(e.prefix)-1] == '/' && len(path) > len(e.prefix) && path[:len(e.prefix)] == e.prefix) {
			return e.limit, true
		}
	}
	return endpointLimit{}, false
}
