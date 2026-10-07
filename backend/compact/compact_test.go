package compact

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/aceaura/model-surge-upstream/backend/codex"
	"github.com/aceaura/model-surge-upstream/backend/provider"
	"github.com/aceaura/model-surge-upstream/backend/resolve"
	"github.com/aceaura/model-surge-upstream/backend/usage"
)

func TestRunProviderOutboundWire(t *testing.T) {
	for _, id := range []string{"deepseek.global.api.standard", "opencode.global.api.zen", "opencode.global.subscribe.go", "anthropic.global.api.standard", "openai.global.api.standard"} {
		spec, _ := provider.Get(id)
		for _, protocol := range spec.Protocols {
			t.Run(id+"/"+protocol, func(t *testing.T) {
				var got http.Header
				var gotPath string
				up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
					got, gotPath = req.Header.Clone(), req.URL.Path
					var body map[string]any
					if err := json.NewDecoder(req.Body).Decode(&body); err != nil || body["model"] != "native" {
						t.Errorf("body=%v err=%v", body, err)
					}
					_, _ = w.Write([]byte(`{"content":[{"type":"text","text":"SUMMARY"}],"choices":[{"message":{"content":"SUMMARY"}}],"output_text":"SUMMARY"}`))
				}))
				defer up.Close()
				basePath := ""
				isOpenCode := spec.DisplayName == "OpenCode"
				if isOpenCode {
					basePath = strings.TrimPrefix(spec.BaseURL, "https://opencode.ai")
				}
				tg := autoTarget(up.URL+basePath, 100)
				tg.ProviderID, tg.Protocol, tg.NativeModel = id, protocol, "native"
				if spec.Auth == provider.AuthBearer {
					tg.Headers = map[string]string{"Authorization": "Bearer upstream-key"}
				}
				r := NewRunner(DefaultConfig())
				r.logf = func(string, ...any) {}
				call := func(key string) http.Header {
					t.Helper()
					body := longAnthropicBody(10)
					listKey := "messages"
					if protocol == provider.ProtocolResponses {
						body["input"] = body["messages"]
						delete(body, "messages")
						listKey = "input"
					}
					body["prompt_cache_key"] = key
					got = nil
					out, _, reject := r.Run(context.Background(), tg, body, nil)
					if reject || got == nil || len(out[listKey].([]any)) >= 20 {
						t.Fatal("summary request not sent or not applied")
					}
					wantPath := basePath + map[string]string{provider.ProtocolAnthropic: "/v1/messages", provider.ProtocolChatCompletions: "/v1/chat/completions", provider.ProtocolResponses: "/v1/responses"}[protocol]
					if id == "deepseek.global.api.standard" && protocol == provider.ProtocolAnthropic {
						wantPath = "/anthropic/v1/messages"
					}
					if gotPath != wantPath {
						t.Errorf("path=%q, want %q", gotPath, wantPath)
					}
					if isOpenCode && protocol == provider.ProtocolAnthropic {
						if got.Get("x-api-key") != "upstream-key" || got.Get("Authorization") != "" || got.Get("anthropic-version") != provider.AnthropicVersion() {
							t.Error(got)
						}
					} else if spec.Auth == provider.AuthBearer {
						if got.Get("Authorization") != "Bearer upstream-key" || got.Get("x-api-key") != "" {
							t.Error(got)
						}
					} else if got.Get("x-api-key") != "k" || got.Get("Authorization") != "" {
						t.Error(got)
					}
					return got
				}
				first := call("cache-1")
				if id == "deepseek.global.api.standard" && protocol == provider.ProtocolAnthropic {
					tg.BaseURL = up.URL + "/anthropic/"
					call("cache-1")
				}
				if !isOpenCode {
					if first.Get("x-opencode-session") != "" || first.Get("User-Agent") == "ModelSurgeUpstream/1.0" {
						t.Fatal("OpenCode defaults leaked")
					}
					return
				}
				sid := first.Get("x-opencode-session")
				if sid == "" || first.Get("User-Agent") != "ModelSurgeUpstream/1.0" || call("cache-1").Get("x-opencode-session") != sid {
					t.Fatal("missing/unstable compact session")
				}
				if call("cache-2").Get("x-opencode-session") == sid {
					t.Fatal("compact cache keys not isolated")
				}
				for _, key := range []string{"proxy:cache-1", "chat:cache-1", "probe:" + tg.ModelID + ":" + protocol} {
					h := http.Header{}
					provider.ApplyRequestHeaders(id, protocol, tg.Account, key, h)
					if h.Get("x-opencode-session") == sid {
						t.Fatal("compact session not isolated")
					}
				}
				tg.Account = "b"
				if call("cache-1").Get("x-opencode-session") == sid {
					t.Fatal("compact accounts not isolated")
				}
				tg.Headers["x-opencode-session"] = "native-session"
				tg.Headers["User-Agent"] = "coding-agent/custom"
				preserved := call("cache-1")
				if preserved.Get("x-opencode-session") != "native-session" || preserved.Get("User-Agent") != "coding-agent/custom" {
					t.Fatal(preserved)
				}
			})
		}
	}
}

// longAnthropicBody 构造一个估算必超窗口的 Anthropic 请求体。
func longAnthropicBody(turns int) map[string]any {
	msgs := []any{}
	pad := strings.Repeat("很长的中文内容用来撑估算。", 20)
	for i := 0; i < turns; i++ {
		msgs = append(msgs,
			map[string]any{"role": "user", "content": pad},
			map[string]any{"role": "assistant", "content": pad},
		)
	}
	return map[string]any{"model": "alias", "messages": msgs}
}

func autoTarget(baseURL string, window int) resolve.ResolvedTarget {
	return resolve.ResolvedTarget{
		ModelID:       "m1",
		Account:       "acc",
		Protocol:      provider.ProtocolAnthropic,
		BaseURL:       baseURL,
		NativeModel:   "claude-native",
		ContextWindow: window,
		Headers:       map[string]string{"x-api-key": "k"},
		Compact:       json.RawMessage(`{"mode":"auto"}`),
	}
}

// fakeSummaryUpstream 区分摘要调用（末条消息含压缩指令）与普通请求。
func fakeSummaryUpstream(t *testing.T, status int) (*httptest.Server, *int32) {
	t.Helper()
	var summaryCalls int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		if strings.Contains(string(raw), "压缩为一份详尽摘要") {
			atomic.AddInt32(&summaryCalls, 1)
			w.WriteHeader(status)
			if status == http.StatusOK {
				_, _ = w.Write([]byte(`{"content":[{"type":"text","text":"[SUMMARY]"}],"usage":{"input_tokens":500,"output_tokens":10}}`))
			}
			return
		}
		t.Error("压缩路径下不应出现非摘要调用")
	}))
	return srv, &summaryCalls
}

func TestRunPassiveNoop(t *testing.T) {
	r := NewRunner(DefaultConfig()) // 全局默认 passive
	tgt := autoTarget("http://unused", 1)
	tgt.Compact = nil // 模型未配置 → 全局默认
	body := longAnthropicBody(10)
	out, _, reject := r.Run(context.Background(), tgt, body, nil)
	if reject {
		t.Fatal("passive 不应拒绝")
	}
	if len(out["messages"].([]any)) != 20 {
		t.Fatal("passive 不应改 body")
	}
}

func TestRunBelowThreshold(t *testing.T) {
	r := NewRunner(DefaultConfig())
	tgt := autoTarget("http://unused", 1000000)
	out, _, reject := r.Run(context.Background(), tgt, longAnthropicBody(2), nil)
	if reject || len(out["messages"].([]any)) != 4 {
		t.Fatal("未超阈值应原样放行")
	}
}

func TestRunErrorModeRejects(t *testing.T) {
	r := NewRunner(DefaultConfig())
	tgt := autoTarget("http://unused", 100)
	tgt.Compact = json.RawMessage(`{"mode":"error"}`)
	_, _, reject := r.Run(context.Background(), tgt, longAnthropicBody(10), nil)
	if !reject {
		t.Fatal("error 模式超限应 reject")
	}
}

func TestRunAutoCompacts(t *testing.T) {
	srv, summaryCalls := fakeSummaryUpstream(t, http.StatusOK)
	defer srv.Close()
	r := NewRunner(DefaultConfig())
	r.logf = func(string, ...any) {}

	var recorded usage.Usage
	tgt := autoTarget(srv.URL, 100)
	out, _, reject := r.Run(context.Background(), tgt, longAnthropicBody(10),
		func(u usage.Usage, _ int, _, _ time.Duration) { recorded = u })
	if reject {
		t.Fatal("auto 模式不应 reject")
	}
	if atomic.LoadInt32(summaryCalls) != 1 {
		t.Fatalf("摘要调用次数 = %d, want 1", summaryCalls)
	}
	msgs := out["messages"].([]any)
	// [摘要user, 假assistant] + 最近 6 轮（12 条）
	if len(msgs) != 14 {
		t.Fatalf("压缩后 messages = %d, want 14", len(msgs))
	}
	sumBlock := msgs[0].(map[string]any)["content"].([]any)[0].(map[string]any)
	if !strings.Contains(sumBlock["text"].(string), "[SUMMARY]") {
		t.Fatalf("首条应为摘要, got %v", sumBlock["text"])
	}
	if recorded.InputTokens != 500 || recorded.OutputTokens != 10 {
		t.Fatalf("摘要用量未记录: %+v", recorded)
	}
}

func TestRunAutoFallbackOnUpstreamError(t *testing.T) {
	srv, _ := fakeSummaryUpstream(t, http.StatusInternalServerError)
	defer srv.Close()
	r := NewRunner(DefaultConfig())
	r.logf = func(string, ...any) {}
	tgt := autoTarget(srv.URL, 100)
	body := longAnthropicBody(10)
	out, _, reject := r.Run(context.Background(), tgt, body, nil)
	if reject {
		t.Fatal("fail-open 不应 reject")
	}
	if len(out["messages"].([]any)) != 20 {
		t.Fatal("摘要失败应原样转发")
	}
}

func TestRunAutoFallbackOnBadJSON(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`not json`))
	}))
	defer srv.Close()
	r := NewRunner(DefaultConfig())
	r.logf = func(string, ...any) {}
	tgt := autoTarget(srv.URL, 100)
	body := longAnthropicBody(10)
	out, _, _ := r.Run(context.Background(), tgt, body, nil)
	if len(out["messages"].([]any)) != 20 {
		t.Fatal("响应解析失败应原样转发")
	}
}

func TestRunUnsupportedProtocol(t *testing.T) {
	r := NewRunner(DefaultConfig())
	tgt := autoTarget("http://unused", 1)
	tgt.Protocol = provider.ProtocolGemini // 二期才覆盖
	body := longAnthropicBody(10)
	out, _, reject := r.Run(context.Background(), tgt, body, nil)
	if reject || len(out["messages"].([]any)) != 20 {
		t.Fatal("未覆盖协议应原样放行")
	}
}

func TestRunZeroWindow(t *testing.T) {
	r := NewRunner(DefaultConfig())
	tgt := autoTarget("http://unused", 0) // 未声明窗口
	body := longAnthropicBody(10)
	out, _, _ := r.Run(context.Background(), tgt, body, nil)
	if len(out["messages"].([]any)) != 20 {
		t.Fatal("context_window=0 不应触发压缩")
	}
}

func TestRunAutoFallbackOnNoCutPoint(t *testing.T) {
	srv, _ := fakeSummaryUpstream(t, http.StatusOK)
	defer srv.Close()
	r := NewRunner(DefaultConfig())
	r.logf = func(string, ...any) {}
	tgt := autoTarget(srv.URL, 100)
	// 一条真人发言 + 长 tool 链：无安全切点
	body := map[string]any{
		"model": "alias",
		"messages": []any{
			map[string]any{"role": "user", "content": strings.Repeat("长", 500)},
			map[string]any{"role": "assistant", "content": []any{map[string]any{"type": "tool_use", "id": "t1"}}},
			map[string]any{"role": "user", "content": []any{map[string]any{"type": "tool_result", "tool_use_id": "t1", "content": strings.Repeat("果", 500)}}},
		},
	}
	out, _, _ := r.Run(context.Background(), tgt, body, nil)
	if len(out["messages"].([]any)) != 3 {
		t.Fatal("无安全切点应原样转发")
	}
}

// longResponsesBody 构造一个估算必超窗口的 Responses 请求体。
func longResponsesBody(turns int) map[string]any {
	var items []any
	pad := strings.Repeat("很长的中文内容用来撑估算。", 20)
	for i := 0; i < turns; i++ {
		items = append(items,
			map[string]any{"type": "message", "role": "user", "content": []any{map[string]any{"type": "input_text", "text": pad}}},
			map[string]any{"type": "message", "role": "assistant", "content": []any{map[string]any{"type": "output_text", "text": pad}}},
		)
	}
	return map[string]any{"model": "alias", "instructions": "你是助手", "input": items}
}

func responsesTarget(baseURL string, window int) resolve.ResolvedTarget {
	return resolve.ResolvedTarget{
		ModelID:       "m1",
		Account:       "acc",
		Protocol:      provider.ProtocolResponses,
		BaseURL:       baseURL,
		NativeModel:   "gpt-native",
		ContextWindow: window,
		Headers:       map[string]string{"Authorization": "Bearer k"},
		Compact:       json.RawMessage(`{"mode":"auto"}`),
	}
}

func TestRunAutoCompactsResponses(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/responses" {
			t.Errorf("摘要调用路径 = %s, want /v1/responses", r.URL.Path)
		}
		raw, _ := io.ReadAll(r.Body)
		if !strings.Contains(string(raw), "压缩为一份详尽摘要") {
			t.Error("压缩路径下不应出现非摘要调用")
		}
		var req map[string]any
		_ = json.Unmarshal(raw, &req)
		if _, streaming := req["stream"]; streaming {
			t.Error("通用 responses 供应商的摘要请求应非流式")
		}
		_, _ = w.Write([]byte(`{"output":[{"type":"message","content":[{"type":"output_text","text":"[SUMMARY]"}]}],"usage":{"input_tokens":500,"output_tokens":10}}`))
	}))
	defer srv.Close()
	r := NewRunner(DefaultConfig())
	r.logf = func(string, ...any) {}

	var recorded usage.Usage
	out, _, reject := r.Run(context.Background(), responsesTarget(srv.URL, 100), longResponsesBody(10),
		func(u usage.Usage, _ int, _, _ time.Duration) { recorded = u })
	if reject {
		t.Fatal("auto 模式不应 reject")
	}
	items := out["input"].([]any)
	// 摘要 user + 最近 6 轮（12 条）
	if len(items) != 13 {
		t.Fatalf("压缩后 input = %d, want 13", len(items))
	}
	first := items[0].(map[string]any)
	if first["role"] != "user" || first["type"] != "message" {
		t.Fatalf("首条应为摘要 user 消息, got %v", first)
	}
	text := first["content"].([]any)[0].(map[string]any)["text"].(string)
	if !strings.Contains(text, "[SUMMARY]") {
		t.Fatalf("首条应为摘要, got %v", text)
	}
	if out["instructions"] != "你是助手" {
		t.Fatal("instructions 应原样保留")
	}
	if recorded.InputTokens != 500 || recorded.OutputTokens != 10 {
		t.Fatalf("摘要用量未记录: %+v", recorded)
	}
}

func TestRunAutoCompactsCodexSubscription(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// MapSuffix:/v1/responses → /responses
		if r.URL.Path != "/responses" {
			t.Errorf("codex 摘要调用路径 = %s, want /responses", r.URL.Path)
		}
		raw, _ := io.ReadAll(r.Body)
		var req map[string]any
		_ = json.Unmarshal(raw, &req)
		// ShapeBody 硬约束:store=false、stream=true、剥 max_output_tokens、instructions 非空
		if req["store"] != false || req["stream"] != true {
			t.Errorf("codex 摘要请求缺硬约束: store=%v stream=%v", req["store"], req["stream"])
		}
		if _, ok := req["max_output_tokens"]; ok {
			t.Error("codex 摘要请求应剥掉 max_output_tokens")
		}
		if s, _ := req["instructions"].(string); strings.TrimSpace(s) == "" {
			t.Error("codex 摘要请求 instructions 应非空")
		}
		w.Header().Set("Content-Type", "text/event-stream")
		// codex 实测形态:completed 的 output 为空数组,正文走 delta 事件。
		_, _ = w.Write([]byte("event: response.output_text.delta\n" +
			"data: {\"type\":\"response.output_text.delta\",\"delta\":\"[SUMMARY]\"}\n\n" +
			"event: response.completed\n" +
			"data: {\"type\":\"response.completed\",\"response\":{\"output\":[],\"usage\":{\"input_tokens\":700,\"output_tokens\":20}}}\n\n"))
	}))
	defer srv.Close()
	r := NewRunner(DefaultConfig())
	r.logf = func(string, ...any) {}

	tgt := responsesTarget(srv.URL, 100)
	tgt.ProviderID = codex.ProviderID
	var recorded usage.Usage
	out, _, reject := r.Run(context.Background(), tgt, longResponsesBody(10),
		func(u usage.Usage, _ int, _, _ time.Duration) { recorded = u })
	if reject {
		t.Fatal("auto 模式不应 reject")
	}
	items := out["input"].([]any)
	if len(items) != 13 {
		t.Fatalf("压缩后 input = %d, want 13", len(items))
	}
	text := items[0].(map[string]any)["content"].([]any)[0].(map[string]any)["text"].(string)
	if !strings.Contains(text, "[SUMMARY]") {
		t.Fatalf("首条应为摘要, got %v", text)
	}
	if recorded.InputTokens != 700 || recorded.OutputTokens != 20 {
		t.Fatalf("SSE 摘要用量未记录: %+v", recorded)
	}
}
