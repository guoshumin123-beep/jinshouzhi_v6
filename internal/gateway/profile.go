package gateway

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"

	"backend_v2/internal/storage"
	"backend_v2/internal/swap"
)

// DefaultProfile 新用户默认画像：STANDARD（火币真实 DMA 通道）+ 全仓。
var DefaultProfile = swap.Profile{Route: swap.RouteStandard, MarginMode: swap.MarginCross}

// ProfileStore 用户交易画像。route 仅 Admin 可改，且只影响未来新开仓；margin_mode 由用户自选。
type ProfileStore struct {
	db       *sql.DB
	defaults swap.Profile
	logger   *slog.Logger
}

func NewProfileStore(db *sql.DB, defaults swap.Profile, logger *slog.Logger) (*ProfileStore, error) {
	if !defaults.Route.Valid() || !defaults.MarginMode.Valid() {
		return nil, fmt.Errorf("%w: default profile %+v", ErrInvalidRequest, defaults)
	}
	if logger == nil {
		logger = slog.Default()
	}
	return &ProfileStore{db: db, defaults: defaults, logger: logger.With(slog.String("component", "trade_profile"))}, nil
}

// Get 读取画像。q 传入路由事务时，读取发生在用户锁之内。
func (s *ProfileStore) Get(ctx context.Context, q storage.Querier, userID int64) (swap.Profile, error) {
	return swap.GetProfile(ctx, q, userID, s.defaults)
}

// Lookup 在事务外读取画像（只读接口使用）。
func (s *ProfileStore) Lookup(ctx context.Context, userID int64) (swap.Profile, error) {
	return s.Get(ctx, s.db, userID)
}

// SetRoute 仅供 Admin 路由组调用。与下单共用用户锁，保证切换与路由判定严格串行。
func (s *ProfileStore) SetRoute(ctx context.Context, userID int64, r swap.Route, operator string) error {
	if !r.Valid() || userID <= 0 || operator == "" {
		return fmt.Errorf("%w: set route user=%d route=%q operator=%q", ErrInvalidRequest, userID, r, operator)
	}
	var prev swap.Profile
	err := storage.WithTx(ctx, s.db, func(tx *sql.Tx) error {
		if err := swap.LockUser(ctx, tx, userID); err != nil {
			return err
		}
		var err error
		if prev, err = s.Get(ctx, tx, userID); err != nil {
			return err
		}
		return swap.SetProfileRoute(ctx, tx, userID, r, s.defaults.MarginMode, operator)
	})
	if err != nil {
		return err
	}
	s.logger.Info("route profile changed (applies to new positions only)",
		slog.Int64("user_id", userID), slog.String("from", string(prev.Route)), slog.String("to", string(r)),
		slog.String("operator", operator))
	return nil
}

// SetMarginMode 用户设置默认保证金模式（只影响之后未显式指定模式的订单）。
func (s *ProfileStore) SetMarginMode(ctx context.Context, userID int64, m swap.MarginMode) error {
	if !m.Valid() || userID <= 0 {
		return fmt.Errorf("%w: set margin mode user=%d mode=%q", ErrInvalidRequest, userID, m)
	}
	if err := swap.SetProfileMarginMode(ctx, s.db, userID, m, s.defaults.Route); err != nil {
		return err
	}
	s.logger.Info("margin mode preference changed", slog.Int64("user_id", userID), slog.String("margin_mode", string(m)))
	return nil
}
