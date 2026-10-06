package kiro

import (
	"bufio"
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

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
			if choice["finish_reason"] != "tool_calls" || message["content"] != nil || len(list(message["tool_calls"])) != 1 {
				t.Fatal(root)
			}
		} else if root["stop_reason"] != "tool_use" || len(list(root["content"])) != 1 {
			t.Fatal(root)
		}
	}
	stop := stub(t, joinedFrames(frame("assistantResponseEvent", object{"content": "answer"}), frame("messageStopEvent", object{"stopReason": "stop_sequence", "stopSequence": "END"})), nil)
	_, data, err := do(t, stop.URL, "anthropic", false)
	if err != nil {
		t.Fatal(err)
	}
	root := parseResult(t, data)
	if root["stop_reason"] != "stop_sequence" || root["stop_sequence"] != "END" {
		t.Fatal(root)
	}
	invalid := stub(t, joinedFrames(frame("assistantResponseEvent", object{"content": "not a tool"}), frame("messageStopEvent", object{"stopReason": "tool_use"})), nil)
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
					server := stub(t, joinedFrames(content, frame("messageStopEvent", object{"stopReason": tc.reason, "stopSequence": "END"})), nil)
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
	if ProviderID != "kiro" || HeaderProvider != "X-Msu-Upstream-Provider" || HeaderProfileARN != "X-Msu-Kiro-Profile-Arn" {
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
		{"opus max", "claude-opus-4.8", object{"reasoning": object{"effort": "max"}}, "output_config", "max", false, ""},
		{"gpt none", "gpt-5.6-sol", object{"thinking": object{"type": "disabled"}}, "reasoning", "none", false, ""},
		{"claude none omitted", "claude-sonnet-4.6", object{"thinking": object{"type": "disabled"}}, "", "", false, ""},
		{"minimal alias", "gpt-5.5", object{"reasoning_effort": "minimal"}, "reasoning", "low", false, ""},
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
	if len(history) != 2 || obj(obj(history[0])["userInputMessage"])["content"] != "system"+thinkingSystemAddition+truncationSystemAddition+"\n\n(empty placeholder)" {
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
	if strings.Contains(jsonText(payload), `"toolUses"`) || strings.Contains(jsonText(payload), `"toolResults"`) || !strings.Contains(jsonText(payload), "Previous tool call") {
		t.Fatal(payload)
	}
	// Trailing assistant text goes to history; current message remains a user.
	root = object{"model": "gpt-5.5", "messages": []any{object{"role": "user", "content": "question"}, object{"role": "assistant", "content": "prefix"}}}
	payload, _, err = convertRequest([]byte(jsonText(root)), "openai", "")
	if err != nil {
		t.Fatal(err)
	}
	state = obj(payload["conversationState"])
	if len(list(state["history"])) != 2 || obj(obj(state["currentMessage"])["userInputMessage"])["content"] != thinkingTagsPrefix(thinkingConfig{})+"(empty placeholder)" {
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
	if len(blocks) != 1 || jsonText(obj(blocks[0])["input"]) != jsonText(input) {
		t.Fatal(blocks)
	}
	conflicting := joinedFrames(frame("toolUseEvent", object{"name": "lookup", "toolUseId": "id", "input": object{"x": 1}, "stop": true}), frame("toolUseEvent", object{"name": "lookup", "toolUseId": "id", "input": object{"x": 2}, "stop": true}), endFrame())
	other := stub(t, conflicting, nil)
	if _, _, err := do(t, other.URL, "openai", false); err == nil {
		t.Fatal("conflicting duplicate tool accepted")
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
			if err == nil || strings.Contains(string(data), "[DONE]") {
				t.Fatalf("trailing error masked: %s %v", data, err)
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
