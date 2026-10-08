package kiro

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestRound27KiroHeaderTimeout(t *testing.T) {
	oldTimeout, oldBackoff := streamStallTimeout, retryBackoff
	streamStallTimeout = 20 * time.Millisecond
	retryBackoff = func(int) time.Duration { return 0 }
	t.Cleanup(func() { streamStallTimeout, retryBackoff = oldTimeout, oldBackoff })
	for _, stream := range []bool{false, true} {
		t.Run(map[bool]string{false: "collect", true: "stream"}[stream], func(t *testing.T) {
			var calls atomic.Int32
			release := make(chan struct{})
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				_, _ = io.Copy(io.Discard, r.Body)
				if calls.Add(1) < 3 {
					select {
					case <-r.Context().Done():
					case <-release:
					}
					return
				}
				w.WriteHeader(200)
				w.(http.Flusher).Flush()
				time.Sleep(40 * time.Millisecond)
				_, _ = w.Write(joinedFrames(frame("assistantResponseEvent", object{"content": "after headers"}), endFrame()))
			}))
			defer server.Close()
			defer close(release)
			base := http.DefaultTransport.(*http.Transport).Clone()
			defer base.CloseIdleConnections()
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			request := clientRequest(tbOf(t), server.URL, "openai", stream).WithContext(ctx)
			response, err := NewTransport(base).RoundTrip(request)
			if err != nil {
				t.Fatalf("header retry failed: calls=%d err=%v", calls.Load(), err)
			}
			data, err := io.ReadAll(response.Body)
			response.Body.Close()
			if err != nil || calls.Load() != 3 || !strings.Contains(string(data), "after headers") {
				t.Fatalf("calls=%d err=%v body=%s", calls.Load(), err, data)
			}
			if base.ResponseHeaderTimeout != 0 {
				t.Fatal("caller transport was changed")
			}
		})
	}
}

func TestRound27KiroHeaderTransportIsolation(t *testing.T) {
	for _, configured := range []time.Duration{0, time.Millisecond, time.Hour} {
		base := http.DefaultTransport.(*http.Transport).Clone()
		base.ResponseHeaderTimeout = configured
		adapted := NewTransport(base).(*transport)
		native, ok := adapted.kiroBase.(*http.Transport)
		want := configured
		if want == 0 || want > streamStallTimeout {
			want = streamStallTimeout
		}
		if !ok || native == base || adapted.base != base || native.ResponseHeaderTimeout != want || base.ResponseHeaderTimeout != configured {
			t.Fatalf("configured=%s native transport isolation failed", configured)
		}
		base.CloseIdleConnections()
		native.CloseIdleConnections()
	}
}

func TestCloseBeforeReadingAndUnexpectedCompression(t *testing.T) {
	for _, encoding := range []string{"", "gzip"} {
		body := &trackedBody{Reader: bytes.NewReader(joinedFrames(frame("assistantResponseEvent", object{"content": "ok"}), endFrame()))}
		base := roundTripFunc(func(*http.Request) (*http.Response, error) {
			return &http.Response{StatusCode: 200, Header: http.Header{"Content-Encoding": []string{encoding}, "Content-Length": []string{"12345"}}, ContentLength: 12345, Body: body}, nil
		})
		tb := testing.TB(t)
		req := clientRequest(&tb, "https://example.invalid", "openai", true)
		resp, err := NewTransport(base).RoundTrip(req)
		if encoding == "gzip" {
			if err == nil {
				t.Fatal("unexpected compression accepted")
			}
		} else {
			if err != nil {
				t.Fatal(err)
			}
			if resp.Header.Get("Content-Length") != "" || resp.ContentLength != -1 {
				t.Fatal("stale length")
			}
			resp.Body.Close()
			resp.Body.Close()
			if _, err := resp.Body.Read(make([]byte, 1)); err != io.ErrClosedPipe {
				t.Fatalf("read after close: %v", err)
			}
		}
		if body.closes.Load() != 1 {
			t.Fatalf("body closed %d times", body.closes.Load())
		}
	}
}

func TestToolsOnlyResponseAndExplicitStopSequence(t *testing.T) {
	server := stub(t, joinedFrames(frame("toolUseEvent", object{"name": "lookup", "toolUseId": "only", "input": object{}, "stop": true}), endFrame()), nil)
	for _, protocol := range []string{"anthropic", "openai"} {
		_, data, err := do(t, server.URL, protocol, false)
		if err != nil {
			t.Fatal(err)
		}
		root := parseResult(t, data)
		if protocol == "openai" {
			choice := obj(list(root["choices"])[0])
			message := obj(choice["message"])
			if choice["finish_reason"] != "tool_calls" || message["content"] != "" || len(list(message["tool_calls"])) != 1 {
				t.Fatal(root)
			}
		} else if root["stop_reason"] != "tool_use" || len(list(root["content"])) != 1 {
			t.Fatal(root)
		}
	}
	stop := stub(t, joinedFrames(frame("assistantResponseEvent", object{"content": "answer"}), frame("messageStopEvent", object{"stopReason": "stop_sequence", "stopSequence": "END"}), frame("usageEvent", object{"contextUsagePercentage": 42})), nil)
	_, data, err := do(t, stop.URL, "anthropic", false)
	if err != nil {
		t.Fatal(err)
	}
	root := parseResult(t, data)
	if root["stop_reason"] != "stop_sequence" || root["stop_sequence"] != "END" {
		t.Fatal(root)
	}
	invalid := stub(t, joinedFrames(frame("assistantResponseEvent", object{"content": "not a tool"}), frame("messageStopEvent", object{"stopReason": "tool_use"}), frame("usageEvent", object{"contextUsagePercentage": 42})), nil)
	if _, _, err := do(t, invalid.URL, "anthropic", false); err == nil {
		t.Fatal("tool stop reason without tool call accepted")
	}
}

func TestStopReasonCaseNormalization(t *testing.T) {
	cases := []struct {
		reason, anthropic, openai string
		tool                      bool
	}{
		{"END_TURN", "end_turn", "stop", false},
		{"STOP", "end_turn", "stop", false},
		{"STOP_SEQUENCE", "stop_sequence", "stop", false},
		{"MAX_TOKENS", "max_tokens", "length", false},
		{"maxTokens", "max_tokens", "length", false},
		{"LENGTH", "max_tokens", "length", false},
		{"CONTENT_FILTER", "refusal", "content_filter", false},
		{"content_filtered", "refusal", "content_filter", false},
		{"REFUSAL", "refusal", "content_filter", false},
		{"TOOL_USE", "tool_use", "tool_calls", true},
		{"toolUse", "tool_use", "tool_calls", true},
		{"TOOL_CALLS", "tool_use", "tool_calls", true},
	}
	for _, tc := range cases {
		for _, protocol := range []string{"anthropic", "openai"} {
			for _, stream := range []bool{false, true} {
				mode := "JSON"
				if stream {
					mode = "SSE"
				}
				t.Run(tc.reason+"/"+protocol+"/"+mode, func(t *testing.T) {
					content := frame("assistantResponseEvent", object{"content": "answer"})
					if tc.tool {
						content = frame("toolUseEvent", object{"name": "lookup", "toolUseId": "only", "input": object{}, "stop": true})
					}
					server := stub(t, joinedFrames(content, frame("messageStopEvent", object{"stopReason": tc.reason, "stopSequence": "END"}), frame("usageEvent", object{"contextUsagePercentage": 42})), nil)
					_, data, err := do(t, server.URL, protocol, stream)
					if err != nil {
						t.Fatal(err)
					}
					var reason string
					var sequence any
					if stream {
						for _, event := range events(t, data) {
							if event["type"] == "error" {
								t.Fatal(event)
							}
							if event["type"] == "message_delta" {
								delta := obj(event["delta"])
								reason, sequence = str(delta["stop_reason"]), delta["stop_sequence"]
							}
							if choices := list(event["choices"]); len(choices) > 0 {
								if value := str(obj(choices[0])["finish_reason"]); value != "" {
									reason = value
								}
							}
						}
					} else {
						root := parseResult(t, data)
						if protocol == "anthropic" {
							reason, sequence = str(root["stop_reason"]), root["stop_sequence"]
						} else {
							reason = str(obj(list(root["choices"])[0])["finish_reason"])
						}
					}
					want := tc.openai
					if protocol == "anthropic" {
						want = tc.anthropic
						if want == "stop_sequence" && sequence != "END" {
							t.Fatalf("stop sequence = %v", sequence)
						}
					}
					if reason != want {
						t.Fatalf("stop reason = %q, want %q", reason, want)
					}
				})
			}
		}
	}
}

func TestRegionAndEndpoints(t *testing.T) {
	tests := []struct{ credential, arn, region, chat string }{
		{"", "", "us-east-1", "https://q.us-east-1.amazonaws.com"},
		{"eu-west-1", "", "eu-west-1", "https://q.eu-west-1.amazonaws.com"},
		{"eu-west-1", "arn:aws:codewhisperer:ap-northeast-1:1:profile/p", "ap-northeast-1", "https://runtime.ap-northeast-1.kiro.dev"},
		{"evil/path", "arn:aws:codewhisperer:evil/path:1:profile/p", "us-east-1", DefaultBaseURL},
	}
	for _, tt := range tests {
		if got := Region(tt.credential, tt.arn); got != tt.region {
			t.Errorf("region=%s", got)
		}
		chat, control := Endpoints(tt.credential, tt.arn)
		if chat != tt.chat || control != "https://q."+tt.region+".amazonaws.com" {
			t.Errorf("endpoints=%s %s", chat, control)
		}
	}
	if ProviderID != "kiro.global.subscribe.standard" || HeaderProvider != "X-Msu-Upstream-Provider" || HeaderProfileARN != "X-Msu-Kiro-Profile-Arn" {
		t.Fatal("public constants changed")
	}
	h := Headers("token", "profile")
	if h["Authorization"] != "Bearer token" || h[HeaderProvider] != ProviderID || h[HeaderProfileARN] != "profile" || h["X-Amz-User-Agent"] == "" {
		t.Fatal(h)
	}
}

func TestNativeEffortSchemas(t *testing.T) {
	tests := []struct {
		name, model  string
		fields       object
		path, effort string
		thinking     bool
		fake         string
	}{
		{"opus exact", "claude-opus-5", object{"output_config": object{"effort": "xhigh"}}, "output_config", "xhigh", false, ""},
		{"sonnet clamp", "claude-sonnet-4-6", object{"reasoning_effort": "xhigh"}, "output_config", "high", false, ""},
		{"opus max", "claude-opus-4.8", object{"reasoning_effort": "max"}, "output_config", "max", false, ""},
		{"gpt none", "gpt-5.6-sol", object{"thinking": object{"type": "disabled"}}, "reasoning", "none", false, ""},
		{"claude none omitted", "claude-sonnet-4.6", object{"thinking": object{"type": "disabled"}}, "", "", false, ""},
		// anthropic 侧无 minimal 别名(config.py:526 只属 openai),未知档走
		// EFFORT_FALLBACK。
		{"anthropic minimal fallback", "gpt-5.5", object{"reasoning_effort": "minimal"}, "reasoning", "medium", false, ""},
		{"unknown tier fallback", "gpt-5.6-terra", object{"reasoning_effort": "ultra"}, "reasoning", "medium", false, ""},
		{"unknown model omitted", "other-model", object{"reasoning_effort": "max", "thinking": object{"type": "adaptive"}}, "", "", false, ""},
		{"native adaptive", "claude-sonnet-5", object{"thinking": object{"type": "adaptive"}, "output_config": object{"effort": "max"}}, "output_config", "max", true, ""},
		// Budget without effort invents no native field; Python injects a fake-reasoning budget tag instead.
		{"no invented budget", "claude-sonnet-5", object{"thinking": object{"type": "enabled", "budget_tokens": 5000}}, "", "", false, "<max_thinking_length>5000</max_thinking_length>"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			root := object{"model": tt.model, "messages": []any{object{"role": "user", "content": "hello"}}}
			for k, v := range tt.fields {
				root[k] = v
			}
			payload, _, err := convertRequest([]byte(jsonText(root)), "anthropic", "")
			if err != nil {
				t.Fatal(err)
			}
			fields := obj(payload["additionalModelRequestFields"])
			if tt.path == "" {
				if len(fields) != 0 {
					t.Fatal(fields)
				}
			} else if obj(fields[tt.path])["effort"] != tt.effort {
				t.Fatal(fields)
			}
			if tt.thinking && obj(fields["thinking"])["type"] != "adaptive" {
				t.Fatal(fields)
			}
			content := str(obj(obj(obj(payload["conversationState"])["currentMessage"])["userInputMessage"])["content"])
			if tt.fake == "" {
				if strings.HasPrefix(content, "<thinking_mode>") {
					t.Fatalf("fake reasoning injected: %q", content)
				}
			} else if !strings.HasPrefix(content, "<thinking_mode>enabled</thinking_mode>\n"+tt.fake+"\n<thinking_instruction>") {
				t.Fatalf("fake reasoning missing: %q", content)
			}
		})
	}
}

func TestRequestRejectionsBeforeNetwork(t *testing.T) {
	root := func() object {
		return object{"model": "claude-sonnet-4.6", "messages": []any{object{"role": "user", "content": "hello"}}, "tools": toolDefinition("anthropic")}
	}
	cases := map[string]func(object){
		"remote image": func(r object) {
			r["messages"] = []any{object{"role": "user", "content": []any{object{"type": "image", "source": object{"type": "url", "url": "http://127.0.0.1/secrets"}}}}}
		},
		"invalid base64": func(r object) {
			r["messages"] = []any{object{"role": "user", "content": []any{object{"type": "image", "source": object{"type": "base64", "media_type": "image/png", "data": "not-base64!"}}}}}
		},
		"remote openai image": func(r object) {
			r["messages"] = []any{object{"role": "user", "content": []any{object{"type": "image_url", "image_url": object{"url": "https://example.invalid/image"}}}}}
		},
		"malformed messages":   func(r object) { r["messages"] = object{"role": "user"} },
		"malformed tools":      func(r object) { r["tools"] = object{"name": "tool"} },
		"no model":             func(r object) { delete(r, "model") },
		"nonboolean stream":    func(r object) { r["stream"] = "yes" },
		"too many completions": func(r object) { r["n"] = 2 },
		"overlong tool name":   func(r object) { r["tools"] = []any{object{"name": strings.Repeat("x", 65)}} },
		"unknown content block": func(r object) {
			r["messages"] = []any{object{"role": "user", "content": []any{object{"type": "document"}}}}
		},
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			r := root()
			mutate(r)
			req, _ := http.NewRequest("POST", "https://example.invalid/v1/messages", strings.NewReader(jsonText(r)))
			for k, v := range Headers("token", "") {
				req.Header.Set(k, v)
			}
			calls := 0
			base := roundTripFunc(func(*http.Request) (*http.Response, error) { calls++; return nil, nil })
			if _, err := NewTransport(base).RoundTrip(req); err == nil || calls != 0 {
				t.Fatalf("err=%v calls=%d", err, calls)
			}
		})
	}
	for _, raw := range []string{"[]", "null", "{} {}", "{bad"} {
		if _, _, err := convertRequest([]byte(raw), "anthropic", ""); err == nil {
			t.Fatalf("bad JSON accepted: %s", raw)
		}
	}
}

func TestPayloadAndRequestSizeLimits(t *testing.T) {
	for _, size := range []int{maxPayloadBytes + 1, maxRequestBytes + 1} {
		root := object{"model": "claude-sonnet-4.6", "messages": []any{object{"role": "user", "content": strings.Repeat("x", size)}}}
		req, _ := http.NewRequest("POST", "https://example.invalid/v1/messages", strings.NewReader(jsonText(root)))
		for k, v := range Headers("token", "") {
			req.Header.Set(k, v)
		}
		calls := 0
		base := roundTripFunc(func(*http.Request) (*http.Response, error) { calls++; return nil, nil })
		if _, err := NewTransport(base).RoundTrip(req); err == nil || calls != 0 {
			t.Fatalf("size %d err=%v calls=%d", size, err, calls)
		}
	}
}

func TestOversizedHistoryTrimmedToLimit(t *testing.T) {
	big := strings.Repeat("x", 100000)
	messages := []any{object{"role": "user", "content": "start"}}
	for i := 1; i <= 8; i++ {
		id := "call_" + string(rune('0'+i))
		messages = append(messages, object{"role": "assistant", "tool_calls": []any{object{
			"id": id, "type": "function",
			"function": object{"name": "lookup", "arguments": `{"x": 1}`},
		}}})
		messages = append(messages, object{"role": "tool", "tool_call_id": id, "content": big})
	}
	messages = append(messages, object{"role": "user", "content": "final question"})
	payload, _, err := convertRequest([]byte(jsonText(object{
		"model": "claude-sonnet-4.6", "messages": messages, "tools": toolDefinition("openai"),
	})), "openai", "")
	if err != nil {
		t.Fatal(err)
	}
	if got := payloadSize(payload); got > maxPayloadBytes {
		t.Fatalf("payload still %d bytes after trim", got)
	}
	history := list(obj(payload["conversationState"])["history"])
	if len(history) < 2 {
		t.Fatalf("history trimmed below the two entry floor: %d", len(history))
	}
	first := obj(obj(history[0])["userInputMessage"])
	if first == nil {
		t.Fatalf("history does not start with a user message: %v", obj(history[0]))
	}
	// The assistant owning the first surviving user's results was trimmed away,
	// so they are orphaned: dropped from context, text kept behind the marker.
	if context := obj(first["userInputMessageContext"]); context != nil && list(context["toolResults"]) != nil {
		t.Fatalf("orphaned tool results survived trim: %v", context)
	}
	if !strings.Contains(str(first["content"]), "\n[trimmed tool result] ") {
		t.Fatalf("orphaned text not preserved: %.80q", str(first["content"]))
	}
	current := obj(obj(obj(payload["conversationState"])["currentMessage"])["userInputMessage"])
	if !strings.HasSuffix(str(current["content"]), "final question") {
		t.Fatalf("current message damaged: %.80q", str(current["content"]))
	}
}

func TestSmallPayloadNotTrimmedOrRepaired(t *testing.T) {
	payload, _, err := convertRequest([]byte(jsonText(object{
		"model": "claude-sonnet-4.6",
		"messages": []any{
			object{"role": "user", "content": "a"},
			object{"role": "assistant", "tool_calls": []any{object{
				"id": "call_1", "type": "function",
				"function": object{"name": "lookup", "arguments": "{}"},
			}}},
			// Unknown id on purpose: under the limit the reference leaves such
			// results for the upstream to judge, so they must survive untouched.
			object{"role": "tool", "tool_call_id": "call_9", "content": "unrelated"},
			object{"role": "user", "content": "b"},
		},
		"tools": toolDefinition("openai"),
	})), "openai", "")
	if err != nil {
		t.Fatal(err)
	}
	users := []object{obj(obj(obj(payload["conversationState"])["currentMessage"])["userInputMessage"])}
	for _, entry := range list(obj(payload["conversationState"])["history"]) {
		users = append(users, obj(obj(entry)["userInputMessage"]))
	}
	found := false
	for _, user := range users {
		for _, result := range list(obj(user["userInputMessageContext"])["toolResults"]) {
			if str(obj(result)["toolUseId"]) == "call_9" {
				found = true
			}
		}
	}
	if !found {
		t.Fatal("under-limit payload was trimmed or repaired")
	}
}

func TestToolResultImagesAndHistoricalNormalization(t *testing.T) {
	image := object{"type": "image", "source": object{"type": "base64", "media_type": "image/png", "data": "aGVsbG8="}}
	root := object{"model": "claude-sonnet-4.6", "system": []any{object{"type": "text", "text": "system"}}, "tools": toolDefinition("anthropic"), "messages": []any{
		object{"role": "assistant", "content": []any{object{"type": "tool_use", "id": "call", "name": "lookup", "input": object{}}}},
		object{"role": "user", "content": []any{object{"type": "tool_result", "tool_use_id": "call", "content": []any{image, object{"type": "text", "text": "image result"}}}}},
	}}
	payload, _, err := convertRequest([]byte(jsonText(root)), "anthropic", "")
	if err != nil {
		t.Fatal(err)
	}
	state := obj(payload["conversationState"])
	history := list(state["history"])
	if len(history) != 2 || obj(obj(history[0])["userInputMessage"])["content"] != "system"+thinkingSystemAddition+truncationSystemAddition+"\n\n\u200b" {
		t.Fatal(history)
	}
	current := obj(obj(state["currentMessage"])["userInputMessage"])
	if len(list(current["images"])) != 1 || len(list(obj(current["userInputMessageContext"])["toolResults"])) != 1 {
		t.Fatal(current)
	}
	// Removing definitions must preserve history as text, not invalid native tools.
	delete(root, "tools")
	payload, _, err = convertRequest([]byte(jsonText(root)), "anthropic", "")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(jsonText(payload), `"toolUses"`) || strings.Contains(jsonText(payload), `"toolResults"`) || !strings.Contains(jsonText(payload), "[Tool: ") {
		t.Fatal(payload)
	}
	// Trailing assistant text goes to history; current message remains a user.
	root = object{"model": "gpt-5.5", "messages": []any{object{"role": "user", "content": "question"}, object{"role": "assistant", "content": "prefix"}}}
	payload, _, err = convertRequest([]byte(jsonText(root)), "openai", "")
	if err != nil {
		t.Fatal(err)
	}
	state = obj(payload["conversationState"])
	// converters_core.py:1815-1819: 末条是 assistant 时占位 user 不注入 thinking tags。
	if len(list(state["history"])) != 2 || obj(obj(state["currentMessage"])["userInputMessage"])["content"] != "\u200b" {
		t.Fatal(state)
	}
}

func TestSchemaPropertyNamesAreNotKeywords(t *testing.T) {
	schema := object{"type": "object", "additionalProperties": false, "required": []any{}, "properties": object{
		"additionalProperties": object{"type": "string"}, "required": object{"type": "array", "items": object{"type": "object", "additionalProperties": true, "required": []any{}}},
	}, "anyOf": []any{object{"type": "object", "additionalProperties": true, "required": []any{}}}}
	before := jsonText(schema)
	clean := obj(sanitizeSchema(schema))
	props := obj(clean["properties"])
	if obj(props["additionalProperties"])["type"] != "string" || obj(props["required"])["type"] != "array" || jsonText(schema) != before {
		t.Fatal("sanitizer lost property names or mutated input")
	}
	if _, ok := clean["additionalProperties"]; ok {
		t.Fatal(clean)
	}
	if _, ok := obj(obj(props["required"])["items"])["required"]; ok {
		t.Fatal(clean)
	}
	for _, namespace := range []string{"$defs", "definitions", "patternProperties"} {
		schema := object{namespace: object{"additionalProperties": object{"type": "string", "additionalProperties": false}, "required": object{"type": "array", "required": []any{}}}}
		clean := obj(sanitizeSchema(schema))
		members := obj(clean[namespace])
		if obj(members["additionalProperties"])["type"] != "string" || obj(members["required"])["type"] != "array" {
			t.Fatalf("%s member names mistaken for schema keywords: %v", namespace, clean)
		}
		if _, ok := obj(members["additionalProperties"])["additionalProperties"]; ok {
			t.Fatalf("%s nested unsupported keyword preserved: %v", namespace, clean)
		}
		if _, ok := obj(members["required"])["required"]; ok {
			t.Fatalf("%s nested empty required preserved: %v", namespace, clean)
		}
	}
}

func TestRealtimeTextWhileSingleToolBuffered(t *testing.T) {
	release := make(chan struct{})
	defer close(release)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write(frame("toolUseEvent", object{"name": "lookup", "toolUseId": "pending", "input": "{\"x\":"}))
		w.Write(frame("assistantResponseEvent", object{"content": "during tool"}))
		w.(http.Flusher).Flush()
		select {
		case <-release:
		case <-r.Context().Done():
			return
		}
	}))
	defer server.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	tb := testing.TB(t)
	req := clientRequest(&tb, server.URL, "anthropic", true).WithContext(ctx)
	resp, err := NewTransport(nil).RoundTrip(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	result := make(chan error, 1)
	go func() {
		reader := bufio.NewReader(resp.Body)
		for {
			line, err := reader.ReadString('\n')
			if err != nil {
				result <- err
				return
			}
			if strings.Contains(line, "during tool") {
				result <- nil
				return
			}
		}
	}()
	select {
	case err := <-result:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("tool buffering delayed body text")
	}
	cancel()
}

func TestToolFragmentAssemblyAndDuplicates(t *testing.T) {
	args := `{"x":1,"nested":{"quoted":"a\\b\"{c}","unicode":"中文"}}`
	wire := frame("toolUseEvent", object{"name": "lookup", "toolUseId": "id", "input": object{}})
	for _, r := range args {
		wire = append(wire, frame("toolUseEvent", object{"input": string(r)})...)
	}
	wire = append(wire, frame("toolUseEvent", object{"stop": true})...)
	input, err := decodeObject(args)
	if err != nil {
		t.Fatal(err)
	}
	wire = append(wire, frame("toolUseEvent", object{"name": "lookup", "toolUseId": "id", "input": input, "stop": true})...)
	wire = append(wire, endFrame()...)
	server := stub(t, wire, nil)
	_, data, err := do(t, server.URL, "anthropic", false)
	if err != nil {
		t.Fatal(err)
	}
	blocks := list(parseResult(t, data)["content"])
	// parsers.py:589-591: get_tool_calls 无条件去重,anthropic 非流式的
	// 同 id 重复帧同样收敛为一个 tool_use 块(十八轮更正十轮矩阵)。
	if len(blocks) != 1 || jsonText(obj(blocks[0])["input"]) != jsonText(input) {
		t.Fatal(blocks)
	}
	conflicting := joinedFrames(frame("toolUseEvent", object{"name": "lookup", "toolUseId": "id", "input": object{"x": 1}, "stop": true}), frame("toolUseEvent", object{"name": "lookup", "toolUseId": "id", "input": object{"x": 2}, "stop": true}), endFrame())
	other := stub(t, conflicting, nil)
	// openai 非流式复用流式生成器,恒去重(streaming_openai.py:284);
	// parsers.py:174-189: 同 id 冲突保留先发出者(等长不替换)。
	_, data, err = do(t, other.URL, "openai", false)
	if err != nil {
		t.Fatal(err)
	}
	calls := list(obj(obj(list(parseResult(t, data)["choices"])[0])["message"])["tool_calls"])
	if len(calls) != 1 || str(obj(obj(calls[0])["function"])["arguments"]) != `{"x": 1}` {
		t.Fatalf("calls=%v", calls)
	}
}

func TestTerminalMarkerDoesNotMaskTrailingFrameFailure(t *testing.T) {
	prefix := joinedFrames(frame("assistantResponseEvent", object{"content": "ok"}), endFrame())
	full := frame("assistantResponseEvent", object{"content": "tail"})
	cases := [][]byte{exceptionFrame(), full[:12], full[:15], frame("internalServerException", object{"message": "failed"}), frame("messageStopEvent", object{"stopReason": "unexpected_error"})}
	for _, tail := range cases {
		for _, stream := range []bool{false, true} {
			server := stub(t, joinedFrames(prefix, tail), nil)
			_, data, err := do(t, server.URL, "openai", stream)
			if err == nil {
				t.Fatalf("trailing error masked: %s", data)
			}
			// streaming_openai.py:431-441: 流式中段错误补 [DONE] 后中断属
			// 参考实现的预期形状;非流式不得出现成功收尾标记。
			if !stream && strings.Contains(string(data), "[DONE]") {
				t.Fatalf("trailing error masked: %s", data)
			}
		}
	}
}

func TestNoReplayBodyIsConsumedWithoutChangingRequestFields(t *testing.T) {
	raw := `{"model":"gpt-5.5","profileArn":"client-profile","messages":[{"role":"user","content":"hello"}]}`
	req, _ := http.NewRequest("POST", "https://example.invalid/v1/chat/completions", nil)
	input := &trackedBody{Reader: strings.NewReader(raw)}
	req.Body = input
	req.ContentLength = int64(len(raw))
	req.Header.Set(HeaderProvider, ProviderID)
	req.Header.Set(HeaderProfileARN, "trusted-profile")
	req.Header.Set("Authorization", "Bearer token")
	base := roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if r == req || r.Body == req.Body {
			t.Fatal("request not cloned")
		}
		b, _ := io.ReadAll(r.Body)
		payload := parseResult(t, b)
		if payload["profileArn"] != "trusted-profile" {
			t.Fatal(payload)
		}
		return &http.Response{StatusCode: 200, Header: http.Header{}, Body: io.NopCloser(bytes.NewReader(joinedFrames(frame("assistantResponseEvent", object{"content": "ok"}), endFrame())))}, nil
	})
	resp, err := NewTransport(base).RoundTrip(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if req.Body != input || input.closes.Load() != 1 || req.URL.Path != "/v1/chat/completions" || req.Header.Get(HeaderProfileARN) != "trusted-profile" || req.ContentLength != int64(len(raw)) {
		t.Fatal("original request fields changed")
	}
}

func TestRound27RequestSurrogateContainers(t *testing.T) {
	for _, tc := range []struct {
		raw, want string
	}{
		{`{"x":"\ud800"}`, `{'x': '\ud800'}`},
		{`{"x":"\uDC00"}`, `{'x': '\udc00'}`},
		{`{"x":"\ud83d\ude00"}`, `{'x': '😀'}`},
		{`{"x":"\\ud800"}`, `{'x': '\\ud800'}`},
		{`{"x":"�"}`, `{'x': '�'}`},
		{`{"\ud800":1,"\ud801":2}`, `{'\ud800': 1, '\ud801': 2}`},
		{`{"\ud800":1,"\uD800":2}`, `{'\ud800': 2}`},
		{`{"x":["\ud800",{"y":"\udc00"}]}`, `{'x': ['\ud800', {'y': '\udc00'}]}`},
	} {
		for _, protocol := range []string{"openai", "anthropic"} {
			content := tc.raw
			if protocol == "anthropic" {
				content = `[{"type":"tool_result","tool_use_id":"i","content":` + content + `}]`
			}
			raw := `{"model":"auto","max_tokens":10,"messages":[{"role":"user","content":` + content + `}]}`
			payload, _, err := convertRequest([]byte(raw), protocol, "")
			if err != nil {
				t.Fatal(err)
			}
			if got := currentContent(t, payload); !strings.HasSuffix(got, tc.want) {
				t.Errorf("protocol=%s raw=%s want suffix=%q", protocol, tc.raw, tc.want)
			}
		}
	}
}

func TestRound27RequestNestedSurrogateInput(t *testing.T) {
	for _, input := range []string{
		`"{\"x\":\"\ud800\"}"`,
		`"{\"x\":\"\\ud800\"}"`,
	} {
		raw := `{"model":"auto","max_tokens":1,"messages":[{"role":"assistant","content":[{"type":"tool_use","id":"i","name":"f","input":` + input + `}]},{"role":"user","content":"continue"}]}`
		payload, _, err := convertRequest([]byte(raw), "anthropic", "")
		if err != nil {
			t.Fatal(err)
		}
		history := list(obj(payload["conversationState"])["history"])
		found := false
		for _, entry := range history {
			if content := str(obj(obj(entry)["assistantResponseMessage"])["content"]); content == `[Tool: f (i)]`+"\n"+`{'x': '\ud800'}` {
				found = true
			}
		}
		if !found {
			t.Errorf("nested input lost surrogate identity: %s", input)
		}
	}
}

func TestRound27JSONRoundTrip(t *testing.T) {
	for _, raw := range []string{
		`{"x":"\ud800","\ud801":"\udc00","nested":[true,false,null,{},[]]}`,
		`{"x":"\ud83d\ude00","y":"\\ud800","z":"�"}`,
		`{"n":[1e0,1.00,-0,-0.0,9007199254740993],"x":"\"\\\b\f\n\r\t/中"}`,
		`{"x":"\uD800\u0041\uDC00","same":"\ud800","same":"\ud801"}`,
	} {
		value, err := DecodeJSON([]byte(raw))
		if err != nil {
			t.Fatal(err)
		}
		encoded, err := EncodeJSON(value)
		if err != nil || !json.Valid(encoded) {
			t.Fatalf("invalid reencoded JSON: %v", err)
		}
		decoded, err := DecodeJSON(encoded)
		if err != nil || pyRepr(decoded) != pyRepr(value) {
			t.Errorf("round trip lost identity raw=%s err=%v", raw, err)
		}
	}
	for _, value := range []any{nil, object(nil), []any(nil)} {
		encoded, err := EncodeJSON(value)
		if err != nil || string(encoded) != "null" {
			t.Errorf("nil encode=%s err=%v", encoded, err)
		}
	}
	for _, value := range []any{make(chan int), json.Number("NaN"), object{"x": make(chan int)}} {
		if _, err := EncodeJSON(value); err == nil {
			t.Error("accepted non-JSON value")
		}
	}
	for _, raw := range []string{`null`, `[]`, `{"x":"\ud800"} {}`, `{"x":"\ud800",}`, `{"x":NaN}`} {
		if _, err := DecodeJSON([]byte(raw)); err == nil {
			t.Errorf("accepted invalid object: %s", raw)
		}
	}
}
