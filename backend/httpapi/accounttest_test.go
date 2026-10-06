package httpapi

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/aceaura/model-surge-upstream/backend/account"
	"github.com/aceaura/model-surge-upstream/backend/credential"
	"github.com/aceaura/model-surge-upstream/backend/model"
	"github.com/aceaura/model-surge-upstream/backend/modelcheck"
	"github.com/aceaura/model-surge-upstream/backend/provider"
)

// 给夹具添一个指向本地 httptest 的账号。账号故意停用：锁「未启用也能先
// 测通」的语义（Resolver 会拒绝，检测不走它）。
func (f *fixture) addTestableAccount(t *testing.T, name, upstreamURL string) {
	t.Helper()
	f.accounts.data[name] = account.Account{
		Name: name, ProviderID: "kimi.global.subscribe.coding", BaseURL: upstreamURL,
		Credential: credential.Credential{Kind: provider.CredAPIKey, APIKey: secret},
		Headers:    map[string]string{}, Enabled: false,
	}
}

func postAccountTest(t *testing.T, f *fixture, name string) modelcheck.Result {
	t.Helper()
	rec := f.do(t, "POST", "/admin/account-test/"+name, adminKey, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", rec.Code, rec.Body.String())
	}
	var res modelcheck.Result
	if err := json.Unmarshal(rec.Body.Bytes(), &res); err != nil {
		t.Fatalf("decode result: %v", err)
	}
	return res
}

// 未启用的账号也能测：上游 200 → ok=true。
func TestAccountTestDisabledAccountStillCheckable(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer upstream.Close()

	f := newFixture(t)
	f.addTestableAccount(t, "probe-acc", upstream.URL)

	res := postAccountTest(t, f, "probe-acc")
	if !res.OK || res.StatusCode != http.StatusOK {
		t.Errorf("result = %+v, want ok 200", res)
	}
}

// 账号下有模型时走模型级探针:带凭据打协议端点,非 2xx 判失败。
func TestAccountTestUsesModelProbe(t *testing.T) {
	var gotPath, gotKey string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// kimi 走 anthropic 认证形态,凭据落在 x-api-key 而非 Authorization。
		gotPath, gotKey = r.URL.Path, r.Header.Get("x-api-key")
		w.WriteHeader(http.StatusOK)
	}))
	defer upstream.Close()

	f := newFixture(t)
	f.addTestableAccount(t, "probe-acc", upstream.URL)
	f.models.data["probe-acc/k2"] = model.Model{
		ID: "probe-acc/k2", Account: "probe-acc", NativeModel: "kimi-k2-turbo",
		Protocol: provider.ProtocolAnthropic, ContextWindow: 262144, Enabled: false,
		Defaults: json.RawMessage(`{}`), Overrides: json.RawMessage(`{}`),
	}

	res := postAccountTest(t, f, "probe-acc")
	if !res.OK || res.StatusCode != http.StatusOK {
		t.Errorf("result = %+v, want ok 200", res)
	}
	if gotPath != "/v1/messages" {
		t.Errorf("path = %q, 账号级检测应走模型协议端点而非根地址", gotPath)
	}
	if gotKey != secret {
		t.Error("账号级检测应带凭据,与模型级同判据")
	}
}

// 与旧的裸可达口径刻意不同:有模型时 403 判失败——根地址多数不接 GET,
// 可达探测恒回 4xx 看着像故障,改用模型级判据回答「能不能用」。
func TestAccountTestModelProbeErrorStatusFails(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusForbidden)
	}))
	defer upstream.Close()

	f := newFixture(t)
	f.addTestableAccount(t, "probe-acc", upstream.URL)
	f.models.data["probe-acc/k2"] = model.Model{
		ID: "probe-acc/k2", Account: "probe-acc", NativeModel: "kimi-k2-turbo",
		Protocol: provider.ProtocolAnthropic, ContextWindow: 262144, Enabled: true,
		Defaults: json.RawMessage(`{}`), Overrides: json.RawMessage(`{}`),
	}

	res := postAccountTest(t, f, "probe-acc")
	if res.OK || res.StatusCode != http.StatusForbidden {
		t.Errorf("result = %+v, want not-ok 403", res)
	}
}

// 账号下没有模型时没有可发的原生模型名,退回根地址可达性:403 仍算可达。
func TestAccountTestWithoutModelsFallsBackToReachability(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusForbidden)
	}))
	defer upstream.Close()

	f := newFixture(t)
	f.addTestableAccount(t, "probe-acc", upstream.URL)

	res := postAccountTest(t, f, "probe-acc")
	if !res.OK || res.StatusCode != http.StatusForbidden {
		t.Errorf("result = %+v, want ok 403 (reachable)", res)
	}
}

// 网络级失败是检测结果而非请求错误：管理面仍回 200，结果 ok=false。
func TestAccountTestNetworkFailure(t *testing.T) {
	dead := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	dead.Close() // 立即关掉，端口即不可达

	f := newFixture(t)
	f.addTestableAccount(t, "probe-acc", dead.URL)

	res := postAccountTest(t, f, "probe-acc")
	if res.OK || res.StatusCode != 0 || res.Error == "" {
		t.Errorf("result = %+v, want not-ok network failure", res)
	}
}

// 账号名含 / 也要能命中（{name...} 通配：Go 路由不允许通配后接静态段，
// 故检测挂独立前缀）。
func TestAccountTestNameWithSlash(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer upstream.Close()

	f := newFixture(t)
	f.addTestableAccount(t, "team/a", upstream.URL)

	res := postAccountTest(t, f, "team/a")
	if !res.OK {
		t.Errorf("result = %+v, want ok for slash-named account", res)
	}
}

func TestAccountTestUnknownAccount(t *testing.T) {
	f := newFixture(t)
	rec := f.do(t, "POST", "/admin/account-test/nope", adminKey, "")
	if rec.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404", rec.Code)
	}
}
