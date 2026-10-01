package quota

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/aceaura/model-surge-upstream/backend/account"
	"github.com/aceaura/model-surge-upstream/backend/apperr"
	"github.com/aceaura/model-surge-upstream/backend/provider"
)

func scriptAcct(baseURL string, code string) account.Account {
	a := acct("relay-1", "openai", baseURL)
	a.QuotaScript = &account.QuotaScript{Enabled: true, Code: code}
	return a
}

const balanceScript = `({
  request: {
    url: "{{baseUrl}}/user/balance",
    method: "GET",
    headers: { "Authorization": "Bearer {{apiKey}}" }
  },
  extractor: function(response) {
    return { planName: "预付费", remaining: response.balance, unit: "USD" };
  }
})`

func TestScriptQuerySuccess(t *testing.T) {
	var gotPath, gotAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath, gotAuth = r.URL.Path, r.Header.Get("Authorization")
		_, _ = w.Write([]byte(`{"balance": 9.5}`))
	}))
	defer srv.Close()

	q := New(fakeAccounts{"relay-1": scriptAcct(srv.URL, balanceScript)}, time.Minute)
	got, err := q.Query(context.Background(), "relay-1")
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	if !got.Queryable || len(got.Meters) != 1 {
		t.Fatalf("report = %+v, want one meter", got)
	}
	m := got.Meters[0]
	if m.Kind != provider.MeterBalance {
		t.Errorf("kind = %q, remaining-only should be balance", m.Kind)
	}
	if m.Unit != provider.UnitCurrency || m.Currency != "USD" {
		t.Errorf("unit/currency = %q/%q", m.Unit, m.Currency)
	}
	if m.Remaining == nil || *m.Remaining != 9.5 {
		t.Errorf("remaining = %v", m.Remaining)
	}
	if m.Label != "预付费" {
		t.Errorf("label = %q", m.Label)
	}
	if gotPath != "/user/balance" {
		t.Errorf("path = %q", gotPath)
	}
	if gotAuth != "Bearer sk-abcdefghijkl" {
		t.Errorf("auth = %q, {{apiKey}} should resolve to the account credential", gotAuth)
	}
}

func TestScriptOverridesBuiltinQuota(t *testing.T) {
	// deepseek 内置声明了 /user/balance;账号配了脚本就走路由脚本的端点。
	var gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		_, _ = w.Write([]byte(`{"used": 42}`))
	}))
	defer srv.Close()

	a := acct("ds-1", "deepseek", srv.URL)
	a.QuotaScript = &account.QuotaScript{Enabled: true, Code: `({
	  request: { url: "{{baseUrl}}/custom/usage" },
	  extractor: function(r) { return { used: r.used, unit: "requests" }; }
	})`}
	q := New(fakeAccounts{"ds-1": a}, time.Minute)
	got, err := q.Query(context.Background(), "ds-1")
	if err != nil {
		t.Fatal(err)
	}
	if gotPath != "/custom/usage" {
		t.Errorf("path = %q, script should override the builtin quota api", gotPath)
	}
	if len(got.Meters) != 1 || got.Meters[0].Unit != provider.UnitRequests {
		t.Errorf("meters = %+v", got.Meters)
	}
}

func TestScriptArrayResultWithPercentAndReset(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"windows":[{"n":"5小时","u":64,"r":"2099-01-01T00:00:00Z"},{"n":"7天","u":18}]}`))
	}))
	defer srv.Close()

	code := `({
  request: { url: "{{baseUrl}}/usage" },
  extractor: function(r) {
    return r.windows.map(function(w) {
      return { planName: w.n, used: w.u, total: 100, unit: "%", resetsAt: w.r };
    });
  }
})`
	q := New(fakeAccounts{"relay-1": scriptAcct(srv.URL, code)}, time.Minute)
	got, err := q.Query(context.Background(), "relay-1")
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Meters) != 2 {
		t.Fatalf("meters = %+v, want two windows", got.Meters)
	}
	m := got.Meters[0]
	if m.Unit != provider.UnitPercent || m.Kind != provider.MeterUsage {
		t.Errorf("unit/kind = %q/%q", m.Unit, m.Kind)
	}
	if m.ResetAt == nil || m.ResetAt.Format(time.RFC3339) != "2099-01-01T00:00:00Z" {
		t.Errorf("reset_at = %v, resetsAt should be parsed", m.ResetAt)
	}
	if got.Meters[1].ResetAt != nil {
		t.Errorf("missing resetsAt should stay nil, got %v", got.Meters[1].ResetAt)
	}
}

func TestScriptInvalidPlan(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"success":false,"message":"密钥已停用"}`))
	}))
	defer srv.Close()

	code := `({
  request: { url: "{{baseUrl}}/api/user/self" },
  extractor: function(r) {
    if (r.success) return { remaining: 1 };
    return { isValid: false, invalidMessage: r.message };
  }
})`
	q := New(fakeAccounts{"relay-1": scriptAcct(srv.URL, code)}, time.Minute)
	_, err := q.Query(context.Background(), "relay-1")
	if !apperr.Is(err, apperr.QuotaUnavailable) {
		t.Fatalf("code = %q, want quota_unavailable", apperr.CodeOf(err))
	}
	if !strings.Contains(err.Error(), "密钥已停用") {
		t.Errorf("error should carry invalidMessage: %v", err)
	}
}

func TestScriptUpstreamNon2xx(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer srv.Close()

	q := New(fakeAccounts{"relay-1": scriptAcct(srv.URL, balanceScript)}, time.Minute)
	if _, err := q.Query(context.Background(), "relay-1"); !apperr.Is(err, apperr.QuotaUnavailable) {
		t.Errorf("code = %q, want quota_unavailable", apperr.CodeOf(err))
	}
}

func TestScriptSyntaxError(t *testing.T) {
	q := New(fakeAccounts{"relay-1": scriptAcct("http://127.0.0.1:1", "({ not js")}, time.Minute)
	if _, err := q.Query(context.Background(), "relay-1"); !apperr.Is(err, apperr.QuotaUnavailable) {
		t.Errorf("code = %q, want quota_unavailable", apperr.CodeOf(err))
	}
}

func TestScriptMissingExtractor(t *testing.T) {
	q := New(fakeAccounts{"relay-1": scriptAcct("http://127.0.0.1:1",
		`({ request: { url: "http://127.0.0.1:1/" } })`)}, time.Minute)
	_, err := q.Query(context.Background(), "relay-1")
	if !apperr.Is(err, apperr.QuotaUnavailable) || !strings.Contains(err.Error(), "extractor") {
		t.Errorf("err = %v, want missing extractor", err)
	}
}

func TestScriptPostBodyAndExtra(t *testing.T) {
	var gotBody string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b := make([]byte, r.ContentLength)
		_, _ = r.Body.Read(b)
		gotBody = string(b)
		_, _ = w.Write([]byte(`{"quota":500,"used_quota":125}`))
	}))
	defer srv.Close()

	code := `({
  request: {
    url: "{{baseUrl}}/api/user/self",
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: "{\"ping\":1}"
  },
  extractor: function(r) {
    return {
      planName: "main",
      remaining: r.quota - r.used_quota,
      total: r.quota,
      used: r.used_quota,
      unit: "credits",
      extra: "每日 0 点重置"
    };
  }
})`
	q := New(fakeAccounts{"relay-1": scriptAcct(srv.URL, code)}, time.Minute)
	got, err := q.Query(context.Background(), "relay-1")
	if err != nil {
		t.Fatal(err)
	}
	if gotBody != `{"ping":1}` {
		t.Errorf("body = %q", gotBody)
	}
	m := got.Meters[0]
	if m.Extra != "每日 0 点重置" {
		t.Errorf("extra = %q", m.Extra)
	}
	if m.Remaining == nil || *m.Remaining != 375 {
		t.Errorf("remaining = %v, extractor arithmetic should survive", m.Remaining)
	}
}

func TestScriptDisabledFallsBackToBuiltin(t *testing.T) {
	var hits int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		if r.URL.Path == "/user/balance" {
			_, _ = w.Write([]byte(`{"balance":5}`))
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()

	a := acct("ds-1", "deepseek", srv.URL)
	a.QuotaScript = &account.QuotaScript{Enabled: false, Code: balanceScript}
	q := New(fakeAccounts{"ds-1": a}, time.Minute)
	got, err := q.Query(context.Background(), "ds-1")
	if err != nil {
		t.Fatal(err)
	}
	if !got.Queryable || len(got.Meters) != 1 {
		t.Errorf("disabled script should fall back to the builtin quota api: %+v", got)
	}
}

func TestTestScriptDoesNotCache(t *testing.T) {
	var hits int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits++
		_, _ = w.Write([]byte(`{"balance":1}`))
	}))
	defer srv.Close()

	q := New(fakeAccounts{"relay-1": scriptAcct(srv.URL, balanceScript)}, time.Hour)
	for range 2 {
		if _, err := q.TestScript(context.Background(), "relay-1", balanceScript, 0); err != nil {
			t.Fatal(err)
		}
	}
	if hits != 2 {
		t.Errorf("upstream hits = %d, test runs must not be cached", hits)
	}
}
