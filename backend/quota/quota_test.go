package quota

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

func TestQueryNotQueryable(t *testing.T) {
	// anthropic 未声明额度接口。
	q := New(fakeAccounts{"a-1": acct("a-1", "anthropic", "")}, time.Minute)
	got, err := q.Query(context.Background(), "a-1")
	if err != nil {
		t.Fatalf("missing quota api must not be an error: %v", err)
	}
	if got.Queryable {
		t.Error("Queryable should be false")
	}
	if got.Remaining != nil || got.Total != nil {
		t.Error("no numbers should be reported")
	}
}

func TestQueryAccountNotFound(t *testing.T) {
	q := New(fakeAccounts{}, time.Minute)
	if _, err := q.Query(context.Background(), "ghost"); !apperr.Is(err, apperr.NotFound) {
		t.Errorf("code = %q, want not_found", apperr.CodeOf(err))
	}
}

func TestQuerySuccess(t *testing.T) {
	var gotPath, gotAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath, gotAuth = r.URL.Path, r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"balance_infos":[{"currency":"CNY","total_balance":"12.34"}]}`))
	}))
	defer srv.Close()

	q := New(fakeAccounts{"ds-1": acct("ds-1", "deepseek", srv.URL)}, time.Minute)
	got, err := q.Query(context.Background(), "ds-1")
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	if !got.Queryable {
		t.Error("Queryable should be true")
	}
	if got.Remaining == nil || *got.Remaining != 12.34 {
		t.Errorf("remaining = %v, want 12.34", got.Remaining)
	}
	if got.Currency != "CNY" {
		t.Errorf("currency = %q", got.Currency)
	}
	if got.Reset != provider.ResetPrepaid {
		t.Errorf("reset = %q, should come from the provider spec", got.Reset)
	}
	if gotPath != "/user/balance" {
		t.Errorf("path = %q", gotPath)
	}
	if gotAuth != "Bearer sk-abcdefghijkl" {
		t.Errorf("auth header = %q", gotAuth)
	}
}

func TestQueryUpstreamFailure(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer srv.Close()

	q := New(fakeAccounts{"ds-1": acct("ds-1", "deepseek", srv.URL)}, time.Minute)
	_, err := q.Query(context.Background(), "ds-1")
	if !apperr.Is(err, apperr.QuotaUnavailable) {
		t.Errorf("code = %q, want quota_unavailable", apperr.CodeOf(err))
	}
}

func TestQueryUnreachableUpstream(t *testing.T) {
	q := New(fakeAccounts{"ds-1": acct("ds-1", "deepseek", "http://127.0.0.1:1")}, time.Minute)
	if _, err := q.Query(context.Background(), "ds-1"); !apperr.Is(err, apperr.QuotaUnavailable) {
		t.Errorf("code = %q, want quota_unavailable", apperr.CodeOf(err))
	}
}

func TestQueryCachesWithinTTL(t *testing.T) {
	var hits int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		atomic.AddInt64(&hits, 1)
		_, _ = w.Write([]byte(`{"balance":5}`))
	}))
	defer srv.Close()

	q := New(fakeAccounts{"ds-1": acct("ds-1", "deepseek", srv.URL)}, time.Minute)
	for range 3 {
		if _, err := q.Query(context.Background(), "ds-1"); err != nil {
			t.Fatal(err)
		}
	}
	if got := atomic.LoadInt64(&hits); got != 1 {
		t.Errorf("upstream hits = %d, want 1 within TTL", got)
	}
}

func TestQueryRefetchesAfterTTL(t *testing.T) {
	var hits int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		atomic.AddInt64(&hits, 1)
		_, _ = w.Write([]byte(`{"balance":5}`))
	}))
	defer srv.Close()

	q := New(fakeAccounts{"ds-1": acct("ds-1", "deepseek", srv.URL)}, time.Nanosecond)
	for range 2 {
		if _, err := q.Query(context.Background(), "ds-1"); err != nil {
			t.Fatal(err)
		}
		time.Sleep(time.Millisecond)
	}
	if got := atomic.LoadInt64(&hits); got != 2 {
		t.Errorf("upstream hits = %d, want 2 after TTL expiry", got)
	}
}

func TestForget(t *testing.T) {
	var hits int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		atomic.AddInt64(&hits, 1)
		_, _ = w.Write([]byte(`{"balance":5}`))
	}))
	defer srv.Close()

	q := New(fakeAccounts{"ds-1": acct("ds-1", "deepseek", srv.URL)}, time.Minute)
	if _, err := q.Query(context.Background(), "ds-1"); err != nil {
		t.Fatal(err)
	}
	q.Forget("ds-1")
	if _, err := q.Query(context.Background(), "ds-1"); err != nil {
		t.Fatal(err)
	}
	if got := atomic.LoadInt64(&hits); got != 2 {
		t.Errorf("upstream hits = %d, Forget should drop the cache", got)
	}
}

func TestParseBalanceShapes(t *testing.T) {
	cases := map[string]struct {
		body string
		want *float64
	}{
		"deepseek":     {`{"balance_infos":[{"total_balance":"12.34"}]}`, ptr(12.34)},
		"plain number": {`{"balance":7}`, ptr(7)},
		"remaining":    {`{"remaining":3.5}`, ptr(3.5)},
		"unknown":      {`{"whatever":1}`, nil},
		"not json":     {`nope`, nil},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			got, _, _ := parseBalance([]byte(tc.body))
			switch {
			case tc.want == nil && got != nil:
				t.Errorf("remaining = %v, want nil", *got)
			case tc.want != nil && got == nil:
				t.Errorf("remaining = nil, want %v", *tc.want)
			case tc.want != nil && *got != *tc.want:
				t.Errorf("remaining = %v, want %v", *got, *tc.want)
			}
		})
	}
}

func ptr(f float64) *float64 { return &f }

func TestConcurrentQueries(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"balance":5}`))
	}))
	defer srv.Close()

	q := New(fakeAccounts{"ds-1": acct("ds-1", "deepseek", srv.URL)}, time.Minute)
	done := make(chan struct{})
	for range 8 {
		go func() {
			defer func() { done <- struct{}{} }()
			if _, err := q.Query(context.Background(), "ds-1"); err != nil {
				t.Errorf("query: %v", err)
			}
		}()
	}
	for range 8 {
		<-done
	}
}
