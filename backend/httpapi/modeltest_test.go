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

// 给夹具添一个指向本地 httptest 的账号与模型，返回模型 id。
// 模型故意停用：锁「未启用也能先测通」的语义（Resolver 会拒绝，检测不走它）。
func (f *fixture) addTestableModel(t *testing.T, upstreamURL string) string {
	t.Helper()
	f.accounts.data["probe-1"] = account.Account{
		Name: "probe-1", ProviderID: "kimi/coding", BaseURL: upstreamURL,
		Credential: credential.Credential{Kind: provider.CredAPIKey, APIKey: secret},
		Headers:    map[string]string{}, Enabled: true,
	}
	f.models.data["probe-1/m"] = model.Model{
		ID: "probe-1/m", Account: "probe-1", NativeModel: "native-x",
		Protocol: provider.ProtocolAnthropic, Enabled: false,
		Defaults: json.RawMessage(`{}`), Overrides: json.RawMessage(`{}`),
	}
	return "probe-1/m"
}

func TestModelTestDisabledModelStillCheckable(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/messages" {
			t.Errorf("probe path = %q, want /v1/messages", r.URL.Path)
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{}`))
	}))
	defer upstream.Close()

	f := newFixture(t)
	id := f.addTestableModel(t, upstream.URL)

	rec := f.do(t, "POST", "/admin/model-test/"+id, adminKey, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", rec.Code, rec.Body.String())
	}
	var res modelcheck.Result
	if err := json.Unmarshal(rec.Body.Bytes(), &res); err != nil {
		t.Fatalf("decode result: %v", err)
	}
	if !res.OK || res.StatusCode != http.StatusOK {
		t.Errorf("result = %+v, want ok 200", res)
	}
}

// 上游拒绝是检测结果而非请求错误：管理面仍回 200，结果里带状态码。
func TestModelTestUpstreamRejects(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"error":"bad key"}`))
	}))
	defer upstream.Close()

	f := newFixture(t)
	id := f.addTestableModel(t, upstream.URL)

	rec := f.do(t, "POST", "/admin/model-test/"+id, adminKey, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", rec.Code, rec.Body.String())
	}
	var res modelcheck.Result
	if err := json.Unmarshal(rec.Body.Bytes(), &res); err != nil {
		t.Fatalf("decode result: %v", err)
	}
	if res.OK || res.StatusCode != http.StatusUnauthorized || res.Error == "" {
		t.Errorf("result = %+v, want not-ok 401 with error", res)
	}
}

func TestModelTestUnknownModel(t *testing.T) {
	f := newFixture(t)
	rec := f.do(t, "POST", "/admin/model-test/nope/x", adminKey, "")
	if rec.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404", rec.Code)
	}
}
