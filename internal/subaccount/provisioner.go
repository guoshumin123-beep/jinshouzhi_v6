package subaccount

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"strings"

	"backend_v2/internal/htx"
	"backend_v2/internal/storage"
	"backend_v2/internal/vault"
)

const maxNameAttempts = 5

// Provisioner 子账户自动开户状态机。
//
// 每一步的外部调用均在数据库事务之外执行，进度以短事务落库（受租约保护，多实例安全）。
// 崩溃恢复：
//   - 创建子用户后丢失响应：重试时火币返回用户名重复（2002），按用户名 + 备注找回 UID；
//   - 生成 Key 后丢失响应或落库失败：Secret 不可再取，按备注标签删除孤儿 Key 后重新生成。
//
// 失败处理：
//   - 可重试错误（网络、5xx、系统错误）：保持 PROVISIONING，释放租约，由 ResumePending 续跑；
//   - 终态错误（如需要 OTP、UID 校验不符、参数被拒）：补偿后置为 FAILED——
//     删除火币侧已生成的 Key、本地 Key 置为 REVOKED，状态机回退到生成 Key 之前，绝不留下可用的半成品。
//     火币子用户本身无删除接口，记录保留以便人工重试时复用。
type Provisioner struct {
	db     *sql.DB
	spot   SpotAPI
	swap   SwapAPI
	vault  *vault.Vault
	keys   KeyProvider // 状态变化时失效其缓存（可为 nil）
	cfg    Config
	logger *slog.Logger
}

func NewProvisioner(db *sql.DB, spot SpotAPI, swap SwapAPI, v *vault.Vault, keys KeyProvider, cfg Config, logger *slog.Logger) (*Provisioner, error) {
	if err := cfg.normalize(); err != nil {
		return nil, err
	}
	if v == nil {
		return nil, fmt.Errorf("%w: vault required", ErrInvalidConfig)
	}
	if logger == nil {
		logger = slog.Default()
	}
	return &Provisioner{db: db, spot: spot, swap: swap, vault: v, keys: keys, cfg: cfg,
		logger: logger.With(slog.String("component", "subaccount_provisioner"))}, nil
}

// stepError 标记某一步失败及其是否可重试。
type stepError struct {
	step      Step
	err       error
	retryable bool
}

func (e *stepError) Error() string { return fmt.Sprintf("subaccount step after %s: %v", e.step, e.err) }
func (e *stepError) Unwrap() error { return e.err }

func classify(step Step, err error) *stepError {
	return &stepError{step: step, err: err, retryable: htx.IsRetryable(err) && !errors.Is(err, htx.ErrOTPRequired)}
}

// Provision 为用户开户（幂等、可重入）：已 READY/LOCKED 直接返回；否则从上次中断的步骤继续。
// 对 FAILED 的账户调用即视为人工重试。
func (p *Provisioner) Provision(ctx context.Context, userID int64) (*Account, error) {
	if userID <= 0 {
		return nil, fmt.Errorf("%w: user id", ErrInvalidRequest)
	}
	a, err := p.claim(ctx, userID, p.cfg.Owner+"#"+randToken())
	if err != nil || a.State == StateReady || a.State == StateLocked {
		return a, err
	}
	p.logger.Info("provisioning sub-account", slog.Int64("user_id", userID), slog.String("from_step", string(a.Step)))

	for a.Step != StepReady {
		if err := p.runStep(ctx, a); err != nil {
			return p.fail(ctx, a, err)
		}
		if err := advance(ctx, p.db, a, a.lease, p.cfg.LeaseTTL); err != nil {
			return a, err
		}
		p.logger.Info("sub-account step completed", slog.Int64("user_id", userID), slog.Int64("sub_uid", a.SubUID),
			slog.String("step", string(a.Step)))
	}
	a.State = StateReady
	if err := release(ctx, p.db, a, a.lease, ""); err != nil {
		return a, err
	}
	p.invalidate(userID)
	p.logger.Info("sub-account ready", slog.Int64("user_id", userID), slog.Int64("sub_uid", a.SubUID))
	return a, nil
}

func (p *Provisioner) claim(ctx context.Context, userID int64, token string) (*Account, error) {
	var a *Account
	err := storage.WithTx(ctx, p.db, func(tx *sql.Tx) error {
		var err error
		if a, err = getAccount(ctx, tx, userID, true); err != nil {
			return err
		}
		if a == nil {
			a = &Account{UserID: userID, UserName: p.cfg.userName(userID, 0), SubAccountType: p.cfg.SubAccountType,
				State: StateProvisioning, Step: StepReserved}
			if err := insertAccount(ctx, tx, a); err != nil {
				return err
			}
		}
		if a.State == StateReady || a.State == StateLocked {
			return nil
		}
		busy, err := leaseHeldByOther(ctx, tx, userID, token)
		if err != nil {
			return err
		}
		if busy {
			return ErrProvisionInProgress
		}
		a.State = StateProvisioning
		a.Attempts++
		a.lease = token
		return claimLease(ctx, tx, userID, token, p.cfg.LeaseTTL)
	})
	if errors.Is(err, ErrProvisionInProgress) {
		return a, err
	}
	if storage.IsDupEntry(err) { // 并发首次插入
		return nil, ErrProvisionInProgress
	}
	return a, err
}

// runStep 执行当前步骤的下一步，成功后更新 a.Step（及相关字段）。
func (p *Provisioner) runStep(ctx context.Context, a *Account) error {
	switch a.Step {
	case StepReserved:
		return p.stepCreate(ctx, a)
	case StepCreated:
		if err := p.swap.SwapSubAuth(ctx, p.cfg.Master, a.SubUID); err != nil {
			return classify(a.Step, fmt.Errorf("enable linear swap: %w", err))
		}
		a.Step = StepContractEnabled
	case StepContractEnabled:
		if err := p.spot.SetDeductMode(ctx, p.cfg.Master, a.SubUID, p.cfg.DeductMode); err != nil {
			return classify(a.Step, fmt.Errorf("set deduct mode: %w", err))
		}
		a.Step = StepConfigured
	case StepConfigured:
		return p.stepIssueKey(ctx, a)
	case StepKeyIssued:
		cred, err := p.credentials(ctx, a.UserID)
		if err != nil {
			return &stepError{step: a.Step, err: err}
		}
		uid, err := p.spot.UserUID(ctx, cred)
		if err != nil {
			return classify(a.Step, fmt.Errorf("verify key uid: %w", err))
		}
		if uid != a.SubUID {
			return &stepError{step: a.Step, err: fmt.Errorf("key belongs to uid %d, expected sub uid %d", uid, a.SubUID)}
		}
		a.Step = StepVerified
	case StepVerified:
		return p.stepContractSetup(ctx, a)
	default:
		return &stepError{step: a.Step, err: fmt.Errorf("unknown step %q", a.Step)}
	}
	return nil
}

func (p *Provisioner) stepCreate(ctx context.Context, a *Account) error {
	note := subUserNote(a.UserID)
	for a.NameAttempt < maxNameAttempts {
		res, err := p.spot.CreateSubUser(ctx, p.cfg.Master, a.UserName, note, a.SubAccountType)
		if err == nil {
			a.SubUID, a.Step = res.UID, StepCreated
			return nil
		}
		var item *htx.ItemError
		if !errors.As(err, &item) || item.Code != "2002" {
			return classify(a.Step, fmt.Errorf("create sub user %s: %w", a.UserName, err))
		}
		// 用户名已存在：若是本系统先前创建（崩溃丢失响应）则找回 UID；否则被他人占用，换名重试
		found, ferr := p.spot.FindSubUserByName(ctx, p.cfg.Master, a.UserName)
		if ferr != nil {
			return classify(a.Step, fmt.Errorf("look up sub user %s: %w", a.UserName, ferr))
		}
		if found != nil && found.Note == note {
			p.logger.Warn("recovered previously created sub user", slog.Int64("user_id", a.UserID), slog.Int64("sub_uid", found.UID))
			a.SubUID, a.Step = found.UID, StepCreated
			return nil
		}
		a.NameAttempt++
		a.UserName = p.cfg.userName(a.UserID, a.NameAttempt)
		// 先落库新用户名，避免崩溃后重复尝试
		if err := advance(ctx, p.db, a, a.lease, p.cfg.LeaseTTL); err != nil {
			return &stepError{step: a.Step, err: err}
		}
	}
	return &stepError{step: a.Step, err: fmt.Errorf("sub user name unavailable after %d attempts", maxNameAttempts)}
}

func (p *Provisioner) stepIssueKey(ctx context.Context, a *Account) error {
	if err := p.sweepOrphanKeys(ctx, a); err != nil {
		return err
	}
	if k, err := activeKey(ctx, p.db, a.UserID); err != nil {
		return &stepError{step: a.Step, err: err}
	} else if k != nil {
		a.Step = StepKeyIssued // 已存在（上次落库成功但推进前崩溃）
		return nil
	}
	note := keyNoteTag(a.UserID) + randToken()
	key, err := p.spot.GenerateSubAPIKey(ctx, p.cfg.Master, htx.SubAPIKeyRequest{
		SubUID: a.SubUID, Note: note, Permission: "readOnly,trade", IPAddresses: p.cfg.KeyIPs,
	})
	if err != nil {
		return classify(a.Step, fmt.Errorf("generate api key: %w", err))
	}
	defer key.Wipe()

	secret := key.Secret()
	sealed, err := p.vault.Seal(secret, secretAAD(a.UserID, key.AccessKey))
	vault.Wipe(secret)
	if err == nil {
		err = storage.WithTx(ctx, p.db, func(tx *sql.Tx) error {
			if err := insertKey(ctx, tx, a.UserID, a.SubUID, key.AccessKey, sealed, key.Permission,
				strings.Join(p.cfg.KeyIPs, ","), note); err != nil {
				return err
			}
			a.Step = StepKeyIssued
			return advance(ctx, tx, a, a.lease, p.cfg.LeaseTTL)
		})
	}
	if err != nil {
		a.Step = StepConfigured
		// 落库失败：立即删除火币侧 Key（失败则留给下次孤儿清理）
		if derr := p.spot.DeleteSubAPIKey(ctx, p.cfg.Master, a.SubUID, key.AccessKey); derr != nil {
			p.logger.Error("compensating api key deletion failed; orphan will be swept on retry",
				slog.Int64("user_id", a.UserID), slog.String("access_key", maskAK(key.AccessKey)), slog.String("error", derr.Error()))
		}
		return &stepError{step: a.Step, err: fmt.Errorf("store api key: %w", err), retryable: true}
	}
	return nil
}

// sweepOrphanKeys 删除火币侧带本系统标签、但本地没有 ACTIVE 记录的 Key（生成后丢失响应或落库失败的遗留）。
func (p *Provisioner) sweepOrphanKeys(ctx context.Context, a *Account) error {
	keys, err := p.spot.ListAPIKeys(ctx, p.cfg.Master, a.SubUID, "")
	if err != nil {
		return classify(a.Step, fmt.Errorf("list api keys: %w", err))
	}
	active, err := activeKey(ctx, p.db, a.UserID)
	if err != nil {
		return &stepError{step: a.Step, err: err}
	}
	for _, k := range keys {
		if !strings.HasPrefix(k.Note, keyNoteTag(a.UserID)) || (active != nil && active.AccessKey == k.AccessKey) {
			continue
		}
		if err := p.spot.DeleteSubAPIKey(ctx, p.cfg.Master, a.SubUID, k.AccessKey); err != nil {
			return classify(a.Step, fmt.Errorf("delete orphan api key: %w", err))
		}
		p.logger.Warn("deleted orphan sub-account api key", slog.Int64("user_id", a.UserID), slog.String("access_key", maskAK(k.AccessKey)))
	}
	return nil
}

func (p *Provisioner) stepContractSetup(ctx context.Context, a *Account) error {
	cred, err := p.credentials(ctx, a.UserID)
	if err != nil {
		return &stepError{step: a.Step, err: err}
	}
	if err := p.swap.SetAssetMode(ctx, cred, htx.AssetModeMulti); err != nil {
		return classify(a.Step, fmt.Errorf("set asset mode: %w", err))
	}
	if mode, err := p.swap.AssetMode(ctx, cred); err != nil {
		return classify(a.Step, fmt.Errorf("read asset mode: %w", err))
	} else if mode != htx.AssetModeMulti {
		return &stepError{step: a.Step, err: fmt.Errorf("asset mode is %d after setting %d", mode, htx.AssetModeMulti)}
	}
	if err := p.swap.SetPositionMode(ctx, cred, htx.PositionModeSingle); err != nil {
		return classify(a.Step, fmt.Errorf("set position mode: %w", err))
	}
	a.Step = StepReady
	return nil
}

// fail 处理步骤失败：可重试则保留进度；终态则补偿并置 FAILED。
func (p *Provisioner) fail(ctx context.Context, a *Account, err error) (*Account, error) {
	var se *stepError
	if !errors.As(err, &se) {
		se = &stepError{step: a.Step, err: err}
	}
	if se.retryable {
		a.State = StateProvisioning
		_ = release(ctx, p.db, a, a.lease, err.Error())
		p.logger.Warn("sub-account provisioning paused (retryable)", slog.Int64("user_id", a.UserID),
			slog.String("step", string(a.Step)), slog.String("error", err.Error()))
		return a, err
	}
	// 终态失败：回收已生成的 Key，状态机回退到生成 Key 之前
	if stepRank(a.Step) >= stepRank(StepKeyIssued) {
		if cerr := p.revokeActiveKey(ctx, a); cerr != nil {
			err = fmt.Errorf("%w (compensation: %v)", err, cerr)
		}
		a.Step = StepConfigured
	}
	a.State = StateFailed
	if rerr := release(ctx, p.db, a, a.lease, err.Error()); rerr != nil {
		err = fmt.Errorf("%w (release: %v)", err, rerr)
	}
	p.invalidate(a.UserID)
	p.logger.Error("sub-account provisioning failed", slog.Int64("user_id", a.UserID), slog.Int64("sub_uid", a.SubUID),
		slog.String("step", string(a.Step)), slog.String("error", err.Error()))
	return a, err
}

func (p *Provisioner) revokeActiveKey(ctx context.Context, a *Account) error {
	k, err := activeKey(ctx, p.db, a.UserID)
	if err != nil || k == nil {
		return err
	}
	if err := p.spot.DeleteSubAPIKey(ctx, p.cfg.Master, k.SubUID, k.AccessKey); err != nil {
		var apiErr *htx.APIError
		// 火币侧已不存在（2002）视为已删除；其他错误保留，孤儿清理会在重试时兜底
		if !errors.As(err, &apiErr) || apiErr.Code != "2002" {
			p.logger.Error("delete api key at HTX failed during compensation", slog.Int64("user_id", a.UserID),
				slog.String("access_key", maskAK(k.AccessKey)), slog.String("error", err.Error()))
		}
	}
	return revokeKey(ctx, p.db, k.ID)
}

func stepRank(s Step) int {
	for i, x := range []Step{StepReserved, StepCreated, StepContractEnabled, StepConfigured, StepKeyIssued, StepVerified, StepReady} {
		if x == s {
			return i
		}
	}
	return -1
}

// credentials 解密用户 ACTIVE Key（开户流程内部使用，不检查账户状态）。
func (p *Provisioner) credentials(ctx context.Context, userID int64) (*htx.Credentials, error) {
	return loadCredentials(ctx, p.db, p.vault, userID)
}

// ResumePending 续跑租约已过期的 PROVISIONING 账户（后台定时调用）。返回成功就绪的数量。
func (p *Provisioner) ResumePending(ctx context.Context, limit int) (int, error) {
	ids, err := listResumable(ctx, p.db, limit)
	if err != nil {
		return 0, err
	}
	ready := 0
	for _, id := range ids {
		a, err := p.Provision(ctx, id)
		if err == nil && a.State == StateReady {
			ready++
		}
	}
	return ready, nil
}

// Lock 冻结用户子账户：先本地置 LOCKED（立即阻断交易），再冻结火币子用户。火币侧失败时保持本地锁定，可重试。
func (p *Provisioner) Lock(ctx context.Context, userID int64, operator, reason string) error {
	a, err := GetAccount(ctx, p.db, userID)
	if err != nil {
		return err
	}
	if a == nil || a.SubUID == 0 {
		return ErrNoSubAccount
	}
	if _, err := setState(ctx, p.db, userID, StateReady, StateLocked); err != nil {
		return err
	}
	p.invalidate(userID)
	if _, err := p.spot.SetSubUserLock(ctx, p.cfg.Master, a.SubUID, true); err != nil {
		return fmt.Errorf("lock HTX sub user %d (local lock kept): %w", a.SubUID, err)
	}
	p.logger.Warn("sub-account locked", slog.Int64("user_id", userID), slog.Int64("sub_uid", a.SubUID),
		slog.String("operator", operator), slog.String("reason", reason))
	return nil
}

// Unlock 解冻：先解冻火币子用户，成功后本地恢复 READY。
func (p *Provisioner) Unlock(ctx context.Context, userID int64, operator string) error {
	a, err := GetAccount(ctx, p.db, userID)
	if err != nil {
		return err
	}
	if a == nil || a.State != StateLocked {
		return fmt.Errorf("%w: account not locked", ErrInvalidRequest)
	}
	if _, err := p.spot.SetSubUserLock(ctx, p.cfg.Master, a.SubUID, false); err != nil {
		return fmt.Errorf("unlock HTX sub user %d: %w", a.SubUID, err)
	}
	if _, err := setState(ctx, p.db, userID, StateLocked, StateReady); err != nil {
		return err
	}
	p.invalidate(userID)
	p.logger.Warn("sub-account unlocked", slog.Int64("user_id", userID), slog.Int64("sub_uid", a.SubUID), slog.String("operator", operator))
	return nil
}

func (p *Provisioner) invalidate(userID int64) {
	if p.keys != nil {
		p.keys.Invalidate(userID)
	}
}

func randToken() string {
	b := make([]byte, 4)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

func maskAK(k string) string {
	if len(k) <= 8 {
		return "***"
	}
	return k[:4] + "***" + k[len(k)-4:]
}
