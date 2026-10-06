package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/aceaura/model-surge-upstream/backend/account"
	"github.com/aceaura/model-surge-upstream/backend/activity"
	"github.com/aceaura/model-surge-upstream/backend/apperr"
	"github.com/aceaura/model-surge-upstream/backend/credential"
	"github.com/aceaura/model-surge-upstream/backend/effort"
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
	data  map[string]account.Account
	order []string
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
	seen := map[string]bool{}
	for _, n := range s.order {
		if a, ok := s.data[n]; ok {
			out = append(out, a)
			seen[n] = true
		}
	}
	for _, a := range s.data {
		if !seen[a.Name] {
			out = append(out, a)
		}
	}
	return out, nil
}

func (s *stubAccounts) Reorder(_ context.Context, names []string) error {
	for _, n := range names {
		if _, ok := s.data[n]; !ok {
			return apperr.New(apperr.NotFound, "account "+n+" not found")
		}
	}
	s.order = append([]string(nil), names...)
	return nil
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
	data  map[string]model.Model
	order []string
}

func (s *stubModels) Create(_ context.Context, in model.Input) (model.Model, error) {
	if _, dup := s.data[in.ID]; dup {
		return model.Model{}, apperr.New(apperr.AlreadyExists, "model "+in.ID+" already exists")
	}
	if in.Protocol == provider.ProtocolResponses {
		return model.Model{}, apperr.New(apperr.InvalidProtocol,
			`provider "kimi.global.subscribe.coding" does not support protocol "responses", supported: anthropic, chat_completions`)
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
	seen := map[string]bool{}
	for _, id := range s.order {
		if m, ok := s.data[id]; ok {
			seen[id] = true
			if accountName == "" || m.Account == accountName {
				out = append(out, m)
			}
		}
	}
	for _, m := range s.data {
		if seen[m.ID] {
			continue
		}
		if accountName == "" || m.Account == accountName {
			out = append(out, m)
		}
	}
	return out, nil
}

func (s *stubModels) Reorder(_ context.Context, ids []string) error {
	for _, id := range ids {
		if _, ok := s.data[id]; !ok {
			return apperr.New(apperr.NotFound, "model "+id+" not found")
		}
	}
	s.order = append([]string(nil), ids...)
	return nil
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
	report   quota.Report
	err      error
	forgot   []string
	queries  int
	cached   quota.Report
	hasCache bool
}

func (s *stubQuota) Query(context.Context, string) (quota.Report, error) {
	s.queries++
	return s.report, s.err
}

func (s *stubQuota) Cached(string) (quota.Report, bool) { return s.cached, s.hasCache }

func (s *stubQuota) Forget(name string) { s.forgot = append(s.forgot, name) }

type stubActivity struct {
	active bool
	idle   time.Duration
}

func (s *stubActivity) Active(_ string, idle time.Duration) bool {
	s.idle = idle
	return s.active
}

type stubUpstreamModels struct {
	report upmodels.Report
	err    error
	forgot []string
	// efforts 按「账号/原生模型」预置的声明档位,供 decorateEfforts 测试。
	efforts map[string][]string
}

func (s *stubUpstreamModels) List(context.Context, string) (upmodels.Report, error) {
	return s.report, s.err
}

func (s *stubUpstreamModels) DeclaredEfforts(_ context.Context, accountName, nativeModel string) []string {
	return s.efforts[accountName+"/"+nativeModel]
}

func (s *stubUpstreamModels) Forget(name string) { s.forgot = append(s.forgot, name) }

type stubHealth struct {
	dbErr      error
	cacheReady bool
}

func (s *stubHealth) PingDB(context.Context) error    { return s.dbErr }
func (s *stubHealth) CacheReady(context.Context) bool { return s.cacheReady }

// ---- 装配 ----

type stubOAuth struct {
	reauth map[string]bool
	resets []string
}

func (s *stubOAuth) NeedsReauth(name string) bool { return s.reauth[name] }
func (s *stubOAuth) Reset(name string) {
	s.resets = append(s.resets, name)
	delete(s.reauth, name)
}

type fixture struct {
	server   http.Handler
	accounts *stubAccounts
	models   *stubModels
	quota    *stubQuota
	upstream *stubUpstreamModels
	health   *stubHealth
	oauth    *stubOAuth
	activity *stubActivity
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	accounts := &stubAccounts{data: map[string]account.Account{
		"kimi-1": {
			Name: "kimi-1", ProviderID: "kimi.global.subscribe.coding",
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
	oa := &stubOAuth{reauth: map[string]bool{}}
	act := &stubActivity{active: true}

	return &fixture{
		server: NewServer(Deps{
			Accounts:       accounts,
			Models:         models,
			Resolver:       resolve.NewResolver(accounts, models).WithEffortDeclarations(up),
			Quota:          q,
			UpstreamModels: up,
			Health:         h,
			OAuth:          oa,
			Activity:       act,
			AdminKey:       adminKey,
			DeliveryKey:    deliveryKey,
		}),
		accounts: accounts, models: models, quota: q, upstream: up, health: h, oauth: oa,
		activity: act,
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
		`{"name":"ds-1","provider_id":"deepseek.global.api.standard","api_key":"sk-deepseek-secret"}`)
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
		"duplicate":        {`{"name":"kimi-1","provider_id":"kimi.global.subscribe.coding","api_key":"sk-x"}`, apperr.AlreadyExists},
		"missing key":      {`{"name":"x","provider_id":"kimi.global.subscribe.coding"}`, apperr.InvalidCredential},
		"bad credential":   {`{"name":"x","provider_id":"kimi.global.subscribe.coding","credential":{"kind":"oauth_refresh"}}`, apperr.InvalidCredential},
		"malformed json":   {`{`, apperr.InvalidJSON},
		"unknown field":    {`{"name":"x","provider_id":"kimi.global.subscribe.coding","api_key":"sk-x","nope":1}`, apperr.InvalidJSON},
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
		`{"name":"x","provider_id":"kimi.global.subscribe.coding","credential":{"kind":"session_token","token":"x"}}`)
	if !strings.Contains(rec.Body.String(), "api_key") {
		t.Errorf("error should list the supported kinds: %s", rec.Body)
	}
}

// oauth_refresh 凭据的管理面往返:创建成功,读回时 refresh_token 脱敏、
// access_token 不出现、account_id 明文(它是标识不是秘密)。
func TestCreateOAuthAccountRoundTrip(t *testing.T) {
	f := newFixture(t)
	rec := f.do(t, "POST", "/admin/accounts", adminKey,
		`{"name":"gpt-1","provider_id":"openai.global.subscribe.codex","credential":{"kind":"oauth_refresh","refresh_token":"rt-secret","account_id":"acc-x"}}`)
	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body)
	}
	if strings.Contains(rec.Body.String(), "rt-secret") {
		t.Errorf("create response leaked refresh_token: %s", rec.Body)
	}

	rec = f.do(t, "GET", "/admin/accounts/gpt-1", adminKey, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body)
	}
	body := rec.Body.String()
	if strings.Contains(body, "rt-secret") {
		t.Errorf("get response leaked refresh_token: %s", body)
	}
	if strings.Contains(body, "access_token") {
		t.Errorf("view must never carry access_token: %s", body)
	}
	if !strings.Contains(body, `"kind":"oauth_refresh"`) || !strings.Contains(body, `"account_id":"acc-x"`) {
		t.Errorf("view should carry kind and account_id: %s", body)
	}
}

// 账号视图叠加 needs_reauth:终态授权失败的账号在列表与详情里都带位。
func TestAccountViewCarriesNeedsReauth(t *testing.T) {
	f := newFixture(t)
	f.oauth.reauth["kimi-1"] = true

	rec := f.do(t, "GET", "/admin/accounts", adminKey, "")
	if !strings.Contains(rec.Body.String(), `"needs_reauth":true`) {
		t.Errorf("list should flag needs_reauth: %s", rec.Body)
	}
	rec = f.do(t, "GET", "/admin/accounts/kimi-1", adminKey, "")
	if !strings.Contains(rec.Body.String(), `"needs_reauth":true`) {
		t.Errorf("get should flag needs_reauth: %s", rec.Body)
	}
}

// 重新粘贴凭据(更新)后清 OAuth 终态标记;创建同名账号同样清。
func TestAccountWritesResetOAuthState(t *testing.T) {
	f := newFixture(t)
	f.oauth.reauth["kimi-1"] = true
	if rec := f.do(t, "PUT", "/admin/accounts/kimi-1", adminKey, `{"enabled":true}`); rec.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body)
	}
	if len(f.oauth.resets) != 1 || f.oauth.resets[0] != "kimi-1" {
		t.Errorf("resets = %v, want [kimi-1]", f.oauth.resets)
	}
	if f.oauth.reauth["kimi-1"] {
		t.Error("reauth flag should be cleared after update")
	}

	if rec := f.do(t, "POST", "/admin/accounts", adminKey,
		`{"name":"gpt-2","provider_id":"openai.global.subscribe.codex","credential":{"kind":"oauth_refresh","refresh_token":"rt-y","account_id":"acc-y"}}`); rec.Code != http.StatusCreated {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body)
	}
	if len(f.oauth.resets) != 2 || f.oauth.resets[1] != "gpt-2" {
		t.Errorf("resets = %v, want create to reset too", f.oauth.resets)
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

func TestReorderAccounts(t *testing.T) {
	f := newFixture(t)
	rec := f.do(t, "POST", "/admin/accounts", adminKey,
		`{"name":"ds-1","provider_id":"deepseek.global.api.standard","api_key":"`+secret+`"}`)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create ds-1 = %d: %s", rec.Code, rec.Body)
	}

	rec = f.do(t, "POST", "/admin/accounts/reorder", adminKey, `{"names":["ds-1","kimi-1"]}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("reorder = %d: %s", rec.Code, rec.Body)
	}
	rec = f.do(t, "GET", "/admin/accounts", adminKey, "")
	var body struct {
		Accounts []struct {
			Name string `json:"name"`
		} `json:"accounts"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if len(body.Accounts) != 2 || body.Accounts[0].Name != "ds-1" || body.Accounts[1].Name != "kimi-1" {
		t.Errorf("list order = %+v, want ds-1 then kimi-1", body.Accounts)
	}
}

func TestReorderAccountsValidation(t *testing.T) {
	cases := map[string]struct {
		body string
		want int
	}{
		"empty names":   {`{"names":[]}`, http.StatusBadRequest},
		"missing names": {`{}`, http.StatusBadRequest},
		"duplicate":     {`{"names":["kimi-1","kimi-1"]}`, http.StatusBadRequest},
		"unknown name":  {`{"names":["kimi-1","ghost"]}`, http.StatusNotFound},
		"unknown field": {`{"names":["kimi-1"],"extra":1}`, http.StatusBadRequest},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			f := newFixture(t)
			rec := f.do(t, "POST", "/admin/accounts/reorder", adminKey, tc.body)
			if rec.Code != tc.want {
				t.Errorf("status = %d, want %d: %s", rec.Code, tc.want, rec.Body)
			}
		})
	}
}

func TestReorderModels(t *testing.T) {
	f := newFixture(t)
	rec := f.do(t, "POST", "/admin/models", adminKey,
		`{"id":"kimi-1/k3","account":"kimi-1","native_model":"kimi-k3","protocol":"anthropic"}`)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create model = %d: %s", rec.Code, rec.Body)
	}

	rec = f.do(t, "POST", "/admin/models/reorder", adminKey, `{"ids":["kimi-1/k3","kimi-1/k2"]}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("reorder = %d: %s", rec.Code, rec.Body)
	}
	rec = f.do(t, "GET", "/admin/models", adminKey, "")
	var body struct {
		Models []struct {
			ID string `json:"id"`
		} `json:"models"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if len(body.Models) != 2 || body.Models[0].ID != "kimi-1/k3" || body.Models[1].ID != "kimi-1/k2" {
		t.Errorf("list order = %+v, want kimi-1/k3 then kimi-1/k2", body.Models)
	}
}

func TestReorderModelsValidation(t *testing.T) {
	cases := map[string]struct {
		body string
		want int
	}{
		"empty ids":     {`{"ids":[]}`, http.StatusBadRequest},
		"missing ids":   {`{}`, http.StatusBadRequest},
		"duplicate":     {`{"ids":["kimi-1/k2","kimi-1/k2"]}`, http.StatusBadRequest},
		"unknown id":    {`{"ids":["kimi-1/k2","ghost/x"]}`, http.StatusNotFound},
		"unknown field": {`{"ids":["kimi-1/k2"],"extra":1}`, http.StatusBadRequest},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			f := newFixture(t)
			rec := f.do(t, "POST", "/admin/models/reorder", adminKey, tc.body)
			if rec.Code != tc.want {
				t.Errorf("status = %d, want %d: %s", rec.Code, tc.want, rec.Body)
			}
		})
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

// TestModelEffortsEffectiveDecoration 锁定模型响应的有效档位现算:
// 自动模式(efforts=null)跟随声明源;显式 [{name,value}] 条目本地校验;
// 显式空数组=不支持。
func TestModelEffortsEffectiveDecoration(t *testing.T) {
	f := newFixture(t)
	f.upstream.efforts = map[string][]string{"kimi-1/kimi-k2-turbo": {"high", "low"}}

	get := func() model.Model {
		t.Helper()
		rec := f.do(t, "GET", "/admin/models/kimi-1/k2", adminKey, "")
		if rec.Code != http.StatusOK {
			t.Fatalf("get = %d: %s", rec.Code, rec.Body)
		}
		var body struct {
			Model model.Model `json:"model"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
			t.Fatal(err)
		}
		return body.Model
	}

	wantAuto := []effort.Entry{{Name: "高", Value: "high"}, {Name: "低", Value: "low"}}
	if got := get().EffortsEffective; !slices.Equal(got, wantAuto) {
		t.Errorf("自动模式 efforts_effective = %v, want %v(声明源,原值声明序)", got, wantAuto)
	}

	m := f.models.data["kimi-1/k2"]
	m.Efforts = json.RawMessage(`[{"name":"超","value":"xhigh"},{"name":"","value":"medium"}]`)
	f.models.data["kimi-1/k2"] = m
	wantExplicit := []effort.Entry{{Name: "超", Value: "xhigh"}, {Name: "中", Value: "medium"}}
	if got := get().EffortsEffective; !slices.Equal(got, wantExplicit) {
		t.Errorf("显式条目 efforts_effective = %v, want %v", got, wantExplicit)
	}

	m.Efforts = json.RawMessage(`[]`)
	f.models.data["kimi-1/k2"] = m
	if got := get().EffortsEffective; len(got) != 0 {
		t.Errorf("显式空数组 efforts_effective = %v, want 空(不支持)", got)
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

// TestDeliveryModelsCarryEfforts 锁定下发面 /v1/models 带有效推理档:
// 下游客户端按它渲染/校验档位(与管理面 efforts_effective、Resolve 同口径)。
func TestDeliveryModelsCarryEfforts(t *testing.T) {
	f := newFixture(t)
	f.upstream.efforts = map[string][]string{"kimi-1/kimi-k2-turbo": {"high", "ultra"}}
	rec := f.do(t, "GET", "/v1/models", deliveryKey, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body)
	}
	var body struct {
		Models []struct {
			ID      string         `json:"id"`
			Efforts []effort.Entry `json:"efforts"`
		} `json:"models"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("bad delivery list: %v", err)
	}
	if len(body.Models) != 1 {
		t.Fatalf("delivery list = %d models, want 1", len(body.Models))
	}
	want := []effort.Entry{{Name: "高", Value: "high"}, {Name: "ultra", Value: "ultra"}}
	if !slices.Equal(body.Models[0].Efforts, want) {
		t.Errorf("delivery efforts = %v, want %v(声明源,原值声明序)", body.Models[0].Efforts, want)
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

func TestQuotaRefreshParamDropsCache(t *testing.T) {
	f := newFixture(t)
	rec := f.do(t, "GET", "/admin/accounts/kimi-1/quota?refresh=1", adminKey, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("quota refresh = %d: %s", rec.Code, rec.Body)
	}
	if len(f.quota.forgot) == 0 || f.quota.forgot[len(f.quota.forgot)-1] != "kimi-1" {
		t.Errorf("refresh=1 should drop the cached report, forgot = %v", f.quota.forgot)
	}

	f.quota.forgot = nil
	rec = f.do(t, "GET", "/admin/accounts/kimi-1/quota", adminKey, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("quota = %d: %s", rec.Code, rec.Body)
	}
	if len(f.quota.forgot) != 0 {
		t.Errorf("plain query must keep the cache, forgot = %v", f.quota.forgot)
	}
}

func TestQuotaAutoSkipsIdleAccount(t *testing.T) {
	f := newFixture(t)
	f.activity.active = false
	used := 64.0
	f.quota.cached = quota.Report{Account: "kimi-1", Queryable: true, Meters: []quota.Meter{
		{Kind: provider.MeterUsage, Unit: provider.UnitPercent, Used: &used},
	}}
	f.quota.hasCache = true

	rec := f.do(t, "GET", "/admin/accounts/kimi-1/quota?auto=1", adminKey, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("auto quota = %d: %s", rec.Code, rec.Body)
	}
	if f.quota.queries != 0 {
		t.Errorf("空闲账号的定时轮询不应打上游, Query 被调 %d 次", f.quota.queries)
	}
	var report quota.Report
	if err := json.Unmarshal(rec.Body.Bytes(), &report); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(report.Meters) != 1 || report.Meters[0].Used == nil || *report.Meters[0].Used != 64 {
		t.Errorf("应回过缓存报告, got %+v", report)
	}

	// 账号恢复活跃后,同一轮询路径重新真实查询。
	f.activity.active = true
	rec = f.do(t, "GET", "/admin/accounts/kimi-1/quota?auto=1", adminKey, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("active auto quota = %d: %s", rec.Code, rec.Body)
	}
	if f.quota.queries != 1 {
		t.Errorf("活跃账号的定时轮询应恢复真实查询, Query 被调 %d 次", f.quota.queries)
	}
}

func TestQuotaAutoUsesAccountStopWindow(t *testing.T) {
	f := newFixture(t)
	// 未配置停止查询间隔:走默认窗口。
	f.do(t, "GET", "/admin/accounts/kimi-1/quota?auto=1", adminKey, "")
	if f.activity.idle != activity.DefaultIdleWindow {
		t.Errorf("未配置时窗口 = %v, want 默认 %v", f.activity.idle, activity.DefaultIdleWindow)
	}

	// 账号配置了 stop_interval_minutes:按账号窗口判空闲。
	acc := f.accounts.data["kimi-1"]
	acc.QuotaSettings = &account.QuotaSettings{StopIntervalMinutes: 10}
	f.accounts.data["kimi-1"] = acc
	f.do(t, "GET", "/admin/accounts/kimi-1/quota?auto=1", adminKey, "")
	if f.activity.idle != 10*time.Minute {
		t.Errorf("配置后窗口 = %v, want 10m", f.activity.idle)
	}
}

func TestQuotaAutoWithoutCacheFallsThrough(t *testing.T) {
	f := newFixture(t)
	f.activity.active = false
	// 空闲但无任何缓存(如进程重启后首轮轮询):照常真实查询一次。
	rec := f.do(t, "GET", "/admin/accounts/kimi-1/quota?auto=1", adminKey, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("auto quota = %d: %s", rec.Code, rec.Body)
	}
	if f.quota.queries != 1 {
		t.Errorf("无缓存时轮询应回落真实查询, Query 被调 %d 次", f.quota.queries)
	}
}

func TestQuotaPlainQueryUnaffectedByIdle(t *testing.T) {
	f := newFixture(t)
	f.activity.active = false
	// 首次加载/手动刷新不带 auto=1,空闲账号也照常查询。
	for _, path := range []string{"/admin/accounts/kimi-1/quota", "/admin/accounts/kimi-1/quota?refresh=1"} {
		rec := f.do(t, "GET", path, adminKey, "")
		if rec.Code != http.StatusOK {
			t.Fatalf("%s = %d: %s", path, rec.Code, rec.Body)
		}
	}
	if f.quota.queries != 2 {
		t.Errorf("非轮询路径不受空闲短路影响, Query 被调 %d 次", f.quota.queries)
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
