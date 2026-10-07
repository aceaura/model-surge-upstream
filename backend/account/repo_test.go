package account

import (
	"context"
	"slices"
	"testing"
	"time"

	"github.com/aceaura/model-surge-upstream/backend/apperr"
	"github.com/aceaura/model-surge-upstream/backend/cache"
	"github.com/aceaura/model-surge-upstream/backend/credential"
	"github.com/aceaura/model-surge-upstream/backend/internal/testenv"
	"github.com/aceaura/model-surge-upstream/backend/provider"
)

func apiKey(v string) credential.Credential {
	return credential.Credential{Kind: provider.CredAPIKey, APIKey: v}
}

func input(name, providerID string) Input {
	return Input{Name: name, ProviderID: providerID, Credential: apiKey("sk-" + name + "-secret"), Enabled: true}
}

func newRepo(t *testing.T) *Repo {
	t.Helper()
	s := testenv.Store(t)
	return NewRepo(s.Pool(), testenv.Cache(t))
}

func TestValidateRejects(t *testing.T) {
	cases := map[string]struct {
		in   Input
		code apperr.Code
	}{
		"empty name":       {Input{ProviderID: "kimi.global.subscribe.coding", Credential: apiKey("sk-x-secret")}, apperr.InvalidRequest},
		"unknown provider": {Input{Name: "a", ProviderID: "nope", Credential: apiKey("sk-x-secret")}, apperr.InvalidProvider},
		"empty credential": {Input{Name: "a", ProviderID: "kimi.global.subscribe.coding"}, apperr.InvalidCredential},
		"blank api key":    {Input{Name: "a", ProviderID: "kimi.global.subscribe.coding", Credential: apiKey("  ")}, apperr.InvalidCredential},
		"bad base url":     {Input{Name: "a", ProviderID: "kimi.global.subscribe.coding", Credential: apiKey("sk-x-secret"), BaseURL: "moonshot.cn"}, apperr.InvalidRequest},
		"mismatched kind":  {Input{Name: "a", ProviderID: "kimi.global.subscribe.coding", Credential: credential.Credential{Kind: "oauth_refresh"}}, apperr.InvalidCredential},
		"auto interval out of range": {Input{Name: "a", ProviderID: "kimi.global.subscribe.coding", Credential: apiKey("sk-x-secret"),
			QuotaSettings: &QuotaSettings{AutoIntervalMinutes: 1441}}, apperr.InvalidRequest},
		"stop interval negative": {Input{Name: "a", ProviderID: "kimi.global.subscribe.coding", Credential: apiKey("sk-x-secret"),
			QuotaSettings: &QuotaSettings{StopIntervalMinutes: -1}}, apperr.InvalidRequest},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := validate(tc.in)
			if err == nil {
				t.Fatal("expected error")
			}
			if got := apperr.CodeOf(err); got != tc.code {
				t.Errorf("code = %q, want %q", got, tc.code)
			}
		})
	}
}

func TestValidateNormalizes(t *testing.T) {
	acc, err := validate(Input{Name: "  kimi-1 ", ProviderID: "kimi.global.subscribe.coding", Credential: apiKey("sk-x-secret"), BaseURL: "https://gw.example.com/"})
	if err != nil {
		t.Fatal(err)
	}
	if acc.Name != "kimi-1" {
		t.Errorf("name = %q", acc.Name)
	}
	if acc.BaseURL != "https://gw.example.com" {
		t.Errorf("base_url = %q, trailing slash should be trimmed", acc.BaseURL)
	}
	if acc.Headers == nil {
		t.Error("headers should default to an empty map")
	}
}

func TestValidateQuotaSettingsAccepted(t *testing.T) {
	acc, err := validate(Input{Name: "a", ProviderID: "kimi.global.subscribe.coding", Credential: apiKey("sk-x-secret"),
		QuotaSettings: &QuotaSettings{AutoIntervalMinutes: 5, StopIntervalMinutes: 8}})
	if err != nil {
		t.Fatal(err)
	}
	if acc.QuotaSettings.AutoIntervalMinutes != 5 || acc.QuotaSettings.StopIntervalMinutes != 8 {
		t.Errorf("quota_settings = %+v", acc.QuotaSettings)
	}
}

func TestEffectiveBaseURL(t *testing.T) {
	spec, _ := provider.Get("kimi.global.subscribe.coding")
	if got := (Account{}).EffectiveBaseURL(spec); got != spec.BaseURL {
		t.Errorf("empty override should fall back to provider default, got %q", got)
	}
	if got := (Account{BaseURL: "https://gw"}).EffectiveBaseURL(spec); got != "https://gw" {
		t.Errorf("override should win, got %q", got)
	}
}

func TestKiroEffectiveBaseURL(t *testing.T) {
	spec, _ := provider.Get("kiro.global.subscribe.standard")
	acc := Account{Credential: credential.Credential{Region: "us-east-1", ProfileARN: "arn:aws:codewhisperer:eu-central-1:123:profile/test"}}
	if got := acc.EffectiveBaseURL(spec); got != "https://runtime.eu-central-1.kiro.dev" {
		t.Fatalf("effective base = %q", got)
	}
	acc.BaseURL = spec.BaseURL
	acc.Credential.APIRegion = "eu-west-1"
	if got := acc.EffectiveBaseURL(spec); got != "https://runtime.eu-west-1.kiro.dev" {
		t.Fatalf("default form URL must follow account API region: %q", got)
	}
	acc.Credential.ProfileARN = ""
	acc.BaseURL = spec.BaseURL
	if got := acc.EffectiveBaseURL(spec); got != "https://q.eu-west-1.amazonaws.com" {
		t.Fatalf("Builder ID fallback = %q", got)
	}
	acc.BaseURL = "https://runtime.ap-southeast-1.kiro.dev/"
	if got := acc.EffectiveBaseURL(spec); got != "https://q.ap-southeast-1.amazonaws.com" {
		t.Fatalf("explicit runtime fallback = %q", got)
	}
	acc.BaseURL = "http://localhost:9001"
	if got := acc.EffectiveBaseURL(spec); got != acc.BaseURL {
		t.Fatalf("custom base URL lost: %q", got)
	}
}

func TestMergeKiroCredential(t *testing.T) {
	existing := credential.Credential{Kind: provider.CredKiroRefresh, RefreshToken: "rt", AccessToken: "at", Expiry: time.Now().Add(time.Hour), Region: "us-east-1", ClientID: "id", ClientSecret: "secret"}
	in := credential.Credential{Kind: provider.CredKiroRefresh, Region: "us-east-1", ClientID: "id"}
	got := mergeKiroCredential(in, existing)
	if got.RefreshToken != "rt" || got.AccessToken != "at" || got.ClientSecret != "secret" {
		t.Fatal("blank secrets must preserve login state for unchanged registration")
	}
	in.Region = "eu-west-1"
	if got := mergeKiroCredential(in, existing); got.AccessToken != "" || !got.Expiry.IsZero() {
		t.Fatal("authentication region change must discard old access token")
	}
	in.Region = "us-east-1"
	in.ClientID = "new-registration"
	if got := mergeKiroCredential(in, existing); got.ClientSecret != "" || got.AccessToken != "" {
		t.Fatal("new registration must not reuse old registration secret")
	}
	in.ClientID = ""
	if got := mergeKiroCredential(in, existing); got.ClientSecret != "" || got.AccessToken != "" {
		t.Fatal("switching to Desktop must remove SSO registration")
	}
}

func TestKiroCredentialUpdateRoundTrip(t *testing.T) {
	r := newRepo(t)
	ctx := context.Background()
	cred := credential.Credential{Kind: provider.CredKiroRefresh, RefreshToken: "rt-original", ClientID: "id", ClientSecret: "secret", Region: "us-east-1", ProfileARN: "arn:aws:codewhisperer:us-east-1:123:profile/test"}
	acc, err := r.Create(ctx, Input{Name: "kiro-1", ProviderID: "kiro.global.subscribe.standard", Credential: cred, Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	if acc.View().Credential.ClientSecret != "***" {
		t.Fatal("SSO secret must be masked")
	}
	cred.AccessToken, cred.Expiry, cred.RefreshToken = "at-live", time.Now().Add(time.Hour), "rt-rotated"
	if err := r.UpdateCredential(ctx, acc.Name, cred); err != nil {
		t.Fatal(err)
	}
	updated, err := r.Update(ctx, Input{Name: acc.Name, ProviderID: "kiro.global.subscribe.standard", Credential: credential.Credential{
		Kind: provider.CredKiroRefresh, ClientID: "id", Region: "us-east-1", ProfileARN: cred.ProfileARN, APIRegion: "eu-central-1",
	}, Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	if updated.Credential.RefreshToken != "rt-rotated" || updated.Credential.AccessToken != "at-live" || updated.Credential.ClientSecret != "secret" || updated.Credential.APIRegion != "eu-central-1" {
		t.Fatal("blank-secret edit lost rotated credentials or region change")
	}
	read, err := r.Get(ctx, acc.Name)
	if err != nil || read.Credential != updated.Credential {
		t.Fatalf("kiro credential persistence/cache mismatch: %v", err)
	}
}

func TestViewRedacts(t *testing.T) {
	v := Account{Name: "kimi-1", Credential: apiKey("sk-abcdefghijkl")}.View()
	if v.Credential.APIKey == "sk-abcdefghijkl" {
		t.Error("view must redact the credential")
	}
}

func TestCreateAndGet(t *testing.T) {
	repo := newRepo(t)
	ctx := context.Background()

	created, err := repo.Create(ctx, input("kimi-1", "kimi.global.subscribe.coding"))
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if created.CreatedAt.IsZero() || created.UpdatedAt.IsZero() {
		t.Error("timestamps should be set")
	}

	got, err := repo.Get(ctx, "kimi-1")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.ProviderID != "kimi.global.subscribe.coding" || got.Credential.APIKey != created.Credential.APIKey {
		t.Errorf("round trip mismatch: %+v", got)
	}
	if !got.Enabled {
		t.Error("enabled should persist")
	}
}

func TestGetNotFound(t *testing.T) {
	repo := newRepo(t)
	_, err := repo.Get(context.Background(), "ghost")
	if !apperr.Is(err, apperr.NotFound) {
		t.Errorf("err = %v, want not_found", err)
	}
}

func TestCreateDuplicate(t *testing.T) {
	repo := newRepo(t)
	ctx := context.Background()
	if _, err := repo.Create(ctx, input("kimi-1", "kimi.global.subscribe.coding")); err != nil {
		t.Fatal(err)
	}
	_, err := repo.Create(ctx, input("kimi-1", "deepseek.global.api.standard"))
	if !apperr.Is(err, apperr.AlreadyExists) {
		t.Errorf("err = %v, want already_exists", err)
	}
}

func TestUpdate(t *testing.T) {
	repo := newRepo(t)
	ctx := context.Background()
	created, err := repo.Create(ctx, input("kimi-1", "kimi.global.subscribe.coding"))
	if err != nil {
		t.Fatal(err)
	}

	in := input("kimi-1", "kimi.global.subscribe.coding")
	in.Enabled = false
	in.BaseURL = "https://gw.example.com"
	in.Headers = map[string]string{"x-trace": "on"}
	updated, err := repo.Update(ctx, in)
	if err != nil {
		t.Fatalf("update: %v", err)
	}
	if updated.Enabled {
		t.Error("enabled should be false after update")
	}
	if updated.BaseURL != "https://gw.example.com" || updated.Headers["x-trace"] != "on" {
		t.Errorf("update did not apply: %+v", updated)
	}
	if !updated.CreatedAt.Equal(created.CreatedAt) {
		t.Error("created_at should be preserved")
	}

	got, err := repo.Get(ctx, "kimi-1")
	if err != nil {
		t.Fatal(err)
	}
	if got.Enabled || got.Headers["x-trace"] != "on" {
		t.Errorf("cache and db disagree with the update: %+v", got)
	}
}

func TestUpdateKeepsCredentialWhenOmitted(t *testing.T) {
	repo := newRepo(t)
	ctx := context.Background()
	created, err := repo.Create(ctx, input("kimi-1", "kimi.global.subscribe.coding"))
	if err != nil {
		t.Fatal(err)
	}
	updated, err := repo.Update(ctx, Input{Name: "kimi-1", Enabled: true})
	if err != nil {
		t.Fatalf("update: %v", err)
	}
	if updated.Credential.APIKey != created.Credential.APIKey {
		t.Error("omitted credential should be preserved")
	}
	if updated.ProviderID != created.ProviderID {
		t.Error("omitted provider should be preserved")
	}
}

func TestMergeAPIKeyCredentialConsoleAccessToken(t *testing.T) {
	existing := credential.Credential{Kind: provider.CredAPIKey, APIKey: "sk-existing", WebRefreshToken: "web-existing", ConsoleAccessToken: "console-existing"}
	for _, blank := range []string{"", "   "} {
		merged := mergeAPIKeyCredential(credential.Credential{Kind: provider.CredAPIKey, APIKey: "sk-new", ConsoleAccessToken: blank}, existing)
		if merged.APIKey != "sk-new" || merged.ConsoleAccessToken != existing.ConsoleAccessToken || merged.WebRefreshToken != existing.WebRefreshToken {
			t.Error("changing API key must preserve blank console/web tokens")
		}
	}
	merged := mergeAPIKeyCredential(credential.Credential{Kind: provider.CredAPIKey, ConsoleAccessToken: "console-new"}, existing)
	if merged.APIKey != existing.APIKey || merged.ConsoleAccessToken != "console-new" {
		t.Error("changing console token must preserve blank API key")
	}
}

func TestMergeBailianAuthKeys(t *testing.T) {
	existing := apiKey("sk-inference")
	existing.BailianAccessKeyID, existing.BailianAccessKeySecret = "old-id", "old-secret"
	existing.ConsoleAccessToken = "old-token"
	existing.ConsoleVerifiedAt = time.Date(2026, 10, 7, 12, 0, 0, 0, time.UTC)
	for _, blank := range []string{"", "   "} {
		in := credential.Credential{Kind: provider.CredAPIKey, BailianAccessKeyID: blank, BailianAccessKeySecret: blank}
		if got := mergeAPIKeyCredential(in, existing); got != existing {
			t.Fatal("blank edit must preserve all authentication fields")
		}
		in.APIKey = "new-inference-key"
		got := mergeAPIKeyCredential(in, existing)
		if got.APIKey != in.APIKey || got.ConsoleAccessToken != existing.ConsoleAccessToken || !got.ConsoleVerifiedAt.Equal(existing.ConsoleVerifiedAt) {
			t.Fatal("inference key changes must not invalidate console authentication")
		}
	}
	for _, change := range []string{"id", "secret"} {
		in := credential.Credential{Kind: provider.CredAPIKey, ConsoleAccessToken: "unverified-token"}
		if change == "id" {
			in.BailianAccessKeyID = "new-id"
		} else {
			in.BailianAccessKeySecret = "new-secret"
		}
		got := mergeAPIKeyCredential(in, existing)
		if got.ConsoleAccessToken != "" || !got.ConsoleVerifiedAt.IsZero() || got.APIKey != existing.APIKey {
			t.Fatal("changed AK/SK must discard unverified token and timestamp")
		}
		in.ConsoleAccessToken, in.ConsoleVerifiedAt = "backend-new-token", existing.ConsoleVerifiedAt.Add(time.Hour)
		got = mergeAPIKeyCredential(in, existing)
		if got.ConsoleAccessToken != in.ConsoleAccessToken || !got.ConsoleVerifiedAt.Equal(in.ConsoleVerifiedAt) {
			t.Fatal("backend verification must survive changed-key merge")
		}
		in.ConsoleAccessToken = existing.ConsoleAccessToken
		got = mergeAPIKeyCredential(in, existing)
		if got.ConsoleAccessToken != existing.ConsoleAccessToken || !got.ConsoleVerifiedAt.Equal(in.ConsoleVerifiedAt) {
			t.Fatal("successful backend verification may mint the same token value")
		}
	}
}

func TestBailianCredentialJSONBRoundTrip(t *testing.T) {
	repo := newRepo(t)
	ctx := context.Background()
	in := input("bl", "bailian.cn.subscribe.token-plan")
	in.Credential.BailianAccessKeyID, in.Credential.BailianAccessKeySecret = "stored-id", "stored-secret"
	in.Credential.ConsoleAccessToken = "first-token"
	in.Credential.ConsoleVerifiedAt = time.Date(2026, 10, 7, 12, 0, 0, 0, time.UTC)
	if _, err := repo.Create(ctx, in); err != nil {
		t.Fatal(err)
	}
	cred := in.Credential
	cred.ConsoleAccessToken = "rotated-token"
	cred.ConsoleVerifiedAt = cred.ConsoleVerifiedAt.Add(time.Hour)
	if err := repo.UpdateCredential(ctx, in.Name, cred); err != nil {
		t.Fatal(err)
	}
	// A fresh repo with no Redis account cache must restore from JSONB.
	fresh := NewRepo(repo.pool, cache.New(nil, time.Minute))
	got, err := fresh.Get(ctx, in.Name)
	if err != nil || got.Credential != cred {
		t.Fatalf("restart restoration: %v", err)
	}
	updated, err := fresh.Update(ctx, Input{Name: in.Name, Credential: credential.Credential{Kind: provider.CredAPIKey}, Enabled: true})
	if err != nil || updated.Credential != cred {
		t.Fatalf("blank edit: %v", err)
	}
	if updated.View().Credential.BailianAccessKeyID != "***" || updated.View().Credential.BailianAccessKeySecret != "***" {
		t.Fatal("account view must mask persisted AK/SK")
	}
	cred.ConsoleAccessToken, cred.ConsoleVerifiedAt = "", time.Time{}
	if err := fresh.UpdateCredential(ctx, in.Name, cred); err != nil {
		t.Fatal(err)
	}
	persisted, err := fresh.List(ctx)
	if err != nil || len(persisted) != 1 || persisted[0].Credential != cred {
		t.Fatalf("cleared authentication must persist without dropping keys: %v", err)
	}
}

func TestUpdateConsoleAccessToken(t *testing.T) {
	repo := newRepo(t)
	ctx := context.Background()
	in := input("bailian-1", "bailian.cn.subscribe.token-plan")
	in.Credential.ConsoleAccessToken = "console-initial"
	if _, err := repo.Create(ctx, in); err != nil {
		t.Fatal(err)
	}
	in.Credential = credential.Credential{Kind: provider.CredAPIKey, APIKey: "sk-new"}
	updated, err := repo.Update(ctx, in)
	if err != nil || updated.Credential.ConsoleAccessToken != "console-initial" {
		t.Fatalf("blank console token should be preserved: %v", err)
	}
	in.Credential = credential.Credential{Kind: provider.CredAPIKey, ConsoleAccessToken: "console-new"}
	if _, err := repo.Update(ctx, in); err != nil {
		t.Fatal(err)
	}
	// List 直读 JSONB,不依赖账号缓存来验证持久化。
	accounts, err := repo.List(ctx)
	if err != nil || len(accounts) != 1 {
		t.Fatalf("list persisted account: %v", err)
	}
	if accounts[0].Credential.APIKey != "sk-new" || accounts[0].Credential.ConsoleAccessToken != "console-new" {
		t.Error("console token and API key must persist independently")
	}
}

func TestUpdateMergesAPIKeyCredentialFields(t *testing.T) {
	repo := newRepo(t)
	ctx := context.Background()
	if _, err := repo.Create(ctx, input("kimi-1", "kimi.global.subscribe.coding")); err != nil {
		t.Fatal(err)
	}

	// 只给网页 token 不给密钥:密钥保留,token 落库。
	updated, err := repo.Update(ctx, Input{
		Name:    "kimi-1",
		Enabled: true,
		Credential: credential.Credential{
			Kind:            provider.CredAPIKey,
			WebRefreshToken: "web-rt-1",
		},
	})
	if err != nil {
		t.Fatalf("update: %v", err)
	}
	if updated.Credential.APIKey != "sk-kimi-1-secret" {
		t.Errorf("密钥留空应保留: %+v", updated.Credential)
	}
	if updated.Credential.WebRefreshToken != "web-rt-1" {
		t.Errorf("web token 应落库: %+v", updated.Credential)
	}

	// 只换密钥不给 token:token 保留,密钥更新。
	updated, err = repo.Update(ctx, Input{
		Name:       "kimi-1",
		Enabled:    true,
		Credential: credential.Credential{Kind: provider.CredAPIKey, APIKey: "sk-new-secret"},
	})
	if err != nil {
		t.Fatalf("update: %v", err)
	}
	if updated.Credential.APIKey != "sk-new-secret" {
		t.Errorf("密钥应被更新: %+v", updated.Credential)
	}
	if updated.Credential.WebRefreshToken != "web-rt-1" {
		t.Errorf("web token 留空应保留: %+v", updated.Credential)
	}
}

func TestUpdateNotFound(t *testing.T) {
	repo := newRepo(t)
	_, err := repo.Update(context.Background(), input("ghost", "kimi.global.subscribe.coding"))
	if !apperr.Is(err, apperr.NotFound) {
		t.Errorf("err = %v, want not_found", err)
	}
}

func TestList(t *testing.T) {
	s := testenv.Store(t)
	repo := NewRepo(s.Pool(), testenv.Cache(t))
	ctx := context.Background()
	for _, name := range []string{"kimi-2", "kimi-1"} {
		if _, err := repo.Create(ctx, input(name, "kimi.global.subscribe.coding")); err != nil {
			t.Fatal(err)
		}
	}
	got, err := repo.List(ctx)
	if err != nil {
		t.Fatal(err)
	}
	// 新账号 sort_order 按创建序递增,列表即手动序(创建序),不再 name 序
	if len(got) != 2 || got[0].Name != "kimi-2" || got[1].Name != "kimi-1" {
		t.Errorf("list should follow creation order, got %+v", got)
	}
	// 老库 sort_order 全并列时回落 name 序,与拖拽功能存在之前一致
	if _, err := s.Pool().Exec(ctx, `UPDATE accounts SET sort_order=0`); err != nil {
		t.Fatal(err)
	}
	got, err = repo.List(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].Name != "kimi-1" || got[1].Name != "kimi-2" {
		t.Errorf("tied sort_order should fall back to name order, got %+v", got)
	}
}

func TestReorder(t *testing.T) {
	repo := newRepo(t)
	ctx := context.Background()
	for _, name := range []string{"a-1", "a-2", "a-3"} {
		if _, err := repo.Create(ctx, input(name, "kimi.global.subscribe.coding")); err != nil {
			t.Fatal(err)
		}
	}
	names := func() []string {
		got, err := repo.List(ctx)
		if err != nil {
			t.Fatal(err)
		}
		out := make([]string, 0, len(got))
		for _, a := range got {
			out = append(out, a.Name)
		}
		return out
	}

	want := []string{"a-3", "a-1", "a-2"}
	if err := repo.Reorder(ctx, want); err != nil {
		t.Fatalf("reorder: %v", err)
	}
	if got := names(); !slices.Equal(got, want) {
		t.Errorf("list = %v, want %v", got, want)
	}

	// 混入不存在的名字整批拒,已写的序号随事务回滚
	if err := repo.Reorder(ctx, []string{"a-2", "ghost"}); !apperr.Is(err, apperr.NotFound) {
		t.Errorf("err = %v, want not_found", err)
	}
	if got := names(); !slices.Equal(got, want) {
		t.Errorf("failed reorder should not persist, list = %v, want %v", got, want)
	}
}

func TestDeleteCascades(t *testing.T) {
	repo := newRepo(t)
	ctx := context.Background()
	if _, err := repo.Create(ctx, input("kimi-1", "kimi.global.subscribe.coding")); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"kimi-1/k2", "kimi-1/k3"} {
		if _, err := repo.pool.Exec(ctx, `INSERT INTO models (id, account, native_model, protocol) VALUES ($1,$2,$3,$4)`,
			id, "kimi-1", "kimi-k2", "anthropic"); err != nil {
			t.Fatal(err)
		}
	}

	n, err := repo.CountModels(ctx, "kimi-1")
	if err != nil || n != 2 {
		t.Fatalf("CountModels = %d, %v", n, err)
	}

	deleted, err := repo.Delete(ctx, "kimi-1")
	if err != nil {
		t.Fatalf("delete: %v", err)
	}
	if len(deleted) != 2 {
		t.Errorf("deleted models = %v, want 2", deleted)
	}
	if _, err := repo.Get(ctx, "kimi-1"); !apperr.Is(err, apperr.NotFound) {
		t.Errorf("account should be gone, err = %v", err)
	}
	var remaining int
	if err := repo.pool.QueryRow(ctx, `SELECT count(*) FROM models WHERE account=$1`, "kimi-1").Scan(&remaining); err != nil {
		t.Fatal(err)
	}
	if remaining != 0 {
		t.Errorf("models remaining = %d, want 0", remaining)
	}
}

func TestDeleteNotFound(t *testing.T) {
	repo := newRepo(t)
	_, err := repo.Delete(context.Background(), "ghost")
	if !apperr.Is(err, apperr.NotFound) {
		t.Errorf("err = %v, want not_found", err)
	}
}

func TestGetBackfillsCache(t *testing.T) {
	backend := testenv.RedisRequired(t)
	s := testenv.Store(t)
	ctx := context.Background()
	key := cache.AccountKey("kimi-1")
	_ = backend.Del(ctx, key)

	repo := NewRepo(s.Pool(), cache.New(backend, time.Minute))
	if _, err := repo.Create(ctx, input("kimi-1", "kimi.global.subscribe.coding")); err != nil {
		t.Fatal(err)
	}
	if raw, err := backend.Get(ctx, key); err != nil || len(raw) == 0 {
		t.Fatalf("create should populate the cache: %v", err)
	}

	_ = backend.Del(ctx, key)
	if _, err := repo.Get(ctx, "kimi-1"); err != nil {
		t.Fatal(err)
	}
	if raw, err := backend.Get(ctx, key); err != nil || len(raw) == 0 {
		t.Fatalf("miss should backfill the cache: %v", err)
	}
}

func TestDeleteInvalidatesCache(t *testing.T) {
	backend := testenv.RedisRequired(t)
	s := testenv.Store(t)
	ctx := context.Background()

	repo := NewRepo(s.Pool(), cache.New(backend, time.Minute))
	if _, err := repo.Create(ctx, input("kimi-1", "kimi.global.subscribe.coding")); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.Delete(ctx, "kimi-1"); err != nil {
		t.Fatal(err)
	}
	if raw, err := backend.Get(ctx, cache.AccountKey("kimi-1")); err == nil && len(raw) > 0 {
		t.Error("delete should invalidate the cache entry")
	}
}
