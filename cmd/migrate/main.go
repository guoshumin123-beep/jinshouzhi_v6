// Command migrate 将 migrations/ 应用到 MYSQL_DSN 指定的数据库。
//
//	MYSQL_DSN='root:<password>@tcp(127.0.0.1:3306)/jinshouzhi_v6?charset=utf8mb4&parseTime=True&loc=Local' \
//	  go run ./cmd/migrate -create-db
package main

import (
	"context"
	"database/sql"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"time"

	"github.com/go-sql-driver/mysql"

	"backend_v2/internal/storage"
)

func main() {
	dir := flag.String("dir", "migrations", "migrations directory")
	createDB := flag.Bool("create-db", false, "CREATE DATABASE IF NOT EXISTS before migrating")
	flag.Parse()

	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	if err := run(*dir, *createDB, logger); err != nil {
		logger.Error("migrate failed", slog.String("error", err.Error()))
		os.Exit(1)
	}
}

func run(dir string, createDB bool, logger *slog.Logger) error {
	cfg, err := mysql.ParseDSN(os.Getenv("MYSQL_DSN"))
	if err != nil {
		return fmt.Errorf("parse MYSQL_DSN: %w", err)
	}
	if cfg.DBName == "" {
		return fmt.Errorf("MYSQL_DSN must include a database name")
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()

	if createDB {
		server := cfg.Clone()
		server.DBName = ""
		admin, err := sql.Open("mysql", server.FormatDSN())
		if err != nil {
			return err
		}
		defer admin.Close()
		if _, err := admin.ExecContext(ctx, "CREATE DATABASE IF NOT EXISTS `"+cfg.DBName+"` CHARACTER SET utf8mb4 COLLATE utf8mb4_bin"); err != nil {
			return fmt.Errorf("create database %s: %v", cfg.DBName, err)
		}
		logger.Info("database ensured", slog.String("database", cfg.DBName))
	}

	cfg.MultiStatements = true
	db, err := sql.Open("mysql", cfg.FormatDSN())
	if err != nil {
		return err
	}
	defer db.Close()
	if err := storage.Migrate(ctx, db, dir, logger); err != nil {
		return err
	}
	logger.Info("migrations up to date", slog.String("database", cfg.DBName))
	return nil
}
