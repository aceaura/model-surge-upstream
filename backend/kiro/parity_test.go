package kiro

import (
	"bytes"
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
		_, data, err := do(t, server.URL, "anthropic", false)
		if err != nil {
			t.Fatal(err)
		}
		blocks := list(parseResult(t, data)["content"])
		if len(blocks) != 1 || jsonText(obj(blocks[0])["input"]) != "{}" {
			t.Fatal(blocks)
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
