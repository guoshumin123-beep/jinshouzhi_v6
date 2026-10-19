// Package subaccount 管理 Standard 用户的专属火币子账户：自动开户、API Key 加密托管、冻结/解冻、资金划转。
//
// 安全约束：
//   - Key 只能经由 KeyProvider 在服务端内存中取用，绝不出现在 User 端接口或日志中。
//   - 用户没有可用子账户时返回错误，调用方不得回退到母账户凭证。
//   - Secret 仅以 vault（AES-256-GCM）密文落库，AAD 绑定 user_id 与 access_key。
package subaccount

import (
	"context"
	"errors"

	"backend_v2/internal/htx"
)

var (
	ErrNoSubAccount        = errors.New("subaccount: user has no HTX sub-account")
	ErrSubAccountNotReady  = errors.New("subaccount: HTX sub-account is not ready")
	ErrSubAccountLocked    = errors.New("subaccount: HTX sub-account is locked")
	ErrProvisionInProgress = errors.New("subaccount: provisioning in progress on another worker")
	ErrTransfersDisabled   = errors.New("subaccount: HTX transfers are disabled (HTX_TRANSFERS_ENABLED=false)")
	ErrTransferInProgress  = errors.New("subaccount: another transfer is in flight for this user")
	ErrInvalidConfig       = errors.New("subaccount: invalid config")
	ErrInvalidRequest      = errors.New("subaccount: invalid request")
)

// KeyProvider 提供用户专属的子账户凭证（含子账户 UID，用于按 UID 限频）。
type KeyProvider interface {
	Get(ctx context.Context, userID int64) (*htx.Credentials, error)
	Invalidate(userID int64)
}
