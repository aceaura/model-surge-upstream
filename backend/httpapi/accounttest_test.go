package httpapi

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/aceaura/model-surge-upstream/backend/account"
	"github.com/aceaura/model-surge-upstream/backend/credential"
	"github.com/aceaura/model-surge-upstream/backend/modelcheck"
	"github.com/aceaura/model-surge-upstream/backend/provider"
)

// 给夹具添一个指向本地 httptest 的账号。账号故意停用：锁「未启用也能先
// 测通」的语义（Resolver 会拒绝，检测不走它）。
func (f *fixture) addTestableAccount(t *testing.T, name, upstreamURL string) {
	t.Helper()
	f.accounts.data[name] = account.Account{
		Name: name, ProviderID: "kimi", BaseURL: upstreamURL,
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

// 与模型级刻意不同（注释见 modelcheck 包注释）：上游回 403 也算可达——
// 账号级只回答「能不能到」，凭据对错归模型级。管理面仍回 200。
func TestAccountTestUpstreamErrorStatusStillOK(t *testing.T) {
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
