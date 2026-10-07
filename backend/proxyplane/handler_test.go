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
	"github.com/aceaura/model-surge-upstream/backend/provider"
	"github.com/aceaura/model-surge-upstream/backend/resolve"
)

func TestProviderOutboundWire(t *testing.T) {
	for _, id := range []string{"deepseek.global.api.standard", "opencode.global.api.zen", "opencode.global.subscribe.go", "anthropic.global.api.standard", "openai.global.api.standard"} {
		spec, _ := provider.Get(id)
		for _, protocol := range spec.Protocols {
			t.Run(id+"/"+protocol, func(t *testing.T) {
				path := map[string]string{provider.ProtocolAnthropic: "/v1/messages", provider.ProtocolChatCompletions: "/v1/chat/completions", provider.ProtocolResponses: "/v1/responses"}[protocol]
				cap := &captured{}
				up := httptest.NewServer(cap.handler(http.StatusOK, `{"ok":true}`))
				defer up.Close()
				basePath := ""
				isOpenCode := spec.DisplayName == "OpenCode"
				if isOpenCode {
					basePath = strings.TrimPrefix(spec.BaseURL, "https://opencode.ai")
				}
				target := resolve.ResolvedTarget{ModelID: "alias", Account: "a", ProviderID: id, Protocol: protocol,
					BaseURL: up.URL + basePath, NativeModel: "native", Headers: map[string]string{"Authorization": "Bearer upstream-key"}}
				if spec.Auth == provider.AuthAnthropicKey {
					target.Headers = map[string]string{"x-api-key": "upstream-key", "anthropic-version": provider.AnthropicVersion()}
				}
				request := func(headers map[string]string, key string) http.Header {
					t.Helper()
					h := NewHandler(testKey, fakeResolver{targets: map[string]resolve.ResolvedTarget{"alias": target}}, nil)
					if headers == nil {
						headers = map[string]string{}
					}
					headers["Authorization"] = "Bearer " + testKey
					headers["x-api-key"] = testKey
					rec := doRequest(t, h, http.MethodPost, path+"?key="+testKey+"&keep=1", headers,
						`{"model":"alias","prompt_cache_key":"`+key+`","messages":[{"role":"user","content":"hi"}],"input":"hi"}`)
					if rec.Code != http.StatusOK {
						t.Fatalf("proxy status=%d: %s", rec.Code, rec.Body.String())
					}
					raw, got, gotPath := cap.snapshot()
					wantPath := basePath + path + "?keep=1"
					if id == "deepseek.global.api.standard" && protocol == provider.ProtocolAnthropic {
						wantPath = "/anthropic" + path + "?keep=1"
					}
					if gotPath != wantPath {
						t.Errorf("path=%q, want %q", gotPath, wantPath)
					}
					if spec.Auth == provider.AuthBearer && !(isOpenCode && protocol == provider.ProtocolAnthropic) {
						if got.Get("Authorization") != "Bearer upstream-key" || got.Get("x-api-key") != "" {
							t.Errorf("auth=%v", got)
						}
					} else if got.Get("x-api-key") != "upstream-key" || got.Get("Authorization") != "" || got.Get("anthropic-version") != provider.AnthropicVersion() {
						t.Errorf("auth=%v", got)
					}
					var body map[string]any
					if err := json.Unmarshal(raw, &body); err != nil || body["model"] != "native" {
						t.Errorf("body=%s", raw)
					}
					return got
				}
				first := request(nil, "cache-1")
				if id == "deepseek.global.api.standard" && protocol == provider.ProtocolAnthropic {
					target.BaseURL = up.URL + "/anthropic/"
					request(nil, "cache-1")
				}
				if !isOpenCode {
					if first.Get("x-opencode-session") != "" || first.Get("User-Agent") == "ModelSurgeUpstream/1.0" {
						t.Fatal("OpenCode headers leaked")
					}
					return
				}
				sid := first.Get("x-opencode-session")
				if sid == "" || first.Get("User-Agent") != "ModelSurgeUpstream/1.0" || request(nil, "cache-1").Get("x-opencode-session") != sid {
					t.Fatal("missing/unstable defaults")
				}
				if request(nil, "cache-2").Get("x-opencode-session") == sid {
					t.Fatal("cache keys not isolated")
				}
				target.Account = "b"
				if request(nil, "cache-1").Get("x-opencode-session") == sid {
					t.Fatal("accounts not isolated")
				}
				for _, name := range []string{"x-opencode-session", "session_id", "x-session-id", "conversation_id", "x-conversation-id"} {
					got := request(map[string]string{name: "native-session", "User-Agent": "coding-agent/custom"}, "ignored")
					if got.Get(name) != "native-session" || got.Get("x-opencode-session") != "native-session" || got.Get("User-Agent") != "coding-agent/custom" {
						t.Errorf("native header %s lost: %v", name, got)
					}
				}
				got := request(map[string]string{"x-opencode-session": "preferred", "session_id": "second"}, "ignored")
				if got.Get("x-opencode-session") != "preferred" || got.Get("session_id") != "second" {
					t.Fatal(got)
				}
				if request(nil, "").Get("x-opencode-session") == "" {
					t.Fatal("missing body cache key must still yield a session")
				}
			})
		}
	}
}

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
			ModelID: "my-claude", Account: "kimi-1", ProviderID: "kimi.global.subscribe.coding",
			Protocol: "anthropic", BaseURL: upstreamURL, NativeModel: "kimi-k2",
			Headers:   map[string]string{"x-api-key": "real-account-key", "anthropic-version": "2023-06-01"},
			Defaults:  json.RawMessage(`{"temperature":0.6,"thinking":{"type":"enabled","budget_tokens":1000}}`),
			Overrides: json.RawMessage(`{"max_tokens":8192,"thinking":{"budget_tokens":2000}}`),
			Efforts: []effort.Entry{
				{Name: "0", Value: "none"},
				{Name: "1", Value: "high"},
			},
		},
		"my-gpt": {
			ModelID: "my-gpt", Account: "openai-1", ProviderID: "openai",
			Protocol: "chat_completions", BaseURL: upstreamURL, NativeModel: "gpt-5",
			Headers: map[string]string{"Authorization": "Bearer real-openai-key"},
			Efforts: []effort.Entry{
				{Name: "0", Value: "none"},
				{Name: "1", Value: "low"},
				{Name: "2", Value: "high"},
			},
		},
		"my-resp": {
			ModelID: "my-resp", Account: "openai-1", ProviderID: "openai",
			Protocol: "responses", BaseURL: upstreamURL, NativeModel: "gpt-6",
			Headers: map[string]string{"Authorization": "Bearer real-openai-key"},
			Efforts: []effort.Entry{
				{Name: "0", Value: "none"},
				{Name: "1", Value: "low"},
			},
		},
		"my-gemini": {
			ModelID: "my-gemini", Account: "g-1", ProviderID: "gemini.global.api.standard",
			Protocol: "gemini", BaseURL: upstreamURL, NativeModel: "gemini-2.5-pro",
			Headers: map[string]string{"Authorization": "Bearer real-gemini-key"},
		},
		// my-kimi 配了显式写入格式:anthropic 外壳但档位走顶层
		// reasoning_effort(kimi K3 官方口径),格式接管写入位置、内置映射
		// (anthropic 本应写 output_config)不再生效。
		"my-kimi": {
			ModelID: "my-kimi", Account: "kimi-1", ProviderID: "kimi.global.subscribe.coding",
			Protocol: "anthropic", BaseURL: upstreamURL, NativeModel: "k3-256k",
			Headers: map[string]string{"x-api-key": "real-account-key"},
			Efforts: []effort.Entry{
				{Name: "1", Value: "low"},
				{Name: "2", Value: "high"},
				{Name: "3", Value: "max"},
			},
			EffortFormat: effort.FormatChatCompletions,
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

// 显式写入格式接管:effort_format=chat_completions 时命中档写顶层
// reasoning_effort;档号未声明/未提供不动体(defaults 的值原样保留,
// 客户端直传值原样透传);0 档(none)原样上发并压过 defaults;overrides
// 恒压映射值;reasoning_level 消费后恒不泄漏。
func TestEffortFormatForward(t *testing.T) {
	for _, tc := range []struct {
		name, params, want string
		emptyEfforts       bool
		override           string
	}{
		{name: "missing", params: `{}`, want: "medium"},
		{name: "unknown level", params: `{"reasoning_level":"99"}`, want: "medium"},
		{name: "invalid level", params: `{"reasoning_level":{}}`, want: "medium"},
		{name: "empty efforts", params: `{"reasoning_level":"2"}`, want: "medium", emptyEfforts: true},
		{name: "client effort passes through", params: `{"reasoning_effort":"low"}`, want: "low"},
		{name: "client none passes through", params: `{"reasoning_effort":"none"}`, want: "none"},
		{name: "disabled level sends none", params: `{"reasoning_level":0,"reasoning_effort":"low"}`, want: "none"},
		{name: "low", params: `{"reasoning_level":"1","reasoning_effort":"ultra"}`, want: "low"},
		{name: "medium", params: `{"reasoning_level":"2"}`, want: "medium"},
		{name: "xhigh", params: `{"reasoning_level":"3"}`, want: "xhigh"},
		{name: "overrides win", params: `{}`, want: "xhigh", override: `{"reasoning_effort":"xhigh"}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cap := &captured{}
			up := httptest.NewServer(cap.handler(http.StatusOK, `{"ok":true}`))
			defer up.Close()
			target := resolve.ResolvedTarget{
				ModelID: "bailian-test", ProviderID: "bailian.cn.subscribe.token-plan", NativeModel: "qwen3.8-max",
				Protocol: provider.ProtocolChatCompletions, BaseURL: up.URL,
				Defaults:     json.RawMessage(`{"reasoning_effort":"medium"}`),
				Overrides:    json.RawMessage(tc.override),
				EffortFormat: effort.FormatChatCompletions,
				Efforts:      []effort.Entry{{Name: "0", Value: "none"}, {Name: "1", Value: "low"}, {Name: "2", Value: "medium"}, {Name: "3", Value: "xhigh"}},
			}
			if tc.emptyEfforts {
				target.Efforts = nil
			}
			h := NewHandler(testKey, fakeResolver{targets: map[string]resolve.ResolvedTarget{"bailian-test": target}}, nil)
			var req map[string]any
			if err := json.Unmarshal([]byte(tc.params), &req); err != nil {
				t.Fatal(err)
			}
			req["model"] = "bailian-test"
			req["messages"] = []any{}
			req["thinking_budget"] = 1024
			encoded, err := json.Marshal(req)
			if err != nil {
				t.Fatal(err)
			}
			rec := doRequest(t, h, http.MethodPost, "/v1/chat/completions", map[string]string{"Authorization": "Bearer " + testKey}, string(encoded))
			if rec.Code != http.StatusOK {
				t.Fatalf("status = %d, body %s", rec.Code, rec.Body.String())
			}
			body, _, _ := cap.snapshot()
			var got map[string]any
			if err := json.Unmarshal(body, &got); err != nil {
				t.Fatal(err)
			}
			if got["reasoning_effort"] != tc.want {
				t.Fatalf("effort = %v, want %s", got["reasoning_effort"], tc.want)
			}
			// 通用底层不再顺手删厂商字段:thinking_budget 原样透传。
			if got["thinking_budget"] != float64(1024) {
				t.Fatalf("thinking_budget 应原样透传: %s", body)
			}
			if _, exists := got["reasoning_level"]; exists {
				t.Fatalf("reasoning_level leaked upstream: %s", body)
			}
		})
	}
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

func TestBailianChatCompletionsForward(t *testing.T) {
	spec, ok := provider.Get("bailian.cn.subscribe.token-plan")
	if !ok {
		t.Fatal("bailian not registered")
	}
	cap := &captured{}
	up := httptest.NewServer(cap.handler(http.StatusOK, `{"choices":[{"message":{"content":"ok"}}]}`))
	defer up.Close()
	baseURL := up.URL + strings.TrimPrefix(spec.BaseURL, "https://token-plan.cn-beijing.maas.aliyuncs.com")
	h := NewHandler(testKey, fakeResolver{targets: map[string]resolve.ResolvedTarget{
		"bailian-1/qwen3.8-max": {
			ModelID: "bailian-1/qwen3.8-max", Account: "bailian-1", ProviderID: spec.ID,
			Protocol: provider.ProtocolChatCompletions, BaseURL: baseURL, NativeModel: "qwen3.8-max",
			Headers: map[string]string{"Authorization": "Bearer bailian-test-key"},
		},
	}}, nil)
	rec := doRequest(t, h, http.MethodPost, "/v1/chat/completions",
		map[string]string{"Authorization": "Bearer " + testKey},
		`{"model":"bailian-1/qwen3.8-max","messages":[{"role":"user","content":"hi"}]}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body %s", rec.Code, rec.Body.String())
	}
	body, headers, path := cap.snapshot()
	if path != "/compatible-mode/v1/chat/completions" {
		t.Fatalf("upstream path = %q", path)
	}
	var got map[string]any
	if err := json.Unmarshal(body, &got); err != nil {
		t.Fatal(err)
	}
	if got["model"] != "qwen3.8-max" || headers.Get("Authorization") != "Bearer bailian-test-key" {
		t.Fatalf("unexpected upstream model or auth: model=%v auth=%q", got["model"], headers.Get("Authorization"))
	}
	if got["thinking"] != nil || got["reasoning_effort"] != nil {
		t.Fatalf("unexpected thinking overrides: %s", body)
	}
}

// TestReasoningLevelMappedIntoThinkingParams 锁定 reasoning_level 数字档:
// 命中声明即消费(不进上游)并赋思考参数,字符串与整数字面量同效;0 档
// 映射 none=关闭思考;不在声明里的值原样透传不动体。
func TestReasoningLevelMappedIntoThinkingParams(t *testing.T) {
	h, cap, cleanup := newTestHandler(t, "")
	defer cleanup()

	upstreamBody := func() map[string]any {
		body, _, _ := cap.snapshot()
		var got map[string]any
		if err := json.Unmarshal(body, &got); err != nil {
			t.Fatalf("upstream body not json: %v", err)
		}
		return got
	}

	// 字符串档号命中:2→high,reasoning_effort 赋值,reasoning_level 不进上游。
	rec := doRequest(t, h, http.MethodPost, "/v1/chat/completions",
		map[string]string{"Authorization": "Bearer " + testKey},
		`{"model":"my-gpt","messages":[],"reasoning_level":"2"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body %s", rec.Code, rec.Body.String())
	}
	got := upstreamBody()
	if got["reasoning_effort"] != "high" {
		t.Fatalf("reasoning_effort = %v, want high", got["reasoning_effort"])
	}
	if _, leaked := got["reasoning_level"]; leaked {
		t.Fatalf("reasoning_level 泄漏到上游: %v", got)
	}

	// 整数字面量同效:0→none 即关闭思考。
	rec = doRequest(t, h, http.MethodPost, "/v1/chat/completions",
		map[string]string{"Authorization": "Bearer " + testKey},
		`{"model":"my-gpt","messages":[],"reasoning_level":0}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body %s", rec.Code, rec.Body.String())
	}
	if got := upstreamBody(); got["reasoning_effort"] != "none" {
		t.Fatalf("0 档 reasoning_effort = %v, want none", got["reasoning_effort"])
	}

	// responses 协议:1→low 进 reasoning.effort。
	rec = doRequest(t, h, http.MethodPost, "/v1/responses",
		map[string]string{"Authorization": "Bearer " + testKey},
		`{"model":"my-resp","input":[],"reasoning_level":"1"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body %s", rec.Code, rec.Body.String())
	}
	got = upstreamBody()
	reasoning, _ := got["reasoning"].(map[string]any)
	if reasoning["effort"] != "low" {
		t.Fatalf("responses reasoning = %v, want effort=low", got)
	}
	if _, leaked := got["reasoning_level"]; leaked {
		t.Fatalf("reasoning_level 泄漏到上游: %v", got)
	}

	// 未声明的档号:不赋任何思考参数;reasoning_level 是网关扩展字段,
	// 未命中也消费删除、不泄漏上游。
	rec = doRequest(t, h, http.MethodPost, "/v1/chat/completions",
		map[string]string{"Authorization": "Bearer " + testKey},
		`{"model":"my-gpt","messages":[],"reasoning_level":"9"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body %s", rec.Code, rec.Body.String())
	}
	got = upstreamBody()
	if _, leaked := got["reasoning_level"]; leaked {
		t.Fatalf("未命中档位也不应泄漏 reasoning_level, got %v", got)
	}
	if _, assigned := got["reasoning_effort"]; assigned {
		t.Fatalf("未命中档位不应赋 reasoning_effort: %v", got)
	}

	// anthropic:1→high 进 output_config.effort;0→none 改写 thinking disabled
	// (defaults 的 thinking 深合并,type 被客户端参数层的映射压盖)。
	rec = doRequest(t, h, http.MethodPost, "/v1/messages",
		map[string]string{"x-api-key": testKey},
		`{"model":"my-claude","max_tokens":1024,"messages":[],"reasoning_level":"1"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body %s", rec.Code, rec.Body.String())
	}
	got = upstreamBody()
	output, _ := got["output_config"].(map[string]any)
	if output["effort"] != "high" {
		t.Fatalf("anthropic output_config = %v, want effort=high", got)
	}
	if _, leaked := got["reasoning_level"]; leaked {
		t.Fatalf("reasoning_level 泄漏到上游: %v", got)
	}

	rec = doRequest(t, h, http.MethodPost, "/v1/messages",
		map[string]string{"x-api-key": testKey},
		`{"model":"my-claude","max_tokens":1024,"messages":[],"reasoning_level":"0"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body %s", rec.Code, rec.Body.String())
	}
	got = upstreamBody()
	thinking, _ := got["thinking"].(map[string]any)
	if thinking["type"] != "disabled" {
		t.Fatalf("anthropic 0 档 thinking = %v, want type=disabled", got)
	}
	if _, assigned := got["output_config"]; assigned {
		t.Fatalf("anthropic 0 档不应写 output_config: %v", got)
	}
}

// TestReasoningLevelYieldsToOverrides 锁定优先级:reasoning_level 映射
// 写在客户端参数层,overrides(JSON 覆盖参数)最后合并,恒压映射值。
func TestReasoningLevelYieldsToOverrides(t *testing.T) {
	cap := &captured{}
	up := httptest.NewServer(cap.handler(http.StatusOK, `{"ok":true}`))
	defer up.Close()
	resolver := fakeResolver{targets: map[string]resolve.ResolvedTarget{
		"my-gpt": {
			ModelID: "my-gpt", Account: "openai-1", ProviderID: "openai",
			Protocol: "chat_completions", BaseURL: up.URL, NativeModel: "gpt-5",
			Headers:   map[string]string{"Authorization": "Bearer real-openai-key"},
			Overrides: json.RawMessage(`{"reasoning_effort":"low"}`),
			Efforts: []effort.Entry{
				{Name: "0", Value: "none"},
				{Name: "2", Value: "high"},
			},
		},
	}}
	h := NewHandler(testKey, resolver, nil)

	rec := doRequest(t, h, http.MethodPost, "/v1/chat/completions",
		map[string]string{"Authorization": "Bearer " + testKey},
		`{"model":"my-gpt","messages":[],"reasoning_level":"2"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body %s", rec.Code, rec.Body.String())
	}
	body, _, _ := cap.snapshot()
	var got map[string]any
	if err := json.Unmarshal(body, &got); err != nil {
		t.Fatalf("upstream body not json: %v", err)
	}
	if got["reasoning_effort"] != "low" {
		t.Fatalf("overrides 应压过映射: reasoning_effort = %v, want low", got["reasoning_effort"])
	}
	if _, leaked := got["reasoning_level"]; leaked {
		t.Fatalf("reasoning_level 泄漏到上游: %v", got)
	}
}

// 显式写入格式接管:模型配了 effort_format 即按格式决定写入位置(顶层
// reasoning_effort),协议内置映射(anthropic 本应写 output_config)不
// 再生效;reasoning_level 照例消费不泄漏。
func TestReasoningLevelFormatTakesOver(t *testing.T) {
	h, cap, cleanup := newTestHandler(t, "")
	defer cleanup()

	rec := doRequest(t, h, http.MethodPost, "/v1/messages",
		map[string]string{"x-api-key": testKey},
		`{"model":"my-kimi","max_tokens":100,"messages":[{"role":"user","content":"hi"}],"reasoning_level":"1"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body %s", rec.Code, rec.Body.String())
	}
	body, _, _ := cap.snapshot()
	var got map[string]any
	if err := json.Unmarshal(body, &got); err != nil {
		t.Fatalf("upstream body not json: %v", err)
	}
	if got["reasoning_effort"] != "low" {
		t.Fatalf("格式应写顶层 reasoning_effort: %v", got)
	}
	if _, ok := got["output_config"]; ok {
		t.Fatalf("格式接管后内置映射不应再写 output_config: %v", got)
	}
	if _, leaked := got["reasoning_level"]; leaked {
		t.Fatalf("reasoning_level 泄漏到上游: %v", got)
	}
	if got["model"] != "k3-256k" {
		t.Fatalf("model = %v, want k3-256k", got["model"])
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
		{ID: "my-claude", ProviderID: "kimi.global.subscribe.coding", Protocol: "anthropic", Enabled: true,
			Efforts: []effort.Entry{{Name: "低", Value: "low"}, {Name: "ultra", Value: "ultra"}}},
		{ID: "my-gpt", ProviderID: "openai", Protocol: "chat_completions", Enabled: true,
			Efforts: []effort.Entry{{Name: "高", Value: "high"}}},
		{ID: "my-o3", ProviderID: "openai", Protocol: "responses", Enabled: true,
			Efforts: []effort.Entry{}},
		{ID: "disabled-one", ProviderID: "kimi.global.subscribe.coding", Protocol: "anthropic", Enabled: false},
		{ID: "my-gemini", ProviderID: "gemini.global.api.standard", Protocol: "gemini", Enabled: true,
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
