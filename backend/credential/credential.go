// Package credential 解析与脱敏账号凭据。凭据以带判别式的 JSON 存储，
// kind 字段决定具体结构，为后续接入刷新型凭据留出扩展位。
//
// 脱敏靠类型区分而非调用点自觉：Redacted 是独立类型，管理面读取路径返回它，
// 解析路径返回 Credential 本身。忘记脱敏会体现为类型不匹配，而不是静默泄露。
package credential

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/aceaura/model-surge-upstream/backend/provider"
)

// SupportedKinds 是本期支持的凭据形态，顺序固定用于错误消息。
var SupportedKinds = []provider.CredentialKind{provider.CredAPIKey, provider.CredOAuthRefresh}

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
}

// Decode 两步解码：先取 kind，再按 kind 校验具体字段。
func Decode(raw []byte) (Credential, error) {
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
	if err := c.Validate(); err != nil {
		return Credential{}, err
	}
	return c, nil
}

func (c Credential) Validate() error {
	switch c.Kind {
	case provider.CredAPIKey:
		if strings.TrimSpace(c.APIKey) == "" {
			return fmt.Errorf("credential api_key is required for kind %q", provider.CredAPIKey)
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
	default:
		return fmt.Errorf("unsupported credential kind %q, supported: %s", c.Kind, kindList())
	}
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
	return Redacted{
		Kind:         c.Kind,
		APIKey:       Mask(c.APIKey),
		RefreshToken: Mask(c.RefreshToken),
		AccountID:    c.AccountID,
	}
}

// String 保证凭据不会因日志格式化而泄露。
func (c Credential) String() string {
	return fmt.Sprintf("Credential{Kind:%q APIKey:%s RefreshToken:%s AccessToken:%s}",
		c.Kind, Mask(c.APIKey), Mask(c.RefreshToken), Mask(c.AccessToken))
}

// Redacted 脱敏视图。AccessToken 是短效续期产物,不下发;AccountID 是标识
// 不是秘密,明文下发(管理面展示授权归属)。
type Redacted struct {
	Kind         provider.CredentialKind `json:"kind"`
	APIKey       string                  `json:"api_key,omitempty"`
	RefreshToken string                  `json:"refresh_token,omitempty"`
	AccountID    string                  `json:"account_id,omitempty"`
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
