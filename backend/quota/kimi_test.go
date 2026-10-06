package quota

import (
	"context"
	"encoding/base64"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/aceaura/model-surge-upstream/backend/account"
	"github.com/aceaura/model-surge-upstream/backend/credential"
	"github.com/aceaura/model-surge-upstream/backend/provider"
)

// fakeJWT 拼一个只带 exp 的假 JWT,够 kimiAccessExpiry 解析。
func fakeJWT(exp time.Time) string {
	header := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"HS256"}`))
	payload := base64.RawURLEncoding.EncodeToString(
		[]byte(fmt.Sprintf(`{"exp":%d}`, exp.Unix())))
	return header + "." + payload + ".sig"
}

func kimiAcct(name, baseURL, webToken string) account.Account {
	a := acct(name, "kimi.global.subscribe.coding", baseURL)
	a.Credential = credential.Credential{
		Kind:            provider.CredAPIKey,
		APIKey:          "sk-abcdefghijkl",
		WebRefreshToken: webToken,
	}
	return a
}

// kimiStubs 起刷新与统计两个桩上游,返回各自地址与调用计数。
func kimiStubs(t *testing.T, refreshBody, statsBody string, statsStatus int) (refreshURL, statsURL string, refreshCalls, statsCalls *int32) {
	t.Helper()
	refreshCalls = new(int32)
	statsCalls = new(int32)
	refreshSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		atomic.AddInt32(refreshCalls, 1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(refreshBody))
	}))
	statsSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(statsCalls, 1)
		if r.Header.Get("Authorization") == "" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(statsStatus)
		_, _ = w.Write([]byte(statsBody))
	}))
	t.Cleanup(func() { refreshSrv.Close(); statsSrv.Close() })
	return refreshSrv.URL, statsSrv.URL, refreshCalls, statsCalls
}

func useKimiStubs(t *testing.T, refreshURL, statsURL string) {
	t.Helper()
	oldRefresh, oldStats := kimiRefreshURL, kimiStatsURL
	kimiRefreshURL, kimiStatsURL = refreshURL, statsURL
	t.Cleanup(func() { kimiRefreshURL, kimiStatsURL = oldRefresh, oldStats })
}

const kimiStatsOK = `{"subscriptionBalance":{"type":"SUBSCRIPTION","unit":"UNIT_CREDIT","amountUsedRatio":0.1515,"expireTime":"2099-10-27T00:00:00Z"}}`

func TestKimiMonthlyAppendedOnBuiltinPath(t *testing.T) {
	refreshURL, statsURL, refreshCalls, statsCalls := kimiStubs(t,
		`{"access_token":"`+fakeJWT(time.Now().Add(time.Hour))+`"}`,
		kimiStatsOK, http.StatusOK)
	useKimiStubs(t, refreshURL, statsURL)

	// 5h/7d 走内置 usages 查询,月度追加为第二条计量。
	usagesSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"usage":{"limit":"100","used":"70","remaining":"30","resetTime":"2099-01-01T00:00:00Z"}}`))
	}))
	defer usagesSrv.Close()

	q := New(fakeAccounts{"kimi-1": kimiAcct("kimi-1", usagesSrv.URL, "web-refresh-token")}, time.Minute)
	report, err := q.Query(context.Background(), "kimi-1")
	if err != nil {
		t.Fatal(err)
	}
	if !report.Queryable {
		t.Error("kimi 有内置额度查询,应可查询")
	}
	if len(report.Meters) != 2 || report.Meters[0].Label != "7天" || report.Meters[1].Label != "本月" {
		t.Errorf("meters = %+v, want 7天+本月", report.Meters)
	}
	if atomic.LoadInt32(refreshCalls) != 1 || atomic.LoadInt32(statsCalls) != 1 {
		t.Errorf("refresh/stats calls = %d/%d, want 1/1", *refreshCalls, *statsCalls)
	}
}

func TestKimiMonthlySkippedWithoutWebToken(t *testing.T) {
	refreshURL, statsURL, refreshCalls, statsCalls := kimiStubs(t, `{}`, kimiStatsOK, http.StatusOK)
	useKimiStubs(t, refreshURL, statsURL)

	usagesSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"usage":{"limit":"100","used":"70","remaining":"30"}}`))
	}))
	defer usagesSrv.Close()

	q := New(fakeAccounts{"kimi-1": kimiAcct("kimi-1", usagesSrv.URL, "")}, time.Minute)
	report, err := q.Query(context.Background(), "kimi-1")
	if err != nil {
		t.Fatal(err)
	}
	if len(report.Meters) != 1 || report.Meters[0].Label != "7天" {
		t.Errorf("无网页 token 应只有内置窗口计量: %+v", report)
	}
	if atomic.LoadInt32(refreshCalls) != 0 || atomic.LoadInt32(statsCalls) != 0 {
		t.Error("无网页 token 不应触发月度链")
	}
}

func TestKimiMonthlySkippedForOtherProviders(t *testing.T) {
	refreshURL, statsURL, refreshCalls, _ := kimiStubs(t, `{}`, kimiStatsOK, http.StatusOK)
	useKimiStubs(t, refreshURL, statsURL)

	balanceSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"balance_infos":[{"currency":"CNY","total_balance":"12.34"}]}`))
	}))
	defer balanceSrv.Close()

	a := acct("ds-1", "deepseek.global.api.standard", balanceSrv.URL)
	a.Credential.WebRefreshToken = "web-refresh-token"
	q := New(fakeAccounts{"ds-1": a}, time.Minute)
	if _, err := q.Query(context.Background(), "ds-1"); err != nil {
		t.Fatal(err)
	}
	if atomic.LoadInt32(refreshCalls) != 0 {
		t.Error("月度链是 kimi 专属,其他 provider 不应触发")
	}
}

func TestKimiMonthlyDegradesOnRefreshFailure(t *testing.T) {
	refreshURL, statsURL, _, _ := kimiStubs(t, `{"code":"unauthenticated"}`, kimiStatsOK, http.StatusOK)
	useKimiStubs(t, refreshURL, statsURL)

	usagesSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"usage":{"limit":"100","used":"70","remaining":"30"}}`))
	}))
	defer usagesSrv.Close()

	a := kimiAcct("kimi-1", usagesSrv.URL, "dead-token")
	q := New(fakeAccounts{"kimi-1": a}, time.Minute)

	// 刷新端点 200 但无 access_token:月度降级,内置主报告不受影响。
	report, err := q.Query(context.Background(), "kimi-1")
	if err != nil {
		t.Fatalf("月度失败不应拖垮主报告: %v", err)
	}
	if len(report.Meters) != 1 || report.Meters[0].Label != "7天" {
		t.Errorf("meters = %+v, want 7天 only", report.Meters)
	}
}

func TestKimiAccessTokenCachedAcrossQueries(t *testing.T) {
	refreshURL, statsURL, refreshCalls, _ := kimiStubs(t,
		`{"accessToken":"`+fakeJWT(time.Now().Add(time.Hour))+`"}`,
		kimiStatsOK, http.StatusOK)
	useKimiStubs(t, refreshURL, statsURL)

	usagesSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"usage":{"limit":"100","used":"70","remaining":"30"}}`))
	}))
	defer usagesSrv.Close()

	// 报告缓存 TTL 调极短强制每次重查,refresh 仍应只换一次。
	q := New(fakeAccounts{"kimi-1": kimiAcct("kimi-1", usagesSrv.URL, "web-refresh-token")}, time.Nanosecond)
	for i := 0; i < 3; i++ {
		if _, err := q.Query(context.Background(), "kimi-1"); err != nil {
			t.Fatal(err)
		}
		time.Sleep(time.Millisecond)
	}
	if atomic.LoadInt32(refreshCalls) != 1 {
		t.Errorf("refresh calls = %d, want 1(access_token 按 exp 缓存)", *refreshCalls)
	}
}

func TestKimiStats401DropsCachedToken(t *testing.T) {
	refreshURL, statsURL, refreshCalls, _ := kimiStubs(t,
		`{"accessToken":"`+fakeJWT(time.Now().Add(time.Hour))+`"}`,
		`{"code":"unauthenticated"}`, http.StatusUnauthorized)
	useKimiStubs(t, refreshURL, statsURL)

	usagesSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"usage":{"limit":"100","used":"70","remaining":"30"}}`))
	}))
	defer usagesSrv.Close()

	q := New(fakeAccounts{"kimi-1": kimiAcct("kimi-1", usagesSrv.URL, "web-refresh-token")}, time.Nanosecond)
	if _, err := q.Query(context.Background(), "kimi-1"); err != nil {
		t.Fatal(err)
	}
	if _, ok := q.kimiTokens.get("kimi-1"); ok {
		t.Error("stats 401 后缓存的 access_token 应被丢弃")
	}
	time.Sleep(time.Millisecond)
	if _, err := q.Query(context.Background(), "kimi-1"); err != nil {
		t.Fatal(err)
	}
	if atomic.LoadInt32(refreshCalls) != 2 {
		t.Errorf("refresh calls = %d, want 2(丢缓存后重新换)", *refreshCalls)
	}
}

func TestKimiMonthlyMeterOfFieldShapes(t *testing.T) {
	snake := map[string]any{
		"subscription_balance": map[string]any{
			"amount_used_ratio": 0.5,
			"expire_time":       "2099-01-01T00:00:00Z",
		},
	}
	m := kimiMonthlyMeterOf(snake)
	if m == nil || m.Used == nil || *m.Used != 50 {
		t.Errorf("snake_case 映射失败: %+v", m)
	}

	if got := kimiMonthlyMeterOf(map[string]any{}); got != nil {
		t.Errorf("无订阅 Balance(免费用户)不应出计量: %+v", got)
	}
	if got := kimiMonthlyMeterOf(map[string]any{
		"subscriptionBalance": map[string]any{"type": "SUBSCRIPTION"},
	}); got != nil {
		t.Errorf("缺 amountUsedRatio 不应出计量: %+v", got)
	}
}
