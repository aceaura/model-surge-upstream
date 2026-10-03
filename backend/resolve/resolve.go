// Package resolve 把 provider/account/model 三层压平成一个请求目标。
// 只回答「目标长什么样」：不接收、不改写、不转发调用方的上游请求体。
package resolve

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/aceaura/model-surge-upstream/backend/account"
	"github.com/aceaura/model-surge-upstream/backend/apperr"
	"github.com/aceaura/model-surge-upstream/backend/codex"
	"github.com/aceaura/model-surge-upstream/backend/credential"
	"github.com/aceaura/model-surge-upstream/backend/model"
	"github.com/aceaura/model-surge-upstream/backend/oauth"
	"github.com/aceaura/model-surge-upstream/backend/provider"
)

// ResolvedTarget 是下发给调用方的目标描述。Headers 含认证头，
// 因此 String() 脱敏而 MarshalJSON 输出原值——响应带凭据、日志不带凭据
// 由类型本身保证，不依赖调用点自觉。
//
// Defaults、Overrides 与 Compact 都原样下发而不在此合并：三者语义各异
// （缺失才填 / 强制压盖 / 压缩策略），调用方要把它们作用到自己构造的
// 上游请求体或处置逻辑上，合并后就分不清了。
type ResolvedTarget struct {
	ModelID       string            `json:"model_id"`
	Account       string            `json:"account"`
	ProviderID    string            `json:"provider_id"`
	Protocol      string            `json:"protocol"`
	BaseURL       string            `json:"base_url"`
	NativeModel   string            `json:"native_model"`
	ContextWindow int               `json:"context_window,omitempty"`
	Headers       map[string]string `json:"headers"`
	Defaults      json.RawMessage   `json:"defaults"`
	Overrides     json.RawMessage   `json:"overrides"`
	Compact       json.RawMessage   `json:"compact,omitempty"`
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

// Tokens 是 OAuth 登录态的 access_token 来源(oauth.Manager 实现):
// 凭据里 token 临期时由它负责续期并落库。
type Tokens interface {
	AccessToken(ctx context.Context, acc account.Account) (string, error)
}

type Resolver struct {
	accounts Accounts
	models   Models
	tokens   Tokens
}

func NewResolver(accounts Accounts, models Models) *Resolver {
	return &Resolver{accounts: accounts, models: models}
}

// WithTokens 挂上 OAuth token 来源:有 oauth_refresh 形态凭据的账号时
// 必须装配,否则解析到这类账号报错。
func (r *Resolver) WithTokens(t Tokens) *Resolver {
	r.tokens = t
	return r
}

func (r *Resolver) Resolve(ctx context.Context, modelID string) (ResolvedTarget, error) {
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

	headers, err := r.authHeaders(ctx, spec, acc)
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
		Headers:       headers,
		Defaults:      m.Defaults,
		Overrides:     m.Overrides,
		Compact:       m.Compact,
	}, nil
}

// HeadersFor 把 authHeaders 暴露给管理面连通性检测等旁路调用:它们刻意
// 不走 Resolve(启用态校验会挡住未启用模型),但 oauth 账号的头构造
// (取活体 token、套 codex 头集)离不开 token 来源。
func (r *Resolver) HeadersFor(ctx context.Context, spec provider.Spec, acc account.Account) (map[string]string, error) {
	return r.authHeaders(ctx, spec, acc)
}

// authHeaders 按凭据形态分派:oauth_refresh 走 token 来源取活体
// access_token 并套 codex 头集(订阅登录态目前只有 codex 一种);
// 其余沿用 provider 声明的静态认证头形态。
func (r *Resolver) authHeaders(ctx context.Context, spec provider.Spec, acc account.Account) (map[string]string, error) {
	if acc.Credential.Kind != provider.CredOAuthRefresh {
		return AuthHeaders(spec, acc), nil
	}
	if r.tokens == nil {
		return nil, apperr.New(apperr.InvalidCredential,
			fmt.Sprintf("account %q uses oauth credential but token source is not wired", acc.Name))
	}
	token, err := r.tokens.AccessToken(ctx, acc)
	if err != nil {
		if errors.Is(err, oauth.ErrNeedsReauth) {
			return nil, apperr.New(apperr.InvalidCredential,
				fmt.Sprintf("account %q login state expired, re-paste credentials", acc.Name))
		}
		return nil, apperr.Wrap(apperr.UpstreamUnavailable, "refresh oauth token", err)
	}
	out := codex.Headers(token, acc.Credential.AccountID)
	// 账号自定义头后写,允许运维者覆盖默认头(与静态形态同语义)。
	for k, v := range acc.Headers {
		out[k] = v
	}
	return out, nil
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
