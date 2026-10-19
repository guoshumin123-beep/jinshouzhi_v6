package subaccount

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
)

// ErrProvisioningDisabled 未开启真实开户（HTX_PROVISIONING_ENABLED=false）或未配置母账户/出口 IP。
var ErrProvisioningDisabled = errors.New("subaccount: HTX provisioning is disabled")

// Service HTTP 层使用的门面。prov 为 nil 时开户、冻结、解冻均返回 ErrProvisioningDisabled，查询仍可用。
type Service struct {
	db   *sql.DB
	prov *Provisioner
}

func NewService(db *sql.DB, prov *Provisioner) *Service { return &Service{db: db, prov: prov} }

// Status 查询用户子账户状态；未开户返回 ErrNoSubAccount。
func (s *Service) Status(ctx context.Context, userID int64) (*Account, error) {
	a, err := GetAccount(ctx, s.db, userID)
	if err != nil {
		return nil, err
	}
	if a == nil {
		return nil, ErrNoSubAccount
	}
	return a, nil
}

// Provision 开户或续跑（幂等）。
func (s *Service) Provision(ctx context.Context, userID int64) (*Account, error) {
	if s.prov == nil {
		return nil, ErrProvisioningDisabled
	}
	return s.prov.Provision(ctx, userID)
}

func (s *Service) Lock(ctx context.Context, userID int64, operator, reason string) error {
	if s.prov == nil {
		return ErrProvisioningDisabled
	}
	if reason == "" {
		return fmt.Errorf("%w: lock reason required", ErrInvalidRequest)
	}
	return s.prov.Lock(ctx, userID, operator, reason)
}

func (s *Service) Unlock(ctx context.Context, userID int64, operator string) error {
	if s.prov == nil {
		return ErrProvisioningDisabled
	}
	return s.prov.Unlock(ctx, userID, operator)
}
