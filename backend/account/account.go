// Package account 是账号仓储：PostgreSQL 权威，Redis 只读投影。
// 不引用 model 包——两者的关联由上层编排，仓储之间不留横向边。
package account

import (
	"regexp"
	"time"

	"github.com/aceaura/model-surge-upstream/backend/credential"
	"github.com/aceaura/model-surge-upstream/backend/provider"
)

// QuotaSettings 账号级额度查询配置。查询本身由 provider 的 Go 内置实现
// 完成(见 quota 包 builtinQuotas),这里承载总开关与两个调度间隔。
type QuotaSettings struct {
	// Enabled 实时额度查询总开关。nil 表示未表态,按开启处理——老账号
	// 没有这个字段,默认行为必须与加开关之前一致。
	Enabled *bool `json:"enabled,omitempty"`
	// AutoIntervalMinutes 客户端自动刷新间隔,0 表示不自动刷。
	AutoIntervalMinutes int `json:"auto_interval_minutes,omitempty"`
	// StopIntervalMinutes 账号无请求超过该间隔后,自动刷新停打上游
	// (0 走默认 5 分钟),下一次请求到达自动恢复。
	StopIntervalMinutes int `json:"stop_interval_minutes,omitempty"`
}

// Empty 判定是否未配置:开关未表态且两间隔均为 0 与未配置同义。
func (s *QuotaSettings) Empty() bool {
	return s == nil ||
		(s.Enabled == nil && s.AutoIntervalMinutes == 0 && s.StopIntervalMinutes == 0)
}

// QuotaEnabled 实时额度查询是否开启:未配置或未表态均按开启。
func (s *QuotaSettings) QuotaEnabled() bool {
	return s == nil || s.Enabled == nil || *s.Enabled
}

type Account struct {
	Name       string                `json:"name"`
	ProviderID string                `json:"provider_id"`
	Credential credential.Credential `json:"credential"`
	// BaseURL 为空表示沿用 provider 默认根地址。
	BaseURL string            `json:"base_url"`
	Headers map[string]string `json:"headers"`
	// QuotaSettings 为 nil 表示未配置查询节奏(不自动刷、停止间隔走默认)。
	QuotaSettings *QuotaSettings `json:"quota_settings,omitempty"`
	Enabled       bool           `json:"enabled"`
	// SortOrder 账号页拖拽排序序号,小者在前;并列回落 name 序。
	// 不进管理面视图:列表数组顺序即顺序,单账号读取无需暴露它。
	SortOrder int       `json:"-"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// View 是管理面读取形态：凭据脱敏。
type View struct {
	Name          string              `json:"name"`
	ProviderID    string              `json:"provider_id"`
	Credential    credential.Redacted `json:"credential"`
	BaseURL       string              `json:"base_url"`
	Headers       map[string]string   `json:"headers"`
	QuotaSettings *QuotaSettings      `json:"quota_settings,omitempty"`
	Enabled       bool                `json:"enabled"`
	CreatedAt     time.Time           `json:"created_at"`
	UpdatedAt     time.Time           `json:"updated_at"`
}

func (a Account) View() View {
	return View{
		Name:          a.Name,
		ProviderID:    a.ProviderID,
		Credential:    a.Credential.Redact(),
		BaseURL:       a.BaseURL,
		Headers:       a.Headers,
		QuotaSettings: a.QuotaSettings,
		Enabled:       a.Enabled,
		CreatedAt:     a.CreatedAt,
		UpdatedAt:     a.UpdatedAt,
	}
}

// Spec 返回账号所属 provider 的规格。
func (a Account) Spec() (provider.Spec, bool) { return provider.Get(a.ProviderID) }

// EffectiveBaseURL 优先取账号覆盖值，否则用 provider 默认值。
func (a Account) EffectiveBaseURL(spec provider.Spec) string {
	base := spec.BaseURL
	if a.BaseURL != "" {
		base = a.BaseURL
	}
	if spec.ID == "kiro.global.subscribe.standard" {
		if a.BaseURL == "" || a.BaseURL == spec.BaseURL {
			base = "https://runtime." + a.Credential.KiroAPIRegion() + ".kiro.dev"
		}
		if a.Credential.ProfileARN == "" {
			if match := kiroRuntimeBase.FindStringSubmatch(base); match != nil {
				return "https://q." + match[1] + ".amazonaws.com"
			}
		}
	}
	return base
}

var kiroRuntimeBase = regexp.MustCompile(`^https://runtime\.([a-z0-9-]+)\.kiro\.dev/?$`)
