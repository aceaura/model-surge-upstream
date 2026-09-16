package resolve

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/aceaura/model-surge-upstream/backend/account"
	"github.com/aceaura/model-surge-upstream/backend/apperr"
	"github.com/aceaura/model-surge-upstream/backend/credential"
	"github.com/aceaura/model-surge-upstream/backend/model"
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
	accounts := fakeAccounts{"kimi-1": acct("kimi-1", "kimi")}
	models := fakeModels{"kimi-1/k2": mdl("kimi-1/k2", "kimi-1", provider.ProtocolAnthropic)}
	return accounts, models, NewResolver(accounts, models)
}

func TestResolve(t *testing.T) {
	_, _, r := fixture()
	got, err := r.Resolve(context.Background(), "kimi-1/k2")
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	spec, _ := provider.Get("kimi")
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
	anth, _ := provider.Get("anthropic")
	h := AuthHeaders(anth, acct("a", "anthropic"))
	if h[headerAnthropicAPIKey] != secret || h[headerAnthropicVersion] == "" {
		t.Errorf("anthropic headers = %v", h)
	}
	if _, present := h[headerAuthorization]; present {
		t.Error("anthropic scheme should not set Authorization")
	}

	ds, _ := provider.Get("deepseek")
	h = AuthHeaders(ds, acct("b", "deepseek"))
	if h[headerAuthorization] != "Bearer "+secret {
		t.Errorf("bearer header = %q", h[headerAuthorization])
	}
	if _, present := h[headerAnthropicAPIKey]; present {
		t.Error("bearer scheme should not set x-api-key")
	}
}

func TestAuthHeadersAccountOverlay(t *testing.T) {
	spec, _ := provider.Get("kimi")
	acc := acct("kimi-1", "kimi")
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
	accounts["ds-1"] = acct("ds-1", "deepseek")
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
	accounts, models, r := fixture()
	models["kimi-1/k3"] = mdl("kimi-1/k3", "kimi-1", provider.ProtocolChatCompletions)

	got, err := r.List(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("listing = %d entries, want 2", len(got))
	}
	for _, l := range got {
		if l.ProviderID != "kimi" {
			t.Errorf("provider_id = %q", l.ProviderID)
		}
		if !l.Enabled {
			t.Errorf("%s should be enabled", l.ID)
		}
	}

	acc := accounts["kimi-1"]
	acc.Enabled = false
	accounts["kimi-1"] = acc
	got, err = NewResolver(accounts, models).List(context.Background())
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
