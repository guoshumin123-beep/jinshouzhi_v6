package subaccount

import (
	"context"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/shopspring/decimal"

	"backend_v2/internal/htx"
)

type State string

const (
	StateProvisioning State = "PROVISIONING"
	StateReady        State = "READY"
	StateLocked       State = "LOCKED"
	StateFailed       State = "FAILED"
)

// Step 开户状态机已完成的最后一步。
type Step string

const (
	StepReserved        Step = "RESERVED"         // 本地已预留用户名
	StepCreated         Step = "CREATED"          // 火币子用户已创建（sub_uid 已知）
	StepContractEnabled Step = "CONTRACT_ENABLED" // 已开通 U 本位合约权限
	StepConfigured      Step = "CONFIGURED"       // 手续费抵扣模式已设置
	StepKeyIssued       Step = "KEY_ISSUED"       // API Key 已生成并加密落库
	StepVerified        Step = "VERIFIED"         // 已用新 Key 校验 UID 绑定
	StepReady           Step = "READY"            // 联合保证金 + 单向持仓已设置
)

// Account 子账户记录。
type Account struct {
	UserID         int64
	UserName       string
	NameAttempt    int
	SubUID         int64
	SubAccountType string
	State          State
	Step           Step
	Attempts       int
	LastError      string

	lease string // 本次 Provision 调用持有的租约令牌（每次调用唯一，进程内并发同样互斥）
}

// SpotAPI 现货域名接口（*htx.Client 实现）。
type SpotAPI interface {
	CreateSubUser(ctx context.Context, master *htx.Credentials, userName, note, accountType string) (*htx.SubUserResult, error)
	FindSubUserByName(ctx context.Context, master *htx.Credentials, userName string) (*htx.SubUser, error)
	ListSubUsers(ctx context.Context, master *htx.Credentials, fromID int64) ([]htx.SubUser, int64, error)
	SetDeductMode(ctx context.Context, master *htx.Credentials, subUID int64, mode string) error
	GenerateSubAPIKey(ctx context.Context, master *htx.Credentials, req htx.SubAPIKeyRequest) (*htx.SubAPIKey, error)
	ListAPIKeys(ctx context.Context, master *htx.Credentials, uid int64, accessKey string) ([]htx.APIKeyInfo, error)
	DeleteSubAPIKey(ctx context.Context, master *htx.Credentials, subUID int64, accessKey string) error
	UserUID(ctx context.Context, cred *htx.Credentials) (int64, error)
	SetSubUserLock(ctx context.Context, master *htx.Credentials, subUID int64, lock bool) (string, error)
	SubUserTransfer(ctx context.Context, master *htx.Credentials, req htx.MasterTransferRequest) (int64, error)
	SubUserSpotBalance(ctx context.Context, master *htx.Credentials, subUID int64) ([]htx.SpotAccount, error)
	AccountTransfer(ctx context.Context, cred *htx.Credentials, req htx.AccountTransferRequest) (int64, error)
}

// SwapAPI 合约域名接口（*htx.Client 实现）。
type SwapAPI interface {
	SwapSubAuth(ctx context.Context, master *htx.Credentials, subUID int64) error
	SetAssetMode(ctx context.Context, cred *htx.Credentials, mode int) error
	AssetMode(ctx context.Context, cred *htx.Credentials) (int, error)
	SetPositionMode(ctx context.Context, cred *htx.Credentials, mode string) error
	PositionMode(ctx context.Context, cred *htx.Credentials) (string, error)
	AccountBalance(ctx context.Context, cred *htx.Credentials) (*htx.AccountBalance, error)
}

var (
	_ SpotAPI = (*htx.Client)(nil)
	_ SwapAPI = (*htx.Client)(nil)
)

// Config 开户配置。
type Config struct {
	Master         *htx.Credentials // 母账户凭证（需带 UID）
	NamePrefix     string           // 子用户名前缀，2-6 位小写字母
	SubAccountType string           // htx.SubAccountGeneral（默认）
	KeyIPs         []string         // 子账户 API Key 绑定的服务器出口 IP（必填）
	DeductMode     string           // 手续费抵扣模式：sub（子账户承担）
	LeaseTTL       time.Duration    // 开户租约时长
	Owner          string           // 本实例标识（租约持有人）
}

var prefixPattern = regexp.MustCompile(`^[a-z]{2,6}$`)

func (c *Config) normalize() error {
	switch {
	case c.Master == nil:
		return fmt.Errorf("%w: master credentials required", ErrInvalidConfig)
	case !prefixPattern.MatchString(c.NamePrefix):
		return fmt.Errorf("%w: name prefix must be 2-6 lower-case letters", ErrInvalidConfig)
	case len(c.KeyIPs) == 0:
		return fmt.Errorf("%w: %v", ErrInvalidConfig, htx.ErrIPBindingRequired)
	case len(c.KeyIPs) > 20:
		return fmt.Errorf("%w: at most 20 key IPs", ErrInvalidConfig)
	}
	if c.SubAccountType == "" {
		c.SubAccountType = htx.SubAccountGeneral
	}
	if c.DeductMode == "" {
		c.DeductMode = "sub"
	}
	if c.LeaseTTL <= 0 {
		c.LeaseTTL = 2 * time.Minute
	}
	if c.Owner == "" {
		c.Owner = "subaccount-" + strconv.FormatInt(time.Now().UnixNano(), 36)
	}
	return nil
}

// userName 子用户名：前缀 + base36(user_id) [+ "x" + 重试序号]，满足火币 6-20 位字母数字、字母开头的约束。
func (c *Config) userName(userID int64, attempt int) string {
	n := c.NamePrefix + strconv.FormatInt(userID, 36)
	if attempt > 0 {
		n += "x" + strconv.Itoa(attempt)
	}
	for len(n) < 6 {
		n = c.NamePrefix + "0" + n[len(c.NamePrefix):]
	}
	return n
}

// subUserNote 写入火币子用户备注，用于崩溃恢复时确认用户名归属。
func subUserNote(userID int64) string { return "u" + strconv.FormatInt(userID, 10) }

// keyNoteTag API Key 备注前缀，用于识别本系统为该用户生成的 Key（孤儿 Key 清理）。
func keyNoteTag(userID int64) string { return "bv2:u" + strconv.FormatInt(userID, 10) + ":" }

func secretAAD(userID int64, accessKey string) []byte {
	return []byte("subaccount:" + strconv.FormatInt(userID, 10) + ":" + accessKey)
}

// ---------------------------------------------------------------------------
// 划转
// ---------------------------------------------------------------------------

type Direction string

const (
	DirToSub   Direction = "TO_SUB"
	DirFromSub Direction = "FROM_SUB"
)

type LegKind string

const (
	LegMasterToSubSpot LegKind = "MASTER_TO_SUB_SPOT" // 母现货 -> 子现货（/v1/subuser/transfer，带 client-order-id）
	LegSubSpotToSwap   LegKind = "SUB_SPOT_TO_SWAP"   // 子现货 -> 子 U 本位（/v2/account/transfer，无幂等键）
	LegSubSwapToSpot   LegKind = "SUB_SWAP_TO_SPOT"
	LegSubSpotToMaster LegKind = "SUB_SPOT_TO_MASTER"
)

func (k LegKind) masterLeg() bool { return k == LegMasterToSubSpot || k == LegSubSpotToMaster }

type LegStatus string

const (
	LegPending   LegStatus = "PENDING"
	LegInFlight  LegStatus = "IN_FLIGHT"
	LegSucceeded LegStatus = "SUCCEEDED"
	LegFailed    LegStatus = "FAILED"
	LegUnknown   LegStatus = "UNKNOWN" // 已发出但结果未知，待核验
	LegManual    LegStatus = "MANUAL"  // 核验无法判定，需人工处理
)

// Leg 一段划转。
type Leg struct {
	ID            uint64
	RequestID     string
	UserID        int64
	SubUID        int64
	Direction     Direction
	Seq           int
	Kind          LegKind
	Currency      string // 本地大写，如 USDT
	Amount        decimal.Decimal
	MarginAccount string
	ClientOrderID string
	Status        LegStatus
	SpotBefore    decimal.NullDecimal
	HTXTransferID int64
	Attempts      int
	LastError     string
}

// TransferResult 一次划转请求的整体结果。
type TransferResult struct {
	RequestID string
	Direction Direction
	Status    LegStatus // 全部完成为 SUCCEEDED；回滚完成为 FAILED；否则为在途/未知/人工
	Legs      []Leg
}

var isolatedMarginAccount = regexp.MustCompile(`^[a-z0-9]+-usdt$`)

// MarginAccountFor 全仓 "USDT"；逐仓为小写合约代码（如 btc-usdt）。
func MarginAccountFor(cross bool, contractCode string) string {
	if cross {
		return "USDT"
	}
	return strings.ToLower(contractCode)
}

func validMarginAccount(m string) bool { return m == "USDT" || isolatedMarginAccount.MatchString(m) }
