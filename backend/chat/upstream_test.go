package chat

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/aceaura/model-surge-upstream/backend/codex"
	"github.com/aceaura/model-surge-upstream/backend/provider"
	"github.com/aceaura/model-surge-upstream/backend/resolve"
)

// history 是一轮典型对话：用户提问 + 助手上一轮回复 + 本轮用户追问。
func history() []Message {
	return []Message{
		{Role: RoleUser, Content: "你好"},
		{Role: RoleAssistant, Content: "在的"},
		{Role: RoleUser, Content: "1+1?"},
	}
}

func target(protocol string) resolve.ResolvedTarget {
	return resolve.ResolvedTarget{
		ModelID:     "m-" + protocol,
		Protocol:    protocol,
		BaseURL:     "https://up.example/api",
		NativeModel: "native-1",
	}
}

// TestBuildRequestSuffixAndShape 校验四协议各自的路径后缀与请求体骨架。
func TestBuildRequestSuffixAndShape(t *testing.T) {
	cases := []struct {
		protocol   string
		wantSuffix string
		// probe 从 body 里取一个能证明形态正确的字段。
		probe func(t *testing.T, body map[string]any)
	}{
		{
			protocol:   provider.ProtocolAnthropic,
			wantSuffix: "/v1/messages",
			probe: func(t *testing.T, body map[string]any) {
				if body["model"] != "native-1" {
					t.Fatalf("model = %v", body["model"])
				}
				if body["max_tokens"] != 2048 {
					t.Fatalf("max_tokens = %v", body["max_tokens"])
				}
				msgs, ok := body["messages"].([]map[string]any)
				if !ok || len(msgs) != 3 {
					t.Fatalf("messages = %#v", body["messages"])
				}
				if msgs[0]["role"] != RoleUser || msgs[0]["content"] != "你好" {
					t.Fatalf("messages[0] = %#v", msgs[0])
				}
			},
		},
		{
			protocol:   provider.ProtocolChatCompletions,
			wantSuffix: "/v1/chat/completions",
			probe: func(t *testing.T, body map[string]any) {
				msgs, ok := body["messages"].([]map[string]any)
				if !ok || len(msgs) != 3 {
					t.Fatalf("messages = %#v", body["messages"])
				}
				if _, has := body["max_tokens"]; has {
					t.Fatalf("chat_completions 不应默认注入 max_tokens")
				}
			},
		},
		{
			protocol:   provider.ProtocolResponses,
			wantSuffix: "/v1/responses",
			probe: func(t *testing.T, body map[string]any) {
				in, ok := body["input"].([]map[string]any)
				if !ok || len(in) != 3 {
					t.Fatalf("input = %#v", body["input"])
				}
				if in[0]["type"] != "message" || in[0]["role"] != RoleUser {
					t.Fatalf("input[0] = %#v", in[0])
				}
			},
		},
		{
			protocol:   provider.ProtocolGemini,
			wantSuffix: "/v1beta/models/native-1:generateContent",
			probe: func(t *testing.T, body map[string]any) {
				contents, ok := body["contents"].([]map[string]any)
				if !ok || len(contents) != 3 {
					t.Fatalf("contents = %#v", body["contents"])
				}
				// assistant 必须改写成 model 角色。
				if contents[1]["role"] != "model" {
					t.Fatalf("contents[1].role = %v, 期望 model", contents[1]["role"])
				}
				parts, ok := contents[0]["parts"].([]map[string]any)
				if !ok || parts[0]["text"] != "你好" {
					t.Fatalf("contents[0].parts = %#v", contents[0]["parts"])
				}
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.protocol, func(t *testing.T) {
			suffix, body, err := buildRequest(target(tc.protocol), history())
			if err != nil {
				t.Fatalf("buildRequest err = %v", err)
			}
			if suffix != tc.wantSuffix {
				t.Fatalf("suffix = %q, 期望 %q", suffix, tc.wantSuffix)
			}
			tc.probe(t, body)
		})
	}
}

// TestBuildRequestWithImages 锁定四协议的带图 part 形态：图片在前、文本在后，
// 纯图消息不产出文本 part；无附件消息仍保持字符串 content（见上方基线用例）。
func TestBuildRequestWithImages(t *testing.T) {
	img := ImageAttachment{Mime: "image/png", Data: "aGVsbG8="}
	hist := []Message{
		{Role: RoleUser, Content: "看这张图", Attachments: []ImageAttachment{img}},
		{Role: RoleAssistant, Content: "看到了"},
		{Role: RoleUser, Attachments: []ImageAttachment{img}}, // 纯图
	}
	cases := []struct {
		protocol string
		probe    func(t *testing.T, body map[string]any)
	}{
		{
			protocol: provider.ProtocolAnthropic,
			probe: func(t *testing.T, body map[string]any) {
				msgs := body["messages"].([]map[string]any)
				parts := msgs[0]["content"].([]map[string]any)
				if len(parts) != 2 || parts[0]["type"] != "image" || parts[1]["type"] != "text" {
					t.Fatalf("content = %#v", parts)
				}
				src := parts[0]["source"].(map[string]any)
				if src["type"] != "base64" || src["media_type"] != "image/png" || src["data"] != "aGVsbG8=" {
					t.Fatalf("source = %#v", src)
				}
				// 助手消息无附件，保持字符串。
				if msgs[1]["content"] != "看到了" {
					t.Fatalf("assistant content = %#v", msgs[1]["content"])
				}
				// 纯图消息只有 image part。
				if got := msgs[2]["content"].([]map[string]any); len(got) != 1 {
					t.Fatalf("纯图 content = %#v", got)
				}
			},
		},
		{
			protocol: provider.ProtocolChatCompletions,
			probe: func(t *testing.T, body map[string]any) {
				msgs := body["messages"].([]map[string]any)
				parts := msgs[0]["content"].([]map[string]any)
				if len(parts) != 2 || parts[0]["type"] != "image_url" || parts[1]["type"] != "text" {
					t.Fatalf("content = %#v", parts)
				}
				url := parts[0]["image_url"].(map[string]any)["url"]
				if url != "data:image/png;base64,aGVsbG8=" {
					t.Fatalf("image_url = %v", url)
				}
			},
		},
		{
			protocol: provider.ProtocolResponses,
			probe: func(t *testing.T, body map[string]any) {
				in := body["input"].([]map[string]any)
				parts := in[0]["content"].([]map[string]any)
				if len(parts) != 2 || parts[0]["type"] != "input_image" || parts[1]["type"] != "input_text" {
					t.Fatalf("content = %#v", parts)
				}
				if parts[0]["image_url"] != "data:image/png;base64,aGVsbG8=" {
					t.Fatalf("image_url = %v", parts[0]["image_url"])
				}
				// 无附件消息 content 仍是字符串。
				if in[1]["content"] != "看到了" {
					t.Fatalf("assistant content = %#v", in[1]["content"])
				}
			},
		},
		{
			protocol: provider.ProtocolGemini,
			probe: func(t *testing.T, body map[string]any) {
				contents := body["contents"].([]map[string]any)
				parts := contents[0]["parts"].([]map[string]any)
				if len(parts) != 2 {
					t.Fatalf("parts = %#v", parts)
				}
				inline := parts[0]["inline_data"].(map[string]any)
				if inline["mime_type"] != "image/png" || inline["data"] != "aGVsbG8=" {
					t.Fatalf("inline_data = %#v", inline)
				}
				if parts[1]["text"] != "看这张图" {
					t.Fatalf("text part = %#v", parts[1])
				}
				// 纯图消息不产出 text part。
				if got := contents[2]["parts"].([]map[string]any); len(got) != 1 {
					t.Fatalf("纯图 parts = %#v", got)
				}
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.protocol, func(t *testing.T) {
			_, body, err := buildRequest(target(tc.protocol), hist)
			if err != nil {
				t.Fatalf("buildRequest err = %v", err)
			}
			tc.probe(t, body)
		})
	}
}

// TestBuildRequestUnknownProtocol 保证未知协议走领域错误而非 panic。
func TestBuildRequestUnknownProtocol(t *testing.T) {
	if _, _, err := buildRequest(target("nope"), history()); err == nil {
		t.Fatal("未知协议应返回错误")
	}
}

// TestBuildRequestMergeParams 校验 defaults 补缺、overrides 压盖的合并语义。
func TestBuildRequestMergeParams(t *testing.T) {
	tgt := target(provider.ProtocolAnthropic)
	// defaults 提供 temperature（body 无此键，应补入）与 max_tokens（body 已有，不应覆盖）。
	tgt.Defaults = json.RawMessage(`{"temperature":0.3,"max_tokens":99}`)
	// overrides 强制压盖 max_tokens。
	tgt.Overrides = json.RawMessage(`{"max_tokens":512}`)

	_, body, err := buildRequest(tgt, history())
	if err != nil {
		t.Fatalf("buildRequest err = %v", err)
	}
	if body["temperature"] != 0.3 {
		t.Fatalf("temperature = %v, 期望 defaults 补入 0.3", body["temperature"])
	}
	// overrides 优先于 body 默认的 2048。
	if got, _ := body["max_tokens"].(float64); got != 512 {
		t.Fatalf("max_tokens = %v, 期望 overrides 压盖为 512", body["max_tokens"])
	}
}

// TestExtractReply 校验四协议成功响应的回复文本抽取。
func TestExtractReply(t *testing.T) {
	cases := []struct {
		protocol string
		raw      string
		want     string
	}{
		{
			protocol: provider.ProtocolAnthropic,
			raw:      `{"content":[{"type":"text","text":"2"},{"type":"text","text":"!"}]}`,
			want:     "2!",
		},
		{
			protocol: provider.ProtocolChatCompletions,
			raw:      `{"choices":[{"message":{"role":"assistant","content":"OK"}}]}`,
			want:     "OK",
		},
		{
			protocol: provider.ProtocolResponses,
			raw:      `{"output_text":"直接命中"}`,
			want:     "直接命中",
		},
		{
			// responses 无 output_text 时回退解析 output[].content[].text。
			protocol: provider.ProtocolResponses,
			raw:      `{"output":[{"type":"reasoning","content":[{"text":"忽略"}]},{"type":"message","content":[{"text":"回退"},{"text":"拼接"}]}]}`,
			want:     "回退拼接",
		},
		{
			protocol: provider.ProtocolGemini,
			raw:      `{"candidates":[{"content":{"parts":[{"text":"答"},{"text":"案"}]}}]}`,
			want:     "答案",
		},
	}
	for _, tc := range cases {
		t.Run(tc.protocol, func(t *testing.T) {
			got, err := extractReply(tc.protocol, []byte(tc.raw))
			if err != nil {
				t.Fatalf("extractReply err = %v", err)
			}
			if got != tc.want {
				t.Fatalf("got %q, 期望 %q", got, tc.want)
			}
		})
	}
}

// TestExtractReplyErrors 校验畸形响应返回错误而非静默空串。
func TestExtractReplyErrors(t *testing.T) {
	if _, err := extractReply(provider.ProtocolChatCompletions, []byte(`not json`)); err == nil {
		t.Fatal("非法 JSON 应返回错误")
	}
	if _, err := extractReply(provider.ProtocolChatCompletions, []byte(`{"choices":[]}`)); err == nil {
		t.Fatal("缺少 choices 应返回错误")
	}
	if _, err := extractReply(provider.ProtocolGemini, []byte(`{}`)); err == nil {
		t.Fatal("缺少 candidates 应返回错误")
	}
}

func codexTarget(baseURL string) resolve.ResolvedTarget {
	return resolve.ResolvedTarget{
		ModelID:     "codex-1/gpt-6.1-sol",
		Account:     "codex-1",
		ProviderID:  codex.ProviderID,
		Protocol:    provider.ProtocolResponses,
		BaseURL:     baseURL,
		NativeModel: "gpt-6.1-sol",
		Headers:     map[string]string{"Authorization": "Bearer at"},
	}
}

// codex 目标：路径映射 /v1/responses→/responses、体硬约束（store=false、
// stream=true、instructions 注入、采样参数剥除）、会话头按账号+会话派生，
// SSE 响应聚合成整轮回复与用量。
func TestCompleteCodexShapingAndSSE(t *testing.T) {
	var gotPath, gotSession, gotConversation, gotClientReq string
	var gotBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotSession = r.Header.Get("session_id")
		gotConversation = r.Header.Get("conversation_id")
		gotClientReq = r.Header.Get("x-client-request-id")
		_ = json.NewDecoder(r.Body).Decode(&gotBody)
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: {\"type\":\"response.output_text.delta\",\"delta\":\"你\"}\n\n")
		fmt.Fprint(w, "data: {\"type\":\"response.output_text.delta\",\"delta\":\"好\"}\n\n")
		fmt.Fprint(w, "data: {\"type\":\"response.completed\",\"response\":{\"output_text\":\"你好\",\"usage\":{\"input_tokens\":5,\"output_tokens\":2}}}\n\n")
		fmt.Fprint(w, "data: [DONE]\n\n")
	}))
	defer srv.Close()

	target := codexTarget(srv.URL)
	target.Overrides = json.RawMessage(`{"temperature":0.9,"store":true}`)
	history := []Message{{Role: RoleUser, Content: "hi"}}
	reply, u, status, err := Complete(context.Background(), target, "sess-1", history)
	if err != nil {
		t.Fatalf("Complete: %v", err)
	}
	if status != http.StatusOK {
		t.Errorf("status = %d, want 200", status)
	}
	if gotPath != "/responses" {
		t.Errorf("path = %q, want /responses (MapSuffix)", gotPath)
	}
	if gotBody["store"] != false || gotBody["stream"] != true {
		t.Errorf("store/stream = %v/%v, want forced false/true", gotBody["store"], gotBody["stream"])
	}
	if _, ok := gotBody["temperature"]; ok {
		t.Error("sampling params must be stripped")
	}
	if inst, _ := gotBody["instructions"].(string); inst == "" {
		t.Error("instructions should be injected when absent")
	}
	if _, ok := gotBody["input"].([]any); !ok {
		t.Errorf("input must be a list, got %T", gotBody["input"])
	}
	wantSID := codex.SessionID("codex-1", "sess-1")
	if gotSession != wantSID || gotConversation != wantSID {
		t.Errorf("session headers = %q/%q, want %q", gotSession, gotConversation, wantSID)
	}
	if gotClientReq == "" {
		t.Error("x-client-request-id should be set")
	}
	if reply != "你好" {
		t.Errorf("reply = %q, want completed 事件的整轮文本", reply)
	}
	if u.InputTokens != 5 || u.OutputTokens != 2 {
		t.Errorf("usage = %+v, want 5/2 from response.completed", u)
	}
}

// codex 上游事件流的 Content-Type 实测是 text/plain,不能靠响应头判 SSE。
func TestCompleteCodexSSEWithPlainContentType(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		fmt.Fprint(w, "event: response.created\n")
		fmt.Fprint(w, "data: {\"type\":\"response.created\",\"response\":{\"id\":\"r1\"}}\n\n")
		fmt.Fprint(w, "data: {\"type\":\"response.completed\",\"response\":{\"output_text\":\"ok\",\"usage\":{\"input_tokens\":1,\"output_tokens\":1}}}\n\n")
	}))
	defer srv.Close()

	reply, u, _, err := Complete(context.Background(), codexTarget(srv.URL), "s",
		[]Message{{Role: RoleUser, Content: "hi"}})
	if err != nil {
		t.Fatalf("Complete: %v", err)
	}
	if reply != "ok" || u.InputTokens != 1 {
		t.Errorf("reply/usage = %q/%+v, want ok/1", reply, u)
	}
}

// completed 缺失时回落 delta 拼接，用量为零值但不报错。
func TestCompleteCodexSSEDeltaFallback(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: {\"type\":\"response.output_text.delta\",\"delta\":\"ab\"}\n\n")
		fmt.Fprint(w, "data: {\"type\":\"response.output_text.delta\",\"delta\":\"cd\"}\n\n")
	}))
	defer srv.Close()

	reply, _, _, err := Complete(context.Background(), codexTarget(srv.URL), "s",
		[]Message{{Role: RoleUser, Content: "hi"}})
	if err != nil {
		t.Fatalf("Complete: %v", err)
	}
	if reply != "abcd" {
		t.Errorf("reply = %q, want joined deltas", reply)
	}
}

// 流里既无 completed 也无 delta：判上游异常，不静默回空串。
func TestCompleteCodexSSEEmpty(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: {\"type\":\"response.created\",\"response\":{\"id\":\"r1\"}}\n\n")
	}))
	defer srv.Close()

	_, _, _, err := Complete(context.Background(), codexTarget(srv.URL), "s",
		[]Message{{Role: RoleUser, Content: "hi"}})
	if err == nil {
		t.Fatal("empty stream must be an error")
	}
}

// 非 codex 目标回归基线：路径不映射、不加会话头、非流式 JSON 照常解析。
func TestCompleteNonCodexUnchanged(t *testing.T) {
	var gotPath, gotSession string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotSession = r.Header.Get("session_id")
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"choices":[{"message":{"content":"pong"}}],"usage":{"prompt_tokens":3,"completion_tokens":1}}`)
	}))
	defer srv.Close()

	target := resolve.ResolvedTarget{
		ModelID:     "a/b",
		Account:     "a",
		ProviderID:  "openai",
		Protocol:    provider.ProtocolChatCompletions,
		BaseURL:     srv.URL,
		NativeModel: "b",
	}
	reply, u, _, err := Complete(context.Background(), target, "s",
		[]Message{{Role: RoleUser, Content: "ping"}})
	if err != nil {
		t.Fatalf("Complete: %v", err)
	}
	if gotPath != "/v1/chat/completions" {
		t.Errorf("path = %q, want unmapped /v1/chat/completions", gotPath)
	}
	if gotSession != "" {
		t.Errorf("session_id = %q, want unset for non-codex", gotSession)
	}
	if reply != "pong" || u.InputTokens != 3 {
		t.Errorf("reply/usage = %q/%+v, want pong/3", reply, u)
	}
}

// 同账号同会话的 session_id 恒定，换会话即变（隔离语义与转发面一致）。
func TestCodexSessionIDStablePerSession(t *testing.T) {
	a := codex.SessionID("codex-1", "sess-1")
	b := codex.SessionID("codex-1", "sess-1")
	c := codex.SessionID("codex-1", "sess-2")
	if a != b {
		t.Error("same account+session must derive the same id")
	}
	if a == c {
		t.Error("different sessions must not share an id")
	}
	if !strings.Contains(a, "-") {
		t.Errorf("id %q should look like a UUID", a)
	}
}
