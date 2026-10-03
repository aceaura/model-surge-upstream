// Package account 是账号仓储：PostgreSQL 权威，Redis 只读投影。
// 不引用 model 包——两者的关联由上层编排，仓储之间不留横向边。
package account

import (
	"time"

	"github.com/aceaura/model-surge-upstream/backend/credential"
	"github.com/aceaura/model-surge-upstream/backend/provider"
)

// QuotaScript 账号级额度查询脚本(CC Switch usage_script 同款机制):
// 内置 provider 未声明额度接口、或其响应形态超出通用解析时,用一段
// JS(request + extractor)定制查询。代码里 {{apiKey}}/{{baseUrl}} 占位符
// 在执行时替换为账号内置凭据与生效地址;oauth_refresh 账号另可用
// {{accessToken}}/{{accountId}}(执行前续期取活体 token)。脚本本身不存密钥。
type QuotaScript struct {
	Enabled bool   `json:"enabled"`
	Code    string `json:"code"`
	// TimeoutSeconds 脚本与上游请求的超时,0 走默认。
	TimeoutSeconds int `json:"timeout_seconds,omitempty"`
	// AutoIntervalMinutes 客户端自动刷新间隔,0 表示不自动刷。
	AutoIntervalMinutes int `json:"auto_interval_minutes,omitempty"`
}

// Active 判定脚本是否参与额度查询:启用且代码非空。
func (s *QuotaScript) Active() bool {
	return s != nil && s.Enabled && s.Code != ""
}

type Account struct {
	Name       string                `json:"name"`
	ProviderID string                `json:"provider_id"`
	Credential credential.Credential `json:"credential"`
	// BaseURL 为空表示沿用 provider 默认根地址。
	BaseURL   string            `json:"base_url"`
	Headers   map[string]string `json:"headers"`
	// QuotaScript 为 nil 表示未配置额度脚本。
	QuotaScript *QuotaScript `json:"quota_script,omitempty"`
	Enabled     bool         `json:"enabled"`
	CreatedAt   time.Time    `json:"created_at"`
	UpdatedAt   time.Time    `json:"updated_at"`
}

// View 是管理面读取形态：凭据脱敏。
type View struct {
	Name        string              `json:"name"`
	ProviderID  string              `json:"provider_id"`
	Credential  credential.Redacted `json:"credential"`
	BaseURL     string              `json:"base_url"`
	Headers     map[string]string   `json:"headers"`
	QuotaScript *QuotaScript        `json:"quota_script,omitempty"`
	Enabled     bool                `json:"enabled"`
	CreatedAt   time.Time           `json:"created_at"`
	UpdatedAt   time.Time           `json:"updated_at"`
}

func (a Account) View() View {
	return View{
		Name:        a.Name,
		ProviderID:  a.ProviderID,
		Credential:  a.Credential.Redact(),
		BaseURL:     a.BaseURL,
		Headers:     a.Headers,
		QuotaScript: a.QuotaScript,
		Enabled:     a.Enabled,
		CreatedAt:   a.CreatedAt,
		UpdatedAt:   a.UpdatedAt,
	}
}

// Spec 返回账号所属 provider 的规格。
func (a Account) Spec() (provider.Spec, bool) { return provider.Get(a.ProviderID) }

// EffectiveBaseURL 优先取账号覆盖值，否则用 provider 默认值。
func (a Account) EffectiveBaseURL(spec provider.Spec) string {
	if a.BaseURL != "" {
		return a.BaseURL
	}
	return spec.BaseURL
}
