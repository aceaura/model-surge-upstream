package quota

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
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
		if host := r.Header.Get("X-Original-Host"); host != "q.us-east-1.amazonaws.com" {
			t.Errorf("host = %q, region should derive from the profile ARN", host)
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
				`"overageCapWithPrecision":2000}],` +
				`"nextToken":"t2","nextDateReset":1893456000,"subscriptionInfo":{"type":"PRO"},` +
				`"overageConfiguration":{"overageStatus":"ENABLED"}}`))
			return
		}
		if strings.Contains(string(body), `"nextToken":"t2"`) {
			sawNextToken = true
		}
		_, _ = w.Write([]byte(`{"usageBreakdownList":[{"displayName":"Agentic requests",` +
			`"usageLimit":500,"currentUsage":100,` +
			`"freeTrialInfo":{"freeTrialStatus":"ACTIVE","freeTrialExpiry":"2099-12-01T00:00:00Z",` +
			`"usageLimitWithPrecision":50,"currentUsageWithPrecision":5}}]}`))
	}))
	defer srv.Close()

	a := acct("kiro-1", "kiro.global.subscribe.standard", "https://q.us-east-1.amazonaws.com")
	a.Credential = credential.Credential{
		Kind:         provider.CredKiroRefresh,
		RefreshToken: "rt-kiro",
		ProfileARN:   "arn:aws:codewhisperer:us-east-1:123456789012:profile/ABC",
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
	t.Setenv("MSU_BAILIAN_CLI_CONFIG", "")
	a := acct("bl-1", "bailian.cn.subscribe.token-plan", "https://token-plan.cn-beijing.maas.aliyuncs.com/compatible-mode")
	q := New(fakeAccounts{"bl-1": a}, time.Minute)
	_, err := q.Query(context.Background(), "bl-1")
	if err == nil || !strings.Contains(err.Error(), "安装 bl 并验证") ||
		!strings.Contains(err.Error(), "MSU_BAILIAN_CLI_CONFIG") {
		t.Fatalf("err = %v, want CLI setup and backend config guidance", err)
	}
	if strings.Contains(err.Error(), "console_access_token") {
		t.Errorf("err = %v, no manual console token field remains in the form", err)
	}
}

func TestBailianConsoleTokenConfig(t *testing.T) {
	for _, tc := range []struct {
		name   string
		config string
	}{
		{"missing", ""},
		{"invalid JSON", "{"},
		{"wrong token type", `{"access_token":123}`},
		{"missing token", `{}`},
		{"blank token", `{"access_token":"   "}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "config.json")
			t.Setenv("MSU_BAILIAN_CLI_CONFIG", path)
			if tc.config != "" {
				if err := os.WriteFile(path, []byte(tc.config), 0600); err != nil {
					t.Fatal(err)
				}
			}
			a := account.Account{}
			a.Credential.ConsoleAccessToken = " ct-stored "
			if got := bailianConsoleToken(a); got != "ct-stored" {
				t.Fatalf("token = %q, want stored token fallback", got)
			}
			a.Credential.ConsoleAccessToken = ""
			a.Credential.APIKey = "sk-inference"
			if got := bailianConsoleToken(a); got != "" {
				t.Fatalf("token = %q, inference key must not authenticate quota queries", got)
			}
		})
	}
}

func TestBuiltinBailianCLIConfigRotation(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	t.Setenv("MSU_BAILIAN_CLI_CONFIG", path)
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		call := calls.Add(1)
		want := "Bearer ct-first"
		if call > 3 {
			want = "Bearer ct-renewed"
		}
		if got := r.Header.Get("Authorization"); got != want {
			t.Errorf("call %d auth = %q, want %q from CLI config", call, got, want)
		}
		if r.URL.Path != "/cli/api.json" || r.Method != http.MethodPost {
			t.Errorf("request = %s %s, want POST /cli/api.json", r.Method, r.URL.Path)
		}
		var payload string
		switch api := r.URL.Query().Get("api"); {
		case strings.HasSuffix(api, "/usage"):
			payload = `{"per5HourPercentage":0.5}`
		case strings.HasSuffix(api, "/subscription"):
			payload = `{"specCode":"pro"}`
		case strings.HasSuffix(api, "/quota-config"):
			payload = `{"pro":{"five_hour":100}}`
		default:
			t.Errorf("api = %q", api)
		}
		_, _ = w.Write([]byte(bailianEnvelope(payload)))
	}))
	defer srv.Close()
	a := acct("bl-1", "bailian.cn.subscribe.token-plan", "https://token-plan.cn-beijing.maas.aliyuncs.com/compatible-mode")
	a.Credential.ConsoleAccessToken = "ct-stale"
	q := builtinQuota(srv.URL, a)
	q.ttl = 0
	for _, token := range []string{"ct-first", "ct-renewed"} {
		if err := os.WriteFile(path, []byte(`{"access_token":" `+token+` "}`), 0600); err != nil {
			t.Fatal(err)
		}
		got, err := q.Query(context.Background(), a.Name)
		if err != nil {
			t.Fatal(err)
		}
		if len(got.Meters) != 1 || got.Meters[0].Used == nil || *got.Meters[0].Used != 50 {
			t.Fatalf("meters = %+v, want 50 credits used", got.Meters)
		}
	}
	if got := calls.Load(); got != 6 {
		t.Fatalf("calls = %d, want three endpoints for each token", got)
	}
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
