package proxyplane

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/aceaura/model-surge-upstream/backend/apperr"
	"github.com/aceaura/model-surge-upstream/backend/effort"
	"github.com/aceaura/model-surge-upstream/backend/resolve"
)

// fakeResolver 按别名表解析，List 返回固定清单。
type fakeResolver struct {
	targets map[string]resolve.ResolvedTarget
	listing []resolve.Listing
}

func (f fakeResolver) Resolve(_ context.Context, id string) (resolve.ResolvedTarget, error) {
	t, ok := f.targets[id]
	if !ok {
		return resolve.ResolvedTarget{}, apperr.New(apperr.NotFound, "model not found")
	}
	return t, nil
}

func (f fakeResolver) List(context.Context) ([]resolve.Listing, error) { return f.listing, nil }

// captured 记录桩上游收到的请求，供断言改写结果。
type captured struct {
	mu      sync.Mutex
	body    []byte
	headers http.Header
	path    string
}

func (c *captured) handler(respStatus int, respBody string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		c.mu.Lock()
		c.body = body
		c.headers = r.Header.Clone()
		c.path = r.URL.RequestURI()
		c.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(respStatus)
		_, _ = io.WriteString(w, respBody)
	})
}

func (c *captured) snapshot() ([]byte, http.Header, string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.body, c.headers, c.path
}

const testKey = "proxy-test-key"

func newTestHandler(t *testing.T, upstreamURL string) (*Handler, *captured, func()) {
	t.Helper()
	cap := &captured{}
	up := httptest.NewServer(cap.handler(http.StatusOK, `{"ok":true}`))

	resolver := fakeResolver{targets: map[string]resolve.ResolvedTarget{
		"my-claude": {
			ModelID: "my-claude", Account: "kimi-1", ProviderID: "kimi",
			Protocol: "anthropic", BaseURL: upstreamURL, NativeModel: "kimi-k2",
			Headers:   map[string]string{"x-api-key": "real-account-key", "anthropic-version": "2023-06-01"},
			Defaults:  json.RawMessage(`{"temperature":0.6,"thinking":{"type":"enabled","budget_tokens":1000}}`),
			Overrides: json.RawMessage(`{"max_tokens":8192,"thinking":{"budget_tokens":2000}}`),
		},
		"my-gpt": {
			ModelID: "my-gpt", Account: "openai-1", ProviderID: "openai",
			Protocol: "chat_completions", BaseURL: upstreamURL, NativeModel: "gpt-5",
			Headers: map[string]string{"Authorization": "Bearer real-openai-key"},
		},
		"my-gemini": {
			ModelID: "my-gemini", Account: "g-1", ProviderID: "gemini",
			Protocol: "gemini", BaseURL: upstreamURL, NativeModel: "gemini-2.5-pro",
			Headers: map[string]string{"Authorization": "Bearer real-gemini-key"},
		},
	}}
	if upstreamURL == "" {
		// 调用方不关心桩地址时用 httptest 自动分配的。
		for id, tgt := range resolver.targets {
			tgt.BaseURL = up.URL
			resolver.targets[id] = tgt
		}
	}
	return NewHandler(testKey, resolver, nil), cap, up.Close
}

func doRequest(t *testing.T, h http.Handler, method, path string, headers map[string]string, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func TestAnthropicForwardRewritesModelMergesParamsSwapsAuth(t *testing.T) {
	h, cap, cleanup := newTestHandler(t, "")
	defer cleanup()

	rec := doRequest(t, h, http.MethodPost, "/v1/messages",
		map[string]string{
			"x-api-key":         testKey,
			"anthropic-version": "2023-06-01",
			"anthropic-beta":    "interleaved-thinking-2025-05-14",
		},
		`{"model":"my-claude","max_tokens":1024,"messages":[{"role":"user","content":"hi"}],"temperature":0.9}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body %s", rec.Code, rec.Body.String())
	}

	body, headers, path := cap.snapshot()
	if path != "/v1/messages" {
		t.Fatalf("upstream path = %q", path)
	}
	var got map[string]any
	if err := json.Unmarshal(body, &got); err != nil {
		t.Fatalf("upstream body not json: %v", err)
	}
	if got["model"] != "kimi-k2" {
		t.Fatalf("model = %v, want kimi-k2", got["model"])
	}
	// overrides 压请求（max_tokens 1024 → 8192），请求压 defaults（temperature 0.9 保留）。
	if got["max_tokens"] != float64(8192) {
		t.Fatalf("max_tokens = %v, want 8192 (override wins)", got["max_tokens"])
	}
	if got["temperature"] != 0.9 {
		t.Fatalf("temperature = %v, want 0.9 (request wins over default)", got["temperature"])
	}
	// 深合并：defaults 的 thinking.type 保留，overrides 的 budget_tokens 压盖。
	thinking := got["thinking"].(map[string]any)
	if thinking["type"] != "enabled" || thinking["budget_tokens"] != float64(2000) {
		t.Fatalf("thinking = %v, want deep-merged {type:enabled, budget_tokens:2000}", thinking)
	}
	// 认证头换账号凭据，客户端的 beta 头透传。
	if headers.Get("x-api-key") != "real-account-key" {
		t.Fatalf("x-api-key = %q", headers.Get("x-api-key"))
	}
	if headers.Get("anthropic-beta") != "interleaved-thinking-2025-05-14" {
		t.Fatalf("anthropic-beta not passed through: %q", headers.Get("anthropic-beta"))
	}
	if headers.Get("Authorization") != "" {
		t.Fatalf("client Authorization leaked upstream")
	}
}

func TestProtocolMismatchRejected(t *testing.T) {
	h, _, cleanup := newTestHandler(t, "")
	defer cleanup()

	// my-gpt 是 chat_completions，打 anthropic 入口必须 400，不做转化。
	rec := doRequest(t, h, http.MethodPost, "/v1/messages",
		map[string]string{"x-api-key": testKey}, `{"model":"my-gpt","messages":[]}`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "does not convert protocols") {
		t.Fatalf("body = %s", rec.Body.String())
	}
	// anthropic 错误外壳。
	var env struct {
		Type  string `json:"type"`
		Error struct {
			Type string `json:"type"`
		} `json:"error"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &env); err != nil || env.Type != "error" {
		t.Fatalf("not anthropic error shape: %s", rec.Body.String())
	}
}

func TestOpenAIChatCompletionsForward(t *testing.T) {
	h, cap, cleanup := newTestHandler(t, "")
	defer cleanup()

	rec := doRequest(t, h, http.MethodPost, "/v1/chat/completions",
		map[string]string{"Authorization": "Bearer " + testKey},
		`{"model":"my-gpt","messages":[{"role":"user","content":"hi"}]}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body %s", rec.Code, rec.Body.String())
	}
	body, headers, path := cap.snapshot()
	if path != "/v1/chat/completions" {
		t.Fatalf("upstream path = %q", path)
	}
	var got map[string]any
	_ = json.Unmarshal(body, &got)
	if got["model"] != "gpt-5" {
		t.Fatalf("model = %v", got["model"])
	}
	if headers.Get("Authorization") != "Bearer real-openai-key" {
		t.Fatalf("Authorization = %q", headers.Get("Authorization"))
	}
}

func TestGeminiForwardRewritesPathAndStripsKeyQuery(t *testing.T) {
	h, cap, cleanup := newTestHandler(t, "")
	defer cleanup()

	rec := doRequest(t, h, http.MethodPost,
		"/v1beta/models/my-gemini:generateContent?key="+testKey+"&alt=json",
		nil, `{"contents":[{"parts":[{"text":"hi"}]}]}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body %s", rec.Code, rec.Body.String())
	}
	_, headers, path := cap.snapshot()
	if path != "/v1beta/models/gemini-2.5-pro:generateContent?alt=json" {
		t.Fatalf("upstream path = %q, want alias rewritten and key= stripped", path)
	}
	if headers.Get("Authorization") != "Bearer real-gemini-key" {
		t.Fatalf("Authorization = %q", headers.Get("Authorization"))
	}
}

func TestUnauthorizedRejected(t *testing.T) {
	h, cap, cleanup := newTestHandler(t, "")
	defer cleanup()

	for _, tc := range []struct{ name, key string }{
		{"missing", ""},
		{"wrong", "nope"},
	} {
		rec := doRequest(t, h, http.MethodPost, "/v1/chat/completions",
			map[string]string{"Authorization": "Bearer " + tc.key}, `{"model":"my-gpt"}`)
		if rec.Code != http.StatusUnauthorized {
			t.Fatalf("%s: status = %d, want 401", tc.name, rec.Code)
		}
	}
	if body, _, _ := cap.snapshot(); body != nil {
		t.Fatalf("unauthorized request reached upstream")
	}
}

func TestListModelsNativeShapes(t *testing.T) {
	h, _, cleanup := newTestHandler(t, "")
	defer cleanup()
	h.resolver = fakeResolver{listing: []resolve.Listing{
		{ID: "my-claude", ProviderID: "kimi", Protocol: "anthropic", Enabled: true,
			Efforts: []effort.Entry{{Name: "低", Value: "low"}, {Name: "ultra", Value: "ultra"}}},
		{ID: "my-gpt", ProviderID: "openai", Protocol: "chat_completions", Enabled: true,
			Efforts: []effort.Entry{{Name: "高", Value: "high"}}},
		{ID: "my-o3", ProviderID: "openai", Protocol: "responses", Enabled: true,
			Efforts: []effort.Entry{}},
		{ID: "disabled-one", ProviderID: "kimi", Protocol: "anthropic", Enabled: false},
		{ID: "my-gemini", ProviderID: "gemini", Protocol: "gemini", Enabled: true,
			Efforts: []effort.Entry{{Name: "中", Value: "medium"}}},
	}}

	// 三族共用 /v1/models:Bearer 放钥按 openai 形态列
	// (chat_completions + responses 都列,禁用的不列),并带有效推理档。
	rec := doRequest(t, h, http.MethodGet, "/v1/models",
		map[string]string{"Authorization": "Bearer " + testKey}, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	var oai struct {
		Object string `json:"object"`
		Data   []struct {
			ID      string         `json:"id"`
			Efforts []effort.Entry `json:"efforts"`
		} `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &oai); err != nil {
		t.Fatalf("bad openai list: %v", err)
	}
	if oai.Object != "list" || len(oai.Data) != 2 {
		t.Fatalf("openai list = %+v", oai)
	}
	if len(oai.Data[0].Efforts) != 1 || oai.Data[0].Efforts[0] != (effort.Entry{Name: "高", Value: "high"}) {
		t.Fatalf("openai my-gpt efforts = %+v", oai.Data[0].Efforts)
	}
	if oai.Data[1].Efforts == nil || len(oai.Data[1].Efforts) != 0 {
		t.Fatalf("openai my-o3 efforts = %+v, want []", oai.Data[1].Efforts)
	}

	// 同一路径带 x-api-key(Anthropic SDK 原生放钥位置)则按 anthropic
	// 形态列,只含 anthropic 协议的启用模型。
	rec = doRequest(t, h, http.MethodGet, "/v1/models",
		map[string]string{"x-api-key": testKey}, "")
	var ant struct {
		Data []struct {
			ID      string         `json:"id"`
			Type    string         `json:"type"`
			Efforts []effort.Entry `json:"efforts"`
		} `json:"data"`
		HasMore bool `json:"has_more"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &ant); err != nil {
		t.Fatalf("bad anthropic list: %v", err)
	}
	if len(ant.Data) != 1 || ant.Data[0].ID != "my-claude" || ant.Data[0].Type != "model" || ant.HasMore {
		t.Fatalf("anthropic list = %+v", ant)
	}
	wantAnt := []effort.Entry{{Name: "低", Value: "low"}, {Name: "ultra", Value: "ultra"}}
	if len(ant.Data[0].Efforts) != 2 || ant.Data[0].Efforts[0] != wantAnt[0] || ant.Data[0].Efforts[1] != wantAnt[1] {
		t.Fatalf("anthropic my-claude efforts = %+v, want %v", ant.Data[0].Efforts, wantAnt)
	}

	// gemini 族：name 带 models/ 前缀。
	rec = doRequest(t, h, http.MethodGet, "/v1beta/models?key="+testKey, nil, "")
	var gem struct {
		Models []struct {
			Name    string         `json:"name"`
			Efforts []effort.Entry `json:"efforts"`
		} `json:"models"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &gem); err != nil {
		t.Fatalf("bad gemini list: %v", err)
	}
	if len(gem.Models) != 1 || gem.Models[0].Name != "models/my-gemini" {
		t.Fatalf("gemini list = %+v", gem)
	}
	if len(gem.Models[0].Efforts) != 1 || gem.Models[0].Efforts[0] != (effort.Entry{Name: "中", Value: "medium"}) {
		t.Fatalf("gemini my-gemini efforts = %+v", gem.Models[0].Efforts)
	}
}

func TestUnknownModelAndUpstreamErrorPassthrough(t *testing.T) {
	h, _, cleanup := newTestHandler(t, "")
	defer cleanup()

	rec := doRequest(t, h, http.MethodPost, "/v1/chat/completions",
		map[string]string{"Authorization": "Bearer " + testKey}, `{"model":"ghost"}`)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("unknown model status = %d, want 404", rec.Code)
	}
}

func TestUpstreamNon2xxPassesThrough(t *testing.T) {
	cap := &captured{}
	up := httptest.NewServer(cap.handler(http.StatusTooManyRequests, `{"error":"slow down"}`))
	defer up.Close()

	resolver := fakeResolver{targets: map[string]resolve.ResolvedTarget{
		"my-gpt": {ModelID: "my-gpt", Protocol: "chat_completions", BaseURL: up.URL,
			NativeModel: "gpt-5", Headers: map[string]string{"Authorization": "Bearer k"}},
	}}
	h := NewHandler(testKey, resolver, nil)

	rec := doRequest(t, h, http.MethodPost, "/v1/chat/completions",
		map[string]string{"Authorization": "Bearer " + testKey}, `{"model":"my-gpt"}`)
	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("status = %d, want 429 passthrough", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "slow down") {
		t.Fatalf("body = %s, want upstream body passthrough", rec.Body.String())
	}
}

func TestMergeParamsDeepMerge(t *testing.T) {
	base := map[string]any{"a": 1, "obj": map[string]any{"x": 1, "y": 2}, "arr": []any{1, 2}}
	overlay := map[string]any{"b": 2, "obj": map[string]any{"y": 3}, "arr": []any{9}}
	got := mergeParams(base, overlay)
	obj := got["obj"].(map[string]any)
	if obj["x"] != 1 || obj["y"] != 3 {
		t.Fatalf("deep merge = %v", obj)
	}
	if arr := got["arr"].([]any); len(arr) != 1 || arr[0] != 9 {
		t.Fatalf("array should be replaced, got %v", arr)
	}
	if got["a"] != 1 || got["b"] != 2 {
		t.Fatalf("scalars = %v", got)
	}
}
