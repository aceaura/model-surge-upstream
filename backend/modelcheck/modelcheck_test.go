package modelcheck

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/aceaura/model-surge-upstream/backend/codex"
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

// codex 订阅端点:探针与转发面同契约——路径 /v1/responses 映射 /responses,
// 体强制 store=false/stream=true 且 instructions 非空,采样参数被剥除。
func TestCheckCodexShaping(t *testing.T) {
	var gotPath string
	var gotBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		raw, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(raw, &gotBody)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{}`))
	}))
	defer srv.Close()

	tg := target(provider.ProtocolResponses, srv.URL)
	tg.ProviderID = codex.ProviderID
	res := Check(context.Background(), tg)
	if !res.OK {
		t.Fatalf("result = %+v, want ok", res)
	}
	if gotPath != "/responses" {
		t.Errorf("path = %q, want codex-mapped /responses", gotPath)
	}
	if gotBody["store"] != false || gotBody["stream"] != true {
		t.Errorf("store/stream = %v/%v, codex contract forces false/true", gotBody["store"], gotBody["stream"])
	}
	if s, _ := gotBody["instructions"].(string); strings.TrimSpace(s) == "" {
		t.Error("instructions must be injected when absent")
	}
	if _, has := gotBody["max_output_tokens"]; has {
		t.Error("max_output_tokens must be dropped for codex")
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

// Reachability：拿到任意 HTTP 状态（含 401/403/404/5xx）即可达
// （CC Switch 同款判据，与 Check 的非 2xx 判失败刻意不同）。
func TestReachabilityAnyHTTPStatusOK(t *testing.T) {
	for _, status := range []int{200, 401, 403, 404, 500} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(status)
			}))
			defer srv.Close()

			res := Reachability(context.Background(), srv.URL)
			if !res.OK || res.StatusCode != status || res.Error != "" {
				t.Errorf("result = %+v, want ok with status %d", res, status)
			}
		})
	}
}

// Reachability 网络级失败：连接被拒 → ok=false、status=0、error 说明原因。
func TestReachabilityNetworkFailure(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	srv.Close() // 立即关掉，端口即不可达

	res := Reachability(context.Background(), srv.URL)
	if res.OK || res.StatusCode != 0 || res.Error == "" {
		t.Errorf("result = %+v, want network failure shape", res)
	}
}

// Reachability 空 baseURL：不发请求直接失败。
func TestReachabilityEmptyBaseURL(t *testing.T) {
	for _, u := range []string{"", "  "} {
		res := Reachability(context.Background(), u)
		if res.OK || res.Error == "" {
			t.Errorf("baseURL %q: result = %+v, want error", u, res)
		}
	}
}

// Reachability 的时延是 TTFB：响应头到达即停表，不等 body。
// 上游先 Flush 响应头、300ms 后才写 body，LatencyMS 必须明显小于 300。
func TestReachabilityLatencyIsTTFB(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		if f, ok := w.(http.Flusher); ok {
			f.Flush()
		}
		time.Sleep(300 * time.Millisecond)
		_, _ = w.Write([]byte("slow body"))
	}))
	defer srv.Close()

	res := Reachability(context.Background(), srv.URL)
	if !res.OK {
		t.Fatalf("result = %+v, want ok", res)
	}
	if res.LatencyMS >= 300 {
		t.Errorf("latency = %d ms, want TTFB (< 300ms body write)", res.LatencyMS)
	}
}

// Reachability 不带鉴权头：可达性不验凭据（CC Switch 同款）。
func TestReachabilitySendsNoAuthHeader(t *testing.T) {
	var gotAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	Reachability(context.Background(), srv.URL)
	if gotAuth != "" {
		t.Errorf("Authorization = %q, want empty", gotAuth)
	}
}
