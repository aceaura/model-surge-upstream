// Package provider 维护内置提供商规格表。规格是编译期常量：运行期不可修改、
// 不持久化。注册期的冲突与缺项直接 panic，让配置错误在进程启动时暴露。
package provider

import "fmt"

// 协议标识。
const (
	ProtocolAnthropic       = "anthropic"
	ProtocolChatCompletions = "chat_completions"
	ProtocolResponses       = "responses"
	ProtocolGemini          = "gemini"
)

// AuthScheme 认证头形态。
type AuthScheme string

const (
	AuthBearer       AuthScheme = "bearer"        // Authorization: Bearer <key>
	AuthAnthropicKey AuthScheme = "anthropic_key" // x-api-key + anthropic-version
)

// CredentialKind 凭据形态判别式。本期只支持静态密钥。
type CredentialKind string

const CredAPIKey CredentialKind = "api_key"

// ResetRule 额度重置规律。
type ResetRule string

const (
	ResetNone    ResetRule = "none"
	ResetRolling ResetRule = "rolling"
	ResetDaily   ResetRule = "daily"
	ResetMonthly ResetRule = "monthly"
	ResetPrepaid ResetRule = "prepaid"
)

// MeterKind 计量项形态。上游额度不止一种语义：预付费看余额，后付费只有
// 已用量，订阅制与速率窗口则是周期配额。
type MeterKind string

const (
	MeterBalance   MeterKind = "balance"    // 预付费余额，充值才涨
	MeterUsage     MeterKind = "usage"      // 后付费已用量，可能带月度上限
	MeterRateLimit MeterKind = "rate_limit" // 滚动速率窗口，如 RPM/TPM
)

// MeterUnit 计量单位。数值本身说不出自己是钱、请求数还是 token，
// 必须显式声明，否则运维只能靠币种字段有没有值去猜。
type MeterUnit string

const (
	UnitCurrency MeterUnit = "currency"
	UnitRequests MeterUnit = "requests"
	UnitTokens   MeterUnit = "tokens"
	UnitCredits  MeterUnit = "credits"
)

// QuotaAPI 额度查询接口声明。provider 未声明时 Spec.Quota 为 nil。
// Kind/Unit/Reset 描述该端点主计量项的形态，作为解析结果的兜底：
// 上游响应自带更精确的信息时以响应为准。
type QuotaAPI struct {
	Path   string    `json:"path"`
	Method string    `json:"method"`
	Kind   MeterKind `json:"kind"`
	Unit   MeterUnit `json:"unit"`
	Reset  ResetRule `json:"reset"`
}

// ModelsAPI 上游模型列举接口声明。provider 未声明时 Spec.Models 为 nil，
// 表示该上游没有可用的列举端点（无此接口，或实测恒鉴权失败）。
type ModelsAPI struct {
	Path   string `json:"path"`
	Method string `json:"method"`
}

type Spec struct {
	ID          string         `json:"id"`
	DisplayName string         `json:"display_name"`
	Website     string         `json:"website"`
	BaseURL     string         `json:"base_url"`
	Protocols   []string       `json:"protocols"`
	Auth        AuthScheme     `json:"auth"`
	Credential  CredentialKind `json:"credential"`
	Quota       *QuotaAPI      `json:"quota,omitempty"`
	Models      *ModelsAPI     `json:"models,omitempty"`
}

func (s Spec) Supports(protocol string) bool {
	for _, p := range s.Protocols {
		if p == protocol {
			return true
		}
	}
	return false
}

var (
	specs   []Spec
	byID    = map[string]int{}
	anthVer = "2023-06-01"
)

// AnthropicVersion 是 anthropic_key 形态附带的版本头取值。
func AnthropicVersion() string { return anthVer }

func register(s Spec) {
	if s.ID == "" {
		panic("provider: empty id")
	}
	if _, dup := byID[s.ID]; dup {
		panic(fmt.Sprintf("provider: duplicate id %q", s.ID))
	}
	if s.BaseURL == "" {
		panic(fmt.Sprintf("provider %q: base_url is required", s.ID))
	}
	if len(s.Protocols) == 0 {
		panic(fmt.Sprintf("provider %q: at least one protocol is required", s.ID))
	}
	if s.Quota != nil && (s.Quota.Kind == "" || s.Quota.Unit == "") {
		panic(fmt.Sprintf("provider %q: quota must declare kind and unit", s.ID))
	}
	byID[s.ID] = len(specs)
	specs = append(specs, s)
}

func Get(id string) (Spec, bool) {
	i, ok := byID[id]
	if !ok {
		return Spec{}, false
	}
	return specs[i], true
}

// All 返回注册序的全部规格。调用方拿到的是副本，改不动内置表。
func All() []Spec {
	out := make([]Spec, len(specs))
	copy(out, specs)
	return out
}

// IDs 返回全部稳定标识，用于错误消息里列出可选值。
func IDs() []string {
	out := make([]string, 0, len(specs))
	for _, s := range specs {
		out = append(out, s.ID)
	}
	return out
}
