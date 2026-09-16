// Package resolve 把 provider/account/model 三层压平成一个请求目标。
// 只回答「目标长什么样」：不接收、不改写、不转发调用方的上游请求体。
package resolve

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/aceaura/model-surge-upstream/backend/account"
	"github.com/aceaura/model-surge-upstream/backend/apperr"
	"github.com/aceaura/model-surge-upstream/backend/credential"
	"github.com/aceaura/model-surge-upstream/backend/model"
	"github.com/aceaura/model-surge-upstream/backend/provider"
)

// ResolvedTarget 是下发给调用方的目标描述。Headers 含认证头，
// 因此 String() 脱敏而 MarshalJSON 输出原值——响应带凭据、日志不带凭据
// 由类型本身保证，不依赖调用点自觉。
type ResolvedTarget struct {
	ModelID       string            `json:"model_id"`
	Account       string            `json:"account"`
	ProviderID    string            `json:"provider_id"`
	Protocol      string            `json:"protocol"`
	BaseURL       string            `json:"base_url"`
	NativeModel   string            `json:"native_model"`
	ContextWindow int               `json:"context_window,omitempty"`
	Headers       map[string]string `json:"headers"`
	Params        json.RawMessage   `json:"params"`
}

// 认证头名。
const (
	headerAuthorization    = "Authorization"
	headerAnthropicAPIKey  = "x-api-key"
	headerAnthropicVersion = "anthropic-version"
)

// sensitiveHeaders 是日志脱敏时需要遮蔽的头。
var sensitiveHeaders = map[string]bool{
	headerAuthorization:   true,
	headerAnthropicAPIKey: true,
}

func (t ResolvedTarget) String() string {
	return fmt.Sprintf("ResolvedTarget{ModelID:%q Protocol:%q BaseURL:%q NativeModel:%q Headers:%v}",
		t.ModelID, t.Protocol, t.BaseURL, t.NativeModel, redactHeaders(t.Headers))
}

func redactHeaders(h map[string]string) map[string]string {
	out := make(map[string]string, len(h))
	for k, v := range h {
		if sensitiveHeaders[k] {
			out[k] = credential.Mask(v)
			continue
		}
		out[k] = v
	}
	return out
}

// Accounts 与 Models 是 resolve 需要的最小读取能力。
type Accounts interface {
	Get(ctx context.Context, name string) (account.Account, error)
}

type Models interface {
	Get(ctx context.Context, id string) (model.Model, error)
	List(ctx context.Context, account string) ([]model.Model, error)
}

type Resolver struct {
	accounts Accounts
	models   Models
}

func NewResolver(accounts Accounts, models Models) *Resolver {
	return &Resolver{accounts: accounts, models: models}
}

func (r *Resolver) Resolve(ctx context.Context, modelID string, clientParams json.RawMessage) (ResolvedTarget, error) {
	m, err := r.models.Get(ctx, modelID)
	if err != nil {
		return ResolvedTarget{}, err
	}
	if !m.Enabled {
		return ResolvedTarget{}, apperr.New(apperr.ModelDisabled, fmt.Sprintf("model %q is disabled", modelID))
	}
	acc, err := r.accounts.Get(ctx, m.Account)
	if err != nil {
		return ResolvedTarget{}, err
	}
	if !acc.Enabled {
		return ResolvedTarget{}, apperr.New(apperr.AccountDisabled,
			fmt.Sprintf("account %q is disabled", acc.Name))
	}
	spec, ok := acc.Spec()
	if !ok {
		return ResolvedTarget{}, apperr.New(apperr.InvalidProvider,
			fmt.Sprintf("account %q references unknown provider %q", acc.Name, acc.ProviderID))
	}

	params, err := MergeParams(m.Defaults, clientParams, m.Overrides)
	if err != nil {
		return ResolvedTarget{}, err
	}

	return ResolvedTarget{
		ModelID:       m.ID,
		Account:       acc.Name,
		ProviderID:    spec.ID,
		Protocol:      m.Protocol,
		BaseURL:       acc.EffectiveBaseURL(spec),
		NativeModel:   m.NativeModel,
		ContextWindow: m.ContextWindow,
		Headers:       AuthHeaders(spec, acc),
		Params:        params,
	}, nil
}

// AuthHeaders 依 provider 声明的认证头形态生成头，再叠加账号自定义头。
// 账号自定义头后写，允许运维者覆盖默认头（如自建网关的版本要求）。
func AuthHeaders(spec provider.Spec, acc account.Account) map[string]string {
	out := map[string]string{}
	switch spec.Auth {
	case provider.AuthAnthropicKey:
		out[headerAnthropicAPIKey] = acc.Credential.APIKey
		out[headerAnthropicVersion] = provider.AnthropicVersion()
	default:
		out[headerAuthorization] = "Bearer " + acc.Credential.APIKey
	}
	for k, v := range acc.Headers {
		out[k] = v
	}
	return out
}

// Listing 是下发面模型列举形态：不含凭据。
type Listing struct {
	ID            string `json:"id"`
	Account       string `json:"account"`
	ProviderID    string `json:"provider_id"`
	Protocol      string `json:"protocol"`
	NativeModel   string `json:"native_model"`
	ContextWindow int    `json:"context_window,omitempty"`
	Enabled       bool   `json:"enabled"`
}

// List 列举模型并附上其账号的 provider。账号停用时模型一并标记为不可用。
func (r *Resolver) List(ctx context.Context) ([]Listing, error) {
	models, err := r.models.List(ctx, "")
	if err != nil {
		return nil, err
	}
	specByAccount := map[string]string{}
	enabledByAccount := map[string]bool{}
	out := make([]Listing, 0, len(models))
	for _, m := range models {
		providerID, seen := specByAccount[m.Account]
		if !seen {
			acc, err := r.accounts.Get(ctx, m.Account)
			if err != nil {
				return nil, err
			}
			providerID = acc.ProviderID
			specByAccount[m.Account] = providerID
			enabledByAccount[m.Account] = acc.Enabled
		}
		out = append(out, Listing{
			ID:            m.ID,
			Account:       m.Account,
			ProviderID:    providerID,
			Protocol:      m.Protocol,
			NativeModel:   m.NativeModel,
			ContextWindow: m.ContextWindow,
			Enabled:       m.Enabled && enabledByAccount[m.Account],
		})
	}
	return out, nil
}
