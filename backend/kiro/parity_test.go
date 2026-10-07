package kiro

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

// Parity coverage for behaviors ported from KiroaaS python-backend: model name
// normalization, fake reasoning (injection + response parsing), tool_choice
// policy, tool pairing repair, truncation recovery, bracket tool calls,
// cache usage passthrough, and 429/5xx retries.

func currentContent(t *testing.T, payload object) string {
	t.Helper()
	return str(obj(obj(obj(payload["conversationState"])["currentMessage"])["userInputMessage"])["content"])
}

func convert(t *testing.T, root object, protocol string) (object, requestOptions) {
	t.Helper()
	payload, opts, err := convertRequest([]byte(jsonText(root)), protocol, "")
	if err != nil {
		t.Fatal(err)
	}
	return payload, opts
}

func TestModelNameNormalization(t *testing.T) {
	cases := map[string]string{
		"claude-sonnet-4-5":          "claude-sonnet-4.5",
		"claude-sonnet-4-5-20250929": "claude-sonnet-4.5",
		"claude-sonnet-4-5-latest":   "claude-sonnet-4.5",
		"claude-opus-4-1":            "claude-opus-4.1",
		"claude-opus-4-1-20250805":   "claude-opus-4.1",
		"claude-haiku-4-5":           "claude-haiku-4.5",
		"claude-sonnet-4":            "claude-sonnet-4",
		"claude-opus-4-20250514":     "claude-opus-4",
		"claude-3-7-sonnet":          "claude-3.7-sonnet",
		"claude-3-7-sonnet-20250219": "claude-3.7-sonnet",
		"claude-3-5-sonnet-latest":   "claude-3.5-sonnet",
		"claude-3.5-sonnet-20241022": "claude-3.5-sonnet",
		"claude-sonnet-4.5-20250929": "claude-sonnet-4.5",
		"claude-4.5-opus-high":       "claude-opus-4.5",
		"claude-sonnet-4-5[200k]":    "claude-sonnet-4.5",
		"gpt-5.5[1m]":                "gpt-5.5",
		"Claude-Sonnet-4-6":          "claude-sonnet-4.6",
		"gpt-5.6-sol":                "gpt-5.6-sol",
		"custom-model":               "custom-model",
		"claude-instant-1-2":         "claude-instant-1-2",
	}
	for in, want := range cases {
		if got := nativeModel(in); got != want {
			t.Errorf("nativeModel(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestFakeReasoningInjection(t *testing.T) {
	base := func() object {
		return object{"model": "claude-sonnet-4.6", "messages": []any{object{"role": "user", "content": "hello"}}}
	}
	t.Run("default budget", func(t *testing.T) {
		payload, opts := convert(t, base(), "anthropic")
		if !opts.fakeReasoning {
			t.Fatal("fake reasoning not enabled")
		}
		content := currentContent(t, payload)
		if !strings.HasPrefix(content, "<thinking_mode>enabled</thinking_mode>\n<max_thinking_length>4000</max_thinking_length>\n<thinking_instruction>") {
			t.Fatalf("content=%q", content)
		}
		if !strings.Contains(content, "# Extended Thinking Mode") || !strings.Contains(content, "# Output Truncation Handling") {
			t.Fatalf("system additions missing: %q", content)
		}
	})
	t.Run("effort tag verbatim", func(t *testing.T) {
		root := base()
		root["model"] = "other-model"
		root["reasoning_effort"] = "high"
		payload, _ := convert(t, root, "openai")
		content := currentContent(t, payload)
		if !strings.HasPrefix(content, "<thinking_mode>enabled</thinking_mode>\n<thinking_effort>high</thinking_effort>") {
			t.Fatalf("content=%q", content)
		}
	})
	t.Run("minimal aliases low", func(t *testing.T) {
		// config.py:526: minimal→low 别名只属 openai 侧。
		root := base()
		root["model"] = "other-model"
		root["reasoning_effort"] = "minimal"
		payload, _ := convert(t, root, "openai")
		if content := currentContent(t, payload); !strings.Contains(content, "<thinking_effort>low</thinking_effort>") {
			t.Fatalf("content=%q", content)
		}
	})
	t.Run("unknown tier falls back medium", func(t *testing.T) {
		root := base()
		root["model"] = "other-model"
		root["reasoning_effort"] = "ultra"
		payload, _ := convert(t, root, "anthropic")
		if content := currentContent(t, payload); !strings.Contains(content, "<thinking_effort>medium</thinking_effort>") {
			t.Fatalf("content=%q", content)
		}
	})
	t.Run("budget capped", func(t *testing.T) {
		root := base()
		root["thinking"] = object{"type": "enabled", "budget_tokens": 99999}
		payload, _ := convert(t, root, "anthropic")
		if content := currentContent(t, payload); !strings.Contains(content, "<max_thinking_length>10000</max_thinking_length>") {
			t.Fatalf("content=%q", content)
		}
	})
	t.Run("disabled skips tags", func(t *testing.T) {
		root := base()
		root["thinking"] = object{"type": "disabled"}
		payload, opts := convert(t, root, "anthropic")
		if opts.fakeReasoning {
			t.Fatal("fake reasoning enabled despite disabled thinking")
		}
		if content := currentContent(t, payload); strings.HasPrefix(content, "<thinking_mode>") {
			t.Fatalf("tags injected: %q", content)
		}
	})
	t.Run("native effort suppresses tags and thinking addition", func(t *testing.T) {
		root := base()
		root["model"] = "claude-opus-5"
		root["reasoning_effort"] = "high"
		payload, opts := convert(t, root, "anthropic")
		if opts.fakeReasoning {
			t.Fatal("fake reasoning enabled despite native effort")
		}
		content := currentContent(t, payload)
		if strings.HasPrefix(content, "<thinking_mode>") || strings.Contains(content, "# Extended Thinking Mode") {
			t.Fatalf("fake reasoning leaked: %q", content)
		}
		if !strings.Contains(content, "# Output Truncation Handling") {
			t.Fatalf("truncation addition missing: %q", content)
		}
	})
	t.Run("adaptive suppresses tags", func(t *testing.T) {
		root := base()
		root["model"] = "other-model"
		root["thinking"] = object{"type": "adaptive"}
		if _, opts := convert(t, root, "anthropic"); opts.fakeReasoning {
			t.Fatal("fake reasoning enabled despite adaptive thinking")
		}
	})
}

func TestThinkingParserFSM(t *testing.T) {
	t.Run("split tags", func(t *testing.T) {
		p := newThinkingParser()
		th, re := p.feed("<th")
		if th != "" || re != "" {
			t.Fatalf("early output %q %q", th, re)
		}
		th, re = p.feed("inking>reason")
		if th != "" || re != "" {
			t.Fatalf("early output %q %q", th, re)
		}
		th, re = p.feed("</thinking>answer")
		if th != "reason" || re != "answer" {
			t.Fatalf("got %q %q", th, re)
		}
		if th, re = p.feed(" more <thinking>plain"); th != "" || re != " more <thinking>plain" {
			t.Fatalf("post-close tags not plain: %q %q", th, re)
		}
	})
	t.Run("non-start tag is content", func(t *testing.T) {
		p := newThinkingParser()
		_, re := p.feed("hello <thinking>not a block")
		if re != "hello <thinking>not a block" {
			t.Fatalf("regular=%q", re)
		}
	})
	t.Run("cautious buffering keeps split close tag", func(t *testing.T) {
		p := newThinkingParser()
		p.feed("<thinking>")
		th, _ := p.feed(strings.Repeat("x", 30))
		if th == "" {
			t.Fatal("long thinking not streamed")
		}
		th2, re := p.feed("</think")
		th3, re2 := p.feed("ing>done")
		if re != "" || re2 != "done" || strings.Contains(th2+th3, "</") {
			t.Fatalf("close tag mangled: %q %q %q %q", th2, th3, re, re2)
		}
	})
	t.Run("unclosed flushes as thinking", func(t *testing.T) {
		p := newThinkingParser()
		p.feed("<think>abc")
		th, re := p.finalize()
		if th != "abc" || re != "" {
			t.Fatalf("finalize %q %q", th, re)
		}
	})
	t.Run("undecided flushes as regular", func(t *testing.T) {
		p := newThinkingParser()
		p.feed("<th")
		th, re := p.finalize()
		if th != "" || re != "<th" {
			t.Fatalf("finalize %q %q", th, re)
		}
	})
}

func TestFakeReasoningResponseParsing(t *testing.T) {
	wire := joinedFrames(
		frame("assistantResponseEvent", object{"content": "<thinking>sec"}),
		frame("assistantResponseEvent", object{"content": "ret</thinking>"}),
		frame("assistantResponseEvent", object{"content": "answer"}),
		endFrame(),
	)
	t.Run("anthropic nonstream", func(t *testing.T) {
		server := stub(t, wire, nil)
		_, data, err := do(t, server.URL, "anthropic", false)
		if err != nil {
			t.Fatal(err)
		}
		blocks := list(parseResult(t, data)["content"])
		if len(blocks) != 2 || obj(blocks[0])["type"] != "thinking" || obj(blocks[0])["thinking"] != "secret" {
			t.Fatal(blocks)
		}
		if sig := str(obj(blocks[0])["signature"]); !strings.HasPrefix(sig, "sig_") {
			t.Fatalf("placeholder signature missing: %v", blocks[0])
		}
		if obj(blocks[1])["text"] != "answer" {
			t.Fatal(blocks)
		}
	})
	t.Run("anthropic stream", func(t *testing.T) {
		server := stub(t, wire, nil)
		_, data, err := do(t, server.URL, "anthropic", true)
		if err != nil {
			t.Fatal(err)
		}
		raw := string(data)
		if !strings.Contains(raw, "thinking_delta") || !strings.Contains(raw, `"thinking":"secret"`) || !strings.Contains(raw, "sig_") || !strings.Contains(raw, `"text":"answer"`) {
			t.Fatal(raw)
		}
	})
	t.Run("openai nonstream", func(t *testing.T) {
		server := stub(t, wire, nil)
		_, data, err := do(t, server.URL, "openai", false)
		if err != nil {
			t.Fatal(err)
		}
		m := obj(obj(list(parseResult(t, data)["choices"])[0])["message"])
		if m["reasoning_content"] != "secret" || m["content"] != "answer" {
			t.Fatal(m)
		}
	})
	t.Run("openai stream", func(t *testing.T) {
		server := stub(t, wire, nil)
		_, data, err := do(t, server.URL, "openai", true)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(data), `"reasoning_content":"secret"`) {
			t.Fatal(string(data))
		}
	})
	t.Run("suppressed tags still parsed from response", func(t *testing.T) {
		// streaming_core.py:296-299: 响应侧思考解析只受全局开关门控,
		// 本请求未注入标签(native effort)时模型自发的 <thinking> 块
		// 同样被剥离进 thinking 通道。
		server := stub(t, wire, nil)
		root := object{"model": "claude-sonnet-4-6", "reasoning_effort": "high", "max_tokens": 1024, "messages": []any{object{"role": "user", "content": "hello"}}, "tools": toolDefinition("anthropic")}
		req, _ := http.NewRequest("POST", server.URL+"/v1/messages", strings.NewReader(jsonText(root)))
		for k, v := range Headers("native-token", "") {
			req.Header.Set(k, v)
		}
		resp, err := NewTransport(nil).RoundTrip(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		data, _ := io.ReadAll(resp.Body)
		blocks := list(parseResult(t, data)["content"])
		if len(blocks) != 2 || obj(blocks[0])["thinking"] != "secret" || obj(blocks[1])["text"] != "answer" {
			t.Fatal(blocks)
		}
	})
}

func TestToolChoiceDirectives(t *testing.T) {
	root := func(protocol string) object {
		return object{"model": "claude-sonnet-4.6", "messages": []any{object{"role": "user", "content": "hello"}}, "tools": toolDefinition(protocol)}
	}
	payloadTools := func(payload object) []any {
		return list(obj(obj(obj(obj(payload["conversationState"])["currentMessage"])["userInputMessage"])["userInputMessageContext"])["tools"])
	}
	t.Run("openai required", func(t *testing.T) {
		r := root("openai")
		r["tool_choice"] = "required"
		payload, _ := convert(t, r, "openai")
		if !strings.Contains(jsonText(payload), "[Tool Policy] For THIS response you MUST call at least one tool. Do not reply with text only.") {
			t.Fatal(payload)
		}
		if len(payloadTools(payload)) != 1 {
			t.Fatal("tools filtered")
		}
	})
	t.Run("openai required without tools", func(t *testing.T) {
		r := root("openai")
		delete(r, "tools")
		r["tool_choice"] = "required"
		if _, _, err := convertRequest([]byte(jsonText(r)), "openai", ""); err == nil {
			t.Fatal("required without tools accepted")
		}
	})
	t.Run("openai named filters tools", func(t *testing.T) {
		r := root("openai")
		r["tool_choice"] = object{"type": "function", "function": object{"name": "lookup"}}
		payload, _ := convert(t, r, "openai")
		if !strings.Contains(jsonText(payload), "[Tool Policy] For THIS response you MUST call the tool named 'lookup'. Do not call any other tool and do not reply with text only.") {
			t.Fatal(payload)
		}
		tools := payloadTools(payload)
		if len(tools) != 1 || str(obj(obj(tools[0])["toolSpecification"])["name"]) != "lookup" {
			t.Fatal(tools)
		}
	})
	t.Run("openai named unknown", func(t *testing.T) {
		r := root("openai")
		r["tool_choice"] = object{"type": "function", "function": object{"name": "missing"}}
		if _, _, err := convertRequest([]byte(jsonText(r)), "openai", ""); err == nil {
			t.Fatal("unknown named tool accepted")
		}
	})
	t.Run("openai none drops tools", func(t *testing.T) {
		r := root("openai")
		r["tool_choice"] = "none"
		payload, opts := convert(t, r, "openai")
		if !opts.forbidTools {
			t.Fatal("forbidTools not set")
		}
		if len(payloadTools(payload)) != 0 {
			t.Fatal("tools not dropped")
		}
		if !strings.Contains(jsonText(payload), "[Tool Policy] For THIS response you must NOT call any tool. Reply with plain content only.") {
			t.Fatal(payload)
		}
	})
	t.Run("openai invalid string", func(t *testing.T) {
		r := root("openai")
		r["tool_choice"] = "sometimes"
		if _, _, err := convertRequest([]byte(jsonText(r)), "openai", ""); err == nil {
			t.Fatal("invalid tool_choice accepted")
		}
	})
	t.Run("anthropic any maps to required", func(t *testing.T) {
		r := root("anthropic")
		r["tool_choice"] = object{"type": "any"}
		payload, _ := convert(t, r, "anthropic")
		if !strings.Contains(jsonText(payload), "MUST call at least one tool") {
			t.Fatal(payload)
		}
	})
	t.Run("anthropic named", func(t *testing.T) {
		r := root("anthropic")
		r["tool_choice"] = object{"type": "tool", "name": "lookup"}
		payload, _ := convert(t, r, "anthropic")
		if !strings.Contains(jsonText(payload), "MUST call the tool named 'lookup'") || len(payloadTools(payload)) != 1 {
			t.Fatal(payload)
		}
	})
	t.Run("anthropic auto with name", func(t *testing.T) {
		r := root("anthropic")
		r["tool_choice"] = object{"type": "auto", "name": "lookup"}
		if _, _, err := convertRequest([]byte(jsonText(r)), "anthropic", ""); err == nil {
			t.Fatal("auto with name accepted")
		}
	})
	t.Run("anthropic tool without name", func(t *testing.T) {
		r := root("anthropic")
		r["tool_choice"] = object{"type": "tool"}
		if _, _, err := convertRequest([]byte(jsonText(r)), "anthropic", ""); err == nil {
			t.Fatal("tool without name accepted")
		}
	})
	t.Run("anthropic bare string", func(t *testing.T) {
		r := root("anthropic")
		r["tool_choice"] = "auto"
		if _, _, err := convertRequest([]byte(jsonText(r)), "anthropic", ""); err == nil {
			t.Fatal("string tool_choice accepted for anthropic")
		}
	})
}

func TestToolPairingRepair(t *testing.T) {
	t.Run("orphan tool result becomes text", func(t *testing.T) {
		root := object{"model": "claude-sonnet-4.6", "tools": toolDefinition("anthropic"), "messages": []any{
			object{"role": "user", "content": []any{object{"type": "tool_result", "tool_use_id": "lost", "content": "orphan data"}}},
		}}
		payload, _ := convert(t, root, "anthropic")
		raw := jsonText(payload)
		if strings.Contains(raw, `"toolResults"`) || !strings.Contains(raw, "[Tool Result (lost)]") || !strings.Contains(raw, "orphan data") {
			t.Fatal(payload)
		}
	})
	t.Run("missing result synthesized", func(t *testing.T) {
		root := object{"model": "claude-sonnet-4.6", "tools": toolDefinition("anthropic"), "messages": []any{
			object{"role": "assistant", "content": []any{object{"type": "tool_use", "id": "call", "name": "lookup", "input": object{}}}},
			object{"role": "user", "content": "continue"},
		}}
		payload, _ := convert(t, root, "anthropic")
		if !strings.Contains(jsonText(payload), "[gateway: tool result was not delivered by the client") {
			t.Fatal(payload)
		}
	})
	t.Run("non-object input coerced", func(t *testing.T) {
		root := object{"model": "claude-sonnet-4.6", "tools": toolDefinition("anthropic"), "messages": []any{
			object{"role": "assistant", "content": []any{object{"type": "tool_use", "id": "call", "name": "lookup", "input": []any{1}}}},
			object{"role": "user", "content": []any{object{"type": "tool_result", "tool_use_id": "call", "content": "done"}}},
		}}
		payload, _ := convert(t, root, "anthropic")
		if !strings.Contains(jsonText(payload), `"input":{}`) {
			t.Fatal(payload)
		}
	})
	t.Run("anthropic rejects non-user-assistant roles", func(t *testing.T) {
		// models_anthropic.py:229 + extensions/role_widening.py:44-46:
		// 宽化后 role 是 Literal["user","assistant","system"],其余角色
		// (developer/function/tool)参考部署仍 422。
		for _, role := range []string{"developer", "function", "tool"} {
			root := object{"model": "claude-sonnet-4.6", "max_tokens": 1024, "messages": []any{
				object{"role": role, "content": "x"},
				object{"role": "user", "content": "hello"},
			}}
			if _, _, err := convertRequest([]byte(jsonText(root)), "anthropic", ""); err == nil {
				t.Fatalf("role %q accepted", role)
			}
		}
	})
	t.Run("unknown role becomes user", func(t *testing.T) {
		root := object{"model": "claude-sonnet-4.6", "messages": []any{
			object{"role": "function", "content": "old"},
			object{"role": "user", "content": "hello"},
		}}
		payload, _ := convert(t, root, "openai")
		// converters_core.py:1728-1740: 合并先于归一化,function 不与 user
		// 合并;归一为 user 后插合成 assistant,"old" 进 history 而非末条。
		content := currentContent(t, payload)
		if !strings.Contains(content, "hello") || strings.Contains(content, "old") {
			t.Fatalf("content=%q", content)
		}
		if !strings.Contains(jsonText(obj(payload["conversationState"])["history"]), "old") {
			t.Fatalf("history lost old: %v", payload)
		}
	})
	t.Run("orphan judged before merge", func(t *testing.T) {
		// converters_core.py:1723-1728: ensure_assistant_before_tool_results
		// 在 merge 之前——带 results 的 user 前驱是无 results 的 user,按
		// 孤儿文本化,不会被 merge 后的 assistant 相邻关系救回。
		root := object{"model": "claude-sonnet-4.6", "tools": toolDefinition("anthropic"), "messages": []any{
			object{"role": "assistant", "content": []any{object{"type": "tool_use", "id": "call", "name": "lookup", "input": object{}}}},
			object{"role": "user", "content": "mid"},
			object{"role": "user", "content": []any{object{"type": "tool_result", "tool_use_id": "lost", "content": "orphan data"}}},
		}}
		payload, _ := convert(t, root, "anthropic")
		raw := jsonText(payload)
		// 孤儿 results 变文本;assistant 的 toolUse 由 repair 补占位接住。
		if !strings.Contains(raw, "[Tool Result (lost)]") || !strings.Contains(raw, "orphan data") ||
			!strings.Contains(raw, "[gateway: tool result was not delivered by the client") {
			t.Fatal(payload)
		}
	})
	t.Run("strip before merge keeps per-message order", func(t *testing.T) {
		// converters_core.py:1718-1728: strip_all_tool_content 在 merge 之前,
		// 相邻 assistant 各自文本化后再以 "\n" 合并,工具文本穿插在各自
		// 正文后,而不是合并正文之后统一追加。
		root := object{"model": "claude-sonnet-4.6", "messages": []any{
			object{"role": "assistant", "content": "a1", "tool_calls": []any{object{"id": "i1", "type": "function", "function": object{"name": "f", "arguments": "{}"}}}},
			object{"role": "assistant", "content": "a2", "tool_calls": []any{object{"id": "i2", "type": "function", "function": object{"name": "f", "arguments": "{}"}}}},
			object{"role": "user", "content": "go"},
		}}
		payload, _ := convert(t, root, "openai")
		want := "a1\n\n[Tool: f (i1)]\n{}\na2\n\n[Tool: f (i2)]\n{}"
		if !strings.Contains(jsonText(obj(payload["conversationState"])["history"]), jsonText(want)[1:len(jsonText(want))-1]) {
			t.Fatalf("history=%v", obj(payload["conversationState"])["history"])
		}
	})
}

func TestContentTruncationRecovery(t *testing.T) {
	content := "truncated generation alpha-7f3k9 for parity test"
	wire := joinedFrames(frame("assistantResponseEvent", object{"content": content}))
	t.Run("anthropic max_tokens", func(t *testing.T) {
		server := stub(t, wire, nil)
		_, data, err := do(t, server.URL, "anthropic", false)
		if err != nil {
			t.Fatal(err)
		}
		root := parseResult(t, data)
		if root["stop_reason"] != "max_tokens" || root["stop_sequence"] != nil {
			t.Fatal(root)
		}
	})
	t.Run("openai length", func(t *testing.T) {
		server := stub(t, wire, nil)
		_, data, err := do(t, server.URL, "openai", false)
		if err != nil {
			t.Fatal(err)
		}
		choice := obj(list(parseResult(t, data)["choices"])[0])
		if choice["finish_reason"] != "length" {
			t.Fatal(choice)
		}
	})
	t.Run("notice injected once into next request", func(t *testing.T) {
		server := stub(t, wire, nil)
		if _, _, err := do(t, server.URL, "anthropic", false); err != nil {
			t.Fatal(err)
		}
		next := func() object {
			return object{"model": "claude-sonnet-4.6", "messages": []any{
				object{"role": "user", "content": "q"},
				object{"role": "assistant", "content": content},
				object{"role": "user", "content": "again"},
			}}
		}
		payload, _ := convert(t, next(), "anthropic")
		if !strings.Contains(jsonText(payload), "[System Notice] Your previous response was truncated by the API") {
			t.Fatal("truncation notice not injected")
		}
		payload, _ = convert(t, next(), "anthropic")
		if strings.Contains(jsonText(payload), "Your previous response was truncated by the API") {
			t.Fatal("notice injected twice")
		}
	})
}

func TestToolTruncationRecovery(t *testing.T) {
	t.Run("truncated arguments become empty object with notice", func(t *testing.T) {
		wire := joinedFrames(frame("toolUseEvent", object{"name": "lookup", "toolUseId": "trunc-tool-1", "input": `{"city":`, "stop": true}), endFrame())
		server := stub(t, wire, nil)
		// 登记只在 anthropic 流式与 openai 生成器(streaming_anthropic.py:
		// 685-701 / streaming_openai.py:368-394),用流式触发登记。
		_, data, err := do(t, server.URL, "anthropic", true)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(data), `"input":{}`) {
			t.Fatal(data)
		}
		next := func() object {
			return object{"model": "claude-sonnet-4.6", "tools": toolDefinition("anthropic"), "messages": []any{
				object{"role": "assistant", "content": []any{object{"type": "tool_use", "id": "trunc-tool-1", "name": "lookup", "input": object{}}}},
				object{"role": "user", "content": []any{object{"type": "tool_result", "tool_use_id": "trunc-tool-1", "content": "sunny"}}},
			}}
		}
		payload, _ := convert(t, next(), "anthropic")
		raw := jsonText(payload)
		if !strings.Contains(raw, "[API Limitation] Your tool call was truncated") || !strings.Contains(raw, "Original tool result:") || !strings.Contains(raw, "sunny") {
			t.Fatal("tool truncation notice not injected")
		}
		payload, _ = convert(t, next(), "anthropic")
		if strings.Contains(jsonText(payload), "Your tool call was truncated") {
			t.Fatal("notice injected twice")
		}
	})
	t.Run("anthropic nonstream truncation not registered", func(t *testing.T) {
		// format_anthropic_response_from_result(streaming_anthropic.py:
		// 800-816)检测截断改 stop_reason 但不登记,下请求无注入。
		wire := joinedFrames(frame("toolUseEvent", object{"name": "lookup", "toolUseId": "trunc-tool-1n", "input": `{"city":`, "stop": true}), endFrame())
		server := stub(t, wire, nil)
		if _, _, err := do(t, server.URL, "anthropic", false); err != nil {
			t.Fatal(err)
		}
		next := object{"model": "claude-sonnet-4.6", "tools": toolDefinition("anthropic"), "messages": []any{
			object{"role": "assistant", "content": []any{object{"type": "tool_use", "id": "trunc-tool-1n", "name": "lookup", "input": object{}}}},
			object{"role": "user", "content": []any{object{"type": "tool_result", "tool_use_id": "trunc-tool-1n", "content": "sunny"}}},
		}}
		payload, _ := convert(t, next, "anthropic")
		if strings.Contains(jsonText(payload), "Your tool call was truncated") {
			t.Fatal("anthropic nonstream truncation registered")
		}
	})
	t.Run("invalid but not truncated arguments are not recorded", func(t *testing.T) {
		wire := joinedFrames(frame("toolUseEvent", object{"name": "lookup", "toolUseId": "trunc-tool-2", "input": "garbage!", "stop": true}), endFrame())
		server := stub(t, wire, nil)
		_, data, err := do(t, server.URL, "anthropic", false)
		if err != nil {
			t.Fatal(err)
		}
		blocks := list(parseResult(t, data)["content"])
		if len(blocks) != 1 || jsonText(obj(blocks[0])["input"]) != "{}" {
			t.Fatal(blocks)
		}
		next := object{"model": "claude-sonnet-4.6", "tools": toolDefinition("anthropic"), "messages": []any{
			object{"role": "assistant", "content": []any{object{"type": "tool_use", "id": "trunc-tool-2", "name": "lookup", "input": object{}}}},
			object{"role": "user", "content": []any{object{"type": "tool_result", "tool_use_id": "trunc-tool-2", "content": "sunny"}}},
		}}
		payload, _ := convert(t, next, "anthropic")
		if strings.Contains(jsonText(payload), "Your tool call was truncated") {
			t.Fatal("non-truncated tool recorded")
		}
	})
}

func TestBracketToolCallRecovery(t *testing.T) {
	t.Run("anthropic", func(t *testing.T) {
		wire := joinedFrames(
			frame("assistantResponseEvent", object{"content": `Let me check. [Called lookup with args: {"city": "Paris"}] and [CALLED lookup WITH ARGS: {"x": 1}] plus [Called lookup with args: {bad}]`}),
			endFrame(),
		)
		server := stub(t, wire, nil)
		_, data, err := do(t, server.URL, "anthropic", false)
		if err != nil {
			t.Fatal(err)
		}
		blocks := list(parseResult(t, data)["content"])
		if len(blocks) != 3 || obj(blocks[0])["type"] != "text" {
			t.Fatal(blocks)
		}
		if !strings.Contains(str(obj(blocks[0])["text"]), "Let me check.") {
			t.Fatal("text content lost")
		}
		first, second := obj(blocks[1]), obj(blocks[2])
		if str(first["name"]) != "lookup" || !strings.HasPrefix(str(first["id"]), "call_") || str(obj(first["input"])["city"]) != "Paris" {
			t.Fatal(first)
		}
		if number(obj(second["input"])["x"]) != 1 {
			t.Fatal(second)
		}
	})
	t.Run("openai", func(t *testing.T) {
		wire := joinedFrames(
			frame("assistantResponseEvent", object{"content": `[Called lookup with args: {"city": "Paris"}]`}),
			endFrame(),
		)
		server := stub(t, wire, nil)
		_, data, err := do(t, server.URL, "openai", false)
		if err != nil {
			t.Fatal(err)
		}
		message := obj(obj(list(parseResult(t, data)["choices"])[0])["message"])
		calls := list(message["tool_calls"])
		if len(calls) != 1 || str(obj(obj(calls[0])["function"])["name"]) != "lookup" || str(obj(obj(calls[0])["function"])["arguments"]) != `{"city": "Paris"}` {
			t.Fatal(message)
		}
		if !strings.Contains(str(message["content"]), "[Called lookup") {
			t.Fatal("text content lost")
		}
	})
}

func TestCacheUsagePassthrough(t *testing.T) {
	wire := joinedFrames(
		frame("assistantResponseEvent", object{"content": "ok"}),
		frame("usageEvent", object{"usage": object{"inputTokens": 10, "outputTokens": 5, "cacheReadInputTokens": 7, "cache_creation_input_tokens": 3}}),
	)
	t.Run("anthropic nonstream", func(t *testing.T) {
		server := stub(t, wire, nil)
		_, data, err := do(t, server.URL, "anthropic", false)
		if err != nil {
			t.Fatal(err)
		}
		usage := obj(parseResult(t, data)["usage"])
		if number(usage["cache_read_input_tokens"]) != 7 || number(usage["cache_creation_input_tokens"]) != 3 {
			t.Fatal(usage)
		}
	})
	t.Run("anthropic stream", func(t *testing.T) {
		server := stub(t, wire, nil)
		_, data, err := do(t, server.URL, "anthropic", true)
		if err != nil {
			t.Fatal(err)
		}
		var usage object
		for _, e := range events(t, data) {
			if e["type"] == "message_delta" {
				usage = obj(e["usage"])
			}
		}
		if number(usage["cache_read_input_tokens"]) != 7 || number(usage["cache_creation_input_tokens"]) != 3 {
			t.Fatal(usage)
		}
	})
}

func TestUpstreamRetry(t *testing.T) {
	defer func(backoff func(int) time.Duration) { retryBackoff = backoff }(retryBackoff)
	retryBackoff = func(int) time.Duration { return 0 }
	ok := func() *http.Response {
		return &http.Response{StatusCode: 200, Header: http.Header{}, Body: io.NopCloser(bytes.NewReader(joinedFrames(frame("assistantResponseEvent", object{"content": "ok"}), endFrame())))}
	}
	t.Run("500 retried to success with fresh invocation ids and replayed body", func(t *testing.T) {
		attempts := 0
		var ids []string
		var bodies []string
		base := roundTripFunc(func(r *http.Request) (*http.Response, error) {
			attempts++
			ids = append(ids, r.Header.Get("Amz-Sdk-Invocation-Id"))
			b, _ := io.ReadAll(r.Body)
			bodies = append(bodies, string(b))
			if attempts < 3 {
				return &http.Response{StatusCode: 500, Header: http.Header{}, Body: io.NopCloser(strings.NewReader("upstream broken"))}, nil
			}
			return ok(), nil
		})
		tb := testing.TB(t)
		req := clientRequest(&tb, "https://example.invalid", "anthropic", false)
		resp, err := NewTransport(base).RoundTrip(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if attempts != 3 {
			t.Fatalf("attempts=%d", attempts)
		}
		if bodies[0] == "" || bodies[0] != bodies[1] || bodies[1] != bodies[2] {
			t.Fatal("body not replayed identically")
		}
		if ids[0] == ids[1] || ids[1] == ids[2] || ids[0] == ids[2] {
			t.Fatalf("invocation ids not fresh: %v", ids)
		}
	})
	t.Run("400 not retried", func(t *testing.T) {
		attempts := 0
		base := roundTripFunc(func(r *http.Request) (*http.Response, error) {
			attempts++
			return &http.Response{StatusCode: 400, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(`{"message":"bad"}`))}, nil
		})
		tb := testing.TB(t)
		req := clientRequest(&tb, "https://example.invalid", "openai", false)
		resp, err := NewTransport(base).RoundTrip(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if attempts != 1 || resp.StatusCode != 400 {
			t.Fatalf("attempts=%d status=%d", attempts, resp.StatusCode)
		}
	})
}

func TestDeploymentWidening(t *testing.T) {
	t.Run("inline system folds to user", func(t *testing.T) {
		// extensions/role_widening.py:44-46: 内联 system 消息被接受,只贡献
		// 文本(converters_anthropic.py:297-303 门控),经 normalize 折为
		// user 进 history。
		root := object{"model": "claude-sonnet-4.6", "max_tokens": 64, "messages": []any{
			object{"role": "system", "content": "reminder-xyz"},
			object{"role": "user", "content": "hello"},
		}}
		payload, _ := convert(t, root, "anthropic")
		content := currentContent(t, payload)
		if !strings.Contains(content, "hello") || strings.Contains(content, "reminder-xyz") {
			t.Fatalf("content=%q", content)
		}
		if !strings.Contains(jsonText(obj(payload["conversationState"])["history"]), "reminder-xyz") {
			t.Fatalf("history lost reminder: %v", payload)
		}
	})
	t.Run("unknown blocks accepted and ignored", func(t *testing.T) {
		// extensions/server_tool_blocks.py:81-98: UnknownContentBlock 兜底
		// 接受任何带字符串 type 的块,转换层静默忽略。
		root := object{"model": "claude-sonnet-4.6", "max_tokens": 64, "messages": []any{
			object{"role": "user", "content": []any{
				object{"type": "server_tool_use", "id": "s1", "name": "web_search", "input": object{}},
				object{"type": "web_search_tool_result", "tool_use_id": "s1", "content": "results"},
				object{"type": "redacted_thinking", "data": "blob"},
				object{"type": "text", "text": "hi"},
			}},
		}}
		payload, _ := convert(t, root, "anthropic")
		if !strings.Contains(currentContent(t, payload), "hi") {
			t.Fatalf("payload=%v", payload)
		}
		// type 缺失或非字符串的块仍在边界拒绝。
		bad := object{"model": "claude-sonnet-4.6", "max_tokens": 64, "messages": []any{
			object{"role": "user", "content": []any{object{"foo": "bar"}}},
		}}
		if _, _, err := convertRequest([]byte(jsonText(bad)), "anthropic", ""); err == nil {
			t.Fatal("type-less block accepted")
		}
	})
	t.Run("billing header stripped", func(t *testing.T) {
		// extensions/billing_header_strip.py:42-62: 系统提示首行 billing
		// 归属被剥离,其余行保留。
		root := object{"model": "claude-sonnet-4.6", "max_tokens": 64, "system": []any{
			object{"type": "text", "text": "x-anthropic-billing-header: cc_version=2.1.0; cch=abcde;"},
			object{"type": "text", "text": "real system"},
		}, "messages": []any{object{"role": "user", "content": "hi"}}}
		payload, _ := convert(t, root, "anthropic")
		raw := jsonText(payload)
		if strings.Contains(raw, "billing-header") || !strings.Contains(raw, "real system") {
			t.Fatalf("payload=%v", payload)
		}
	})
	t.Run("tool name alias roundtrip", func(t *testing.T) {
		// extensions/tool_name_alias.py:27-60: 超长/非法名换
		// t_<sha256[:12]>_<suffix> 别名上行,响应事件恢复原名
		// (tool_name_alias.py:153-168)。
		long := "mcp__filesystem__read_text_file_with_an_extremely_long_name_beyond_64"
		alias := aliasToolName(long)
		if alias == long || !strings.HasPrefix(alias, "t_") || len(alias) > 64 {
			t.Fatalf("alias=%q", alias)
		}
		root := object{"model": "claude-sonnet-4.6", "max_tokens": 64, "tools": []any{
			object{"name": long, "description": "read", "input_schema": object{}},
		}, "messages": []any{object{"role": "user", "content": "hi"}}}
		payload, _ := convert(t, root, "anthropic")
		ctx := obj(obj(obj(payload["conversationState"])["currentMessage"])["userInputMessage"])["userInputMessageContext"]
		spec := obj(obj(list(obj(ctx)["tools"])[0])["toolSpecification"])
		if str(spec["name"]) != alias {
			t.Fatalf("upstream name=%q want %q", str(spec["name"]), alias)
		}
		wire := joinedFrames(frame("toolUseEvent", object{"name": alias, "toolUseId": "call_1", "input": object{}, "stop": true}), endFrame())
		server := stub(t, wire, nil)
		req, _ := http.NewRequest("POST", server.URL+"/v1/messages", strings.NewReader(jsonText(root)))
		for k, v := range Headers("native-token", "") {
			req.Header.Set(k, v)
		}
		resp, err := NewTransport(nil).RoundTrip(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		data, err := io.ReadAll(resp.Body)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(data), `"name":"`+long+`"`) || strings.Contains(string(data), alias) {
			t.Fatalf("data=%s", data)
		}
	})
}

func TestSmartUnionFallback(t *testing.T) {
	img := func(data string) object {
		return object{"type": "image", "source": object{"type": "base64", "media_type": "image/png", "data": data}}
	}
	t.Run("named tool_choice resolves before aliasing", func(t *testing.T) {
		// converters_anthropic.py:377-385: 策略解析/过滤用客户端原名,
		// 被选中工具在 build_kiro_payload 才别名——长名 named 合法。
		long := "mcp__filesystem__read_text_file_with_an_extremely_long_name_beyond_64"
		root := object{"model": "claude-sonnet-4.6", "max_tokens": 64, "tools": []any{
			object{"name": long, "description": "read", "input_schema": object{}},
		}, "tool_choice": object{"type": "tool", "name": long},
			"messages": []any{object{"role": "user", "content": "hi"}}}
		payload, _ := convert(t, root, "anthropic")
		ctx := obj(obj(obj(payload["conversationState"])["currentMessage"])["userInputMessage"])["userInputMessageContext"]
		spec := obj(obj(list(obj(ctx)["tools"])[0])["toolSpecification"])
		if str(spec["name"]) != aliasToolName(long) {
			t.Fatalf("upstream name=%q", str(spec["name"]))
		}
	})
	t.Run("malformed known blocks fall back", func(t *testing.T) {
		// pydantic smart union: 字段畸形的已知块落 UnknownContentBlock 兜底。
		// thinking 缺字段忽略;image 缺 source 跳过;tool_use 缺 input 以 {}
		// 上行、缺 id/name 丢弃;tool_result 缺 tool_use_id 丢弃
		// (converters_anthropic.py:228-245/151,converters_core.py:322-360)。
		root := object{"model": "claude-sonnet-4.6", "max_tokens": 64, "tools": toolDefinition("anthropic"), "messages": []any{
			object{"role": "user", "content": []any{
				object{"type": "thinking"},
				object{"type": "image"},
				object{"type": "tool_result", "content": "no id"},
				object{"type": "text", "text": "q"},
			}},
			object{"role": "assistant", "content": []any{
				object{"type": "tool_use", "name": "lookup", "input": object{}},
				object{"type": "tool_use", "id": "c1", "name": "lookup"},
			}},
			object{"role": "user", "content": []any{object{"type": "tool_result", "tool_use_id": "c1", "content": "ok"}}},
		}}
		payload, _ := convert(t, root, "anthropic")
		raw := jsonText(payload)
		if !strings.Contains(raw, `"toolUses"`) || !strings.Contains(raw, `"input":{}`) {
			t.Fatalf("payload=%v", payload)
		}
		// text 缺 text:参考实现转换层 AttributeError 整请求 500,Go 保持 400。
		bad := object{"model": "claude-sonnet-4.6", "max_tokens": 64, "messages": []any{
			object{"role": "user", "content": []any{object{"type": "text"}}},
		}}
		if _, _, err := convertRequest([]byte(jsonText(bad)), "anthropic", ""); err == nil {
			t.Fatal("text-less text block accepted")
		}
	})
	t.Run("image_url data url extracted", func(t *testing.T) {
		// converters_core.py:322-353: image_url 块 data-URL 图片提取上行。
		root := object{"model": "claude-sonnet-4.6", "max_tokens": 64, "messages": []any{
			object{"role": "user", "content": []any{
				object{"type": "image_url", "image_url": object{"url": "data:image/png;base64,aGVsbG8="}},
			}},
		}}
		payload, _ := convert(t, root, "anthropic")
		images := list(obj(obj(obj(payload["conversationState"])["currentMessage"])["userInputMessage"])["images"])
		if len(images) != 1 || obj(images[0])["format"] != "png" {
			t.Fatalf("images=%v", images)
		}
	})
	t.Run("merge drops merged-away images", func(t *testing.T) {
		// converters_core.py:1220-1249: merge 不并 images,被并消息图片丢弃。
		root := object{"model": "claude-sonnet-4.6", "max_tokens": 64, "messages": []any{
			object{"role": "user", "content": []any{object{"type": "text", "text": "one"}, img("aGVsbG8=")}},
			object{"role": "user", "content": []any{object{"type": "text", "text": "two"}, img("d29ybGQ=")}},
		}}
		payload, _ := convert(t, root, "anthropic")
		images := list(obj(obj(obj(payload["conversationState"])["currentMessage"])["userInputMessage"])["images"])
		if len(images) != 1 || obj(obj(images[0])["source"])["bytes"] != "aGVsbG8=" {
			t.Fatalf("images=%v", images)
		}
	})
}
func TestRound15Fixes(t *testing.T) {
	t.Run("non-string truthy tool ids preserved", func(t *testing.T) {
		// converters_anthropic.py:245/151: tool_use id/name 与 tool_result
		// tool_use_id 只验真值,int 原样上行并参与 repair 配对
		// (converters_core.py:1413-1420 原值集合)。
		root := object{"model": "claude-sonnet-4.6", "max_tokens": 64, "tools": toolDefinition("anthropic"), "messages": []any{
			object{"role": "user", "content": "q"},
			object{"role": "assistant", "content": []any{
				object{"type": "tool_use", "id": json.Number("5"), "name": "lookup", "input": object{}},
			}},
			object{"role": "user", "content": []any{
				object{"type": "tool_result", "tool_use_id": json.Number("5"), "content": "ok"},
			}},
		}}
		payload, _ := convert(t, root, "anthropic")
		raw := jsonText(payload)
		if !strings.Contains(raw, `"toolUseId":5`) {
			t.Fatalf("int id not preserved: %v", raw)
		}
		if strings.Contains(raw, "not delivered by the client") {
			t.Fatalf("int ids failed to pair: %v", raw)
		}
	})
	t.Run("tool_result content lenient", func(t *testing.T) {
		// smart union 落 Unknown 后 converters_anthropic.py:151-156:
		// 标量 str()、dict repr、裸字符串拼接、未知块跳过、假值收空。
		mk := func(content any) object {
			return object{"model": "claude-sonnet-4.6", "max_tokens": 64, "tools": toolDefinition("anthropic"), "messages": []any{
				object{"role": "user", "content": "q"},
				object{"role": "assistant", "content": []any{
					object{"type": "tool_use", "id": "c1", "name": "lookup", "input": object{}},
				}},
				object{"role": "user", "content": []any{
					object{"type": "tool_result", "tool_use_id": "c1", "content": content},
				}},
			}}
		}
		cases := []struct {
			name    string
			content any
			want    string
		}{
			{"scalar truthy", json.Number("5"), `"text":"5"`},
			{"scalar falsy", json.Number("0"), `(empty result)`},
			{"dict", object{"foo": json.Number("1")}, `"text":"{'foo': 1}"`},
			{"bare strings and unknown", []any{"raw", object{"type": "text", "text": "x"}, object{"type": "video"}}, `"text":"rawx"`},
			{"text key harvested", []any{object{"text": "note"}}, `"text":"note"`},
		}
		for _, c := range cases {
			payload, _ := convert(t, mk(c.content), "anthropic")
			if !strings.Contains(jsonText(payload), c.want) {
				t.Fatalf("%s: %v", c.name, jsonText(payload))
			}
		}
	})
	t.Run("output_config effort type checked", func(t *testing.T) {
		// models_anthropic.py:349: effort Optional[str],非字符串 422。
		bad := object{"model": "claude-sonnet-4.6", "max_tokens": 64, "output_config": object{"effort": json.Number("5")}, "messages": []any{
			object{"role": "user", "content": "hi"}}}
		if _, _, err := convertRequest([]byte(jsonText(bad)), "anthropic", ""); err == nil {
			t.Fatal("non-string effort accepted")
		}
	})
	t.Run("image data url edge cases", func(t *testing.T) {
		img := func(media string, data any) object {
			src := object{"type": "base64", "data": data}
			if media != "" {
				src["media_type"] = media
			}
			return object{"type": "image", "source": src}
		}
		first := func(payload object) object {
			return obj(list(obj(obj(obj(payload["conversationState"])["currentMessage"])["userInputMessage"])["images"])[0])
		}
		mk := func(block object) object {
			return object{"model": "claude-sonnet-4.6", "max_tokens": 64, "messages": []any{
				object{"role": "user", "content": []any{block}}}}
		}
		// converters_core.py:767-770: 无逗号 data-URL 仅 warning,原始 data 上行。
		p1, _ := convert(t, mk(img("image/png", "data:xyz")), "anthropic")
		if got := obj(obj(first(p1))["source"])["bytes"]; got != "data:xyz" {
			t.Fatalf("no-comma data=%v", got)
		}
		// 多参数 header 取 ";" 首段(converters_core.py:763)。
		p2, _ := convert(t, mk(img("", "data:image/jpeg;base64;charset=utf-8,QUJD")), "anthropic")
		if got := first(p2)["format"]; got != "jpeg" {
			t.Fatalf("multi-param format=%v", got)
		}
		// format 取 media_type "/" 末段(converters_core.py:773)。
		p3, _ := convert(t, mk(img("application/pdf", "QUJD")), "anthropic")
		if got := first(p3)["format"]; got != "pdf" {
			t.Fatalf("non-image format=%v", got)
		}
	})
	t.Run("named violation reports disallowed", func(t *testing.T) {
		// converters_core.py:144-145 + streaming_core.py:161-162: named
		// 白名单只含被点名工具,调用其他已声明工具报 disallowed。
		s := &responseState{options: requestOptions{
			policyMode: "named", policyTool: "lookup",
			allowedTools: map[string]bool{"lookup": true, "other": true},
		}}
		s.tool = &pendingTool{id: "1", name: "other"}
		s.tool.args.WriteString("{}")
		err := s.finishTool()
		v, ok := err.(*toolViolation)
		if !ok || v.msg != "response returned disallowed tool 'other'" {
			t.Fatalf("err=%v", err)
		}
	})
	t.Run("boundary field validation", func(t *testing.T) {
		// models_anthropic.py:398-399 / models_openai.py:163/177: 字段不
		// 上行但类型错误 422。
		badAnth := func(mutate func(object)) object {
			r := object{"model": "claude-sonnet-4.6", "max_tokens": 64, "messages": []any{
				object{"role": "user", "content": "hi"}}}
			mutate(r)
			return r
		}
		badOpen := func(mutate func(object)) object {
			r := object{"model": "claude-sonnet-4.6", "messages": []any{
				object{"role": "user", "content": "hi"}}}
			mutate(r)
			return r
		}
		rejects := []struct {
			name     string
			root     object
			protocol string
		}{
			{"stop_sequences item", badAnth(func(r object) { r["stop_sequences"] = []any{"a", json.Number("5")} }), "anthropic"},
			{"stop_sequences type", badAnth(func(r object) { r["stop_sequences"] = "abc" }), "anthropic"},
			{"metadata type", badAnth(func(r object) { r["metadata"] = "x" }), "anthropic"},
			{"stop type", badOpen(func(r object) { r["stop"] = json.Number("5") }), "openai"},
			{"stop item", badOpen(func(r object) { r["stop"] = []any{"a", json.Number("5")} }), "openai"},
			{"logit_bias type", badOpen(func(r object) { r["logit_bias"] = "x" }), "openai"},
			{"logit_bias value", badOpen(func(r object) { r["logit_bias"] = object{"1": "x"} }), "openai"},
		}
		for _, c := range rejects {
			if _, _, err := convertRequest([]byte(jsonText(c.root)), c.protocol, ""); err == nil {
				t.Fatalf("%s accepted", c.name)
			}
		}
		// pydantic lax: 数值字符串 logit_bias 合法。
		ok := badOpen(func(r object) { r["logit_bias"] = object{"1": "1.5"} })
		if _, _, err := convertRequest([]byte(jsonText(ok)), "openai", ""); err != nil {
			t.Fatalf("numeric-string logit_bias rejected: %v", err)
		}
	})
}
func TestRound16Fixes(t *testing.T) {
	t.Run("empty tool id gets toolu fallback", func(t *testing.T) {
		// streaming_anthropic.py:366/785: 空 toolUseId 回退 toolu_<24hex>;
		// 键缺省仍由 parsers.py:396 生成 call_+8hex。
		wire := joinedFrames(frame("toolUseEvent", object{"name": "lookup", "toolUseId": "", "input": object{}, "stop": true}), endFrame())
		for _, stream := range []bool{false, true} {
			server := stub(t, wire, nil)
			_, data, err := do(t, server.URL, "anthropic", stream)
			if err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(string(data), `"id":"toolu_`) {
				t.Fatalf("stream=%v: %s", stream, data)
			}
		}
	})
	t.Run("truncation heuristic naive counts", func(t *testing.T) {
		// parsers.py:522-576: 计数不看字符串上下文,端不匹配/计数不平/
		// 引号奇数均判截断。
		cases := map[string]bool{
			`{"a":1}}`:      true,
			`{"a":"}"}`:     true,
			`{"a":1}`:       false,
			`{"a":`:         true,
			`[1,2`:          true,
			`{"a":"b\"}`:    true,
			`{"a":"b\""}`:   false,
			``:              false,
			`{"a":"hello"}`: false,
		}
		for in, want := range cases {
			if got := looksTruncatedJSON(in); got != want {
				t.Fatalf("looksTruncatedJSON(%q)=%v want %v", in, got, want)
			}
		}
	})
	t.Run("bracket parser guard and brace search", func(t *testing.T) {
		// parsers.py:111 守卫区分大小写;:117 正则 IGNORECASE;:122
		// find('{') 跳过 args: 后任意文本。
		if n := len(parseBracketToolCalls(`[called foo with args: {}]`)); n != 0 {
			t.Fatalf("lowercase-only guard bypass: %d", n)
		}
		if n := len(parseBracketToolCalls(`[Called foo with args: junk {}]`)); n != 1 {
			t.Fatalf("find('{') skip: %d", n)
		}
		calls := parseBracketToolCalls(`[Called a with args: {}] then [called b with args: {}]`)
		if len(calls) != 2 {
			t.Fatalf("case-insensitive matching: %d", len(calls))
		}
	})
	t.Run("openai tool type non-string rejected", func(t *testing.T) {
		// models_openai.py:117: type 为 str,pydantic lax 拒数字与 null。
		for _, tv := range []any{json.Number("5"), nil} {
			root := object{"model": "claude-sonnet-4.6", "tools": []any{
				object{"type": tv, "function": object{"name": "x"}},
			}, "messages": []any{object{"role": "user", "content": "hi"}}}
			if _, _, err := convertRequest([]byte(jsonText(root)), "openai", ""); err == nil {
				t.Fatalf("type=%v accepted", tv)
			}
		}
	})
	t.Run("count_tokens skips generation validation", func(t *testing.T) {
		// models_anthropic.py:404-425: count_tokens 模型只有
		// model/messages/system/tools,生成参数 extra 忽略。
		root := object{"model": "claude-sonnet-4.6", "temperature": json.Number("5"), "stream": "maybe",
			"thinking": "x", "tool_choice": object{"type": "bogus"},
			"stop_sequences": json.Number("3"), "metadata": json.Number("4"),
			"messages": []any{object{"role": "user", "content": "hi"}}}
		if _, _, err := convertRequest([]byte(jsonText(root)), "count_tokens", ""); err != nil {
			t.Fatalf("count_tokens rejected: %v", err)
		}
		bad := object{"model": "claude-sonnet-4.6", "messages": "nope"}
		if _, _, err := convertRequest([]byte(jsonText(bad)), "count_tokens", ""); err == nil {
			t.Fatal("count_tokens accepted malformed messages")
		}
	})
	t.Run("nonstream thinking merged with sig", func(t *testing.T) {
		// streaming_anthropic.py:761-766: 非流式 thinking 合并单块置首,
		// 无签名帧生成 sig_ 占位;text 同样合并单块。
		wire := joinedFrames(
			frame("reasoningContentEvent", object{"text": "a"}),
			frame("assistantResponseEvent", object{"content": "x"}),
			frame("reasoningContentEvent", object{"text": "b"}),
			frame("assistantResponseEvent", object{"content": "y"}),
			endFrame())
		server := stub(t, wire, nil)
		_, data, err := do(t, server.URL, "anthropic", false)
		if err != nil {
			t.Fatal(err)
		}
		m := parseResult(t, data)
		content := list(m["content"])
		if len(content) != 2 {
			t.Fatalf("blocks=%v", content)
		}
		tb := obj(content[0])
		if str(tb["type"]) != "thinking" || str(tb["thinking"]) != "ab" || !strings.HasPrefix(str(tb["signature"]), "sig_") {
			t.Fatalf("thinking block=%v", tb)
		}
		if str(obj(content[1])["text"]) != "xy" {
			t.Fatalf("text block=%v", content[1])
		}
	})
}
func TestRound17Fixes(t *testing.T) {
	t.Run("null contextUsage not terminal", func(t *testing.T) {
		// streaming_core.py:494-496 / streaming_anthropic.py:543 /
		// streaming_openai.py:274: 三条消费路径都要求非 None。
		s := &responseState{options: requestOptions{protocol: "anthropic"}}
		if err := s.accept(wireEvent{kind: "contextUsageEvent", data: object{"contextUsagePercentage": nil}}); err != nil {
			t.Fatal(err)
		}
		if s.terminal {
			t.Fatal("explicit null contextUsage marked terminal")
		}
		if err := s.accept(wireEvent{kind: "contextUsageEvent", data: object{"contextUsagePercentage": json.Number("50")}}); err != nil {
			t.Fatal(err)
		}
		if !s.terminal {
			t.Fatal("numeric contextUsage not terminal")
		}
	})
	t.Run("truncation registration gated", func(t *testing.T) {
		// 登记只在 anthropic 流式与 openai 生成器(含非流式复用);
		// anthropic 非流式与严格路径不登记(streaming_anthropic.py:685 /
		// streaming_openai.py:368 / format_*_from_result)。
		cases := []struct {
			opts requestOptions
			want bool
		}{
			{requestOptions{protocol: "anthropic", stream: true}, true},
			{requestOptions{protocol: "anthropic"}, false},
			{requestOptions{protocol: "openai"}, true},
			{requestOptions{protocol: "openai", stream: true}, true},
			{requestOptions{protocol: "openai", policyMode: "required"}, false},
			{requestOptions{protocol: "anthropic", stream: true, policyMode: "named"}, false},
		}
		for i, c := range cases {
			s := &responseState{options: c.opts}
			if got := s.registersTruncation(); got != c.want {
				t.Fatalf("case %d: got %v want %v", i, got, c.want)
			}
		}
	})
	t.Run("openai strict pct0 prompt zero", func(t *testing.T) {
		// format_openai_response_from_result(streaming_openai.py:502-512):
		// pct=0 经 streaming_core.py:528 pct>0 守卫落 unknown,
		// prompt=0、total=completion,不回退请求估算。
		s := &responseState{options: requestOptions{protocol: "openai", policyMode: "required"}}
		s.hasContextPct = true
		s.contextPct = 0
		s.inputTokens = 100
		s.outputRunes = 40
		u := s.openAIUsage()
		if u["prompt_tokens"] != 0 || u["total_tokens"] != u["completion_tokens"] {
			t.Fatalf("usage=%v", u)
		}
		// 非严格路径 pct=0 仍回退估算(streaming_openai.py:322)。
		s2 := &responseState{options: requestOptions{protocol: "openai"}}
		s2.hasContextPct = true
		s2.contextPct = 0
		s2.inputTokens = 100
		s2.outputRunes = 40
		if u2 := s2.openAIUsage(); u2["prompt_tokens"] != 100 {
			t.Fatalf("non-strict usage=%v", u2)
		}
	})
}

func TestRound18Fixes(t *testing.T) {
	t.Run("anthropic stream defers tool blocks and dedups native", func(t *testing.T) {
		// streaming_core.py:362-368: 原生工具事件只在全部字节消费完后
		// 统一交出;get_tool_calls(parsers.py:589-591)无条件去重。
		// 同 id 同参数的两帧收敛为一个块,且工具块在全部正文之后发出。
		wire := joinedFrames(
			frame("assistantResponseEvent", object{"content": "before"}),
			frame("toolUseEvent", object{"name": "lookup", "toolUseId": "t1", "input": `{"x": 1}`, "stop": true}),
			frame("toolUseEvent", object{"name": "lookup", "toolUseId": "t1", "input": `{"x": 1}`, "stop": true}),
			frame("assistantResponseEvent", object{"content": "after"}),
			endFrame())
		server := stub(t, wire, nil)
		_, data, err := do(t, server.URL, "anthropic", true)
		if err != nil {
			t.Fatal(err)
		}
		body := string(data)
		if n := strings.Count(body, `"type":"tool_use"`); n != 1 {
			t.Fatalf("tool blocks=%d: %s", n, body)
		}
		if strings.Index(body, "after") > strings.Index(body, `"type":"tool_use"`) {
			t.Fatalf("tool block emitted before trailing text: %s", body)
		}
	})
	t.Run("anthropic stream bracket tools not deduped", func(t *testing.T) {
		// streaming_anthropic.py:570-612: 流式括号恢复块追加在原生工具
		// 之后,不再去重。
		wire := joinedFrames(
			frame("assistantResponseEvent", object{"content": `[Called lookup with args: {"x": 1}] [Called lookup with args: {"x": 1}]`}),
			endFrame())
		server := stub(t, wire, nil)
		_, data, err := do(t, server.URL, "anthropic", true)
		if err != nil {
			t.Fatal(err)
		}
		if n := strings.Count(string(data), `"type":"tool_use"`); n != 2 {
			t.Fatalf("bracket blocks=%d: %s", n, data)
		}
	})
	t.Run("budget tokens beat effort none", func(t *testing.T) {
		// converters_anthropic.py:423-430: budget 检查在 effort 之前,
		// 命中预算即返回,effort="none" 不再禁用思考。
		root := object{"model": "claude-sonnet-4.6", "max_tokens": json.Number("100"),
			"thinking":      object{"type": "enabled", "budget_tokens": json.Number("5000")},
			"output_config": object{"effort": "none"},
			"messages":      []any{object{"role": "user", "content": "hi"}}}
		payload, _ := convert(t, root, "anthropic")
		if content := currentContent(t, payload); !strings.Contains(content, "<max_thinking_length>5000</max_thinking_length>") {
			t.Fatalf("budget lost to effort=none: %s", content)
		}
		root2 := object{"model": "claude-sonnet-4.6", "max_tokens": json.Number("100"),
			"output_config": object{"effort": "none"},
			"messages":      []any{object{"role": "user", "content": "hi"}}}
		payload2, _ := convert(t, root2, "anthropic")
		if content := currentContent(t, payload2); strings.HasPrefix(content, "<thinking_mode>") {
			t.Fatalf("effort=none did not disable: %s", content)
		}
	})
	t.Run("anthropic scalar system rejected", func(t *testing.T) {
		// models_anthropic.py:343: SystemPrompt=Union[str,List[Block],
		// List[Dict]]——标量与列表内非对象项 422。
		for _, sys := range []any{json.Number("5"), true, []any{"x"}, []any{json.Number("1")}} {
			root := object{"model": "claude-sonnet-4.6", "max_tokens": json.Number("100"), "system": sys,
				"messages": []any{object{"role": "user", "content": "hi"}}}
			if _, _, err := convertRequest([]byte(jsonText(root)), "anthropic", ""); err == nil {
				t.Fatalf("system=%v accepted", sys)
			}
		}
	})
	t.Run("openai tool_call_id and name non-string rejected", func(t *testing.T) {
		// models_openai.py:80-82: tool_call_id/name 为 Optional[str],
		// pydantic lax 拒数字;缺省与 null 合法。
		badID := object{"model": "gpt-5.5", "messages": []any{
			object{"role": "assistant", "content": "x", "tool_calls": []any{object{"id": "c1", "type": "function", "function": object{"name": "lookup", "arguments": "{}"}}}},
			object{"role": "tool", "tool_call_id": json.Number("5"), "content": "r"}}}
		if _, _, err := convertRequest([]byte(jsonText(badID)), "openai", ""); err == nil {
			t.Fatal("numeric tool_call_id accepted")
		}
		badName := object{"model": "gpt-5.5", "messages": []any{object{"role": "user", "content": "hi", "name": json.Number("5")}}}
		if _, _, err := convertRequest([]byte(jsonText(badName)), "openai", ""); err == nil {
			t.Fatal("numeric name accepted")
		}
		nullName := object{"model": "gpt-5.5", "messages": []any{object{"role": "user", "content": "hi", "name": nil}}}
		if _, _, err := convertRequest([]byte(jsonText(nullName)), "openai", ""); err != nil {
			t.Fatalf("null name rejected: %v", err)
		}
	})
	t.Run("nonstream signature last wins", func(t *testing.T) {
		// streaming_core.py:487-488: 多个签名帧覆盖赋值,合并后取最后者。
		wire := joinedFrames(
			frame("reasoningContentEvent", object{"text": "a"}),
			frame("reasoningContentEvent", object{"signature": "sig-one"}),
			frame("reasoningContentEvent", object{"text": "b"}),
			frame("reasoningContentEvent", object{"signature": "sig-two"}),
			endFrame())
		server := stub(t, wire, nil)
		_, data, err := do(t, server.URL, "anthropic", false)
		if err != nil {
			t.Fatal(err)
		}
		blocks := list(parseResult(t, data)["content"])
		if len(blocks) != 1 || str(obj(blocks[0])["thinking"]) != "ab" || str(obj(blocks[0])["signature"]) != "sig-two" {
			t.Fatalf("blocks=%v", blocks)
		}
	})
}
