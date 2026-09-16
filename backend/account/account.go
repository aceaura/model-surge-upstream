// Package account 是账号仓储：PostgreSQL 权威，Redis 只读投影。
// 不引用 model 包——两者的关联由上层编排，仓储之间不留横向边。
package account

import (
	"time"

	"github.com/aceaura/model-surge-upstream/backend/credential"
	"github.com/aceaura/model-surge-upstream/backend/provider"
)

type Account struct {
	Name       string                `json:"name"`
	ProviderID string                `json:"provider_id"`
	Credential credential.Credential `json:"credential"`
	// BaseURL 为空表示沿用 provider 默认根地址。
	BaseURL   string            `json:"base_url"`
	Headers   map[string]string `json:"headers"`
	Enabled   bool              `json:"enabled"`
	CreatedAt time.Time         `json:"created_at"`
	UpdatedAt time.Time         `json:"updated_at"`
}

// View 是管理面读取形态：凭据脱敏。
type View struct {
	Name       string              `json:"name"`
	ProviderID string              `json:"provider_id"`
	Credential credential.Redacted `json:"credential"`
	BaseURL    string              `json:"base_url"`
	Headers    map[string]string   `json:"headers"`
	Enabled    bool                `json:"enabled"`
	CreatedAt  time.Time           `json:"created_at"`
	UpdatedAt  time.Time           `json:"updated_at"`
}

func (a Account) View() View {
	return View{
		Name:       a.Name,
		ProviderID: a.ProviderID,
		Credential: a.Credential.Redact(),
		BaseURL:    a.BaseURL,
		Headers:    a.Headers,
		Enabled:    a.Enabled,
		CreatedAt:  a.CreatedAt,
		UpdatedAt:  a.UpdatedAt,
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
