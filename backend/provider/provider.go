// Package provider 维护内置提供商规格表。规格是编译期常量：运行期不可修改、
// 不持久化。注册期的冲突与缺项直接 panic，让配置错误在进程启动时暴露。
package provider

import (
	"crypto/sha256"
	"fmt"
	"net/http"
	"strings"
)

func UpstreamURL(providerID, baseURL, suffix string) string {
	baseURL = strings.TrimRight(baseURL, "/")
	if providerID == "deepseek.global.api.standard" &&
		(suffix == "/v1/messages" || strings.HasPrefix(suffix, "/v1/messages/")) &&
		!strings.HasSuffix(baseURL, "/anthropic") {
		suffix = "/anthropic" + suffix
	}
	return baseURL + suffix
}

func ApplyRequestHeaders(providerID, protocol, account, sessionKey string, headers http.Header) {
	if providerID != "opencode.global.api.zen" && providerID != "opencode.global.subscribe.go" {
		return
	}
	if protocol == ProtocolAnthropic {
		auth := headers.Get("Authorization")
		if len(auth) >= 7 && strings.EqualFold(auth[:7], "Bearer ") {
			headers.Set("x-api-key", strings.TrimSpace(auth[7:]))
		}
		headers.Del("Authorization")
		headers.Set("anthropic-version", AnthropicVersion())
	} else {
		headers.Del("x-api-key")
	}
	if strings.TrimSpace(headers.Get("User-Agent")) == "" {
		headers.Set("User-Agent", "ModelSurgeUpstream/1.0")
	}
	if sessionKey == "" {
		return
	}
	for _, name := range []string{"x-opencode-session", "session_id", "x-session-id", "conversation_id", "x-conversation-id"} {
		if value := headers.Get(name); strings.TrimSpace(value) != "" {
			if name != "x-opencode-session" {
				headers.Set("x-opencode-session", value)
			}
			return
		}
	}
	// Hash local identities so upstream telemetry does not receive account names or cache keys.
	sum := sha256.Sum256([]byte(fmt.Sprintf("opencode:%d:%s:%s", len(account), account, sessionKey)))
	headers.Set("x-opencode-session", fmt.Sprintf("msu-%x", sum[:16]))
}

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

// CredentialKind 凭据形态判别式。api_key 是静态密钥;oauth_refresh 是
// OAuth 刷新型登录态(refresh_token 续期 access_token,如 ChatGPT 订阅)。
type CredentialKind string

const (
	CredAPIKey       CredentialKind = "api_key"
	CredOAuthRefresh CredentialKind = "oauth_refresh"
	CredKiroRefresh  CredentialKind = "kiro_refresh"
)

// Billing 计费模式判别式:订阅制按周期配额收费(如 Kimi For Coding),
// 按量计费按实际用量结算(预付费余额或后付费账单)。
type Billing string

const (
	BillingSubscription Billing = "subscription" // 订阅
	BillingPayGo        Billing = "paygo"        // 按量计费
)

// 服务区域取值。存储英文、展示层译中文;取值开放(不限于这两个),
// 阿里云类云厂商还可能拆出更多分区。
const (
	RegionCN     = "CN"
	RegionGlobal = "Global"
)

// PlanStandard 是厂商只有一种服务类型时的统一标签:表单服务类型级
// 对所有供应商渲染,这类厂商只有"标准"一个选项(自动落定、不可改)。
// 订阅档位(如 ChatGPT Plus/Pro)不是服务类型,不拆变体。
const PlanStandard = "Standard"

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
	// UnitPercent 周期配额的已用百分比(订阅窗口形态,如 5 小时/7 天窗口)。
	UnitPercent MeterUnit = "percent"
)

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
	Billing     Billing        `json:"billing"`
	// Region 服务区域(CN/Global 或厂商自定义分区),普通字符串:
	// 值域开放,只强制非空。
	Region string `json:"region"`
	Plan   string `json:"plan,omitempty"`
	// QuotaQueryable 表示该 provider 有内置的额度查询实现(quota 包按
	// provider ID 自动整合)。
	QuotaQueryable bool       `json:"quota_queryable,omitempty"`
	Models         *ModelsAPI `json:"models,omitempty"`
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
	switch s.Billing {
	case BillingSubscription, BillingPayGo:
	default:
		panic(fmt.Sprintf("provider %q: billing must be subscription or paygo", s.ID))
	}
	if s.Region == "" {
		panic(fmt.Sprintf("provider %q: region is required", s.ID))
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
