package model

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/aceaura/model-surge-upstream/backend/account"
	"github.com/aceaura/model-surge-upstream/backend/apperr"
	"github.com/aceaura/model-surge-upstream/backend/cache"
	"github.com/aceaura/model-surge-upstream/backend/credential"
	"github.com/aceaura/model-surge-upstream/backend/internal/testenv"
	"github.com/aceaura/model-surge-upstream/backend/provider"
)

// lookupOf 用 account 仓储构造 AccountLookup，与生产装配方式一致。
func lookupOf(repo *account.Repo) AccountLookup {
	return func(ctx context.Context, name string) (provider.Spec, error) {
		acc, err := repo.Get(ctx, name)
		if err != nil {
			return provider.Spec{}, err
		}
		spec, ok := acc.Spec()
		if !ok {
			return provider.Spec{}, apperr.New(apperr.InvalidProvider,
				fmt.Sprintf("account %q references unknown provider %q", name, acc.ProviderID))
		}
		return spec, nil
	}
}

func fixtures(t *testing.T) (*Repo, *account.Repo) {
	t.Helper()
	s := testenv.Store(t)
	c := testenv.Cache(t)
	accounts := account.NewRepo(s.Pool(), c)
	if _, err := accounts.Create(context.Background(), account.Input{
		Name:       "kimi-1",
		ProviderID: "kimi",
		Credential: credential.Credential{Kind: provider.CredAPIKey, APIKey: "sk-kimi-secret"},
		Enabled:    true,
	}); err != nil {
		t.Fatalf("seed account: %v", err)
	}
	return NewRepo(s.Pool(), c, lookupOf(accounts)), accounts
}

func input() Input {
	return Input{
		ID:            "kimi-1/k2",
		Account:       "kimi-1",
		NativeModel:   "kimi-k2-turbo",
		Protocol:      provider.ProtocolAnthropic,
		ContextWindow: 262144,
		Enabled:       true,
	}
}

func TestNormalizeObject(t *testing.T) {
	for _, in := range []json.RawMessage{nil, {}, json.RawMessage(`null`), json.RawMessage(`{}`)} {
		got, err := normalizeObject(in, "defaults")
		if err != nil {
			t.Fatalf("normalizeObject(%s): %v", in, err)
		}
		if string(got) != `{}` {
			t.Errorf("normalizeObject(%s) = %s, want {}", in, got)
		}
	}
	for _, in := range []string{`[1,2]`, `"str"`, `7`, `{oops}`} {
		if _, err := normalizeObject(json.RawMessage(in), "overrides"); err == nil {
			t.Errorf("normalizeObject(%s) should fail", in)
		} else if !apperr.Is(err, apperr.InvalidJSON) {
			t.Errorf("normalizeObject(%s) code = %q", in, apperr.CodeOf(err))
		}
	}
}

func TestCreateAndGet(t *testing.T) {
	repo, _ := fixtures(t)
	ctx := context.Background()

	created, err := repo.Create(ctx, input())
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if string(created.Defaults) != `{}` || string(created.Overrides) != `{}` {
		t.Errorf("empty params should normalize to {}: %+v", created)
	}

	got, err := repo.Get(ctx, "kimi-1/k2")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.NativeModel != "kimi-k2-turbo" || got.ContextWindow != 262144 || got.Protocol != provider.ProtocolAnthropic {
		t.Errorf("round trip mismatch: %+v", got)
	}
}

func TestCreateWithParams(t *testing.T) {
	repo, _ := fixtures(t)
	in := input()
	in.Defaults = json.RawMessage(`{"temperature":0.6}`)
	in.Overrides = json.RawMessage(`{"max_tokens":8192}`)

	created, err := repo.Create(context.Background(), in)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	var defaults, overrides map[string]any
	if err := json.Unmarshal(created.Defaults, &defaults); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(created.Overrides, &overrides); err != nil {
		t.Fatal(err)
	}
	if defaults["temperature"] != 0.6 || overrides["max_tokens"] != float64(8192) {
		t.Errorf("params not persisted: %v %v", defaults, overrides)
	}
}

func TestCreateRejects(t *testing.T) {
	repo, _ := fixtures(t)
	ctx := context.Background()

	cases := map[string]struct {
		mutate func(*Input)
		code   apperr.Code
	}{
		"empty id":          {func(in *Input) { in.ID = "" }, apperr.InvalidRequest},
		"empty account":     {func(in *Input) { in.Account = "" }, apperr.InvalidRequest},
		"empty native":      {func(in *Input) { in.NativeModel = "" }, apperr.InvalidRequest},
		"negative window":   {func(in *Input) { in.ContextWindow = -1 }, apperr.InvalidRequest},
		"missing account":   {func(in *Input) { in.Account = "ghost" }, apperr.NotFound},
		"bad protocol":      {func(in *Input) { in.Protocol = provider.ProtocolResponses }, apperr.InvalidProtocol},
		"empty protocol":    {func(in *Input) { in.Protocol = "" }, apperr.InvalidProtocol},
		"bad defaults json": {func(in *Input) { in.Defaults = json.RawMessage(`[1]`) }, apperr.InvalidJSON},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			in := input()
			tc.mutate(&in)
			_, err := repo.Create(ctx, in)
			if err == nil {
				t.Fatal("expected error")
			}
			if got := apperr.CodeOf(err); got != tc.code {
				t.Errorf("code = %q, want %q (err: %v)", got, tc.code, err)
			}
		})
	}
}

func TestProtocolErrorListsSupported(t *testing.T) {
	repo, _ := fixtures(t)
	in := input()
	in.Protocol = provider.ProtocolResponses
	_, err := repo.Create(context.Background(), in)
	if err == nil {
		t.Fatal("expected error")
	}
	spec, _ := provider.Get("kimi")
	for _, p := range spec.Protocols {
		if !contains(err.Error(), p) {
			t.Errorf("error %q should list supported protocol %q", err, p)
		}
	}
}

func contains(haystack, needle string) bool {
	return len(needle) > 0 && len(haystack) >= len(needle) && (func() bool {
		for i := 0; i+len(needle) <= len(haystack); i++ {
			if haystack[i:i+len(needle)] == needle {
				return true
			}
		}
		return false
	})()
}

func TestCreateDuplicate(t *testing.T) {
	repo, _ := fixtures(t)
	ctx := context.Background()
	if _, err := repo.Create(ctx, input()); err != nil {
		t.Fatal(err)
	}
	_, err := repo.Create(ctx, input())
	if !apperr.Is(err, apperr.AlreadyExists) {
		t.Errorf("err = %v, want already_exists", err)
	}
}

func TestUpdate(t *testing.T) {
	repo, _ := fixtures(t)
	ctx := context.Background()
	created, err := repo.Create(ctx, input())
	if err != nil {
		t.Fatal(err)
	}

	in := input()
	in.Enabled = false
	in.ContextWindow = 131072
	in.Overrides = json.RawMessage(`{"thinking":{"budget_tokens":65535}}`)
	updated, err := repo.Update(ctx, in)
	if err != nil {
		t.Fatalf("update: %v", err)
	}
	if updated.Enabled || updated.ContextWindow != 131072 {
		t.Errorf("update did not apply: %+v", updated)
	}
	if !updated.CreatedAt.Equal(created.CreatedAt) {
		t.Error("created_at should be preserved")
	}

	got, err := repo.Get(ctx, in.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Enabled || got.ContextWindow != 131072 {
		t.Errorf("cache and db disagree: %+v", got)
	}
}

func TestUpdateKeepsOmittedFields(t *testing.T) {
	repo, _ := fixtures(t)
	ctx := context.Background()
	if _, err := repo.Create(ctx, input()); err != nil {
		t.Fatal(err)
	}
	updated, err := repo.Update(ctx, Input{ID: "kimi-1/k2", Enabled: true})
	if err != nil {
		t.Fatalf("update: %v", err)
	}
	if updated.Account != "kimi-1" || updated.NativeModel != "kimi-k2-turbo" || updated.Protocol != provider.ProtocolAnthropic {
		t.Errorf("omitted fields should be preserved: %+v", updated)
	}
}

func TestUpdateNotFound(t *testing.T) {
	repo, _ := fixtures(t)
	in := input()
	in.ID = "ghost/x"
	if _, err := repo.Update(context.Background(), in); !apperr.Is(err, apperr.NotFound) {
		t.Errorf("err = %v, want not_found", err)
	}
}

func TestListFiltersByAccount(t *testing.T) {
	repo, accounts := fixtures(t)
	ctx := context.Background()
	if _, err := accounts.Create(ctx, account.Input{
		Name:       "ds-1",
		ProviderID: "deepseek",
		Credential: credential.Credential{Kind: provider.CredAPIKey, APIKey: "sk-ds-secret"},
		Enabled:    true,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.Create(ctx, input()); err != nil {
		t.Fatal(err)
	}
	other := input()
	other.ID = "ds-1/v4"
	other.Account = "ds-1"
	other.NativeModel = "deepseek-v4"
	if _, err := repo.Create(ctx, other); err != nil {
		t.Fatal(err)
	}

	all, err := repo.List(ctx, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 2 {
		t.Errorf("unfiltered list = %d, want 2", len(all))
	}

	only, err := repo.List(ctx, "ds-1")
	if err != nil {
		t.Fatal(err)
	}
	if len(only) != 1 || only[0].ID != "ds-1/v4" {
		t.Errorf("filtered list = %+v", only)
	}
}

func TestDeleteKeepsAccount(t *testing.T) {
	repo, accounts := fixtures(t)
	ctx := context.Background()
	if _, err := repo.Create(ctx, input()); err != nil {
		t.Fatal(err)
	}
	if err := repo.Delete(ctx, "kimi-1/k2"); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if _, err := repo.Get(ctx, "kimi-1/k2"); !apperr.Is(err, apperr.NotFound) {
		t.Errorf("model should be gone, err = %v", err)
	}
	if _, err := accounts.Get(ctx, "kimi-1"); err != nil {
		t.Errorf("deleting a model must not affect its account: %v", err)
	}
}

func TestDeleteNotFound(t *testing.T) {
	repo, _ := fixtures(t)
	if err := repo.Delete(context.Background(), "ghost/x"); !apperr.Is(err, apperr.NotFound) {
		t.Errorf("err = %v, want not_found", err)
	}
}

func TestGetBackfillsCache(t *testing.T) {
	backend := testenv.RedisRequired(t)
	s := testenv.Store(t)
	ctx := context.Background()
	c := cache.New(backend, time.Minute)

	accounts := account.NewRepo(s.Pool(), c)
	if _, err := accounts.Create(ctx, account.Input{
		Name: "kimi-1", ProviderID: "kimi",
		Credential: credential.Credential{Kind: provider.CredAPIKey, APIKey: "sk-kimi-secret"},
		Enabled:    true,
	}); err != nil {
		t.Fatal(err)
	}
	repo := NewRepo(s.Pool(), c, lookupOf(accounts))
	if _, err := repo.Create(ctx, input()); err != nil {
		t.Fatal(err)
	}

	key := cache.ModelKey("kimi-1/k2")
	_ = backend.Del(ctx, key)
	if _, err := repo.Get(ctx, "kimi-1/k2"); err != nil {
		t.Fatal(err)
	}
	if raw, err := backend.Get(ctx, key); err != nil || len(raw) == 0 {
		t.Fatalf("miss should backfill the cache: %v", err)
	}
}
