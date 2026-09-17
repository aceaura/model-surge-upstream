package upmodels

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/aceaura/model-surge-upstream/backend/account"
	"github.com/aceaura/model-surge-upstream/backend/apperr"
	"github.com/aceaura/model-surge-upstream/backend/credential"
	"github.com/aceaura/model-surge-upstream/backend/provider"
)

type fakeAccounts map[string]account.Account

func (f fakeAccounts) Get(_ context.Context, name string) (account.Account, error) {
	acc, ok := f[name]
	if !ok {
		return account.Account{}, apperr.New(apperr.NotFound, "account "+name+" not found")
	}
	return acc, nil
}

func acct(name, providerID, baseURL string) account.Account {
	return account.Account{
		Name:       name,
		ProviderID: providerID,
		Credential: credential.Credential{Kind: provider.CredAPIKey, APIKey: "sk-abcdefghijkl"},
		BaseURL:    baseURL,
		Headers:    map[string]string{},
		Enabled:    true,
	}
}

func TestListNotQueryable(t *testing.T) {
	// ark 未声明列举接口。
	l := New(fakeAccounts{"ark-1": acct("ark-1", "ark", "")}, time.Minute)
	got, err := l.List(context.Background(), "ark-1")
	if err != nil {
		t.Fatalf("missing listing api must not be an error: %v", err)
	}
	if got.Queryable {
		t.Error("Queryable should be false")
	}
	if got.Models == nil {
		t.Error("Models should be an empty slice, not null")
	}
}

func TestListAccountNotFound(t *testing.T) {
	l := New(fakeAccounts{}, time.Minute)
	if _, err := l.List(context.Background(), "ghost"); !apperr.Is(err, apperr.NotFound) {
		t.Errorf("code = %q, want not_found", apperr.CodeOf(err))
	}
}

func TestListSuccessOpenAIShape(t *testing.T) {
	var gotPath, gotAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath, gotAuth = r.URL.Path, r.Header.Get("Authorization")
		_, _ = w.Write([]byte(`{"data":[{"id":"deepseek-chat"},{"id":"deepseek-reasoner"}]}`))
	}))
	defer srv.Close()

	l := New(fakeAccounts{"ds-1": acct("ds-1", "deepseek", srv.URL)}, time.Minute)
	got, err := l.List(context.Background(), "ds-1")
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if !got.Queryable {
		t.Error("Queryable should be true")
	}
	if len(got.Models) != 2 {
		t.Fatalf("models = %v, want 2 entries", got.Models)
	}
	if got.Models[0].ID != "deepseek-chat" || got.Models[1].ID != "deepseek-reasoner" {
		t.Errorf("models should be sorted by id, got %v", got.Models)
	}
	if gotPath != "/models" {
		t.Errorf("path = %q, should come from the provider spec", gotPath)
	}
	if gotAuth != "Bearer sk-abcdefghijkl" {
		t.Errorf("auth header = %q", gotAuth)
	}
}

func TestListAnthropicKeyAuth(t *testing.T) {
	var gotKey, gotVersion string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotKey = r.Header.Get("x-api-key")
		gotVersion = r.Header.Get("anthropic-version")
		_, _ = w.Write([]byte(`{"data":[{"id":"kimi-k2-turbo"}]}`))
	}))
	defer srv.Close()

	l := New(fakeAccounts{"kimi-1": acct("kimi-1", "kimi", srv.URL)}, time.Minute)
	if _, err := l.List(context.Background(), "kimi-1"); err != nil {
		t.Fatalf("list: %v", err)
	}
	if gotKey != "sk-abcdefghijkl" {
		t.Errorf("x-api-key = %q, anthropic_key providers must not use bearer", gotKey)
	}
	if gotVersion == "" {
		t.Error("anthropic-version header should be set")
	}
}

func TestListUpstreamFailure(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer srv.Close()

	l := New(fakeAccounts{"ds-1": acct("ds-1", "deepseek", srv.URL)}, time.Minute)
	_, err := l.List(context.Background(), "ds-1")
	if !apperr.Is(err, apperr.UpstreamUnavailable) {
		t.Errorf("code = %q, want upstream_unavailable", apperr.CodeOf(err))
	}
}

func TestListUnreachableUpstream(t *testing.T) {
	l := New(fakeAccounts{"ds-1": acct("ds-1", "deepseek", "http://127.0.0.1:1")}, time.Minute)
	if _, err := l.List(context.Background(), "ds-1"); !apperr.Is(err, apperr.UpstreamUnavailable) {
		t.Errorf("code = %q, want upstream_unavailable", apperr.CodeOf(err))
	}
}

func TestListCachesWithinTTL(t *testing.T) {
	var hits int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		atomic.AddInt64(&hits, 1)
		_, _ = w.Write([]byte(`{"data":[{"id":"m"}]}`))
	}))
	defer srv.Close()

	l := New(fakeAccounts{"ds-1": acct("ds-1", "deepseek", srv.URL)}, time.Minute)
	for range 3 {
		if _, err := l.List(context.Background(), "ds-1"); err != nil {
			t.Fatal(err)
		}
	}
	if got := atomic.LoadInt64(&hits); got != 1 {
		t.Errorf("upstream hits = %d, want 1 within TTL", got)
	}
}

func TestForget(t *testing.T) {
	var hits int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		atomic.AddInt64(&hits, 1)
		_, _ = w.Write([]byte(`{"data":[{"id":"m"}]}`))
	}))
	defer srv.Close()

	l := New(fakeAccounts{"ds-1": acct("ds-1", "deepseek", srv.URL)}, time.Minute)
	if _, err := l.List(context.Background(), "ds-1"); err != nil {
		t.Fatal(err)
	}
	l.Forget("ds-1")
	if _, err := l.List(context.Background(), "ds-1"); err != nil {
		t.Fatal(err)
	}
	if got := atomic.LoadInt64(&hits); got != 2 {
		t.Errorf("upstream hits = %d, Forget should drop the cache", got)
	}
}

func TestParseEntriesShapes(t *testing.T) {
	cases := map[string]struct {
		body string
		want []Entry
	}{
		"openai data": {
			`{"data":[{"id":"gpt-4"},{"id":"gpt-3"}]}`,
			[]Entry{{ID: "gpt-3"}, {ID: "gpt-4"}},
		},
		"gemini models with display name": {
			`{"models":[{"name":"models/gemini-pro","displayName":"Gemini Pro"}]}`,
			[]Entry{{ID: "models/gemini-pro", DisplayName: "Gemini Pro"}},
		},
		"duplicates collapse": {
			`{"data":[{"id":"a"},{"id":"a"}]}`,
			[]Entry{{ID: "a"}},
		},
		"entries without id are skipped": {
			`{"data":[{"object":"model"},{"id":"ok"}]}`,
			[]Entry{{ID: "ok"}},
		},
		"unknown envelope": {`{"whatever":[{"id":"a"}]}`, []Entry{}},
		"not json":         {`nope`, []Entry{}},
		"not a list":       {`{"data":"oops"}`, []Entry{}},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			got := parseEntries([]byte(tc.body))
			if len(got) != len(tc.want) {
				t.Fatalf("entries = %v, want %v", got, tc.want)
			}
			for i := range got {
				if got[i] != tc.want[i] {
					t.Errorf("entry %d = %+v, want %+v", i, got[i], tc.want[i])
				}
			}
		})
	}
}

func TestConcurrentLists(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"data":[{"id":"m"}]}`))
	}))
	defer srv.Close()

	l := New(fakeAccounts{"ds-1": acct("ds-1", "deepseek", srv.URL)}, time.Minute)
	done := make(chan struct{})
	for range 8 {
		go func() {
			defer func() { done <- struct{}{} }()
			if _, err := l.List(context.Background(), "ds-1"); err != nil {
				t.Errorf("list: %v", err)
			}
		}()
	}
	for range 8 {
		<-done
	}
}
