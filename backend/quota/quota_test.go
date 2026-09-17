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
	if len(got.Meters) != 0 {
		t.Errorf("meters = %v, want none", got.Meters)
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
	if len(got.Meters) != 1 {
		t.Fatalf("meters = %v, want exactly one", got.Meters)
	}
	m := got.Meters[0]
	if m.Kind != provider.MeterBalance || m.Unit != provider.UnitCurrency {
		t.Errorf("kind/unit = %q/%q, should come from the provider spec", m.Kind, m.Unit)
	}
	if m.Remaining == nil || *m.Remaining != 12.34 {
		t.Errorf("remaining = %v, want 12.34", m.Remaining)
	}
	if m.Currency != "CNY" {
		t.Errorf("currency = %q", m.Currency)
	}
	if m.Reset != provider.ResetPrepaid {
		t.Errorf("reset = %q, should come from the provider spec", m.Reset)
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

func TestParseBodyMeterShapes(t *testing.T) {
	decl := provider.QuotaAPI{Kind: provider.MeterBalance, Unit: provider.UnitCurrency, Reset: provider.ResetPrepaid}
	cases := map[string]struct {
		body      string
		wantCount int
		remaining *float64
		used      *float64
	}{
		"deepseek": {`{"balance_infos":[{"total_balance":"12.34"}]}`, 1, ptr(12.34), nil},
		"multi currency": {`{"balance_infos":[{"currency":"CNY","total_balance":"12.34"},` +
			`{"currency":"USD","total_balance":"1.5"}]}`, 2, ptr(12.34), nil},
		"plain number": {`{"balance":7}`, 1, ptr(7), nil},
		"remaining":    {`{"remaining":3.5}`, 1, ptr(3.5), nil},
		"usage only":   {`{"total_usage":42}`, 1, nil, ptr(42)},
		"unknown":      {`{"whatever":1}`, 0, nil, nil},
		"not json":     {`nope`, 0, nil, nil},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			got := parseBodyMeters([]byte(tc.body), decl)
			if len(got) != tc.wantCount {
				t.Fatalf("meters = %v, want %d", got, tc.wantCount)
			}
			if tc.wantCount == 0 {
				return
			}
			assertNumber(t, "remaining", got[0].Remaining, tc.remaining)
			assertNumber(t, "used", got[0].Used, tc.used)
		})
	}
}

func TestParseBodyMeterReadsResetAt(t *testing.T) {
	decl := provider.QuotaAPI{Kind: provider.MeterUsage, Unit: provider.UnitRequests, Reset: provider.ResetMonthly}
	got := parseBodyMeters([]byte(`{"used":10,"reset_at":"2026-10-01T00:00:00Z"}`), decl)
	if len(got) != 1 {
		t.Fatalf("meters = %v, want one", got)
	}
	if got[0].ResetAt == nil {
		t.Fatal("reset_at should be parsed: it is the useful figure for periodic quota")
	}
	if got[0].ResetAt.Format(time.RFC3339) != "2026-10-01T00:00:00Z" {
		t.Errorf("reset_at = %v", got[0].ResetAt)
	}
}

func TestParseRateLimitMetersSplitsDimensions(t *testing.T) {
	h := http.Header{}
	h.Set("x-ratelimit-remaining-requests", "58")
	h.Set("x-ratelimit-limit-requests", "60")
	h.Set("x-ratelimit-reset-requests", "1790000000")
	h.Set("x-ratelimit-remaining-tokens", "9000")
	h.Set("x-ratelimit-limit-tokens", "10000")
	h.Set("x-ratelimit-reset-tokens", "2026-10-01T00:00:00Z")

	got := parseRateLimitMeters(h)
	if len(got) != 2 {
		t.Fatalf("meters = %v, requests and tokens are independent dimensions", got)
	}
	for _, m := range got {
		if m.Kind != provider.MeterRateLimit {
			t.Errorf("kind = %q, want rate_limit", m.Kind)
		}
		if m.Reset != provider.ResetRolling {
			t.Errorf("reset = %q, want rolling", m.Reset)
		}
		if m.ResetAt == nil {
			t.Errorf("%s: reset_at should be parsed", m.Unit)
		}
	}
	if got[0].Unit != provider.UnitRequests || got[1].Unit != provider.UnitTokens {
		t.Errorf("units = %q, %q", got[0].Unit, got[1].Unit)
	}
	if got[0].Remaining == nil || *got[0].Remaining != 58 {
		t.Errorf("requests remaining = %v", got[0].Remaining)
	}
}

func TestParseRateLimitMetersSkipsAbsentDimensions(t *testing.T) {
	h := http.Header{}
	h.Set("x-ratelimit-remaining-requests", "58")
	if got := parseRateLimitMeters(h); len(got) != 1 {
		t.Errorf("meters = %v, only the reported dimension should appear", got)
	}
	if got := parseRateLimitMeters(nil); got != nil {
		t.Errorf("meters = %v, want none without headers", got)
	}
}

func TestQueryCombinesBodyAndHeaderMeters(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("x-ratelimit-remaining-requests", "58")
		w.Header().Set("x-ratelimit-limit-requests", "60")
		_, _ = w.Write([]byte(`{"balance_infos":[{"currency":"CNY","total_balance":"12.34"}]}`))
	}))
	defer srv.Close()

	q := New(fakeAccounts{"ds-1": acct("ds-1", "deepseek", srv.URL)}, time.Minute)
	got, err := q.Query(context.Background(), "ds-1")
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Meters) != 2 {
		t.Fatalf("meters = %v, body balance and header rate limit should coexist", got.Meters)
	}
	if got.Meters[0].Kind != provider.MeterBalance || got.Meters[1].Kind != provider.MeterRateLimit {
		t.Errorf("kinds = %q, %q", got.Meters[0].Kind, got.Meters[1].Kind)
	}
}

func assertNumber(t *testing.T, field string, got, want *float64) {
	t.Helper()
	switch {
	case want == nil && got != nil:
		t.Errorf("%s = %v, want nil", field, *got)
	case want != nil && got == nil:
		t.Errorf("%s = nil, want %v", field, *want)
	case want != nil && *got != *want:
		t.Errorf("%s = %v, want %v", field, *got, *want)
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
