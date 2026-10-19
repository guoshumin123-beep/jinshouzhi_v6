package htx

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"sync"
	"testing"
	"time"
)

type fakeClock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *fakeClock) now() time.Time { c.mu.Lock(); defer c.mu.Unlock(); return c.t }
func (c *fakeClock) add(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(d)
}

func newTestLimiter() (*RateLimiter, *fakeClock) {
	clk := &fakeClock{t: time.Unix(1_700_000_000, 0)}
	l := DefaultRateLimiter()
	l.now = clk.now
	return l, clk
}

func TestRateLimiterDocumentedQuotas(t *testing.T) {
	l, clk := newTestLimiter()
	for i := 0; i < DefaultTradeLimit; i++ {
		if d := l.reserve("uid:1", BucketTrade); d != 0 {
			t.Fatalf("trade request %d throttled (%s)", i+1, d)
		}
	}
	if d := l.reserve("uid:1", BucketTrade); d != DefaultRateWindow+DefaultRateGuard {
		t.Fatalf("73rd trade request: wait %s, want %s", d, DefaultRateWindow+DefaultRateGuard)
	}
	// 查询桶独立：合计可达 144
	for i := 0; i < DefaultQueryLimit; i++ {
		if d := l.reserve("uid:1", BucketQuery); d != 0 {
			t.Fatalf("query request %d throttled", i+1)
		}
	}
	if d := l.reserve("uid:1", BucketQuery); d <= 0 {
		t.Fatal("73rd query request not throttled")
	}
	// 其它 UID 不受影响
	if d := l.reserve("uid:2", BucketTrade); d != 0 {
		t.Fatal("different UID throttled")
	}
	// 滑动窗口：窗口 + 安全余量之后额度恢复
	clk.add(DefaultRateWindow)
	if d := l.reserve("uid:1", BucketTrade); d != DefaultRateGuard {
		t.Fatalf("within guard margin: wait %s, want %s", d, DefaultRateGuard)
	}
	clk.add(DefaultRateGuard)
	if d := l.reserve("uid:1", BucketTrade); d != 0 {
		t.Fatalf("after window still throttled: %s", d)
	}
}

func TestRateLimiterObservesHeaders(t *testing.T) {
	l, clk := newTestLimiter()
	h := http.Header{}
	h.Set("ratelimit-remaining", "0")
	h.Set("ratelimit-reset", "1500") // 时长（毫秒）
	l.Observe("uid:1", BucketQuery, h)
	if d := l.reserve("uid:1", BucketQuery); d != 1500*time.Millisecond {
		t.Fatalf("query wait = %s", d)
	}
	if d := l.reserve("uid:1", BucketTrade); d != 0 {
		t.Fatal("trade bucket must not be blocked by query exhaustion")
	}

	// recovery-time（撤单率限制禁用）：该 UID 全部请求暂停到恢复时间戳
	h2 := http.Header{}
	h2.Set("recovery-time", strconv.FormatInt(clk.now().Add(10*time.Second).UnixMilli(), 10))
	l.Observe("uid:3", BucketTrade, h2)
	for _, b := range []Bucket{BucketTrade, BucketQuery} {
		if d := l.reserve("uid:3", b); d != 10*time.Second {
			t.Fatalf("%s wait = %s", b, d)
		}
	}
	// 时间戳形式的 ratelimit-reset
	h3 := http.Header{}
	h3.Set("ratelimit-remaining", "0")
	h3.Set("ratelimit-reset", strconv.FormatInt(clk.now().Add(2*time.Second).UnixMilli(), 10))
	l.Observe("uid:4", BucketTrade, h3)
	if d := l.reserve("uid:4", BucketTrade); d != 2*time.Second {
		t.Fatalf("timestamp reset wait = %s", d)
	}
}

func TestRateLimiterWaitHonorsContext(t *testing.T) {
	l := NewRateLimiter(time.Hour, 1, 1)
	ctx := context.Background()
	if err := l.Wait(ctx, "k", BucketTrade); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(ctx, 20*time.Millisecond)
	defer cancel()
	if err := l.Wait(ctx, "k", BucketTrade); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("want deadline exceeded, got %v", err)
	}
}
