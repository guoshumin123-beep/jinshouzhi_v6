// Package middleware 提供 HTTP 公共中间件：panic 兜底与 JWT 鉴权。
package middleware

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"runtime/debug"
	"strconv"
	"strings"
	"time"

	"github.com/golang-jwt/jwt/v5"

	"backend_v2/api/httpx"
)

// Recover 捕获 Handler 内的 panic，返回 500 并记录堆栈，进程不退出。
func Recover(logger *slog.Logger, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if p := recover(); p != nil {
				logger.Error("http handler panic recovered",
					slog.String("method", r.Method), slog.String("path", r.URL.Path),
					slog.Any("panic", p), slog.String("stack", string(debug.Stack())))
				httpx.Error(w, http.StatusInternalServerError, "INTERNAL", "internal error")
			}
		}()
		next.ServeHTTP(w, r)
	})
}

// Role 令牌角色。Admin 与 User 使用不同的签名密钥与不同的路由组。
type Role string

const (
	RoleUser  Role = "user"
	RoleAdmin Role = "admin"
)

// Principal 鉴权通过的调用方。
type Principal struct {
	UserID  int64  // RoleUser：用户 ID
	Subject string // RoleAdmin：操作人标识
	Role    Role
}

type ctxKey struct{}

func PrincipalFrom(ctx context.Context) (Principal, bool) {
	p, ok := ctx.Value(ctxKey{}).(Principal)
	return p, ok
}

// Claims JWT 载荷：sub 为用户 ID（user）或操作人（admin），role 为角色，exp 必填。
type Claims struct {
	Role Role `json:"role"`
	jwt.RegisteredClaims
}

// IssueToken 签发 HS256 令牌（供登录服务与测试使用）。
func IssueToken(secret []byte, subject string, role Role, ttl time.Duration) (string, error) {
	now := time.Now()
	return jwt.NewWithClaims(jwt.SigningMethodHS256, Claims{
		Role: role,
		RegisteredClaims: jwt.RegisteredClaims{
			Subject: subject, IssuedAt: jwt.NewNumericDate(now), ExpiresAt: jwt.NewNumericDate(now.Add(ttl)),
		},
	}).SignedString(secret)
}

// Auth 校验 Bearer 令牌：仅接受 HS256、必须未过期、角色必须为 want。
func Auth(secret []byte, want Role, next http.Handler) http.Handler {
	parser := jwt.NewParser(jwt.WithValidMethods([]string{jwt.SigningMethodHS256.Alg()}), jwt.WithExpirationRequired())
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
		if !ok || raw == "" {
			httpx.Error(w, http.StatusUnauthorized, "UNAUTHORIZED", "missing bearer token")
			return
		}
		var c Claims
		if _, err := parser.ParseWithClaims(raw, &c, func(*jwt.Token) (any, error) { return secret, nil }); err != nil {
			httpx.Error(w, http.StatusUnauthorized, "UNAUTHORIZED", "invalid token")
			return
		}
		if c.Role != want {
			httpx.Error(w, http.StatusForbidden, "FORBIDDEN", "role not allowed")
			return
		}
		p, err := principal(c)
		if err != nil {
			httpx.Error(w, http.StatusUnauthorized, "UNAUTHORIZED", "invalid subject")
			return
		}
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), ctxKey{}, p)))
	})
}

func principal(c Claims) (Principal, error) {
	p := Principal{Subject: c.Subject, Role: c.Role}
	if c.Subject == "" {
		return p, errors.New("empty subject")
	}
	if c.Role == RoleUser {
		id, err := strconv.ParseInt(c.Subject, 10, 64)
		if err != nil || id <= 0 {
			return p, fmt.Errorf("user subject %q: %w", c.Subject, err)
		}
		p.UserID = id
	}
	return p, nil
}

// Middleware HTTP 中间件。
type Middleware func(http.Handler) http.Handler

// Chain 按书写顺序由外到内包裹：Chain(h, a, b) == a(b(h))。
func Chain(h http.Handler, mws ...Middleware) http.Handler {
	for i := len(mws) - 1; i >= 0; i-- {
		h = mws[i](h)
	}
	return h
}

// Limiter 接口限流器（预留）。Allow 返回 false 时请求被拒绝（429）。
// 位于 JWT 之后，可按 PrincipalFrom(r.Context()) 的用户 ID 或角色限流。
type Limiter interface {
	Allow(r *http.Request) bool
}

// NoopLimiter 默认放行（API 限流中间件占位，后续替换为真实实现）。
type NoopLimiter struct{}

func (NoopLimiter) Allow(*http.Request) bool { return true }

// RateLimit 限流中间件。l 为 nil 时等同 NoopLimiter。
func RateLimit(l Limiter) Middleware {
	if l == nil {
		l = NoopLimiter{}
	}
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if !l.Allow(r) {
				httpx.Error(w, http.StatusTooManyRequests, "RATE_LIMITED", "too many requests")
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

// AuthMW Auth 的 Middleware 形式。
func AuthMW(secret []byte, want Role) Middleware {
	return func(next http.Handler) http.Handler { return Auth(secret, want, next) }
}

// Server 组装一个 API 服务：Recover → [/health 免鉴权] → JWT → 限流 → 业务路由。
func Server(logger *slog.Logger, health http.Handler, secret []byte, role Role, limiter Limiter, routes http.Handler) http.Handler {
	top := http.NewServeMux()
	if health != nil {
		top.Handle("GET /health", health)
	}
	top.Handle("/", Chain(routes, AuthMW(secret, role), RateLimit(limiter)))
	return Recover(logger, top)
}
