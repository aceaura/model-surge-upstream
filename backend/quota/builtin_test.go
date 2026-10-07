package quota

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/aceaura/model-surge-upstream/backend/account"
	"github.com/aceaura/model-surge-upstream/backend/credential"
	"github.com/aceaura/model-surge-upstream/backend/provider"
)

// 内置查询命中的是固定主机(auth.kimi.com、q.<region>.amazonaws.com、
// bailian-cs.console.aliyun.com),测试用这个 transport 把请求改道到本地
// 桩服务器,原始主机名放进 X-Original-Host 供桩按主机分发。
type hostRewrite struct{ target string }

func (h hostRewrite) RoundTrip(r *http.Request) (*http.Response, error) {
	r2 := r.Clone(r.Context())
	r2.Header = r.Header.Clone()
	r2.Header.Set("X-Original-Host", r.URL.Host)
	r2.URL.Scheme = "http"
	r2.URL.Host = strings.TrimPrefix(h.target, "http://")
	r2.Host = r2.URL.Host
	return http.DefaultTransport.RoundTrip(r2)
}

func builtinQuota(srvURL string, acc account.Account) *Quota {
	q := New(fakeAccounts{acc.Name: acc}, time.Minute)
	q.SetClient(&http.Client{Transport: hostRewrite{target: srvURL}})
	return q
}

// fakeTokens 是 TokenSource 桩:kiro 内置查询续期时返回固定 token。
type fakeTokens struct {
	token string
	err   error
	calls int
}

func (f *fakeTokens) AccessToken(context.Context, account.Account) (string, error) {
	f.calls++
	return f.token, f.err
}

func TestBuiltinDeepSeekMultiCurrency(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"balance_infos":[{"currency":"CNY","total_balance":"12.34"},` +
			`{"currency":"USD","total_balance":"1.5"},{"currency":"EUR"}]}`))
	}))
	defer srv.Close()

	// 不带账号脚本的 deepseek 账号走内置查询;缺 total_balance 的条目跳过。
	q := New(fakeAccounts{"ds-1": acct("ds-1", "deepseek.global.api.standard", srv.URL)}, time.Minute)
	got, err := q.Query(context.Background(), "ds-1")
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Meters) != 2 {
		t.Fatalf("meters = %+v, want one per currency with a balance", got.Meters)
	}
	if got.Meters[0].Currency != "CNY" || got.Meters[1].Currency != "USD" {
		t.Errorf("currencies = %q, %q", got.Meters[0].Currency, got.Meters[1].Currency)
	}
	if got.Meters[1].Remaining == nil || *got.Meters[1].Remaining != 1.5 {
		t.Errorf("usd remaining = %v", got.Meters[1].Remaining)
	}
}

const kimiUsagesBody = `{"limits":[{"detail":{"limit":100,"remaining":36,"resetTime":"2099-01-01T00:00:00Z"}}],` +
	`"usage":{"limit":500,"used":90,"resetTime":"2099-02-01T00:00:00Z"}}`

func TestBuiltinKimiJoinsWebMonthly(t *testing.T) {
	var gotWebAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Header.Get("X-Original-Host") {
		case "api.kimi.com":
			if r.URL.Path != "/coding/v1/usages" {
				t.Errorf("api path = %q", r.URL.Path)
			}
			if r.Header.Get("Authorization") != "Bearer sk-abcdefghijkl" {
				t.Errorf("api auth = %q", r.Header.Get("Authorization"))
			}
			_, _ = w.Write([]byte(kimiUsagesBody))
		case "auth.kimi.com":
			body, _ := io.ReadAll(r.Body)
			if !strings.Contains(string(body), `"refresh_token":"rt-web-token"`) {
				t.Errorf("refresh body = %q", body)
			}
			_, _ = w.Write([]byte(`{"accessToken":"at-web-token"}`))
		case "www.kimi.com":
			gotWebAuth = r.Header.Get("Authorization")
			_, _ = w.Write([]byte(`{"subscription_balance":{"amount_used_ratio":0.256,` +
				`"expire_time":"2099-03-01T00:00:00Z"}}`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()

	a := acct("kimi-1", "kimi.global.subscribe.coding", "https://api.kimi.com/coding")
	a.Credential.WebRefreshToken = "rt-web-token"
	q := builtinQuota(srv.URL, a)
	got, err := q.Query(context.Background(), "kimi-1")
	if err != nil {
		t.Fatal(err)
	}
	if gotWebAuth != "Bearer at-web-token" {
		t.Errorf("web auth = %q, {{$1.accessToken}} should resolve from the refresh response", gotWebAuth)
	}
	if len(got.Meters) != 3 {
		t.Fatalf("meters = %+v, want 5h/7d/monthly", got.Meters)
	}
	h5, d7, mon := got.Meters[0], got.Meters[1], got.Meters[2]
	if h5.Label != "5小时" || h5.Used == nil || *h5.Used != 64 {
		t.Errorf("5h meter = %+v, used should derive from limit-remaining", h5)
	}
	if d7.Label != "7天" || d7.Used == nil || *d7.Used != 90 {
		t.Errorf("7d meter = %+v", d7)
	}
	if mon.Label != "本月" || mon.Unit != provider.UnitPercent || mon.Used == nil || *mon.Used != 25.6 {
		t.Errorf("monthly meter = %+v", mon)
	}
	if mon.ResetAt == nil || mon.ResetAt.Format(time.RFC3339) != "2099-03-01T00:00:00Z" {
		t.Errorf("monthly reset_at = %v", mon.ResetAt)
	}
}

func TestBuiltinKimiMonthlyOptional(t *testing.T) {
	// 网页令牌换发失败:optional 块给 null,月维度缺席但主报告不垮。
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Original-Host") == "api.kimi.com" {
			_, _ = w.Write([]byte(kimiUsagesBody))
			return
		}
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer srv.Close()

	a := acct("kimi-1", "kimi.global.subscribe.coding", "https://api.kimi.com/coding")
	a.Credential.WebRefreshToken = "rt-web-token"
	q := builtinQuota(srv.URL, a)
	got, err := q.Query(context.Background(), "kimi-1")
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Meters) != 2 {
		t.Fatalf("meters = %+v, monthly should be absent when the web chain fails", got.Meters)
	}
}

func TestBuiltinKiroPaginatesAndGrants(t *testing.T) {
	var pages int64
	var sawNextToken bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if host := r.Header.Get("X-Original-Host"); host != "q.ap-northeast-1.amazonaws.com" {
			t.Errorf("host = %q, explicit API region should override the profile ARN", host)
		}
		if r.Header.Get("Authorization") != "Bearer at-kiro" {
			t.Errorf("auth = %q", r.Header.Get("Authorization"))
		}
		if r.Header.Get("Amz-Sdk-Invocation-Id") == "" {
			t.Error("{{uuid}} should expand per request")
		}
		body, _ := io.ReadAll(r.Body)
		if !strings.Contains(string(body), `"profileArn":"arn:aws:codewhisperer:us-east-1:123456789012:profile/ABC"`) {
			t.Errorf("body = %q", body)
		}
		n := atomic.AddInt64(&pages, 1)
		if n == 1 {
			if strings.Contains(string(body), "nextToken") {
				t.Errorf("first page must not carry nextToken: %q", body)
			}
			_, _ = w.Write([]byte(`{"usageBreakdownList":[{"displayName":"Agentic requests",` +
				`"usageLimitWithPrecision":1000,"currentUsageWithPrecision":250,` +
				`"overageCapWithPrecision":2000,"overageEnabled":true}],` +
				`"userInfo":{"email":"test@example.com","provider":"IAM"},` +
				`"nextToken":"t2","nextDateReset":1893456000,"subscriptionInfo":{"type":"PRO"},` +
				`"overageConfiguration":{"overageStatus":"ENABLED"}}`))
			return
		}
		if strings.Contains(string(body), `"nextToken":"t2"`) {
			sawNextToken = true
		}
		_, _ = w.Write([]byte(`{"usageBreakdownList":[{"displayName":"Agentic requests",` +
			`"usageLimit":500,"currentUsage":100,"overageEnabled":false,` +
			`"freeTrialInfo":{"freeTrialStatus":"ACTIVE","freeTrialExpiry":"2099-12-01T00:00:00Z",` +
			`"usageLimitWithPrecision":50,"currentUsageWithPrecision":5}}]}`))
	}))
	defer srv.Close()

	a := acct("kiro-1", "kiro.global.subscribe.standard", "https://q.us-east-1.amazonaws.com")
	a.Credential = credential.Credential{
		Kind:         provider.CredKiroRefresh,
		RefreshToken: "rt-kiro",
		ProfileARN:   "arn:aws:codewhisperer:us-east-1:123456789012:profile/ABC",
		APIRegion:    "ap-northeast-1",
	}
	q := builtinQuota(srv.URL, a)
	tokens := &fakeTokens{token: "at-kiro"}
	q.SetTokenSource(tokens)
	got, err := q.Query(context.Background(), "kiro-1")
	if err != nil {
		t.Fatal(err)
	}
	if !sawNextToken {
		t.Error("second page should carry nextToken from the first response")
	}
	if len(got.Meters) != 3 {
		t.Fatalf("meters = %+v, want two usage rows and the active free trial", got.Meters)
	}
	main := got.Meters[0]
	if main.Unit != provider.UnitCredits || main.Total == nil || *main.Total != 1000 ||
		main.Remaining == nil || *main.Remaining != 750 {
		t.Errorf("page1 meter = %+v", main)
	}
	if main.ResetAt == nil || main.ResetAt.Format(time.RFC3339) != "2030-01-01T00:00:00Z" {
		t.Errorf("page1 reset_at = %v, nextDateReset unix seconds should parse", main.ResetAt)
	}
	if main.Extra == "" || !strings.Contains(main.Extra, "PRO") {
		t.Errorf("page1 extra = %q, subscriptionInfo should ride along", main.Extra)
	}
	if !strings.Contains(main.Extra, `"overageStatus":"ENABLED"`) || !strings.Contains(main.Extra, `"overageCap":2000`) {
		t.Errorf("page1 extra = %q, overage status and cap should ride along", main.Extra)
	}
	if !strings.Contains(main.Extra, `"overageEnabled":true`) || !strings.Contains(main.Extra, `"email":"test@example.com"`) || !strings.Contains(main.Extra, `"provider":"IAM"`) {
		t.Errorf("page1 extra = %q, userInfo and per-resource overage should ride along", main.Extra)
	}
	if !strings.Contains(got.Meters[1].Extra, `"overageEnabled":false`) || strings.Contains(got.Meters[1].Extra, "test@example.com") {
		t.Errorf("page2 extra = %q, metadata must not bleed between pages", got.Meters[1].Extra)
	}
	if main.Label != "本月" {
		t.Errorf("page1 label = %q, 月度窗标签应与其他供应商统一", main.Label)
	}
	trial := got.Meters[2]
	if trial.Label != "试用" || trial.Remaining == nil || *trial.Remaining != 45 {
		t.Errorf("free trial meter = %+v", trial)
	}
	if tokens.calls != 1 {
		t.Errorf("token calls = %d, want one across both pages", tokens.calls)
	}
}

func TestBuiltinCodexRateLimitWindows(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if host := r.Header.Get("X-Original-Host"); host != "chatgpt.com" {
			t.Errorf("host = %q", host)
		}
		// wham 不在 /backend-api/codex 子路径下:拼 provider BaseURL 会 404。
		if r.URL.Path != "/backend-api/wham/usage" {
			t.Errorf("path = %q", r.URL.Path)
		}
		if r.Header.Get("Authorization") != "Bearer at-codex" {
			t.Errorf("auth = %q", r.Header.Get("Authorization"))
		}
		if r.Header.Get("Chatgpt-Account-Id") != "acc-id-1" {
			t.Errorf("account id header = %q", r.Header.Get("Chatgpt-Account-Id"))
		}
		if r.Header.Get("Sec-Fetch-Mode") != "no-cors" {
			t.Errorf("sec-fetch-mode = %q, the Cloudflare header group must stay intact",
				r.Header.Get("Sec-Fetch-Mode"))
		}
		_, _ = w.Write([]byte(`{"rate_limit":{"primary_window":{"used_percent":12.5,` +
			`"limit_window_seconds":18000,"reset_at":1893456000},` +
			`"secondary_window":{"used_percent":3,"limit_window_seconds":604800}}}`))
	}))
	defer srv.Close()

	a := acct("codex-1", "openai.global.subscribe.codex", "https://chatgpt.com/backend-api/codex")
	a.Credential = credential.Credential{
		Kind:         provider.CredOAuthRefresh,
		RefreshToken: "rt-codex",
		AccountID:    "acc-id-1",
	}
	q := builtinQuota(srv.URL, a)
	q.SetTokenSource(&fakeTokens{token: "at-codex"})
	got, err := q.Query(context.Background(), "codex-1")
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Meters) != 2 {
		t.Fatalf("meters = %+v, want the 5h and weekly windows", got.Meters)
	}
	h5, d7 := got.Meters[0], got.Meters[1]
	if h5.Label != "5小时" || h5.Unit != provider.UnitPercent ||
		h5.Used == nil || *h5.Used != 12.5 || h5.Total == nil || *h5.Total != 100 {
		t.Errorf("5h meter = %+v, used_percent is already a percentage", h5)
	}
	if h5.Reset != provider.ResetRolling {
		t.Errorf("5h reset rule = %q", h5.Reset)
	}
	if h5.ResetAt == nil || h5.ResetAt.Format(time.RFC3339) != "2030-01-01T00:00:00Z" {
		t.Errorf("5h reset_at = %v, reset_at is unix seconds", h5.ResetAt)
	}
	if d7.Label != "7天" || d7.Used == nil || *d7.Used != 3 {
		t.Errorf("weekly meter = %+v", d7)
	}
	if d7.ResetAt != nil {
		t.Errorf("weekly reset_at = %v, missing reset_at must stay absent", d7.ResetAt)
	}
}

func TestBuiltinCodexNoWindows(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"error":{"message":"nope"}}`))
	}))
	defer srv.Close()

	a := acct("codex-1", "openai.global.subscribe.codex", "https://chatgpt.com/backend-api/codex")
	a.Credential = credential.Credential{
		Kind: provider.CredOAuthRefresh, RefreshToken: "rt-codex", AccountID: "acc-id-1"}
	q := builtinQuota(srv.URL, a)
	q.SetTokenSource(&fakeTokens{token: "at-codex"})
	// 没有窗口是「查不到」而不是「额度为零」:报错而非出空报告。
	if _, err := q.Query(context.Background(), "codex-1"); err == nil {
		t.Error("err = nil, a response without rate_limit windows must fail")
	}
}

func bailianEnvelope(payload string) string {
	return `{"data":{"success":true,"DataV2":{"data":{"success":true,"code":"SUCCESS","data":` + payload + `}}}}`
}

func bailianStub(t *testing.T, usagePayload string) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if host := r.Header.Get("X-Original-Host"); host != "bailian-cs.console.aliyun.com" {
			t.Errorf("host = %q", host)
		}
		if r.Header.Get("Authorization") != "Bearer ct-console" {
			t.Errorf("auth = %q, console token should authenticate, not the api key", r.Header.Get("Authorization"))
		}
		var payload string
		switch api := r.URL.Query().Get("api"); {
		case strings.HasSuffix(api, "/usage"):
			payload = usagePayload
		case strings.HasSuffix(api, "/subscription"):
			payload = `{"specCode":"pro"}`
		case strings.HasSuffix(api, "/quota-config"):
			payload = `{"pro":{"five_hour":100,"monthly":180000}}`
		default:
			t.Errorf("api = %q", api)
		}
		_, _ = w.Write([]byte(bailianEnvelope(payload)))
	}))
}

func TestBuiltinBailian(t *testing.T) {
	t.Setenv("MSU_BAILIAN_CLI_CONFIG", "")
	srv := bailianStub(t, `{"per5HourPercentage":0.5,"per5HourResetTime":1893456000000,`+
		`"per1MonthPercentage":0.02}`)
	defer srv.Close()

	a := acct("bl-1", "bailian.cn.subscribe.token-plan", "https://token-plan.cn-beijing.maas.aliyuncs.com/compatible-mode")
	a.Credential.ConsoleAccessToken = "ct-console"
	q := builtinQuota(srv.URL, a)
	got, err := q.Query(context.Background(), "bl-1")
	if err != nil {
		t.Fatal(err)
	}
	// per1Week 未出现在 usage 里:缺席窗口不出计量,而不是按零报。
	if len(got.Meters) != 2 {
		t.Fatalf("meters = %+v, want 5h and monthly", got.Meters)
	}
	h5 := got.Meters[0]
	if h5.Label != "5小时" || h5.Unit != provider.UnitCredits ||
		h5.Total == nil || *h5.Total != 100 || h5.Used == nil || *h5.Used != 50 ||
		h5.Remaining == nil || *h5.Remaining != 50 {
		t.Errorf("5h meter = %+v", h5)
	}
	if h5.ResetAt == nil || h5.ResetAt.Format(time.RFC3339) != "2030-01-01T00:00:00Z" {
		t.Errorf("5h reset_at = %v, ResetTime is unix millis", h5.ResetAt)
	}
	if h5.Extra != "Pro" {
		t.Errorf("extra = %q, specCode should surface capitalized", h5.Extra)
	}
	mon := got.Meters[1]
	if mon.Label != "本月" || mon.Used == nil || *mon.Used != 3600 || mon.Remaining == nil || *mon.Remaining != 176400 {
		t.Errorf("monthly meter = %+v", mon)
	}
}

func TestBuiltinBailianNotLogined(t *testing.T) {
	t.Setenv("MSU_BAILIAN_CLI_CONFIG", "")
	srv := bailianStub(t, `{"code":"NotLogined","message":"x"}`)
	defer srv.Close()

	a := acct("bl-1", "bailian.cn.subscribe.token-plan", "https://token-plan.cn-beijing.maas.aliyuncs.com/compatible-mode")
	a.Credential.ConsoleAccessToken = "ct-console"
	q := builtinQuota(srv.URL, a)
	_, err := q.Query(context.Background(), "bl-1")
	if err == nil || !strings.Contains(err.Error(), bailianLoginHint) {
		t.Fatalf("err = %v, want the token renewal hint", err)
	}
	if strings.Contains(err.Error(), "重新导入") {
		t.Errorf("err = %v, CLI token renewal does not require importing", err)
	}
}

func TestBuiltinBailianMissingConsoleToken(t *testing.T) {
	t.Setenv("MSU_BAILIAN_CLI_CONFIG", "ignored")
	t.Setenv("ALIBABA_CLOUD_ACCESS_KEY_ID", "ignored-id")
	t.Setenv("ALIBABA_CLOUD_ACCESS_KEY_SECRET", "ignored-secret")
	a := acct("bl-1", "bailian.cn.subscribe.token-plan", "https://untrusted.invalid")
	q := New(fakeAccounts{a.Name: a}, time.Minute)
	_, err := q.Query(context.Background(), a.Name)
	if err == nil || !strings.Contains(err.Error(), bailianLoginHint) {
		t.Fatalf("err = %v, want account reauthentication guidance", err)
	}
}

func TestBuiltinBailianAccountTokenIsolation(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(path, []byte(`{"access_token":"foreign-cli-token"}`), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("MSU_BAILIAN_CLI_CONFIG", path)
	t.Setenv("BAILIAN_CONFIG_DIR", filepath.Dir(path))
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.Header.Get("X-Original-Host") != "bailian-cs.console.aliyun.com" {
			t.Error("account base URL must not control the quota endpoint")
		}
		auth := r.Header.Get("Authorization")
		if auth != "Bearer ct-first" && auth != "Bearer ct-second" && auth != "Bearer ct-rotated" {
			t.Error("token must come only from the requested account")
		}
		writeBailianFixture(w, r)
	}))
	defer srv.Close()
	a := acct("first", "bailian.cn.subscribe.token-plan", "https://untrusted.invalid")
	a.Credential.ConsoleAccessToken = "ct-first"
	b := a
	b.Name, b.Credential.ConsoleAccessToken = "second", "ct-second"
	store := fakeAccounts{a.Name: a, b.Name: b}
	q := New(store, time.Minute)
	q.SetClient(&http.Client{Transport: hostRewrite{target: srv.URL}})
	for _, name := range []string{a.Name, b.Name, a.Name} {
		if _, err := q.Query(context.Background(), name); err != nil {
			t.Fatal(err)
		}
	}
	if calls.Load() != 6 {
		t.Fatal("valid quota must use the existing cache")
	}
	a.Credential.ConsoleAccessToken = "ct-rotated"
	if err := store.UpdateCredential(context.Background(), a.Name, a.Credential); err != nil {
		t.Fatal(err)
	}
	q.Forget(a.Name)
	if _, err := q.Query(context.Background(), a.Name); err != nil {
		t.Fatal(err)
	}
	if store[b.Name].Credential.ConsoleAccessToken != "ct-second" {
		t.Fatal("account tokens leaked across accounts")
	}
}

func writeBailianFixture(w http.ResponseWriter, r *http.Request) {
	payload := `{"per5HourPercentage":0.5}`
	if strings.HasSuffix(r.URL.Query().Get("api"), "/subscription") {
		payload = `{"specCode":"pro"}`
	} else if strings.HasSuffix(r.URL.Query().Get("api"), "/quota-config") {
		payload = `{"pro":{"five_hour":100}}`
	}
	_, _ = w.Write([]byte(bailianEnvelope(payload)))
}

// 注册表与规格标记必须一一对应:加了内置实现忘了标 QuotaQueryable(或
// 反过来)时,管理面展示的「是否可查」会与实际行为分叉。
func TestBuiltinRegistryMatchesQueryableSpecs(t *testing.T) {
	marked := map[string]bool{}
	for _, s := range provider.All() {
		if s.QuotaQueryable {
			marked[s.ID] = true
		}
	}
	for id := range builtinQuotas {
		if !marked[id] {
			t.Errorf("builtin quota %q has no QuotaQueryable spec", id)
		}
	}
	for id := range marked {
		if _, ok := builtinQuotas[id]; !ok {
			t.Errorf("provider %q declares QuotaQueryable but has no builtin query", id)
		}
	}
}

func bailianAuthCredential() credential.Credential {
	return credential.Credential{Kind: provider.CredAPIKey, APIKey: "sk-inference", BailianAccessKeyID: "fixture-id", BailianAccessKeySecret: "fixture-secret"}
}

func TestBailianSigningCLIParity(t *testing.T) {
	// Fixture follows installed CLI Ht/Wt/Gt with fixed date/UUID, empty
	// query/body and sorted headers INCLUDING x-acs-version and final newline.
	req, _ := http.NewRequest(http.MethodPost, bailianMintURL, nil)
	signBailian(req, bailianAuthCredential(), "2026-10-07T12:00:00Z", "00000000-0000-4000-8000-000000000000")
	want := "ACS3-HMAC-SHA256 Credential=fixture-id,SignedHeaders=content-type;host;x-acs-action;x-acs-content-sha256;x-acs-date;x-acs-signature-nonce;x-acs-version,Signature=1d2acc812a34e557cae7b3499bd593d086f2fb2ae2512dc7854d1725a2c34652"
	if req.Header.Get("Authorization") != want {
		t.Fatal("ACS3 signature differs from CLI fixture")
	}
	if req.Host != "modelstudio.cn-beijing.aliyuncs.com" || req.URL.RawQuery != "" || req.Body != nil || req.Header.Get("X-Acs-Version") != "2026-02-10" {
		t.Fatal("mint request differs from official CLI")
	}
}

func TestBailianVerifyRequiresWholeTriple(t *testing.T) {
	for _, failAt := range []string{"", "usage", "subscription", "quota-config", "meters"} {
		t.Run(failAt, func(t *testing.T) {
			var calls atomic.Int32
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				if r.URL.Path == "/modelstudio/cli/generateAccessToken" {
					body, _ := io.ReadAll(r.Body)
					if len(body) != 0 || r.URL.RawQuery != "" || r.Method != "POST" || r.Header.Get("X-Original-Host") != "modelstudio.cn-beijing.aliyuncs.com" || r.Header.Get("X-Acs-Signature-Nonce") == "" {
						t.Error("mint must use fixed endpoint, empty POST and random nonce")
					}
					_, _ = w.Write([]byte(`{"cliAccessToken":"new-console-token"}`))
					return
				}
				if r.Header.Get("Authorization") != "Bearer new-console-token" {
					t.Error("new console token required")
				}
				if strings.HasSuffix(r.URL.Query().Get("api"), "/"+failAt) {
					w.WriteHeader(500)
					return
				}
				if failAt == "meters" && strings.HasSuffix(r.URL.Query().Get("api"), "/usage") {
					_, _ = w.Write([]byte(bailianEnvelope(`{"per5HourPercentage":2}`)))
					return
				}
				writeBailianFixture(w, r)
			}))
			defer srv.Close()
			q := New(fakeAccounts{}, time.Minute)
			q.SetClient(&http.Client{Transport: hostRewrite{target: srv.URL}})
			original := bailianAuthCredential()
			original.ConsoleAccessToken = "old-console-token"
			got, err := q.VerifyBailian(context.Background(), original)
			if failAt == "" {
				if err != nil || got.ConsoleAccessToken != "new-console-token" || got.ConsoleVerifiedAt.IsZero() || got.ConsoleVerifiedAt.Location() != time.UTC || got.APIKey != original.APIKey || calls.Load() != 4 {
					t.Fatalf("verify: %v", err)
				}
			} else if err == nil || got != (credential.Credential{}) {
				t.Fatal("partial success must never grant verification")
			}
		})
	}
}

type bailianRoundTripFunc func(*http.Request) (*http.Response, error)

func (f bailianRoundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestBailianMintFailureSanitization(t *testing.T) {
	for _, tc := range []struct {
		name   string
		status int
		body   string
	}{
		{"http", 500, `fixture-secret fixture-id sk-inference`},
		{"failure", 200, `{"Success":false,"Message":"fixture-secret","cliAccessToken":"leaked-token"}`},
		{"missing", 200, `{"Message":"fixture-secret"}`},
		{"malformed", 200, `fixture-secret`},
		{"oversize", 200, strings.Repeat("s", bodyLimit+1)},
		{"redirect", 302, `fixture-secret`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var calls atomic.Int32
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				w.Header().Set("Location", "/leak")
				w.WriteHeader(tc.status)
				_, _ = w.Write([]byte(tc.body))
			}))
			defer srv.Close()
			q := New(fakeAccounts{}, time.Minute)
			q.SetClient(&http.Client{Transport: hostRewrite{target: srv.URL}})
			_, err := q.mintBailian(context.Background(), bailianAuthCredential())
			if err == nil || calls.Load() != 1 {
				t.Fatal("invalid mint or redirect must fail without following redirects")
			}
			for _, secret := range []string{"fixture-secret", "fixture-id", "sk-inference", "leaked-token"} {
				if strings.Contains(err.Error(), secret) {
					t.Fatal("upstream error leaked authentication material")
				}
			}
		})
	}
	q := New(fakeAccounts{}, time.Minute)
	q.SetClient(&http.Client{Transport: bailianRoundTripFunc(func(*http.Request) (*http.Response, error) { return nil, errors.New("fixture-secret") })})
	if _, err := q.mintBailian(context.Background(), bailianAuthCredential()); err == nil || strings.Contains(err.Error(), "fixture-secret") {
		t.Fatal("transport error must be sanitized")
	}
}

func TestBailianRenewalRetryAndRestart(t *testing.T) {
	for _, reject := range []string{"missing", "NotLogined", "401", "403", "retry-rejected", "mint-failed"} {
		t.Run(reject, func(t *testing.T) {
			var mints, consoleCalls atomic.Int32
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/modelstudio/cli/generateAccessToken" {
					mints.Add(1)
					if reject == "mint-failed" {
						w.WriteHeader(500)
						return
					}
					_, _ = w.Write([]byte(`{"cliAccessToken":"rotated-console"}`))
					return
				}
				consoleCalls.Add(1)
				if r.Header.Get("Authorization") == "Bearer stale-console" || reject == "retry-rejected" {
					if reject == "401" {
						w.WriteHeader(401)
					} else if reject == "403" {
						w.WriteHeader(403)
					} else {
						_, _ = w.Write([]byte(`{"data":{"success":false,"errorCode":"NotLogined"}}`))
					}
					return
				}
				writeBailianFixture(w, r)
			}))
			defer srv.Close()
			a := acct("bl", "bailian.cn.subscribe.token-plan", "https://untrusted.invalid")
			a.Credential = bailianAuthCredential()
			a.Credential.ConsoleAccessToken = "stale-console"
			a.Credential.ConsoleVerifiedAt = time.Now().UTC()
			if reject == "missing" {
				a.Credential.ConsoleAccessToken = ""
			}
			store := fakeAccounts{a.Name: a}
			q := New(store, time.Minute)
			client := &http.Client{Transport: hostRewrite{target: srv.URL}}
			q.SetClient(client)
			_, err := q.Query(context.Background(), a.Name)
			persisted := store[a.Name].Credential
			if mints.Load() != 1 {
				t.Fatal("renewal must mint at most once")
			}
			if reject == "mint-failed" || reject == "retry-rejected" {
				if err == nil || !strings.Contains(err.Error(), bailianLoginHint) || persisted.ConsoleAccessToken != "" || !persisted.ConsoleVerifiedAt.IsZero() {
					t.Fatal("failed renewal must clear persisted verified state")
				}
				if persisted.BailianAccessKeySecret != a.Credential.BailianAccessKeySecret || persisted.APIKey != a.Credential.APIKey {
					t.Fatal("reauth must retain AK/SK and inference key")
				}
				if consoleCalls.Load() > 2 {
					t.Fatal("renewal retry exceeded limit")
				}
				return
			}
			if err != nil || persisted.ConsoleAccessToken != "rotated-console" || persisted.ConsoleVerifiedAt.IsZero() {
				t.Fatalf("renewal: %v", err)
			}
			wantCalls := int32(4)
			if reject == "missing" {
				wantCalls = 3
			}
			if consoleCalls.Load() != wantCalls {
				t.Fatal("renewal must retry all three quota calls")
			}
			// Simulate restart by round-tripping the persisted credential JSON,
			// constructing a fresh accounts store and a fresh Quota (no caches).
			raw, _ := persisted.Encode()
			restored, err := credential.Decode(raw)
			if err != nil {
				t.Fatal(err)
			}
			a.Credential = restored
			fresh := New(fakeAccounts{a.Name: a}, time.Minute)
			fresh.SetClient(client)
			if _, err := fresh.Query(context.Background(), a.Name); err != nil || mints.Load() != 1 {
				t.Fatalf("restart failed to restore token: %v", err)
			}
		})
	}
}

type lockedBailianAccounts struct {
	mu  sync.Mutex
	raw []byte
}

func (s *lockedBailianAccounts) Get(context.Context, string) (account.Account, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var a account.Account
	err := json.Unmarshal(s.raw, &a)
	return a, err
}
func (s *lockedBailianAccounts) UpdateCredential(ctx context.Context, name string, cred credential.Credential) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	var a account.Account
	if err := json.Unmarshal(s.raw, &a); err != nil {
		return err
	}
	a.Credential = cred
	var err error
	s.raw, err = json.Marshal(a)
	return err
}

func TestBailianLateExpiryRestartsWholeTriple(t *testing.T) {
	for _, expiredAt := range []string{"subscription", "quota-config"} {
		t.Run(expiredAt, func(t *testing.T) {
			var mints atomic.Int32
			var mu sync.Mutex
			var calls []string
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/modelstudio/cli/generateAccessToken" {
					mints.Add(1)
					_, _ = w.Write([]byte(`{"cliAccessToken":"rotated-console"}`))
					return
				}
				api := r.URL.Query().Get("api")
				stage := api[strings.LastIndex(api, "/")+1:]
				mu.Lock()
				calls = append(calls, stage)
				mu.Unlock()
				if r.Header.Get("Authorization") == "Bearer stale-console" && stage == expiredAt {
					_, _ = w.Write([]byte(`{"data":{"success":true,"DataV2":{"data":{"success":false,"message":"FAIL::BailianGateway.Login.NotLogined::expired"}}}}`))
					return
				}
				writeBailianFixture(w, r)
			}))
			defer srv.Close()
			a := acct("bl", "bailian.cn.subscribe.token-plan", "")
			a.Credential = bailianAuthCredential()
			a.Credential.ConsoleAccessToken = "stale-console"
			q := builtinQuota(srv.URL, a)
			if _, err := q.Query(context.Background(), a.Name); err != nil {
				t.Fatal(err)
			}
			mu.Lock()
			defer mu.Unlock()
			want := "usage,subscription,usage,subscription,quota-config"
			if expiredAt == "quota-config" {
				want = "usage,subscription,quota-config,usage,subscription,quota-config"
			}
			if mints.Load() != 1 || strings.Join(calls, ",") != want {
				t.Fatal("late expiry must restart usage/subscription/config exactly once")
			}
		})
	}
}

func TestBailianNonAuthFailureDoesNotRenew(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.URL.Path != "/cli/api.json" {
			t.Error("ordinary quota error must not trigger mint")
		}
		w.WriteHeader(500)
		_, _ = w.Write([]byte(`private-secret upstream-error`))
	}))
	defer srv.Close()
	a := acct("bl", "bailian.cn.subscribe.token-plan", "")
	a.Credential = bailianAuthCredential()
	a.Credential.ConsoleAccessToken = "live-console"
	a.Credential.ConsoleVerifiedAt = time.Now().UTC()
	store := fakeAccounts{a.Name: a}
	q := New(store, time.Minute)
	q.SetClient(&http.Client{Transport: hostRewrite{target: srv.URL}})
	if _, err := q.Query(context.Background(), a.Name); err == nil || strings.Contains(err.Error(), "private-secret") {
		t.Fatal("quota failure must be safely reported")
	}
	if calls.Load() != 1 || store[a.Name].Credential != a.Credential {
		t.Fatal("non-auth failure must not invalidate auth or retry")
	}
}

func TestBailianConcurrentRefreshSingleMint(t *testing.T) {
	var mints atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/modelstudio/cli/generateAccessToken" {
			mints.Add(1)
			_, _ = w.Write([]byte(`{"cliAccessToken":"rotated-console"}`))
			return
		}
		if r.Header.Get("Authorization") != "Bearer rotated-console" {
			t.Error("stale token after refresh")
		}
		writeBailianFixture(w, r)
	}))
	defer srv.Close()
	a := acct("bl", "bailian.cn.subscribe.token-plan", "")
	a.Credential = bailianAuthCredential()
	raw, _ := json.Marshal(a)
	store := &lockedBailianAccounts{raw: raw}
	q := New(store, time.Minute)
	q.SetClient(&http.Client{Transport: hostRewrite{target: srv.URL}})
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := q.Query(context.Background(), a.Name); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	if mints.Load() != 1 {
		t.Fatal("concurrent queries must reload the rotated account under the refresh lock")
	}
}
