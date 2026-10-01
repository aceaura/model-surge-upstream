package modelcheck

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/aceaura/model-surge-upstream/backend/provider"
	"github.com/aceaura/model-surge-upstream/backend/resolve"
)

func target(protocol, baseURL string) resolve.ResolvedTarget {
	return resolve.ResolvedTarget{
		ModelID:     "a/m",
		Account:     "a",
		Protocol:    protocol,
		BaseURL:     baseURL,
		NativeModel: "native-x",
		Headers:     map[string]string{"Authorization": "Bearer sk-test"},
	}
}

// 各协议探测路径与最小体：路径按协议形态、model 用 native、max_tokens 压低。
func TestCheckProbeShapes(t *testing.T) {
	cases := []struct {
		protocol  string
		wantPath  string
		wantModel bool // 体里是否应带 model 字段（gemini 在路径里）
	}{
		{provider.ProtocolAnthropic, "/v1/messages", true},
		{provider.ProtocolChatCompletions, "/v1/chat/completions", true},
		{provider.ProtocolResponses, "/v1/responses", true},
		{provider.ProtocolGemini, "/v1beta/models/native-x:generateContent", false},
	}
	for _, tc := range cases {
		t.Run(tc.protocol, func(t *testing.T) {
			var gotPath string
			var gotBody map[string]any
			var gotAuth string
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				gotPath = r.URL.Path
				gotAuth = r.Header.Get("Authorization")
				raw, _ := io.ReadAll(r.Body)
				_ = json.Unmarshal(raw, &gotBody)
				w.WriteHeader(http.StatusOK)
				_, _ = w.Write([]byte(`{}`))
			}))
			defer srv.Close()

			res := Check(context.Background(), target(tc.protocol, srv.URL))
			if !res.OK || res.StatusCode != http.StatusOK {
				t.Fatalf("result = %+v, want ok 200", res)
			}
			if gotPath != tc.wantPath {
				t.Errorf("path = %q, want %q", gotPath, tc.wantPath)
			}
			if gotAuth != "Bearer sk-test" {
				t.Errorf("auth header = %q, want target headers applied", gotAuth)
			}
			if tc.wantModel && gotBody["model"] != "native-x" {
				t.Errorf("body model = %v, want native-x", gotBody["model"])
			}
			if !tc.wantModel {
				if _, has := gotBody["model"]; has {
					t.Errorf("gemini body must not carry model field: %v", gotBody)
				}
			}
		})
	}
}

// 非 2xx：链路可达但判失败，带状态码与上游说明（CC Switch 语义：
// 收到 HTTP 响应即非网络故障，但模型级检测要验证凭据）。
func TestCheckUpstreamHTTPError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"error":"bad key"}`))
	}))
	defer srv.Close()

	res := Check(context.Background(), target(provider.ProtocolAnthropic, srv.URL))
	if res.OK {
		t.Fatal("401 must not be ok")
	}
	if res.StatusCode != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401", res.StatusCode)
	}
	if res.Error == "" {
		t.Error("error must carry upstream explanation")
	}
}

// 网络级失败：连接被拒 → ok=false、status=0、error 说明原因。
func TestCheckNetworkFailure(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	srv.Close() // 立即关掉，端口即不可达

	res := Check(context.Background(), target(provider.ProtocolChatCompletions, srv.URL))
	if res.OK || res.StatusCode != 0 || res.Error == "" {
		t.Errorf("result = %+v, want network failure shape", res)
	}
}

// 未知协议：不发请求直接失败。
func TestCheckUnknownProtocol(t *testing.T) {
	res := Check(context.Background(), target("weird", "http://127.0.0.1:1"))
	if res.OK || res.Error == "" {
		t.Errorf("result = %+v, want protocol error", res)
	}
}
