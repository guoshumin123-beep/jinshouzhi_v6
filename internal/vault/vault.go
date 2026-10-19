// Package vault 提供基于 AES-256-GCM 的对称加密，用于加密存储子账户 API Secret 等敏感数据。
//
// 设计要点：
//   - 主密钥（KEK）只从环境变量读取：VAULT_KEY_<id>=<base64(32 字节)>，VAULT_ACTIVE_KEY_ID=<id> 指定加密所用密钥。
//     保留旧 id 的环境变量即可继续解密历史密文，实现密钥轮换。
//   - 每次加密使用随机 96 位 nonce；密文绑定调用方提供的 AAD（如 "subaccount:<user_id>:<access_key>"），
//     且 AAD 中自动包含密钥 id，防止密文被挪用到其它记录或被篡改密钥 id。
//   - 解密失败一律返回 ErrDecrypt，不区分原因、不携带任何密文/明文细节。
//   - Vault 不暴露密钥，String/GoString 脱敏；调用方用完明文后应调用 Wipe 清零。
package vault

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"os"
	"regexp"
	"sort"
	"strings"
)

const (
	KeySize   = 32 // AES-256
	nonceSize = 12

	EnvActiveKeyID = "VAULT_ACTIVE_KEY_ID"
	EnvKeyPrefix   = "VAULT_KEY_"

	aadVersion = "vault.v1"
)

var (
	ErrDecrypt    = errors.New("vault: decryption failed")
	ErrUnknownKey = errors.New("vault: unknown key id")
	ErrConfig     = errors.New("vault: invalid configuration")
)

var keyIDPattern = regexp.MustCompile(`^[A-Za-z0-9_]{1,32}$`)

// Sealed 一段密文。三个字段需一并持久化。
type Sealed struct {
	KeyID      string
	Nonce      []byte
	Ciphertext []byte // 含 16 字节 GCM 认证标签
}

// Vault 并发安全（cipher.AEAD 可并发使用）。
type Vault struct {
	active string
	aeads  map[string]cipher.AEAD
}

// New 以给定密钥集合构造 Vault。keys 的值必须为 32 字节。
func New(keys map[string][]byte, activeID string) (*Vault, error) {
	if _, ok := keys[activeID]; !ok {
		return nil, fmt.Errorf("%w: active key %q not provided", ErrConfig, activeID)
	}
	v := &Vault{active: activeID, aeads: make(map[string]cipher.AEAD, len(keys))}
	for id, k := range keys {
		if !keyIDPattern.MatchString(id) {
			return nil, fmt.Errorf("%w: key id %q must match %s", ErrConfig, id, keyIDPattern)
		}
		if len(k) != KeySize {
			return nil, fmt.Errorf("%w: key %q must be %d bytes, got %d", ErrConfig, id, KeySize, len(k))
		}
		block, err := aes.NewCipher(k)
		if err != nil {
			return nil, fmt.Errorf("%w: key %q: %v", ErrConfig, id, err)
		}
		aead, err := cipher.NewGCM(block)
		if err != nil {
			return nil, fmt.Errorf("%w: key %q: %v", ErrConfig, id, err)
		}
		v.aeads[id] = aead
	}
	return v, nil
}

// FromEnv 从环境变量加载：全部 VAULT_KEY_<id> 作为可解密密钥，VAULT_ACTIVE_KEY_ID 指定加密密钥。
func FromEnv() (*Vault, error) {
	return fromEnviron(os.Environ(), os.Getenv(EnvActiveKeyID))
}

func fromEnviron(environ []string, active string) (*Vault, error) {
	if active == "" {
		return nil, fmt.Errorf("%w: %s not set", ErrConfig, EnvActiveKeyID)
	}
	keys := map[string][]byte{}
	for _, kv := range environ {
		name, val, ok := strings.Cut(kv, "=")
		if !ok || !strings.HasPrefix(name, EnvKeyPrefix) {
			continue
		}
		id := strings.TrimPrefix(name, EnvKeyPrefix)
		k, err := base64.StdEncoding.DecodeString(strings.TrimSpace(val))
		if err != nil {
			// 不回显变量值
			return nil, fmt.Errorf("%w: %s is not valid base64", ErrConfig, name)
		}
		keys[id] = k
	}
	if len(keys) == 0 {
		return nil, fmt.Errorf("%w: no %s<id> variables found", ErrConfig, EnvKeyPrefix)
	}
	return New(keys, active)
}

// ActiveKeyID 当前用于加密的密钥 id。
func (v *Vault) ActiveKeyID() string { return v.active }

// KeyIDs 可用于解密的全部密钥 id（升序）。
func (v *Vault) KeyIDs() []string {
	ids := make([]string, 0, len(v.aeads))
	for id := range v.aeads {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids
}

func (v *Vault) String() string {
	return fmt.Sprintf("vault(active=%s, keys=%d)", v.active, len(v.aeads))
}
func (v *Vault) GoString() string { return v.String() }

func boundAAD(keyID string, aad []byte) []byte {
	b := make([]byte, 0, len(aadVersion)+len(keyID)+len(aad)+2)
	b = append(b, aadVersion...)
	b = append(b, '|')
	b = append(b, keyID...)
	b = append(b, '|')
	return append(b, aad...)
}

// Seal 使用当前活动密钥加密 plaintext，并绑定 aad。aad 不能为空：每条密文都必须绑定其归属记录。
func (v *Vault) Seal(plaintext, aad []byte) (Sealed, error) {
	if len(aad) == 0 {
		return Sealed{}, fmt.Errorf("%w: aad is required", ErrConfig)
	}
	nonce := make([]byte, nonceSize)
	if _, err := rand.Read(nonce); err != nil {
		return Sealed{}, fmt.Errorf("vault: generate nonce: %w", err)
	}
	ct := v.aeads[v.active].Seal(nil, nonce, plaintext, boundAAD(v.active, aad))
	return Sealed{KeyID: v.active, Nonce: nonce, Ciphertext: ct}, nil
}

// Open 解密。任何失败（密钥 id 未知除外）都返回 ErrDecrypt。
func (v *Vault) Open(s Sealed, aad []byte) ([]byte, error) {
	aead, ok := v.aeads[s.KeyID]
	if !ok {
		return nil, fmt.Errorf("%w: %q", ErrUnknownKey, s.KeyID)
	}
	if len(s.Nonce) != nonceSize || len(s.Ciphertext) < aead.Overhead() {
		return nil, ErrDecrypt
	}
	pt, err := aead.Open(nil, s.Nonce, s.Ciphertext, boundAAD(s.KeyID, aad))
	if err != nil {
		return nil, ErrDecrypt
	}
	return pt, nil
}

// NeedsRotation 报告密文是否由非活动密钥加密。
func (v *Vault) NeedsRotation(s Sealed) bool { return s.KeyID != v.active }

// Reseal 用活动密钥重新加密（密钥轮换）。明文仅在函数内短暂存在并被清零。
func (v *Vault) Reseal(s Sealed, aad []byte) (Sealed, error) {
	pt, err := v.Open(s, aad)
	if err != nil {
		return Sealed{}, err
	}
	defer Wipe(pt)
	return v.Seal(pt, aad)
}

// Wipe 将敏感字节清零。
func Wipe(b []byte) {
	for i := range b {
		b[i] = 0
	}
}

// GenerateKey 生成一把新的随机主密钥（base64），用于运维初始化 VAULT_KEY_<id>。
func GenerateKey() (string, error) {
	k := make([]byte, KeySize)
	if _, err := rand.Read(k); err != nil {
		return "", err
	}
	defer Wipe(k)
	return base64.StdEncoding.EncodeToString(k), nil
}
