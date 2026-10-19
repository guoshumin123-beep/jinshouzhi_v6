package subaccount

import (
	"context"
	"database/sql"
	"fmt"
	"sync"
	"time"

	"backend_v2/internal/htx"
	"backend_v2/internal/storage"
	"backend_v2/internal/vault"
)

// DBKeyProvider 从 subaccount_api_keys 读取密文并经 vault 解密，带短 TTL 内存缓存。
// 仅 READY 状态的账户返回凭证：LOCKED → ErrSubAccountLocked；PROVISIONING/FAILED → ErrSubAccountNotReady。
type DBKeyProvider struct {
	db    *sql.DB
	vault *vault.Vault
	ttl   time.Duration
	now   func() time.Time
	cache sync.Map // userID -> cachedCred
}

type cachedCred struct {
	cred    *htx.Credentials
	expires time.Time
}

var _ KeyProvider = (*DBKeyProvider)(nil)

func NewDBKeyProvider(db *sql.DB, v *vault.Vault, ttl time.Duration) *DBKeyProvider {
	if ttl <= 0 {
		ttl = time.Minute
	}
	return &DBKeyProvider{db: db, vault: v, ttl: ttl, now: time.Now}
}

func (p *DBKeyProvider) Get(ctx context.Context, userID int64) (*htx.Credentials, error) {
	if v, ok := p.cache.Load(userID); ok {
		if c := v.(cachedCred); p.now().Before(c.expires) {
			return c.cred, nil
		}
		p.cache.Delete(userID)
	}
	a, err := GetAccount(ctx, p.db, userID)
	if err != nil {
		return nil, err
	}
	switch {
	case a == nil:
		return nil, ErrNoSubAccount
	case a.State == StateLocked:
		return nil, ErrSubAccountLocked
	case a.State != StateReady:
		return nil, fmt.Errorf("%w: state %s step %s", ErrSubAccountNotReady, a.State, a.Step)
	}
	cred, err := loadCredentials(ctx, p.db, p.vault, userID)
	if err != nil {
		return nil, err
	}
	p.cache.Store(userID, cachedCred{cred: cred, expires: p.now().Add(p.ttl)})
	return cred, nil
}

func (p *DBKeyProvider) Invalidate(userID int64) { p.cache.Delete(userID) }

// loadCredentials 解密用户 ACTIVE Key（不检查账户状态）。明文 Secret 仅存在于返回的凭证对象中。
func loadCredentials(ctx context.Context, q storage.Querier, v *vault.Vault, userID int64) (*htx.Credentials, error) {
	k, err := activeKey(ctx, q, userID)
	if err != nil {
		return nil, err
	}
	if k == nil {
		return nil, fmt.Errorf("%w: no active api key", ErrSubAccountNotReady)
	}
	secret, err := v.Open(k.Sealed, secretAAD(userID, k.AccessKey))
	if err != nil {
		return nil, fmt.Errorf("decrypt api key for user %d: %w", userID, err)
	}
	defer vault.Wipe(secret)
	cred, err := htx.NewCredentials(k.AccessKey, string(secret))
	if err != nil {
		return nil, err
	}
	cred.UID = k.SubUID
	return cred, nil
}
