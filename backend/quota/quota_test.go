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

func (f fakeAccounts) UpdateCredential(ctx context.Context, name string, cred credential.Credential) error {
	acc, err := f.Get(ctx, name)
	if err != nil {
		return err
	}
	acc.Credential = cred
	f[name] = acc
	return nil
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
	q := New(fakeAccounts{"a-1": acct("a-1", "anthropic.global.api.standard", "")}, time.Minute)
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

func TestQueryDisabledByAccountSettings(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		t.Error("关闭实时查询的账号不应打上游")
		_, _ = w.Write([]byte(`{"balance_infos":[{"currency":"CNY","total_balance":"12.34"}]}`))
	}))
	defer srv.Close()

	a := acct("ds-1", "deepseek.global.api.standard", srv.URL)
	off := false
	a.QuotaSettings = &account.QuotaSettings{Enabled: &off}
	q := New(fakeAccounts{"ds-1": a}, time.Minute)
	got, err := q.Query(context.Background(), "ds-1")
	if err != nil {
		t.Fatalf("关掉查询不是错误: %v", err)
	}
	if got.Queryable || len(got.Meters) != 0 {
		t.Errorf("report = %+v, 关闭时应与不可查同形", got)
	}
}

func TestQueryEnabledUnsetDefaultsOn(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"balance_infos":[{"currency":"CNY","total_balance":"12.34"}]}`))
	}))
	defer srv.Close()

	// 老账号只存了间隔、没有 enabled 字段:必须照旧查询。
	a := acct("ds-1", "deepseek.global.api.standard", srv.URL)
	a.QuotaSettings = &account.QuotaSettings{AutoIntervalMinutes: 5}
	q := New(fakeAccounts{"ds-1": a}, time.Minute)
	got, err := q.Query(context.Background(), "ds-1")
	if err != nil {
		t.Fatal(err)
	}
	if !got.Queryable || len(got.Meters) != 1 {
		t.Errorf("report = %+v, enabled 未表态应按开启", got)
	}
}

func TestQueryAccountNotFound(t *testing.T) {
	q := New(fakeAccounts{}, time.Minute)
	if _, err := q.Query(context.Background(), "ghost"); !apperr.Is(err, apperr.NotFound) {
		t.Errorf("code = %q, want not_found", apperr.CodeOf(err))
	}
}

func TestCachedReturnsStaleReport(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"balance_infos":[{"currency":"CNY","total_balance":"12.34"}]}`))
	}))
	defer srv.Close()

	// TTL 调到极短,查完即过期:验证 Cached 无视存活期回过缓存。
	q := New(fakeAccounts{"ds-1": acct("ds-1", "deepseek.global.api.standard", srv.URL)}, time.Nanosecond)
	if _, err := q.Query(context.Background(), "ds-1"); err != nil {
		t.Fatalf("query: %v", err)
	}
	time.Sleep(time.Millisecond)
	if _, ok := q.lookup("ds-1"); ok {
		t.Fatal("lookup 应按 TTL 判过期")
	}
	got, ok := q.Cached("ds-1")
	if !ok {
		t.Fatal("过期缓存 Cached 仍应命中")
	}
	if len(got.Meters) != 1 || got.Meters[0].Remaining == nil || *got.Meters[0].Remaining != 12.34 {
		t.Errorf("Cached 报告内容不符: %+v", got)
	}
	if _, ok := q.Cached("ghost"); ok {
		t.Error("从未缓存的账号 Cached 应返回 ok=false")
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

	q := New(fakeAccounts{"ds-1": acct("ds-1", "deepseek.global.api.standard", srv.URL)}, time.Minute)
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
		t.Errorf("kind/unit = %q/%q, remaining-only script result should map to balance/currency", m.Kind, m.Unit)
	}
	if m.Remaining == nil || *m.Remaining != 12.34 {
		t.Errorf("remaining = %v, want 12.34", m.Remaining)
	}
	if m.Currency != "CNY" {
		t.Errorf("currency = %q", m.Currency)
	}
	if m.Reset != provider.ResetPrepaid {
		t.Errorf("reset = %q, deepseek balance is prepaid", m.Reset)
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

	q := New(fakeAccounts{"ds-1": acct("ds-1", "deepseek.global.api.standard", srv.URL)}, time.Minute)
	_, err := q.Query(context.Background(), "ds-1")
	if !apperr.Is(err, apperr.QuotaUnavailable) {
		t.Errorf("code = %q, want quota_unavailable", apperr.CodeOf(err))
	}
}

func TestQueryUnreachableUpstream(t *testing.T) {
	q := New(fakeAccounts{"ds-1": acct("ds-1", "deepseek.global.api.standard", "http://127.0.0.1:1")}, time.Minute)
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

	q := New(fakeAccounts{"ds-1": acct("ds-1", "deepseek.global.api.standard", srv.URL)}, time.Minute)
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

	q := New(fakeAccounts{"ds-1": acct("ds-1", "deepseek.global.api.standard", srv.URL)}, time.Nanosecond)
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

	q := New(fakeAccounts{"ds-1": acct("ds-1", "deepseek.global.api.standard", srv.URL)}, time.Minute)
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

func TestConcurrentQueries(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"balance":5}`))
	}))
	defer srv.Close()

	q := New(fakeAccounts{"ds-1": acct("ds-1", "deepseek.global.api.standard", srv.URL)}, time.Minute)
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
