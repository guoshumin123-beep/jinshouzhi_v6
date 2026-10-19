// Package config 从环境变量加载服务配置。密钥类配置只从环境变量读取，String 输出脱敏。
package config

import (
	"encoding/base64"
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/go-sql-driver/mysql"

	"backend_v2/internal/htx"
	"backend_v2/internal/vault"
)

var ErrConfig = errors.New("config: invalid")

type Config struct {
	MySQLDSN string

	UserAddr  string // 用户 API 监听地址
	AdminAddr string // Admin API 监听地址（默认仅本机）

	UserJWTSecret  []byte
	AdminJWTSecret []byte

	HTXSwapBaseURL string
	HTXSpotBaseURL string
	Master         *htx.Credentials // 母账户；未配置为 nil
	ChannelCode    string           // 合约下单 Broker ID

	// 安全开关：火币无测试网，任何会真实改变火币状态的动作都必须显式开启
	TradingEnabled      bool // HTX_TRADING_ENABLED：Standard 真实下单
	ProvisioningEnabled bool // HTX_PROVISIONING_ENABLED：真实创建子账户 / API Key（子账户不可删除）
	TransfersEnabled    bool // HTX_TRANSFERS_ENABLED：真实资金划转

	SubUserNamePrefix string
	SubKeyIPs         []string

	Vault *vault.Vault

	ShutdownTimeout time.Duration
	ResumeInterval  time.Duration
}

// Load 从进程环境变量加载。
func Load() (*Config, error) { return LoadFrom(os.Getenv, os.Environ()) }

// LoadFrom 便于测试注入环境。environ 用于加载 VAULT_KEY_<id>。
func LoadFrom(getenv func(string) string, environ []string) (*Config, error) {
	get := func(k, def string) string {
		if v := strings.TrimSpace(getenv(k)); v != "" {
			return v
		}
		return def
	}
	var errs []string
	fail := func(f string, a ...any) { errs = append(errs, fmt.Sprintf(f, a...)) }

	c := &Config{
		UserAddr:          get("USER_HTTP_ADDR", ":8080"),
		AdminAddr:         get("ADMIN_HTTP_ADDR", "127.0.0.1:8081"),
		HTXSwapBaseURL:    get("HTX_SWAP_BASE_URL", htx.SwapBaseURL),
		HTXSpotBaseURL:    get("HTX_SPOT_BASE_URL", htx.SpotBaseURL),
		SubUserNamePrefix: get("HTX_SUBUSER_NAME_PREFIX", "bv"),
		ShutdownTimeout:   15 * time.Second,
		ResumeInterval:    30 * time.Second,
	}

	// MySQL：强制 parseTime，统一连接参数
	if dsn := get("MYSQL_DSN", ""); dsn == "" {
		fail("MYSQL_DSN is required")
	} else if cfg, err := mysql.ParseDSN(dsn); err != nil || cfg.DBName == "" {
		fail("MYSQL_DSN is invalid or has no database name")
	} else {
		cfg.ParseTime = true
		c.MySQLDSN = cfg.FormatDSN()
	}

	c.UserJWTSecret = []byte(get("JWT_USER_SECRET", ""))
	c.AdminJWTSecret = []byte(get("JWT_ADMIN_SECRET", ""))
	switch {
	case len(c.UserJWTSecret) < 32 || len(c.AdminJWTSecret) < 32:
		fail("JWT_USER_SECRET and JWT_ADMIN_SECRET must each be at least 32 bytes")
	case string(c.UserJWTSecret) == string(c.AdminJWTSecret):
		fail("JWT_USER_SECRET and JWT_ADMIN_SECRET must differ (user/admin isolation)")
	}

	var err error
	for name, dst := range map[string]*bool{
		"HTX_TRADING_ENABLED": &c.TradingEnabled, "HTX_PROVISIONING_ENABLED": &c.ProvisioningEnabled,
		"HTX_TRANSFERS_ENABLED": &c.TransfersEnabled,
	} {
		if *dst, err = strconv.ParseBool(get(name, "false")); err != nil {
			fail("%s must be true or false", name)
		}
	}

	if ak, sk := get(htx.EnvAccessKey, ""), get(htx.EnvSecretKey, ""); ak != "" || sk != "" {
		if c.Master, err = htx.NewCredentials(ak, sk); err != nil {
			fail("%s and %s must both be set", htx.EnvAccessKey, htx.EnvSecretKey)
		} else if uid := get("HTX_MASTER_UID", ""); uid != "" {
			if c.Master.UID, err = strconv.ParseInt(uid, 10, 64); err != nil || c.Master.UID <= 0 {
				fail("HTX_MASTER_UID must be a positive integer")
			}
		}
	}

	if cc := get(htx.EnvSwapChannelCode, ""); cc != "" {
		if err := (htx.SwapOrderRequest{ContractCode: "X", MarginMode: htx.MarginCross, Side: htx.SideBuy,
			Type: htx.OrderTypeMarket, Volume: "1", ChannelCode: cc}).Validate(); err != nil {
			fail("%s must be 1-32 letters/digits", htx.EnvSwapChannelCode)
		}
		c.ChannelCode = cc
	}

	for _, ip := range strings.Split(get("HTX_SUB_APIKEY_IPS", ""), ",") {
		if ip = strings.TrimSpace(ip); ip != "" {
			c.SubKeyIPs = append(c.SubKeyIPs, ip)
		}
	}
	if c.ProvisioningEnabled {
		if c.Master == nil {
			fail("HTX_PROVISIONING_ENABLED requires master credentials (%s / %s)", htx.EnvAccessKey, htx.EnvSecretKey)
		}
		if len(c.SubKeyIPs) == 0 {
			fail("HTX_PROVISIONING_ENABLED requires HTX_SUB_APIKEY_IPS (sub-account keys must be IP-bound)")
		}
	}
	if c.TransfersEnabled && c.Master == nil {
		fail("HTX_TRANSFERS_ENABLED requires master credentials")
	}

	// Vault：子账户 Secret 的加解密依赖，始终必需
	if v, err := vaultFrom(environ, get(vault.EnvActiveKeyID, "")); err != nil {
		fail("vault: %v", err)
	} else {
		c.Vault = v
	}

	if len(errs) > 0 {
		return nil, fmt.Errorf("%w:\n  - %s", ErrConfig, strings.Join(errs, "\n  - "))
	}
	return c, nil
}

func vaultFrom(environ []string, active string) (*vault.Vault, error) {
	if active == "" {
		return nil, fmt.Errorf("%s not set", vault.EnvActiveKeyID)
	}
	keys := map[string][]byte{}
	for _, kv := range environ {
		name, val, ok := strings.Cut(kv, "=")
		if !ok || !strings.HasPrefix(name, vault.EnvKeyPrefix) {
			continue
		}
		k, err := decodeKey(strings.TrimSpace(val))
		if err != nil {
			return nil, fmt.Errorf("%s is not valid base64", name)
		}
		keys[strings.TrimPrefix(name, vault.EnvKeyPrefix)] = k
	}
	return vault.New(keys, active)
}

// String 脱敏输出，便于启动日志记录生效配置。
func (c *Config) String() string {
	master := "unset"
	if c.Master != nil {
		master = c.Master.String()
	}
	return fmt.Sprintf("config(user=%s admin=%s swap=%s spot=%s master=%s channel_code_set=%t trading=%t provisioning=%t transfers=%t key_ips=%d vault=%s)",
		c.UserAddr, c.AdminAddr, c.HTXSwapBaseURL, c.HTXSpotBaseURL, master, c.ChannelCode != "",
		c.TradingEnabled, c.ProvisioningEnabled, c.TransfersEnabled, len(c.SubKeyIPs), c.Vault)
}

func decodeKey(s string) ([]byte, error) { return base64.StdEncoding.DecodeString(s) }
