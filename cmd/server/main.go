// Command server 启动交易所后端：用户 API（USER_HTTP_ADDR）与 Admin API（ADMIN_HTTP_ADDR，默认仅本机）。
//
//	go run ./cmd/server
//
// 配置全部来自环境变量（见 .env.example）。收到 SIGINT/SIGTERM 时优雅退出：
// 停止接收新连接并等待在途请求 → 停止后台任务 → 关闭数据库。
package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"runtime/debug"
	"sync"
	"syscall"
	"time"

	"backend_v2/internal/app"
	"backend_v2/internal/config"
	"backend_v2/internal/pkg/safego"
)

func main() { os.Exit(run()) }

func run() (code int) {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))
	slog.SetDefault(logger)
	// 全局兜底：主 goroutine 的 panic 记录后以非零码退出
	defer func() {
		if p := recover(); p != nil {
			logger.Error("fatal panic", slog.Any("panic", p), slog.String("stack", string(debug.Stack())))
			code = 1
		}
	}()

	cfg, err := config.Load()
	if err != nil {
		logger.Error("invalid configuration", slog.String("error", err.Error()))
		return 2
	}
	logger.Info("starting", slog.String("config", cfg.String()))

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	a, err := app.New(ctx, cfg, logger)
	if err != nil {
		logger.Error("startup failed", slog.String("error", err.Error()))
		return 1
	}
	defer func() {
		if err := a.Close(); err != nil {
			logger.Error("close app", slog.String("error", err.Error()))
		}
	}()

	servers := []*http.Server{
		newServer("user", cfg.UserAddr, a.UserHandler, logger),
		newServer("admin", cfg.AdminAddr, a.AdminHandler, logger),
	}
	errCh := make(chan error, len(servers))
	for _, s := range servers {
		ln, err := net.Listen("tcp", s.Addr)
		if err != nil {
			logger.Error("listen failed", slog.String("addr", s.Addr), slog.String("error", err.Error()))
			return 1
		}
		s := s
		logger.Info("http server listening", slog.String("addr", ln.Addr().String()))
		safego.Go(logger, "http:"+s.Addr, func() {
			if err := s.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
				errCh <- fmt.Errorf("serve %s: %w", s.Addr, err)
			}
		})
	}

	bgCtx, cancelBG := context.WithCancel(context.Background())
	var bg sync.WaitGroup
	bg.Add(1)
	safego.Go(logger, "background", func() {
		defer bg.Done()
		a.Run(bgCtx)
	})
	logger.Info("ready", slog.Any("background_tasks", a.Tasks()))

	select {
	case <-ctx.Done():
		logger.Info("shutdown signal received")
	case err := <-errCh:
		logger.Error("server error, shutting down", slog.String("error", err.Error()))
		code = 1
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), cfg.ShutdownTimeout)
	defer cancel()
	var wg sync.WaitGroup
	for _, s := range servers {
		s := s
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := s.Shutdown(shutdownCtx); err != nil {
				logger.Error("http shutdown", slog.String("addr", s.Addr), slog.String("error", err.Error()))
			}
		}()
	}
	wg.Wait()
	cancelBG()
	bg.Wait()
	logger.Info("stopped")
	return code
}

func newServer(name, addr string, h http.Handler, logger *slog.Logger) *http.Server {
	return &http.Server{
		Addr:              addr,
		Handler:           h,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       15 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       60 * time.Second,
		MaxHeaderBytes:    1 << 20,
		ErrorLog:          log.New(slogWriter{logger.With(slog.String("server", name))}, "", 0),
	}
}

// slogWriter 将 net/http 内部错误日志转入结构化日志。
type slogWriter struct{ l *slog.Logger }

func (w slogWriter) Write(p []byte) (int, error) {
	w.l.Warn("http server", slog.String("msg", string(p)))
	return len(p), nil
}
