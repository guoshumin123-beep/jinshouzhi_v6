// Package storagetest 为集成测试提供隔离的 MySQL 数据库。
package storagetest

import (
	"context"
	"database/sql"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/go-sql-driver/mysql"

	"backend_v2/internal/storage"
)

// EnvDSN 指向开发库的 DSN，例如
//
//	TEST_MYSQL_DSN='root:<password>@tcp(127.0.0.1:3306)/jinshouzhi_v6?charset=utf8mb4&parseTime=True&loc=Local'
//
// 测试永远不会写入该库本身，而是使用派生的 <dbname>_test_<package> 库，并在每次调用时清空重建。
const EnvDSN = "TEST_MYSQL_DSN"

// OpenDB 返回一个已执行全部 migration 的空白测试库。未设置 TEST_MYSQL_DSN 时跳过测试。
func OpenDB(t *testing.T) *sql.DB {
	t.Helper()
	db, _ := open(t)
	return db
}

// OpenDBWithDSN 同 OpenDB，额外返回该测试库的 DSN（供需要自行建立连接的组件使用）。
func OpenDBWithDSN(t *testing.T) (*sql.DB, string) {
	t.Helper()
	return open(t)
}

func open(t *testing.T) (*sql.DB, string) {
	t.Helper()
	raw := os.Getenv(EnvDSN)
	if raw == "" {
		t.Skip(EnvDSN + " not set; skipping MySQL integration test")
	}
	cfg, err := mysql.ParseDSN(raw)
	if err != nil {
		t.Fatalf("parse %s: %v", EnvDSN, err)
	}
	if cfg.DBName == "" {
		t.Fatalf("%s must include a database name", EnvDSN)
	}
	// go test 会并行运行多个包，每个包使用独立的库，避免互相 DROP
	testDB := cfg.DBName + "_test_" + callerPackage()

	server := cfg.Clone()
	server.DBName = ""
	admin, err := sql.Open("mysql", server.FormatDSN())
	if err != nil {
		t.Fatal(err)
	}
	defer admin.Close()
	if _, err := admin.Exec("DROP DATABASE IF EXISTS `" + testDB + "`"); err != nil {
		t.Fatalf("drop test db: %v", err)
	}
	if _, err := admin.Exec("CREATE DATABASE `" + testDB + "` CHARACTER SET utf8mb4 COLLATE utf8mb4_bin"); err != nil {
		t.Fatalf("create test db: %v", err)
	}

	tc := cfg.Clone()
	tc.DBName = testDB
	tc.MultiStatements = true
	db, err := sql.Open("mysql", tc.FormatDSN())
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(32)
	t.Cleanup(func() { db.Close() })

	if err := storage.Migrate(context.Background(), db, MigrationsDir(), QuietLogger()); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	plain := tc.Clone()
	plain.MultiStatements = false
	return db, plain.FormatDSN()
}

// callerPackage 返回调用 OpenDB 的测试文件所在目录名（如 dealer、gateway）。
func callerPackage() string {
	_, file, _, _ := runtime.Caller(3)
	name := strings.Map(func(r rune) rune {
		if r >= 'a' && r <= 'z' || r >= '0' && r <= '9' {
			return r
		}
		return '_'
	}, strings.ToLower(filepath.Base(filepath.Dir(file))))
	return name
}

// MigrationsDir 返回仓库根目录下的 migrations 目录。
func MigrationsDir() string {
	_, file, _, _ := runtime.Caller(0)
	return filepath.Join(filepath.Dir(file), "..", "..", "..", "migrations")
}

func QuietLogger() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }
