package storage

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// Migrate 按文件名顺序应用 dir 下尚未执行的 *.up.sql，版本记录在 schema_migrations。
// db 的 DSN 必须开启 multiStatements=true。
// 注意：MySQL DDL 会隐式提交，单个文件中途失败时需人工处理。
func Migrate(ctx context.Context, db *sql.DB, dir string, logger *slog.Logger) error {
	if logger == nil {
		logger = slog.Default()
	}
	if _, err := db.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS schema_migrations (
		version    VARCHAR(128) NOT NULL PRIMARY KEY,
		applied_at DATETIME(6)  NOT NULL DEFAULT CURRENT_TIMESTAMP(6)
	) ENGINE = InnoDB DEFAULT CHARSET = utf8mb4`); err != nil {
		return fmt.Errorf("migrate: create schema_migrations: %w", err)
	}

	files, err := filepath.Glob(filepath.Join(dir, "*.up.sql"))
	if err != nil {
		return fmt.Errorf("migrate: list %s: %w", dir, err)
	}
	sort.Strings(files)

	for _, f := range files {
		version := strings.TrimSuffix(filepath.Base(f), ".up.sql")
		var n int
		if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM schema_migrations WHERE version = ?`, version).Scan(&n); err != nil {
			return fmt.Errorf("migrate: check %s: %w", version, err)
		}
		if n > 0 {
			continue
		}
		body, err := os.ReadFile(f)
		if err != nil {
			return fmt.Errorf("migrate: read %s: %w", f, err)
		}
		if _, err := db.ExecContext(ctx, string(body)); err != nil {
			return fmt.Errorf("migrate: apply %s: %w", version, err)
		}
		if _, err := db.ExecContext(ctx, `INSERT INTO schema_migrations (version) VALUES (?)`, version); err != nil {
			return fmt.Errorf("migrate: record %s: %w", version, err)
		}
		logger.Info("migration applied", slog.String("version", version))
	}
	return nil
}
