// Package credential 解析与脱敏账号凭据。凭据以带判别式的 JSON 存储，
// kind 字段决定具体结构，为后续接入刷新型凭据留出扩展位。
//
// 脱敏靠类型区分而非调用点自觉：Redacted 是独立类型，管理面读取路径返回它，
// 解析路径返回 Credential 本身。忘记脱敏会体现为类型不匹配，而不是静默泄露。
package credential

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/aceaura/model-surge-upstream/backend/provider"
)

var SupportedKinds = []provider.CredentialKind{provider.CredAPIKey, provider.CredOAuthRefresh, provider.CredKiroRefresh}

var awsRegion = regexp.MustCompile(`^[a-z]{2}(?:-[a-z]+)+-[0-9]+$`)

type Credential struct {
	Kind   provider.CredentialKind `json:"kind"`
	APIKey string                  `json:"api_key,omitempty"`
	// oauth_refresh 形态:RefreshToken/AccountID 由用户粘贴(codex CLI
	// auth.json 的 tokens.refresh_token/tokens.account_id);AccessToken/Expiry
	// 是续期产物,由 oauth 包运行时维护并落库,不要求初始填写。
	RefreshToken string    `json:"refresh_token,omitempty"`
	AccessToken  string    `json:"access_token,omitempty"`
	Expiry       time.Time `json:"expiry,omitzero"`
	AccountID    string    `json:"account_id,omitempty"`
	// WebRefreshToken 是网页会话刷新令牌(kimi 会员月总额度只在网页
	// 网关可查,API key 拿不到),api_key 形态账号的可选附加项;配额
	// 查询链用它换短效 access_token,令牌本体不落库轮换(上游无硬轮换)。
	WebRefreshToken string `json:"web_refresh_token,omitempty"`
	// 额度 Token 与推理 Key 分离，AK/SK 用于二进制内置续期。
	ConsoleAccessToken     string    `json:"console_access_token,omitempty"`
	BailianAccessKeyID     string    `json:"bailian_access_key_id,omitempty"`
	BailianAccessKeySecret string    `json:"bailian_access_key_secret,omitempty"`
	ConsoleVerifiedAt      time.Time `json:"console_verified_at,omitzero"`
	ProfileARN             string    `json:"profile_arn,omitempty"`
	Region                 string    `json:"region,omitempty"`
	APIRegion              string    `json:"api_region,omitempty"`
	ClientID               string    `json:"client_id,omitempty"`
	ClientSecret           string    `json:"client_secret,omitempty"`
}

// Decode 两步解码：先取 kind，再按 kind 校验具体字段。
func Decode(raw []byte) (Credential, error) {
	c, err := DecodeShaped(raw)
	if err != nil {
		return Credential{}, err
	}
	if err := c.Validate(); err != nil {
		return Credential{}, err
	}
	return c, nil
}

// DecodeShaped 只校验 JSON 与 kind 合法性,不校验字段必填。账号更新走
// 留空保留语义(空 api_key=不换密钥),必填校验在仓储合并后统一进行;
// 创建路径同样经仓储 validate 兜底,不会放进空密钥。
func DecodeShaped(raw []byte) (Credential, error) {
	var probe struct {
		Kind provider.CredentialKind `json:"kind"`
	}
	if err := json.Unmarshal(raw, &probe); err != nil {
		return Credential{}, fmt.Errorf("credential is not valid json: %v", err)
	}
	if probe.Kind == "" {
		return Credential{}, fmt.Errorf("credential kind is required, supported: %s", kindList())
	}
	if !supported(probe.Kind) {
		return Credential{}, fmt.Errorf("unsupported credential kind %q, supported: %s", probe.Kind, kindList())
	}

	var c Credential
	if err := json.Unmarshal(raw, &c); err != nil {
		return Credential{}, fmt.Errorf("credential is not valid json: %v", err)
	}
	return c, nil
}

func (c Credential) Validate() error {
	switch c.Kind {
	case provider.CredAPIKey:
		if strings.TrimSpace(c.APIKey) == "" {
			return fmt.Errorf("credential api_key is required for kind %q", provider.CredAPIKey)
		}
		if (strings.TrimSpace(c.BailianAccessKeyID) == "") != (strings.TrimSpace(c.BailianAccessKeySecret) == "") {
			return fmt.Errorf("bailian access key ID and secret must be provided together")
		}
		return nil
	case provider.CredOAuthRefresh:
		if strings.TrimSpace(c.RefreshToken) == "" {
			return fmt.Errorf("credential refresh_token is required for kind %q", provider.CredOAuthRefresh)
		}
		if strings.TrimSpace(c.AccountID) == "" {
			return fmt.Errorf("credential account_id is required for kind %q", provider.CredOAuthRefresh)
		}
		return nil
	case provider.CredKiroRefresh:
		if strings.TrimSpace(c.RefreshToken) == "" {
			return fmt.Errorf("credential refresh_token is required for kind %q", c.Kind)
		}
		if (c.ClientID == "") != (c.ClientSecret == "") {
			return fmt.Errorf("kiro client_id and client_secret must be provided together")
		}
		for _, region := range []string{c.Region, c.APIRegion} {
			if region != "" && !awsRegion.MatchString(region) {
				return fmt.Errorf("invalid kiro region %q", region)
			}
		}
		if c.ProfileARN != "" {
			parts := strings.Split(c.ProfileARN, ":")
			if len(parts) < 6 || parts[0] != "arn" || !awsRegion.MatchString(parts[3]) || strings.ContainsAny(c.ProfileARN, "\r\n\t ") {
				return fmt.Errorf("invalid kiro profile_arn")
			}
		}
		return nil
	default:
		return fmt.Errorf("unsupported credential kind %q, supported: %s", c.Kind, kindList())
	}
}

func (c Credential) KiroAuthRegion() string {
	if c.Region != "" {
		return c.Region
	}
	return "us-east-1"
}

func (c Credential) KiroAPIRegion() string {
	if c.APIRegion != "" {
		return c.APIRegion
	}
	if parts := strings.Split(c.ProfileARN, ":"); len(parts) >= 6 && awsRegion.MatchString(parts[3]) {
		return parts[3]
	}
	return c.KiroAuthRegion()
}

// ValidateAgainstProvider 校验凭据形态与 provider 声明一致。
func (c Credential) ValidateAgainstProvider(spec provider.Spec) error {
	if c.Kind != spec.Credential {
		return fmt.Errorf("provider %q expects credential kind %q, got %q", spec.ID, spec.Credential, c.Kind)
	}
	return c.Validate()
}

func (c Credential) Encode() ([]byte, error) { return json.Marshal(c) }

// Redact 转成脱敏视图，用于任何会离开进程且非下发面的路径。
func (c Credential) Redact() Redacted {
	refresh := Mask(c.RefreshToken)
	if c.Kind == provider.CredKiroRefresh {
		refresh = maskConsoleToken(c.RefreshToken)
	}
	return Redacted{
		Kind:                   c.Kind,
		APIKey:                 Mask(c.APIKey),
		RefreshToken:           refresh,
		AccountID:              c.AccountID,
		WebRefreshToken:        Mask(c.WebRefreshToken),
		ConsoleAccessToken:     maskConsoleToken(c.ConsoleAccessToken),
		BailianAccessKeyID:     maskConsoleToken(c.BailianAccessKeyID),
		BailianAccessKeySecret: maskConsoleToken(c.BailianAccessKeySecret),
		ConsoleVerifiedAt:      c.ConsoleVerifiedAt,
		ProfileARN:             c.ProfileARN,
		Region:                 c.Region,
		APIRegion:              c.APIRegion,
		ClientID:               c.ClientID,
		ClientSecret:           maskConsoleToken(c.ClientSecret),
	}
}

// String 保证凭据不会因日志格式化而泄露。
func (c Credential) String() string {
	v := c.Redact()
	return fmt.Sprintf("Credential{Kind:%q APIKey:%s RefreshToken:%s AccessToken:%s WebRefreshToken:%s ConsoleAccessToken:%s BailianAccessKeyID:%s BailianAccessKeySecret:%s ClientSecret:%s}",
		c.Kind, v.APIKey, v.RefreshToken, maskConsoleToken(c.AccessToken), v.WebRefreshToken, v.ConsoleAccessToken, v.BailianAccessKeyID, v.BailianAccessKeySecret, v.ClientSecret)
}

// Redacted 脱敏视图。AccessToken 是短效续期产物,不下发;AccountID 是标识
// 不是秘密,明文下发(管理面展示授权归属)。
type Redacted struct {
	Kind                   provider.CredentialKind `json:"kind"`
	APIKey                 string                  `json:"api_key,omitempty"`
	RefreshToken           string                  `json:"refresh_token,omitempty"`
	AccountID              string                  `json:"account_id,omitempty"`
	WebRefreshToken        string                  `json:"web_refresh_token,omitempty"`
	ConsoleAccessToken     string                  `json:"console_access_token,omitempty"`
	BailianAccessKeyID     string                  `json:"bailian_access_key_id,omitempty"`
	BailianAccessKeySecret string                  `json:"bailian_access_key_secret,omitempty"`
	ConsoleVerifiedAt      time.Time               `json:"console_verified_at,omitzero"`
	ProfileARN             string                  `json:"profile_arn,omitempty"`
	Region                 string                  `json:"region,omitempty"`
	APIRegion              string                  `json:"api_region,omitempty"`
	ClientID               string                  `json:"client_id,omitempty"`
	ClientSecret           string                  `json:"client_secret,omitempty"`
}

// 控制台令牌不保留任何前后缀,避免暴露 JWT 片段。
func maskConsoleToken(s string) string {
	if s == "" {
		return ""
	}
	return "***"
}

// Mask 短值全掩，长值保留前 4 后 4。
func Mask(s string) string {
	if s == "" {
		return ""
	}
	if len(s) <= 8 {
		return "***"
	}
	return s[:4] + "***" + s[len(s)-4:]
}

func supported(k provider.CredentialKind) bool {
	for _, s := range SupportedKinds {
		if s == k {
			return true
		}
	}
	return false
}

func kindList() string {
	out := make([]string, 0, len(SupportedKinds))
	for _, k := range SupportedKinds {
		out = append(out, string(k))
	}
	return strings.Join(out, ", ")
}
