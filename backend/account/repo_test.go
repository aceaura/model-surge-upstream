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
		"empty name":       {Input{ProviderID: "kimi", Credential: apiKey("sk-x-secret")}, apperr.InvalidRequest},
		"unknown provider": {Input{Name: "a", ProviderID: "nope", Credential: apiKey("sk-x-secret")}, apperr.InvalidProvider},
		"empty credential": {Input{Name: "a", ProviderID: "kimi"}, apperr.InvalidCredential},
		"blank api key":    {Input{Name: "a", ProviderID: "kimi", Credential: apiKey("  ")}, apperr.InvalidCredential},
		"bad base url":     {Input{Name: "a", ProviderID: "kimi", Credential: apiKey("sk-x-secret"), BaseURL: "moonshot.cn"}, apperr.InvalidRequest},
		"mismatched kind":  {Input{Name: "a", ProviderID: "kimi", Credential: credential.Credential{Kind: "oauth_refresh"}}, apperr.InvalidCredential},
		"script var reserved name": {Input{Name: "a", ProviderID: "kimi", Credential: apiKey("sk-x-secret"),
			QuotaScript: &QuotaScript{Code: "({})", Variables: map[string]string{"apiKey": "override"}}}, apperr.InvalidRequest},
		"script var bad identifier": {Input{Name: "a", ProviderID: "kimi", Credential: apiKey("sk-x-secret"),
			QuotaScript: &QuotaScript{Code: "({})", Variables: map[string]string{"1bad": "v"}}}, apperr.InvalidRequest},
		"script var blank name": {Input{Name: "a", ProviderID: "kimi", Credential: apiKey("sk-x-secret"),
			QuotaScript: &QuotaScript{Code: "({})", Variables: map[string]string{"": "v"}}}, apperr.InvalidRequest},
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
	acc, err := validate(Input{Name: "  kimi-1 ", ProviderID: "kimi", Credential: apiKey("sk-x-secret"), BaseURL: "https://gw.example.com/"})
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

func TestValidateScriptVariablesAccepted(t *testing.T) {
	acc, err := validate(Input{Name: "a", ProviderID: "kimi", Credential: apiKey("sk-x-secret"),
		QuotaScript: &QuotaScript{Code: "({})", Variables: map[string]string{"web_token": "abc", "_x": "1"}}})
	if err != nil {
		t.Fatal(err)
	}
	if len(acc.QuotaScript.Variables) != 2 || acc.QuotaScript.Variables["web_token"] != "abc" {
		t.Errorf("variables = %+v", acc.QuotaScript.Variables)
	}
}

func TestEffectiveBaseURL(t *testing.T) {
	spec, _ := provider.Get("kimi")
	if got := (Account{}).EffectiveBaseURL(spec); got != spec.BaseURL {
		t.Errorf("empty override should fall back to provider default, got %q", got)
	}
	if got := (Account{BaseURL: "https://gw"}).EffectiveBaseURL(spec); got != "https://gw" {
		t.Errorf("override should win, got %q", got)
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

	created, err := repo.Create(ctx, input("kimi-1", "kimi"))
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
	if got.ProviderID != "kimi" || got.Credential.APIKey != created.Credential.APIKey {
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
	if _, err := repo.Create(ctx, input("kimi-1", "kimi")); err != nil {
		t.Fatal(err)
	}
	_, err := repo.Create(ctx, input("kimi-1", "deepseek"))
	if !apperr.Is(err, apperr.AlreadyExists) {
		t.Errorf("err = %v, want already_exists", err)
	}
}

func TestUpdate(t *testing.T) {
	repo := newRepo(t)
	ctx := context.Background()
	created, err := repo.Create(ctx, input("kimi-1", "kimi"))
	if err != nil {
		t.Fatal(err)
	}

	in := input("kimi-1", "kimi")
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
	created, err := repo.Create(ctx, input("kimi-1", "kimi"))
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

func TestUpdateMergesAPIKeyCredentialFields(t *testing.T) {
	repo := newRepo(t)
	ctx := context.Background()
	if _, err := repo.Create(ctx, input("kimi-1", "kimi")); err != nil {
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

func TestUpdateNotFound(t *testing.T) {	repo := newRepo(t)
	_, err := repo.Update(context.Background(), input("ghost", "kimi"))
	if !apperr.Is(err, apperr.NotFound) {
		t.Errorf("err = %v, want not_found", err)
	}
}

func TestList(t *testing.T) {
	s := testenv.Store(t)
	repo := NewRepo(s.Pool(), testenv.Cache(t))
	ctx := context.Background()
	for _, name := range []string{"kimi-2", "kimi-1"} {
		if _, err := repo.Create(ctx, input(name, "kimi")); err != nil {
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
		if _, err := repo.Create(ctx, input(name, "kimi")); err != nil {
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
	if _, err := repo.Create(ctx, input("kimi-1", "kimi")); err != nil {
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
	if _, err := repo.Create(ctx, input("kimi-1", "kimi")); err != nil {
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
	if _, err := repo.Create(ctx, input("kimi-1", "kimi")); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.Delete(ctx, "kimi-1"); err != nil {
		t.Fatal(err)
	}
	if raw, err := backend.Get(ctx, cache.AccountKey("kimi-1")); err == nil && len(raw) > 0 {
		t.Error("delete should invalidate the cache entry")
	}
}
