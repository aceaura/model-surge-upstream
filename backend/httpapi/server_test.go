package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/aceaura/model-surge-upstream/backend/account"
	"github.com/aceaura/model-surge-upstream/backend/apperr"
	"github.com/aceaura/model-surge-upstream/backend/credential"
	"github.com/aceaura/model-surge-upstream/backend/model"
	"github.com/aceaura/model-surge-upstream/backend/provider"
	"github.com/aceaura/model-surge-upstream/backend/quota"
	"github.com/aceaura/model-surge-upstream/backend/resolve"
	"github.com/aceaura/model-surge-upstream/backend/upmodels"
)

const (
	adminKey    = "admin-secret"
	deliveryKey = "delivery-secret"
	secret      = "sk-abcdefghijklmnop"
)

// ---- 内存桩 ----

type stubAccounts struct {
	data map[string]account.Account
}

func (s *stubAccounts) Create(_ context.Context, in account.Input) (account.Account, error) {
	if _, dup := s.data[in.Name]; dup {
		return account.Account{}, apperr.New(apperr.AlreadyExists, "account "+in.Name+" already exists")
	}
	spec, ok := provider.Get(in.ProviderID)
	if !ok {
		return account.Account{}, apperr.New(apperr.InvalidProvider, "unknown provider "+in.ProviderID)
	}
	if err := in.Credential.ValidateAgainstProvider(spec); err != nil {
		return account.Account{}, apperr.New(apperr.InvalidCredential, err.Error())
	}
	acc := account.Account{
		Name: in.Name, ProviderID: in.ProviderID, Credential: in.Credential,
		BaseURL: in.BaseURL, Headers: in.Headers, Enabled: in.Enabled,
		CreatedAt: time.Now(), UpdatedAt: time.Now(),
	}
	s.data[in.Name] = acc
	return acc, nil
}

func (s *stubAccounts) Get(_ context.Context, name string) (account.Account, error) {
	acc, ok := s.data[name]
	if !ok {
		return account.Account{}, apperr.New(apperr.NotFound, "account "+name+" not found")
	}
	return acc, nil
}

func (s *stubAccounts) List(context.Context) ([]account.Account, error) {
	out := []account.Account{}
	for _, a := range s.data {
		out = append(out, a)
	}
	return out, nil
}

func (s *stubAccounts) Update(_ context.Context, in account.Input) (account.Account, error) {
	existing, ok := s.data[in.Name]
	if !ok {
		return account.Account{}, apperr.New(apperr.NotFound, "account "+in.Name+" not found")
	}
	existing.Enabled = in.Enabled
	if in.BaseURL != "" {
		existing.BaseURL = in.BaseURL
	}
	if in.Credential.Kind != "" {
		existing.Credential = in.Credential
	}
	s.data[in.Name] = existing
	return existing, nil
}

func (s *stubAccounts) Delete(_ context.Context, name string) ([]string, error) {
	if _, ok := s.data[name]; !ok {
		return nil, apperr.New(apperr.NotFound, "account "+name+" not found")
	}
	delete(s.data, name)
	return []string{name + "/k2"}, nil
}

func (s *stubAccounts) CountModels(_ context.Context, name string) (int, error) {
	if _, ok := s.data[name]; !ok {
		return 0, apperr.New(apperr.NotFound, "account "+name+" not found")
	}
	return 1, nil
}

type stubModels struct {
	data map[string]model.Model
}

func (s *stubModels) Create(_ context.Context, in model.Input) (model.Model, error) {
	if _, dup := s.data[in.ID]; dup {
		return model.Model{}, apperr.New(apperr.AlreadyExists, "model "+in.ID+" already exists")
	}
	if in.Protocol == provider.ProtocolResponses {
		return model.Model{}, apperr.New(apperr.InvalidProtocol,
			`provider "kimi" does not support protocol "responses", supported: anthropic, chat_completions`)
	}
	m := model.Model{
		ID: in.ID, Account: in.Account, NativeModel: in.NativeModel, Protocol: in.Protocol,
		ContextWindow: in.ContextWindow, Enabled: in.Enabled,
		Defaults: json.RawMessage(`{}`), Overrides: json.RawMessage(`{}`),
	}
	s.data[in.ID] = m
	return m, nil
}

func (s *stubModels) Get(_ context.Context, id string) (model.Model, error) {
	m, ok := s.data[id]
	if !ok {
		return model.Model{}, apperr.New(apperr.NotFound, "model "+id+" not found")
	}
	return m, nil
}

func (s *stubModels) List(_ context.Context, accountName string) ([]model.Model, error) {
	out := []model.Model{}
	for _, m := range s.data {
		if accountName == "" || m.Account == accountName {
			out = append(out, m)
		}
	}
	return out, nil
}

func (s *stubModels) Update(_ context.Context, in model.Input) (model.Model, error) {
	m, ok := s.data[in.ID]
	if !ok {
		return model.Model{}, apperr.New(apperr.NotFound, "model "+in.ID+" not found")
	}
	m.Enabled = in.Enabled
	s.data[in.ID] = m
	return m, nil
}

func (s *stubModels) Delete(_ context.Context, id string) error {
	if _, ok := s.data[id]; !ok {
		return apperr.New(apperr.NotFound, "model "+id+" not found")
	}
	delete(s.data, id)
	return nil
}

type stubQuota struct {
	report quota.Report
	err    error
	forgot []string
}

func (s *stubQuota) Query(context.Context, string) (quota.Report, error) {
	return s.report, s.err
}

func (s *stubQuota) Forget(name string) { s.forgot = append(s.forgot, name) }

type stubUpstreamModels struct {
	report upmodels.Report
	err    error
	forgot []string
}

func (s *stubUpstreamModels) List(context.Context, string) (upmodels.Report, error) {
	return s.report, s.err
}

func (s *stubUpstreamModels) Forget(name string) { s.forgot = append(s.forgot, name) }

type stubHealth struct {
	dbErr      error
	cacheReady bool
}

func (s *stubHealth) PingDB(context.Context) error    { return s.dbErr }
func (s *stubHealth) CacheReady(context.Context) bool { return s.cacheReady }

// ---- 装配 ----

type fixture struct {
	server   http.Handler
	accounts *stubAccounts
	models   *stubModels
	quota    *stubQuota
	upstream *stubUpstreamModels
	health   *stubHealth
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	accounts := &stubAccounts{data: map[string]account.Account{
		"kimi-1": {
			Name: "kimi-1", ProviderID: "kimi",
			Credential: credential.Credential{Kind: provider.CredAPIKey, APIKey: secret},
			Headers:    map[string]string{}, Enabled: true,
		},
	}}
	models := &stubModels{data: map[string]model.Model{
		"kimi-1/k2": {
			ID: "kimi-1/k2", Account: "kimi-1", NativeModel: "kimi-k2-turbo",
			Protocol: provider.ProtocolAnthropic, ContextWindow: 262144, Enabled: true,
			Defaults: json.RawMessage(`{}`), Overrides: json.RawMessage(`{}`),
		},
	}}
	q := &stubQuota{report: quota.Report{Account: "kimi-1", Queryable: false, Meters: []quota.Meter{}}}
	up := &stubUpstreamModels{report: upmodels.Report{
		Account:   "kimi-1",
		Queryable: true,
		Models:    []upmodels.Entry{{ID: "kimi-k2-turbo"}},
	}}
	h := &stubHealth{cacheReady: true}

	return &fixture{
		server: NewServer(Deps{
			Accounts:       accounts,
			Models:         models,
			Resolver:       resolve.NewResolver(accounts, models),
			Quota:          q,
			UpstreamModels: up,
			Health:         h,
			AdminKey:       adminKey,
			DeliveryKey:    deliveryKey,
		}),
		accounts: accounts, models: models, quota: q, upstream: up, health: h,
	}
}

func (f *fixture) do(t *testing.T, method, path, key, body string) *httptest.ResponseRecorder {
	t.Helper()
	var reader *strings.Reader
	if body == "" {
		reader = strings.NewReader("")
	} else {
		reader = strings.NewReader(body)
	}
	req := httptest.NewRequest(method, path, reader)
	if key != "" {
		req.Header.Set("Authorization", "Bearer "+key)
	}
	rec := httptest.NewRecorder()
	f.server.ServeHTTP(rec, req)
	return rec
}

func codeOf(t *testing.T, rec *httptest.ResponseRecorder) apperr.Code {
	t.Helper()
	var env errorEnvelope
	if err := json.Unmarshal(rec.Body.Bytes(), &env); err != nil {
		t.Fatalf("body is not an error envelope: %s", rec.Body.String())
	}
	if env.Error.Status != rec.Code {
		t.Errorf("envelope status %d disagrees with http status %d", env.Error.Status, rec.Code)
	}
	return env.Error.Code
}

// ---- 鉴权 ----

func TestKeysAreNotInterchangeable(t *testing.T) {
	f := newFixture(t)

	if rec := f.do(t, "GET", "/v1/models", adminKey, ""); rec.Code != http.StatusUnauthorized {
		t.Errorf("admin key on delivery plane = %d, want 401", rec.Code)
	}
	if rec := f.do(t, "GET", "/admin/providers", deliveryKey, ""); rec.Code != http.StatusUnauthorized {
		t.Errorf("delivery key on admin plane = %d, want 401", rec.Code)
	}
	if rec := f.do(t, "GET", "/v1/models", deliveryKey, ""); rec.Code != http.StatusOK {
		t.Errorf("delivery key on delivery plane = %d, want 200", rec.Code)
	}
	if rec := f.do(t, "GET", "/admin/providers", adminKey, ""); rec.Code != http.StatusOK {
		t.Errorf("admin key on admin plane = %d, want 200", rec.Code)
	}
}

func TestMissingKeyRejected(t *testing.T) {
	f := newFixture(t)
	for _, path := range []string{"/admin/providers", "/admin/accounts", "/v1/models"} {
		rec := f.do(t, "GET", path, "", "")
		if rec.Code != http.StatusUnauthorized {
			t.Errorf("%s without key = %d, want 401", path, rec.Code)
		}
		if got := codeOf(t, rec); got != apperr.Unauthorized {
			t.Errorf("%s code = %q", path, got)
		}
	}
}

func TestMalformedAuthHeaderRejected(t *testing.T) {
	f := newFixture(t)
	for _, header := range []string{adminKey, "Basic " + adminKey, "Bearer", "Bearer wrong"} {
		req := httptest.NewRequest("GET", "/admin/providers", strings.NewReader(""))
		req.Header.Set("Authorization", header)
		rec := httptest.NewRecorder()
		f.server.ServeHTTP(rec, req)
		if rec.Code != http.StatusUnauthorized {
			t.Errorf("header %q = %d, want 401", header, rec.Code)
		}
	}
}

func TestBearerPrefixIsCaseInsensitive(t *testing.T) {
	f := newFixture(t)
	req := httptest.NewRequest("GET", "/admin/providers", strings.NewReader(""))
	req.Header.Set("Authorization", "bearer "+adminKey)
	rec := httptest.NewRecorder()
	f.server.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Errorf("lowercase bearer = %d, want 200", rec.Code)
	}
}

// ---- 管理面 ----

func TestListProviders(t *testing.T) {
	f := newFixture(t)
	rec := f.do(t, "GET", "/admin/providers", adminKey, "")
	var body struct{ Providers []provider.Spec }
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if len(body.Providers) != len(provider.All()) {
		t.Errorf("providers = %d, want %d", len(body.Providers), len(provider.All()))
	}
}

func TestAccountResponsesRedact(t *testing.T) {
	f := newFixture(t)
	for _, path := range []string{"/admin/accounts", "/admin/accounts/kimi-1"} {
		rec := f.do(t, "GET", path, adminKey, "")
		if rec.Code != http.StatusOK {
			t.Fatalf("%s = %d: %s", path, rec.Code, rec.Body)
		}
		if strings.Contains(rec.Body.String(), secret) {
			t.Errorf("%s leaked the credential: %s", path, rec.Body)
		}
	}
}

func TestGetAccountReportsModelCount(t *testing.T) {
	f := newFixture(t)
	rec := f.do(t, "GET", "/admin/accounts/kimi-1", adminKey, "")
	var body struct {
		ModelCount int `json:"model_count"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body.ModelCount != 1 {
		t.Errorf("model_count = %d, want 1 (drives the cascade warning)", body.ModelCount)
	}
}

func TestCreateAccount(t *testing.T) {
	f := newFixture(t)
	rec := f.do(t, "POST", "/admin/accounts", adminKey,
		`{"name":"ds-1","provider_id":"deepseek","api_key":"sk-deepseek-secret"}`)
	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body)
	}
	if _, ok := f.accounts.data["ds-1"]; !ok {
		t.Error("account was not created")
	}
	if strings.Contains(rec.Body.String(), "sk-deepseek-secret") {
		t.Errorf("create response leaked the credential: %s", rec.Body)
	}
}

func TestCreateAccountValidationErrors(t *testing.T) {
	f := newFixture(t)
	cases := map[string]struct {
		body string
		code apperr.Code
	}{
		"unknown provider": {`{"name":"x","provider_id":"nope","api_key":"sk-x"}`, apperr.InvalidProvider},
		"duplicate":        {`{"name":"kimi-1","provider_id":"kimi","api_key":"sk-x"}`, apperr.AlreadyExists},
		"missing key":      {`{"name":"x","provider_id":"kimi"}`, apperr.InvalidCredential},
		"bad credential":   {`{"name":"x","provider_id":"kimi","credential":{"kind":"oauth_refresh"}}`, apperr.InvalidCredential},
		"malformed json":   {`{`, apperr.InvalidJSON},
		"unknown field":    {`{"name":"x","provider_id":"kimi","api_key":"sk-x","nope":1}`, apperr.InvalidJSON},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			rec := f.do(t, "POST", "/admin/accounts", adminKey, tc.body)
			if got := codeOf(t, rec); got != tc.code {
				t.Errorf("code = %q, want %q (body: %s)", got, tc.code, rec.Body)
			}
		})
	}
}

func TestUnsupportedCredentialKindListsSupported(t *testing.T) {
	f := newFixture(t)
	rec := f.do(t, "POST", "/admin/accounts", adminKey,
		`{"name":"x","provider_id":"kimi","credential":{"kind":"oauth_refresh","api_key":"x"}}`)
	if !strings.Contains(rec.Body.String(), "api_key") {
		t.Errorf("error should list the supported kinds: %s", rec.Body)
	}
}

func TestUpdateAccountTogglesEnabled(t *testing.T) {
	f := newFixture(t)
	rec := f.do(t, "PUT", "/admin/accounts/kimi-1", adminKey, `{"enabled":false}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body)
	}
	if f.accounts.data["kimi-1"].Enabled {
		t.Error("enabled should be false")
	}
	if len(f.quota.forgot) == 0 {
		t.Error("account update should drop its quota cache")
	}
	if len(f.upstream.forgot) == 0 {
		t.Error("account update should drop its upstream model listing cache")
	}
}

func TestDeleteAccountReportsCascade(t *testing.T) {
	f := newFixture(t)
	rec := f.do(t, "DELETE", "/admin/accounts/kimi-1", adminKey, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body)
	}
	var body struct {
		DeletedModels []string `json:"deleted_models"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if len(body.DeletedModels) != 1 {
		t.Errorf("deleted_models = %v, want the cascaded ids", body.DeletedModels)
	}
}

func TestAccountNotFound(t *testing.T) {
	f := newFixture(t)
	for _, tc := range []struct{ method, path, body string }{
		{"GET", "/admin/accounts/ghost", ""},
		{"PUT", "/admin/accounts/ghost", `{"enabled":true}`},
		{"DELETE", "/admin/accounts/ghost", ""},
	} {
		rec := f.do(t, tc.method, tc.path, adminKey, tc.body)
		if got := codeOf(t, rec); got != apperr.NotFound {
			t.Errorf("%s %s code = %q, want not_found", tc.method, tc.path, got)
		}
	}
}

func TestModelCRUD(t *testing.T) {
	f := newFixture(t)

	rec := f.do(t, "POST", "/admin/models", adminKey,
		`{"id":"kimi-1/k3","account":"kimi-1","native_model":"kimi-k3","protocol":"chat_completions","context_window":262144}`)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create = %d: %s", rec.Code, rec.Body)
	}

	// 模型标识含斜杠，路由必须用通配段捕获。
	if rec := f.do(t, "GET", "/admin/models/kimi-1/k3", adminKey, ""); rec.Code != http.StatusOK {
		t.Fatalf("get = %d: %s", rec.Code, rec.Body)
	}
	if rec := f.do(t, "PUT", "/admin/models/kimi-1/k3", adminKey, `{"enabled":false}`); rec.Code != http.StatusOK {
		t.Fatalf("update = %d: %s", rec.Code, rec.Body)
	}
	if f.models.data["kimi-1/k3"].Enabled {
		t.Error("enabled should be false after update")
	}
	if rec := f.do(t, "DELETE", "/admin/models/kimi-1/k3", adminKey, ""); rec.Code != http.StatusNoContent {
		t.Fatalf("delete = %d: %s", rec.Code, rec.Body)
	}
	if _, ok := f.models.data["kimi-1/k3"]; ok {
		t.Error("model should be gone")
	}
}

func TestModelProtocolErrorListsSupported(t *testing.T) {
	f := newFixture(t)
	rec := f.do(t, "POST", "/admin/models", adminKey,
		`{"id":"kimi-1/bad","account":"kimi-1","native_model":"x","protocol":"responses"}`)
	if got := codeOf(t, rec); got != apperr.InvalidProtocol {
		t.Fatalf("code = %q, want invalid_protocol", got)
	}
	for _, want := range []string{"anthropic", "chat_completions"} {
		if !strings.Contains(rec.Body.String(), want) {
			t.Errorf("error should list %q: %s", want, rec.Body)
		}
	}
}

func TestListModelsFiltersByAccount(t *testing.T) {
	f := newFixture(t)
	rec := f.do(t, "GET", "/admin/models?account=ghost", adminKey, "")
	var body struct{ Models []model.Model }
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if len(body.Models) != 0 {
		t.Errorf("filtered list = %v, want empty", body.Models)
	}
}

// ---- 下发面 ----

func TestResolve(t *testing.T) {
	f := newFixture(t)
	rec := f.do(t, "POST", "/v1/resolve", deliveryKey, `{"model_id":"kimi-1/k2"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body)
	}
	var target resolve.ResolvedTarget
	if err := json.Unmarshal(rec.Body.Bytes(), &target); err != nil {
		t.Fatal(err)
	}
	if target.NativeModel != "kimi-k2-turbo" {
		t.Errorf("native_model = %q", target.NativeModel)
	}
	if target.Headers["x-api-key"] != secret {
		t.Error("delivery response must carry the credential")
	}
}

func TestResolveRequiresModelID(t *testing.T) {
	f := newFixture(t)
	rec := f.do(t, "POST", "/v1/resolve", deliveryKey, `{}`)
	if got := codeOf(t, rec); got != apperr.InvalidRequest {
		t.Errorf("code = %q, want invalid_request", got)
	}
}

func TestResolveNotFound(t *testing.T) {
	f := newFixture(t)
	rec := f.do(t, "POST", "/v1/resolve", deliveryKey, `{"model_id":"ghost/x"}`)
	if rec.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404", rec.Code)
	}
	if got := codeOf(t, rec); got != apperr.NotFound {
		t.Errorf("code = %q", got)
	}
}

func TestResolveDisabledBranchesDiffer(t *testing.T) {
	t.Run("model disabled", func(t *testing.T) {
		f := newFixture(t)
		m := f.models.data["kimi-1/k2"]
		m.Enabled = false
		f.models.data["kimi-1/k2"] = m
		rec := f.do(t, "POST", "/v1/resolve", deliveryKey, `{"model_id":"kimi-1/k2"}`)
		if rec.Code != http.StatusConflict {
			t.Errorf("status = %d, want 409", rec.Code)
		}
		if got := codeOf(t, rec); got != apperr.ModelDisabled {
			t.Errorf("code = %q, want model_disabled", got)
		}
	})
	t.Run("account disabled", func(t *testing.T) {
		f := newFixture(t)
		acc := f.accounts.data["kimi-1"]
		acc.Enabled = false
		f.accounts.data["kimi-1"] = acc
		rec := f.do(t, "POST", "/v1/resolve", deliveryKey, `{"model_id":"kimi-1/k2"}`)
		if got := codeOf(t, rec); got != apperr.AccountDisabled {
			t.Errorf("code = %q, want account_disabled", got)
		}
	})
}

func TestResolveBadParams(t *testing.T) {
	f := newFixture(t)
	rec := f.do(t, "POST", "/v1/resolve", deliveryKey, `{"model_id":"kimi-1/k2","params":[1,2]}`)
	if got := codeOf(t, rec); got != apperr.InvalidJSON {
		t.Errorf("code = %q, want invalid_json", got)
	}
}

func TestDeliveryModelsCarryNoCredential(t *testing.T) {
	f := newFixture(t)
	rec := f.do(t, "GET", "/v1/models", deliveryKey, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body)
	}
	if strings.Contains(rec.Body.String(), secret) {
		t.Errorf("listing leaked a credential: %s", rec.Body)
	}
}

func TestQuotaOnAdminPlane(t *testing.T) {
	f := newFixture(t)
	rec := f.do(t, "GET", "/admin/accounts/kimi-1/quota", adminKey, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("admin quota = %d: %s", rec.Code, rec.Body)
	}
	if rec := f.do(t, "GET", "/admin/accounts/kimi-1/quota", deliveryKey, ""); rec.Code != http.StatusUnauthorized {
		t.Errorf("delivery key on admin quota = %d, want 401", rec.Code)
	}
}

func TestQuotaNotQueryable(t *testing.T) {
	f := newFixture(t)
	rec := f.do(t, "GET", "/v1/accounts/kimi-1/quota", deliveryKey, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("not-queryable must not be an error, got %d: %s", rec.Code, rec.Body)
	}
	var report quota.Report
	if err := json.Unmarshal(rec.Body.Bytes(), &report); err != nil {
		t.Fatal(err)
	}
	if report.Queryable {
		t.Error("queryable should be false")
	}
	if report.Meters == nil {
		t.Error("meters should serialize as an empty list, not null")
	}
}

func TestQuotaCarriesEveryMeter(t *testing.T) {
	f := newFixture(t)
	used := 42.0
	resetAt := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	f.quota.report = quota.Report{
		Account:   "kimi-1",
		Queryable: true,
		Meters: []quota.Meter{
			{Kind: provider.MeterUsage, Unit: provider.UnitCurrency, Currency: "USD", Used: &used, ResetAt: &resetAt},
			{Kind: provider.MeterRateLimit, Unit: provider.UnitTokens, Label: "tokens"},
		},
	}
	rec := f.do(t, "GET", "/v1/accounts/kimi-1/quota", deliveryKey, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body)
	}
	var report quota.Report
	if err := json.Unmarshal(rec.Body.Bytes(), &report); err != nil {
		t.Fatal(err)
	}
	if len(report.Meters) != 2 {
		t.Fatalf("meters = %v, every dimension must survive the wire", report.Meters)
	}
	if report.Meters[0].Used == nil || *report.Meters[0].Used != used {
		t.Errorf("used = %v, post-paid accounts report usage only", report.Meters[0].Used)
	}
	if report.Meters[0].ResetAt == nil || !report.Meters[0].ResetAt.Equal(resetAt) {
		t.Errorf("reset_at = %v", report.Meters[0].ResetAt)
	}
	if report.Meters[1].Unit != provider.UnitTokens {
		t.Errorf("unit = %q, want tokens", report.Meters[1].Unit)
	}
}

func TestQuotaUpstreamFailure(t *testing.T) {
	f := newFixture(t)
	f.quota.err = apperr.New(apperr.QuotaUnavailable, "upstream quota query returned 401")
	rec := f.do(t, "GET", "/v1/accounts/kimi-1/quota", deliveryKey, "")
	if rec.Code != http.StatusBadGateway {
		t.Errorf("status = %d, want 502", rec.Code)
	}
	if got := codeOf(t, rec); got != apperr.QuotaUnavailable {
		t.Errorf("code = %q", got)
	}
}

func TestUpstreamModelsOnBothPlanes(t *testing.T) {
	f := newFixture(t)
	for _, tc := range []struct{ path, key string }{
		{"/admin/accounts/kimi-1/upstream-models", adminKey},
		{"/v1/accounts/kimi-1/upstream-models", deliveryKey},
	} {
		rec := f.do(t, "GET", tc.path, tc.key, "")
		if rec.Code != http.StatusOK {
			t.Fatalf("%s = %d: %s", tc.path, rec.Code, rec.Body)
		}
		var report upmodels.Report
		if err := json.Unmarshal(rec.Body.Bytes(), &report); err != nil {
			t.Fatal(err)
		}
		if len(report.Models) != 1 || report.Models[0].ID != "kimi-k2-turbo" {
			t.Errorf("%s models = %v", tc.path, report.Models)
		}
	}
}

func TestUpstreamModelsRejectsWrongKey(t *testing.T) {
	f := newFixture(t)
	if rec := f.do(t, "GET", "/admin/accounts/kimi-1/upstream-models", deliveryKey, ""); rec.Code != http.StatusUnauthorized {
		t.Errorf("delivery key on admin plane = %d, want 401", rec.Code)
	}
	if rec := f.do(t, "GET", "/v1/accounts/kimi-1/upstream-models", adminKey, ""); rec.Code != http.StatusUnauthorized {
		t.Errorf("admin key on delivery plane = %d, want 401", rec.Code)
	}
}

func TestUpstreamModelsFailure(t *testing.T) {
	f := newFixture(t)
	f.upstream.err = apperr.New(apperr.UpstreamUnavailable, "upstream model listing returned 401")
	rec := f.do(t, "GET", "/v1/accounts/kimi-1/upstream-models", deliveryKey, "")
	if rec.Code != http.StatusBadGateway {
		t.Errorf("status = %d, want 502", rec.Code)
	}
	if got := codeOf(t, rec); got != apperr.UpstreamUnavailable {
		t.Errorf("code = %q", got)
	}
}

func TestUpstreamModelsFailureDoesNotAffectResolve(t *testing.T) {
	f := newFixture(t)
	f.upstream.err = apperr.New(apperr.UpstreamUnavailable, "down")
	if rec := f.do(t, "POST", "/v1/resolve", deliveryKey, `{"model_id":"kimi-1/k2"}`); rec.Code != http.StatusOK {
		t.Errorf("resolve = %d, upstream listing failure must not affect it", rec.Code)
	}
}

func TestQuotaFailureDoesNotAffectResolve(t *testing.T) {
	f := newFixture(t)
	f.quota.err = apperr.New(apperr.QuotaUnavailable, "down")
	if rec := f.do(t, "POST", "/v1/resolve", deliveryKey, `{"model_id":"kimi-1/k2"}`); rec.Code != http.StatusOK {
		t.Errorf("resolve = %d, quota failure must not affect it", rec.Code)
	}
}

// ---- 健康检查 ----

func TestHealthNeedsNoKey(t *testing.T) {
	f := newFixture(t)
	rec := f.do(t, "GET", "/healthz", "", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body)
	}
}

func TestHealthReadyWithoutCache(t *testing.T) {
	f := newFixture(t)
	f.health.cacheReady = false
	rec := f.do(t, "GET", "/healthz", "", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("cache down must not affect readiness, got %d", rec.Code)
	}
	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body["ready"] != true {
		t.Error("ready should be true when only the cache is down")
	}
	if body["cache"] != false {
		t.Error("cache status should be reported separately")
	}
}

func TestHealthNotReadyWhenDBDown(t *testing.T) {
	f := newFixture(t)
	f.health.dbErr = errors.New("connection refused")
	rec := f.do(t, "GET", "/healthz", "", "")
	if rec.Code != http.StatusServiceUnavailable {
		t.Errorf("status = %d, want 503", rec.Code)
	}
}

func TestInternalErrorHidesDetails(t *testing.T) {
	// 非领域错误只回通用文案，不泄露底层细节。
	rec := httptest.NewRecorder()
	writeError(rec, errors.New("dial tcp 10.0.0.1:5432: connection refused"))
	if strings.Contains(rec.Body.String(), "10.0.0.1") {
		t.Errorf("internal error leaked details: %s", rec.Body)
	}
	if rec.Code != http.StatusInternalServerError {
		t.Errorf("status = %d, want 500", rec.Code)
	}
}

func TestEveryCodeHasStatus(t *testing.T) {
	codes := []apperr.Code{
		apperr.Unauthorized, apperr.NotFound, apperr.AlreadyExists,
		apperr.AccountDisabled, apperr.ModelDisabled, apperr.InvalidProvider,
		apperr.InvalidCredential, apperr.InvalidProtocol, apperr.InvalidJSON,
		apperr.InvalidRequest, apperr.QuotaUnavailable, apperr.StorageError,
	}
	for _, c := range codes {
		if _, ok := statusByCode[c]; !ok {
			t.Errorf("code %q has no status mapping", c)
		}
	}
}
