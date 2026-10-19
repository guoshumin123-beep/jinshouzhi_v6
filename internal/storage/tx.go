// Package storage 提供 MySQL 事务封装与 migration 执行器。
package storage

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/go-sql-driver/mysql"
)

const (
	maxTxAttempts    = 3
	mysqlErrDupEntry = 1062
	mysqlErrDeadlock = 1213
)

// Querier 由 *sql.DB 与 *sql.Tx 共同实现。
type Querier interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

// TxOptions 全系统统一的事务隔离级别。READ COMMITTED 可避免 InnoDB 间隙锁引发的死锁。
var TxOptions = &sql.TxOptions{Isolation: sql.LevelReadCommitted}

// WithTx 在事务中执行 fn：fn 返回 error 或 panic 时回滚（panic 会继续上抛给 recover 中间件），
// 否则提交。遇到 InnoDB 死锁时整体重试，因此 fn 必须可重入（不得在 fn 外保留其中间状态）。
func WithTx(ctx context.Context, db *sql.DB, fn func(tx *sql.Tx) error) error {
	var err error
	for attempt := 1; attempt <= maxTxAttempts; attempt++ {
		err = runTx(ctx, db, fn)
		if !IsDeadlock(err) || attempt == maxTxAttempts {
			return err
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(time.Duration(attempt*10) * time.Millisecond):
		}
	}
	return err
}

func runTx(ctx context.Context, db *sql.DB, fn func(tx *sql.Tx) error) (err error) {
	tx, err := db.BeginTx(ctx, TxOptions)
	if err != nil {
		return fmt.Errorf("storage: begin tx: %w", err)
	}
	defer func() {
		if p := recover(); p != nil {
			_ = tx.Rollback()
			panic(p)
		}
		if err != nil {
			_ = tx.Rollback()
		}
	}()
	if err = fn(tx); err != nil {
		return err
	}
	if err = tx.Commit(); err != nil {
		return fmt.Errorf("storage: commit: %w", err)
	}
	return nil
}

func IsDeadlock(err error) bool { return isMySQLErr(err, mysqlErrDeadlock) }
func IsDupEntry(err error) bool { return isMySQLErr(err, mysqlErrDupEntry) }

func isMySQLErr(err error, number uint16) bool {
	var me *mysql.MySQLError
	return errors.As(err, &me) && me.Number == number
}
