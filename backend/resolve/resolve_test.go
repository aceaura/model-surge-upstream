package resolve

import (
	"context"
	"encoding/json"
	"slices"
	"strings"
	"testing"

	"github.com/aceaura/model-surge-upstream/backend/account"
	"github.com/aceaura/model-surge-upstream/backend/apperr"
	"github.com/aceaura/model-surge-upstream/backend/credential"
	"github.com/aceaura/model-surge-upstream/backend/effort"
	"github.com/aceaura/model-surge-upstream/backend/model"
	"github.com/aceaura/model-surge-upstream/backend/oauth"
	"github.com/aceaura/model-surge-upstream/backend/provider"
)

const secret = "sk-abcdefghijklmnop"

type fakeAccounts map[string]account.Account

func (f fakeAccounts) Get(_ context.Context, name string) (account.Account, error) {
	acc, ok := f[name]
	if !ok {
		return account.Account{}, apperr.New(apperr.NotFound, "account "+name+" not found")
	}
	return acc, nil
}

type fakeModels map[string]model.Model

func (f fakeModels) Get(_ context.Context, id string) (model.Model, error) {
	m, ok := f[id]
	if !ok {
		return model.Model{}, apperr.New(apperr.NotFound, "model "+id+" not found")
	}
	return m, nil
}

func (f fakeModels) List(_ context.Context, accountName string) ([]model.Model, error) {
	out := []model.Model{}
	for _, m := range f {
		if accountName == "" || m.Account == accountName {
			out = append(out, m)
		}
	}
	return out, nil
}

func acct(name, providerID string) account.Account {
	return account.Account{
		Name:       name,
		ProviderID: providerID,
		Credential: credential.Credential{Kind: provider.CredAPIKey, APIKey: secret},
		Headers:    map[string]string{},
		Enabled:    true,
	}
}

func mdl(id, accountName, protocol string) model.Model {
	return model.Model{
		ID:            id,
		Account:       accountName,
		NativeModel:   "kimi-k2-turbo",
		Protocol:      protocol,
		ContextWindow: 262144,
		Defaults:      json.RawMessage(`{}`),
		Overrides:     json.RawMessage(`{}`),
		Enabled:       true,
	}
}

func fixture() (fakeAccounts, fakeModels, *Resolver) {
	accounts := fakeAccounts{"kimi-1": acct("kimi-1", "kimi/coding")}
	models := fakeModels{"kimi-1/k2": mdl("kimi-1/k2", "kimi-1", provider.ProtocolAnthropic)}
	return accounts, models, NewResolver(accounts, models)
}

func TestResolveKiroHeaders(t *testing.T) {
	acc := account.Account{Name: "kiro-1", ProviderID: "kiro", Enabled: true,
		Credential: credential.Credential{Kind: provider.CredKiroRefresh, RefreshToken: "rt", ProfileARN: "arn:aws:codewhisperer:eu-central-1:123:profile/test"}}
	accounts := fakeAccounts{acc.Name: acc}
	m := mdl("kiro-1/sonnet", acc.Name, provider.ProtocolAnthropic)
	m.NativeModel = "claude-sonnet-4.5"
	tokens := &fakeTokens{token: "kiro-access"}
	r := NewResolver(accounts, fakeModels{m.ID: m}).WithTokens(tokens)
	target, err := r.Resolve(context.Background(), m.ID)
	if err != nil {
		t.Fatal(err)
	}
	if target.BaseURL != "https://runtime.eu-central-1.kiro.dev" || target.Headers["Authorization"] != "Bearer kiro-access" {
		t.Fatalf("kiro target = %s", target)
	}
	if target.Headers["X-Msu-Upstream-Provider"] != "kiro" || target.Headers["X-Msu-Kiro-Profile-Arn"] != acc.Credential.ProfileARN {
		t.Fatal("native Kiro adapter metadata missing")
	}
	if target.Headers["ChatGPT-Account-Id"] != "" || target.Headers["originator"] != "" {
		t.Fatal("Kiro must not receive Codex identity headers")
	}
	acc.Credential.ProfileARN = ""
	accounts[acc.Name] = acc
	target, err = r.Resolve(context.Background(), m.ID)
	if err != nil || target.BaseURL != "https://q.us-east-1.amazonaws.com" || target.Headers["X-Msu-Kiro-Profile-Arn"] != "" {
		t.Fatalf("Builder ID fallback = %s, %v", target, err)
	}
}

func TestResolveKiroWithoutTokens(t *testing.T) {
	acc := account.Account{Name: "kiro-1", ProviderID: "kiro", Enabled: true, Credential: credential.Credential{Kind: provider.CredKiroRefresh, RefreshToken: "rt"}}
	m := mdl("kiro-1/sonnet", acc.Name, provider.ProtocolAnthropic)
	r := NewResolver(fakeAccounts{acc.Name: acc}, fakeModels{m.ID: m})
	if _, err := r.Resolve(context.Background(), m.ID); apperr.CodeOf(err) != apperr.InvalidCredential {
		t.Fatalf("missing token source = %v", err)
	}
}

// fakeDecls 桩上游推理档声明来源:记录调用,按「账号/原生模型」返回。
type fakeDecls struct {
	calls int
	data  map[string][]string
}

func (f *fakeDecls) DeclaredEfforts(_ context.Context, accountName, nativeModel string) []string {
	f.calls++
	return f.data[accountName+"/"+nativeModel]
}

// TestResolveEfforts 锁定有效档位的声明式语义:自动模式跟随上游声明(未装配
// 声明来源即无声明=不支持);显式 [{name,value}] 条目本地校验且不查声明来源。
func TestResolveEfforts(t *testing.T) {
	t.Run("自动模式跟随上游声明", func(t *testing.T) {
		accounts, models, _ := fixture()
		decls := &fakeDecls{data: map[string][]string{"kimi-1/kimi-k2-turbo": {"high", "low", "ultra"}}}
		r := NewResolver(accounts, models).WithEffortDeclarations(decls)
		got, err := r.Resolve(context.Background(), "kimi-1/k2")
		if err != nil {
			t.Fatal(err)
		}
		want := []effort.Entry{
			{Name: "高", Value: "high"},
			{Name: "低", Value: "low"},
			{Name: "ultra", Value: "ultra"}, // 私有档原值保留(声明序)
		}
		if !slices.Equal(got.Efforts, want) {
			t.Errorf("efforts = %v, want %v(原值声明序)", got.Efforts, want)
		}
		if decls.calls != 1 {
			t.Errorf("声明来源调用 = %d, want 1", decls.calls)
		}
	})
	t.Run("显式条目压过声明且不查上游", func(t *testing.T) {
		accounts, models, _ := fixture()
		m := models["kimi-1/k2"]
		m.Efforts = json.RawMessage(`[{"name":"超","value":"xhigh"},{"name":"","value":"medium"}]`)
		models["kimi-1/k2"] = m
		decls := &fakeDecls{data: map[string][]string{"kimi-1/kimi-k2-turbo": {"low"}}}
		r := NewResolver(accounts, models).WithEffortDeclarations(decls)
		got, err := r.Resolve(context.Background(), "kimi-1/k2")
		if err != nil {
			t.Fatal(err)
		}
		want := []effort.Entry{
			{Name: "超", Value: "xhigh"},
			{Name: "中", Value: "medium"}, // 名留空按值自动命名
		}
		if !slices.Equal(got.Efforts, want) {
			t.Errorf("efforts = %v, want %v(显式声明)", got.Efforts, want)
		}
		if decls.calls != 0 {
			t.Errorf("显式条目不应查声明来源,调用 = %d", decls.calls)
		}
	})
	t.Run("未装配声明来源=无声明=不支持", func(t *testing.T) {
		_, _, r := fixture()
		got, err := r.Resolve(context.Background(), "kimi-1/k2")
		if err != nil {
			t.Fatal(err)
		}
		if len(got.Efforts) != 0 {
			t.Errorf("efforts = %v, want 空(自动模式无声明来源)", got.Efforts)
		}
	})
}

func TestResolve(t *testing.T) {
	_, _, r := fixture()
	got, err := r.Resolve(context.Background(), "kimi-1/k2")
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	spec, _ := provider.Get("kimi/coding")
	if got.BaseURL != spec.BaseURL {
		t.Errorf("base_url = %q, want provider default %q", got.BaseURL, spec.BaseURL)
	}
	if got.NativeModel != "kimi-k2-turbo" || got.Protocol != provider.ProtocolAnthropic {
		t.Errorf("target = %+v", got)
	}
	if got.ContextWindow != 262144 {
		t.Errorf("context_window = %d", got.ContextWindow)
	}
	if got.Headers[headerAnthropicAPIKey] != secret {
		t.Error("delivery response must carry the real key")
	}
	if got.Headers[headerAnthropicVersion] == "" {
		t.Error("anthropic_key scheme should set the version header")
	}
	if string(got.Defaults) != `{}` || string(got.Overrides) != `{}` {
		t.Errorf("defaults = %s, overrides = %s", got.Defaults, got.Overrides)
	}
}

func TestResolveAccountBaseURLOverride(t *testing.T) {
	accounts, models, _ := fixture()
	acc := accounts["kimi-1"]
	acc.BaseURL = "https://gw.example.com"
	accounts["kimi-1"] = acc

	got, err := NewResolver(accounts, models).Resolve(context.Background(), "kimi-1/k2")
	if err != nil {
		t.Fatal(err)
	}
	if got.BaseURL != "https://gw.example.com" {
		t.Errorf("base_url = %q, account override should win", got.BaseURL)
	}
}

func TestResolvePassesParamLayersThrough(t *testing.T) {
	accounts, models, _ := fixture()
	m := models["kimi-1/k2"]
	m.Defaults = json.RawMessage(`{"temperature":0.6,"top_p":0.9}`)
	m.Overrides = json.RawMessage(`{"max_tokens":8192}`)
	models["kimi-1/k2"] = m

	got, err := NewResolver(accounts, models).Resolve(context.Background(), "kimi-1/k2")
	if err != nil {
		t.Fatal(err)
	}
	if string(got.Defaults) != `{"temperature":0.6,"top_p":0.9}` {
		t.Errorf("defaults = %s, want verbatim passthrough", got.Defaults)
	}
	if string(got.Overrides) != `{"max_tokens":8192}` {
		t.Errorf("overrides = %s, want verbatim passthrough", got.Overrides)
	}
}

func TestResolveDisabledBranches(t *testing.T) {
	t.Run("model disabled", func(t *testing.T) {
		accounts, models, _ := fixture()
		m := models["kimi-1/k2"]
		m.Enabled = false
		models["kimi-1/k2"] = m
		_, err := NewResolver(accounts, models).Resolve(context.Background(), "kimi-1/k2")
		if !apperr.Is(err, apperr.ModelDisabled) {
			t.Fatalf("code = %q, want model_disabled", apperr.CodeOf(err))
		}
		if !strings.Contains(err.Error(), "kimi-1/k2") {
			t.Errorf("error should name the model: %v", err)
		}
	})
	t.Run("account disabled", func(t *testing.T) {
		accounts, models, _ := fixture()
		acc := accounts["kimi-1"]
		acc.Enabled = false
		accounts["kimi-1"] = acc
		_, err := NewResolver(accounts, models).Resolve(context.Background(), "kimi-1/k2")
		if !apperr.Is(err, apperr.AccountDisabled) {
			t.Fatalf("code = %q, want account_disabled", apperr.CodeOf(err))
		}
		if !strings.Contains(err.Error(), "kimi-1") {
			t.Errorf("error should name the account: %v", err)
		}
	})
}

func TestResolveNotFound(t *testing.T) {
	_, _, r := fixture()
	if _, err := r.Resolve(context.Background(), "ghost/x"); !apperr.Is(err, apperr.NotFound) {
		t.Errorf("code = %q, want not_found", apperr.CodeOf(err))
	}
}

func TestResolveUnknownProvider(t *testing.T) {
	accounts, models, _ := fixture()
	acc := accounts["kimi-1"]
	acc.ProviderID = "retired-provider"
	accounts["kimi-1"] = acc
	_, err := NewResolver(accounts, models).Resolve(context.Background(), "kimi-1/k2")
	if !apperr.Is(err, apperr.InvalidProvider) {
		t.Errorf("code = %q, want invalid_provider", apperr.CodeOf(err))
	}
}

func TestAuthHeaders(t *testing.T) {
	anth, _ := provider.Get("anthropic/api")
	h := AuthHeaders(anth, acct("a", "anthropic/api"))
	if h[headerAnthropicAPIKey] != secret || h[headerAnthropicVersion] == "" {
		t.Errorf("anthropic headers = %v", h)
	}
	if _, present := h[headerAuthorization]; present {
		t.Error("anthropic scheme should not set Authorization")
	}

	ds, _ := provider.Get("deepseek/api")
	h = AuthHeaders(ds, acct("b", "deepseek/api"))
	if h[headerAuthorization] != "Bearer "+secret {
		t.Errorf("bearer header = %q", h[headerAuthorization])
	}
	if _, present := h[headerAnthropicAPIKey]; present {
		t.Error("bearer scheme should not set x-api-key")
	}
}

func TestAuthHeadersAccountOverlay(t *testing.T) {
	spec, _ := provider.Get("kimi/coding")
	acc := acct("kimi-1", "kimi/coding")
	acc.Headers = map[string]string{"x-trace": "on", headerAnthropicVersion: "2024-01-01"}
	h := AuthHeaders(spec, acc)
	if h["x-trace"] != "on" {
		t.Error("custom headers should be applied")
	}
	if h[headerAnthropicVersion] != "2024-01-01" {
		t.Errorf("account headers should be able to override defaults, got %q", h[headerAnthropicVersion])
	}
}

func TestStringRedactsWhileJSONDoesNot(t *testing.T) {
	_, _, r := fixture()
	got, err := r.Resolve(context.Background(), "kimi-1/k2")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(got.String(), secret) {
		t.Errorf("String() leaked the key: %s", got.String())
	}
	raw, err := json.Marshal(got)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), secret) {
		t.Error("MarshalJSON must carry the real key for the delivery plane")
	}
}

func TestStringRedactsBearer(t *testing.T) {
	accounts, models, _ := fixture()
	accounts["ds-1"] = acct("ds-1", "deepseek/api")
	models["ds-1/v4"] = mdl("ds-1/v4", "ds-1", provider.ProtocolChatCompletions)
	got, err := NewResolver(accounts, models).Resolve(context.Background(), "ds-1/v4")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(got.String(), secret) {
		t.Errorf("String() leaked the bearer token: %s", got.String())
	}
}

func TestList(t *testing.T) {
	accounts, models, _ := fixture()
	models["kimi-1/k3"] = mdl("kimi-1/k3", "kimi-1", provider.ProtocolChatCompletions)
	decls := &fakeDecls{data: map[string][]string{"kimi-1/kimi-k2-turbo": {"high", "ultra"}}}
	r := NewResolver(accounts, models).WithEffortDeclarations(decls)

	got, err := r.List(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("listing = %d entries, want 2", len(got))
	}
	want := []effort.Entry{{Name: "高", Value: "high"}, {Name: "ultra", Value: "ultra"}}
	for _, l := range got {
		if l.ProviderID != "kimi/coding" {
			t.Errorf("provider_id = %q", l.ProviderID)
		}
		if !l.Enabled {
			t.Errorf("%s should be enabled", l.ID)
		}
		if !slices.Equal(l.Efforts, want) {
			t.Errorf("%s efforts = %v, want %v", l.ID, l.Efforts, want)
		}
	}

	acc := accounts["kimi-1"]
	acc.Enabled = false
	accounts["kimi-1"] = acc
	got, err = NewResolver(accounts, models).WithEffortDeclarations(decls).List(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	for _, l := range got {
		if l.Enabled {
			t.Errorf("%s should be unavailable when its account is disabled", l.ID)
		}
	}
}

func TestListingCarriesNoCredential(t *testing.T) {
	_, _, r := fixture()
	got, err := r.List(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(got)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), secret) {
		t.Errorf("listing leaked a credential: %s", raw)
	}
}

// fakeTokens 桩 token 来源:记录调用,按脚本返回。
type fakeTokens struct {
	calls int
	token string
	err   error
}

func (f *fakeTokens) AccessToken(_ context.Context, _ account.Account) (string, error) {
	f.calls++
	return f.token, f.err
}

func codexFixture(tokens Tokens) (*Resolver, *fakeAccounts) {
	accounts := fakeAccounts{"gpt-1": {
		Name:       "gpt-1",
		ProviderID: "openai/codex",
		Credential: credential.Credential{
			Kind:         provider.CredOAuthRefresh,
			RefreshToken: "rt-1",
			AccountID:    "acc-id-1",
		},
		Headers: map[string]string{},
		Enabled: true,
	}}
	models := fakeModels{"gpt-1/codex": {
		ID: "gpt-1/codex", Account: "gpt-1", NativeModel: "gpt-5-codex",
		Protocol: provider.ProtocolResponses, Enabled: true,
	}}
	return NewResolver(accounts, models).WithTokens(tokens), &accounts
}

func TestResolveOAuthAccountGetsCodexHeaders(t *testing.T) {
	tokens := &fakeTokens{token: "at-live"}
	r, _ := codexFixture(tokens)
	got, err := r.Resolve(context.Background(), "gpt-1/codex")
	if err != nil {
		t.Fatal(err)
	}
	if tokens.calls != 1 {
		t.Errorf("token source calls = %d", tokens.calls)
	}
	if got.Headers["Authorization"] != "Bearer at-live" {
		t.Errorf("Authorization = %q", got.Headers["Authorization"])
	}
	if got.Headers["chatgpt-account-id"] != "acc-id-1" {
		t.Errorf("chatgpt-account-id = %q", got.Headers["chatgpt-account-id"])
	}
	if got.Headers["originator"] == "" || got.Headers["OpenAI-Beta"] == "" {
		t.Errorf("codex identity headers missing: %v", got.Headers)
	}
}

func TestResolveOAuthNeedsReauthMapsToInvalidCredential(t *testing.T) {
	r, _ := codexFixture(&fakeTokens{err: oauth.ErrNeedsReauth})
	_, err := r.Resolve(context.Background(), "gpt-1/codex")
	if !apperr.Is(err, apperr.InvalidCredential) {
		t.Fatalf("code = %q, want invalid_credential", apperr.CodeOf(err))
	}
	if !strings.Contains(err.Error(), "gpt-1") {
		t.Errorf("error should name the account: %v", err)
	}
}

func TestResolveOAuthWithoutTokensWired(t *testing.T) {
	r, _ := codexFixture(nil)
	r.tokens = nil
	_, err := r.Resolve(context.Background(), "gpt-1/codex")
	if !apperr.Is(err, apperr.InvalidCredential) {
		t.Fatalf("code = %q, want invalid_credential", apperr.CodeOf(err))
	}
}

func TestResolveOAuthAccountHeaderOverlay(t *testing.T) {
	tokens := &fakeTokens{token: "at-live"}
	r, accounts := codexFixture(tokens)
	acc := (*accounts)["gpt-1"]
	acc.Headers = map[string]string{"originator": "custom-origin"}
	(*accounts)["gpt-1"] = acc
	got, err := r.Resolve(context.Background(), "gpt-1/codex")
	if err != nil {
		t.Fatal(err)
	}
	if got.Headers["originator"] != "custom-origin" {
		t.Errorf("account headers should override defaults, originator = %q", got.Headers["originator"])
	}
}
