package htx

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"log/slog"
	"net/url"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"
)

const (
	signatureMethod  = "HmacSHA256"
	signatureVersion = "2"
	timestampLayout  = "2006-01-02T15:04:05"

	EnvAccessKey = "HUOBI_ACCESS_KEY"
	EnvSecretKey = "HUOBI_SECRET_KEY"
)

// Credentials 一组 HTX API Key。Secret 不导出，且所有格式化输出都是脱敏的，
// 防止通过日志、fmt 或 JSON 泄露。
type Credentials struct {
	AccessKey string
	// UID 该 Key 所属火币（子）账户 UID。火币按 UID 限频，客户端据此隔离限频桶；为 0 时退化为按 AccessKey 隔离。
	UID    int64
	secret []byte
}

// rateKey 限频桶的隔离键。
func (c *Credentials) rateKey() string {
	if c.UID > 0 {
		return "uid:" + strconv.FormatInt(c.UID, 10)
	}
	return "ak:" + c.AccessKey
}

func NewCredentials(accessKey, secretKey string) (*Credentials, error) {
	if accessKey == "" || secretKey == "" {
		return nil, errors.New("htx: access key and secret key are required")
	}
	return &Credentials{AccessKey: accessKey, secret: []byte(secretKey)}, nil
}

// CredentialsFromEnv 从 HUOBI_ACCESS_KEY / HUOBI_SECRET_KEY 读取凭证。
// 仅用于母账户托管任务与测试；Standard 用户下单必须使用其子账户凭证。
func CredentialsFromEnv() (*Credentials, error) {
	c, err := NewCredentials(os.Getenv(EnvAccessKey), os.Getenv(EnvSecretKey))
	if err != nil {
		return nil, errors.New("htx: " + EnvAccessKey + " / " + EnvSecretKey + " not set")
	}
	return c, nil
}

func (c Credentials) String() string               { return "htx.Credentials(" + maskKey(c.AccessKey) + ")" }
func (c Credentials) GoString() string             { return c.String() }
func (c Credentials) LogValue() slog.Value         { return slog.StringValue(c.String()) }
func (c Credentials) MarshalJSON() ([]byte, error) { return []byte(`"` + c.String() + `"`), nil }

func maskKey(k string) string {
	if len(k) <= 8 {
		return "***"
	}
	return k[:4] + "***" + k[len(k)-4:]
}

// SignParams 生成签名后的完整参数集合（不修改入参）：
// 追加 AccessKeyId/SignatureMethod/SignatureVersion/Timestamp，再追加 Signature。
func SignParams(cred *Credentials, method, host, path string, params url.Values, now time.Time) url.Values {
	out := make(url.Values, len(params)+5)
	for k, v := range params {
		out[k] = append([]string(nil), v...)
	}
	out.Set("AccessKeyId", cred.AccessKey)
	out.Set("SignatureMethod", signatureMethod)
	out.Set("SignatureVersion", signatureVersion)
	out.Set("Timestamp", now.UTC().Format(timestampLayout))
	out.Set("Signature", Sign(cred.secret, method, host, path, out))
	return out
}

// Sign 计算 Base64(HmacSHA256(secret, payload))，payload 见 PreSignString。
func Sign(secret []byte, method, host, path string, params url.Values) string {
	mac := hmac.New(sha256.New, secret)
	mac.Write([]byte(PreSignString(method, host, path, params)))
	return base64.StdEncoding.EncodeToString(mac.Sum(nil))
}

// PreSignString 构造待签名字符串：
//
//	{METHOD}\n{host}\n{path}\n{按 key 字母序排序并 URL 编码的参数}
//
// 参数中若已存在 Signature 会被忽略。
func PreSignString(method, host, path string, params url.Values) string {
	var sb strings.Builder
	sb.WriteString(strings.ToUpper(method))
	sb.WriteByte('\n')
	sb.WriteString(strings.ToLower(host))
	sb.WriteByte('\n')
	sb.WriteString(path)
	sb.WriteByte('\n')
	sb.WriteString(canonicalQuery(params, "Signature"))
	return sb.String()
}

// canonicalQuery 按 key 的 ASCII 顺序排序并编码；空格编码为 %20（而非 +）。
func canonicalQuery(params url.Values, skip string) string {
	keys := make([]string, 0, len(params))
	for k := range params {
		if k != skip {
			keys = append(keys, k)
		}
	}
	sort.Strings(keys)
	var sb strings.Builder
	for _, k := range keys {
		for _, v := range params[k] {
			if sb.Len() > 0 {
				sb.WriteByte('&')
			}
			sb.WriteString(escape(k))
			sb.WriteByte('=')
			sb.WriteString(escape(v))
		}
	}
	return sb.String()
}

func escape(s string) string {
	return strings.ReplaceAll(url.QueryEscape(s), "+", "%20")
}

// EncodeQuery 以与签名一致的编码方式输出最终的 query string（Signature 放在末尾）。
func EncodeQuery(params url.Values) string {
	q := canonicalQuery(params, "Signature")
	if sig := params.Get("Signature"); sig != "" {
		if q != "" {
			q += "&"
		}
		q += "Signature=" + escape(sig)
	}
	return q
}
