// Package safego 提供带 recover 的 goroutine 启动器。
// 系统内所有后台 goroutine 必须经由 Go 启动，严防 panic 导致进程退出。
package safego

import (
	"log/slog"
	"runtime/debug"
)

// Go 在新 goroutine 中执行 fn，panic 会被捕获并记录。
func Go(logger *slog.Logger, name string, fn func()) {
	go func() {
		defer Recover(logger, name)
		fn()
	}()
}

// Recover 用于 defer，捕获 panic 并输出带堆栈的结构化日志。
func Recover(logger *slog.Logger, name string) {
	if r := recover(); r != nil {
		if logger == nil {
			logger = slog.Default()
		}
		logger.Error("goroutine panic recovered",
			slog.String("goroutine", name),
			slog.Any("panic", r),
			slog.String("stack", string(debug.Stack())),
		)
	}
}
