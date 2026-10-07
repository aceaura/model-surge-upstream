package proxyplane

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"hash/crc32"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"github.com/aceaura/model-surge-upstream/backend/chat"
	"github.com/aceaura/model-surge-upstream/backend/kiro"
	"github.com/aceaura/model-surge-upstream/backend/modelcheck"
	"github.com/aceaura/model-surge-upstream/backend/provider"
	"github.com/aceaura/model-surge-upstream/backend/resolve"
)

func kiroTestFrame(event, payload string) []byte {
	headers := []byte{}
	for _, pair := range [][2]string{{":message-type", "event"}, {":event-type", event}, {":content-type", "application/json"}} {
		headers = append(headers, byte(len(pair[0])))
		headers = append(headers, pair[0]...)
		headers = append(headers, 7, byte(len(pair[1])>>8), byte(len(pair[1])))
		headers = append(headers, pair[1]...)
	}
	frame := make([]byte, 12)
	binary.BigEndian.PutUint32(frame[0:4], uint32(16+len(headers)+len(payload)))
	binary.BigEndian.PutUint32(frame[4:8], uint32(len(headers)))
	binary.BigEndian.PutUint32(frame[8:12], crc32.ChecksumIEEE(frame[:8]))
	frame = append(frame, headers...)
	frame = append(frame, payload...)
	crc := make([]byte, 4)
	binary.BigEndian.PutUint32(crc, crc32.ChecksumIEEE(frame))
	return append(frame, crc...)
}

type round29ByteBody struct{ io.ReadCloser }

func (b round29ByteBody) Read(p []byte) (int, error) {
	if len(p) > 1 {
		p = p[:1]
	}
	return b.ReadCloser.Read(p)
}

type round29RoundTripFunc func(*http.Request) (*http.Response, error)

func (f round29RoundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestRound30KiroRequestBoundaryHTTP(t *testing.T) {
	for _, tc := range []struct {
		name       string
		protocol   string
		patch      map[string]any
		valid      bool
		resultText string
	}{
		{"union strict", provider.ProtocolAnthropic, map[string]any{"messages": json.RawMessage(`[{"role":"user","content":[{"tool_use_id":"t","is_error":"true","id":"s","name":"f"}]}]`)}, true, ""},
		{"union native", provider.ProtocolAnthropic, map[string]any{"messages": json.RawMessage(`[{"role":"user","content":[{"tool_use_id":"t","is_error":true,"id":"s","name":"f"}]}]`)}, true, "(empty result)"},
		{"modeled text", provider.ProtocolAnthropic, map[string]any{"messages": json.RawMessage(`[{"role":"user","content":[{"type":"tool_result","tool_use_id":"t","content":[{"type":"tool_reference","tool_name":"f","text":"nested preserved"}]}]}]`)}, true, "nested preserved"},
		{"raw text", provider.ProtocolChatCompletions, map[string]any{"messages": json.RawMessage(`[{"role":"user","content":[{"type":"tool_result","tool_use_id":"t","content":[{"type":"tool_reference","tool_name":"f","text":"nested hidden"}]}]}]`)}, true, "(empty result)"},
		{"hex anthropic", provider.ProtocolAnthropic, map[string]any{"temperature": "0x1p-1"}, false, ""},
		{"hex openai", provider.ProtocolChatCompletions, map[string]any{"temperature": "0x1p-1"}, false, ""},
		{"underscores anthropic", provider.ProtocolAnthropic, map[string]any{"temperature": "1_.0"}, true, ""},
		{"underscores openai", provider.ProtocolChatCompletions, map[string]any{"temperature": "1_.0"}, true, ""},
		{"float min anthropic", provider.ProtocolAnthropic, map[string]any{"max_tokens": json.Number("-9223372036854775808.0")}, false, ""},
		{"float min openai", provider.ProtocolChatCompletions, map[string]any{"max_tokens": json.Number("-9223372036854775808.0")}, false, ""},
		{"integer min", provider.ProtocolAnthropic, map[string]any{"max_tokens": json.Number("-9223372036854775808")}, true, ""},
	} {
		for _, source := range []string{"client", "defaults", "overrides"} {
			for _, stream := range []bool{false, true} {
				t.Run(fmt.Sprintf("%s/%s/stream=%v", tc.name, source, stream), func(t *testing.T) {
					calls := 0
					up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
						calls++
						var payload struct {
							ConversationState struct {
								CurrentMessage struct{ UserInputMessage struct{ Content string } }
							}
						}
						if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
							t.Error(err)
						}
						content := payload.ConversationState.CurrentMessage.UserInputMessage.Content
						if tc.resultText == "" && strings.Contains(content, "[Tool Result (t)]") || tc.resultText != "" && !strings.HasSuffix(content, "[Tool Result (t)]\n"+tc.resultText) {
							t.Errorf("native content=%q want result=%q", content, tc.resultText)
						}
						if strings.Contains(content, "nested hidden") {
							t.Error("raw tool reference text escaped")
						}
						w.Header().Set("Content-Type", "application/vnd.amazon.eventstream")
						_, _ = w.Write(kiroTestFrame("assistantResponseEvent", `{"content":"boundary reply"}`))
						_, _ = w.Write(kiroTestFrame("metadataEvent", `{"contextUsagePercentage":1}`))
					}))
					defer up.Close()
					target := resolve.ResolvedTarget{ModelID: "kiro-r30", ProviderID: kiro.ProviderID, Protocol: tc.protocol, BaseURL: up.URL, NativeModel: "claude-sonnet-4.5", Headers: kiro.Headers("test-access", "")}
					body := map[string]any{"model": target.ModelID, "stream": stream}
					if source != "defaults" {
						body["messages"] = []any{map[string]any{"role": "user", "content": "ping"}}
						body["max_tokens"] = 1
					}
					part, _ := json.Marshal(tc.patch)
					switch source {
					case "client":
						for key, value := range tc.patch {
							body[key] = value
						}
					case "defaults":
						defaults := map[string]any{"messages": []any{map[string]any{"role": "user", "content": "ping"}}, "max_tokens": 1}
						for key, value := range tc.patch {
							defaults[key] = value
						}
						target.Defaults, _ = json.Marshal(defaults)
					case "overrides":
						target.Overrides = part
					}
					h := NewHandler(testKey, fakeResolver{targets: map[string]resolve.ResolvedTarget{target.ModelID: target}}, nil)
					base := http.DefaultTransport.(*http.Transport).Clone()
					defer base.CloseIdleConnections()
					h.client.Transport = kiro.NewTransport(round29RoundTripFunc(base.RoundTrip))
					proxy := httptest.NewServer(h)
					defer proxy.Close()
					path := "/v1/messages"
					if tc.protocol == provider.ProtocolChatCompletions {
						path = "/v1/chat/completions"
					}
					data, _ := json.Marshal(body)
					r, err := http.NewRequest("POST", proxy.URL+path, strings.NewReader(string(data)))
					if err != nil {
						t.Fatal(err)
					}
					r.Header.Set("Authorization", "Bearer "+testKey)
					resp, err := proxy.Client().Do(r)
					if err != nil {
						t.Fatal(err)
					}
					data, err = io.ReadAll(resp.Body)
					resp.Body.Close()
					wantStatus, wantCalls := 400, 0
					if tc.valid {
						wantStatus, wantCalls = 200, 1
					}
					if err != nil || resp.StatusCode != wantStatus || calls != wantCalls || tc.valid && !strings.Contains(string(data), "boundary reply") {
						t.Fatalf("status=%d calls=%d err=%v body=%s", resp.StatusCode, calls, err, data)
					}
				})
			}
		}
	}
}

func TestRound29KiroUnicodeOutputHTTP(t *testing.T) {
	const text, thought, toolID = "A中😀Bé界", "想法😀é", "调用_中😀"
	wantArgs := map[string]any{"city": "中😀é", "escaped": "\\ud800", "pair": "😀"}
	for _, protocol := range []string{provider.ProtocolAnthropic, provider.ProtocolChatCompletions} {
		for _, stream := range []bool{false, true} {
			for _, strict := range []bool{false, true} {
				t.Run(fmt.Sprintf("%s/stream=%v/strict=%v", protocol, stream, strict), func(t *testing.T) {
					calls := 0
					up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
						calls++
						var payload struct {
							ConversationState struct {
								CurrentMessage struct {
									UserInputMessage struct {
										UserInputMessageContext struct {
											Tools []struct{ ToolSpecification struct{ Name string } }
										}
									}
								}
							}
						}
						if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
							t.Error(err)
							return
						}
						tools := payload.ConversationState.CurrentMessage.UserInputMessage.UserInputMessageContext.Tools
						if len(tools) != 1 {
							t.Errorf("native tools=%d", len(tools))
							return
						}
						w.Header().Set("Content-Type", "application/vnd.amazon.eventstream")
						_, _ = w.Write(kiroTestFrame("reasoningContentEvent", `{"text":"想法\ud83d\ude00é"}`))
						_, _ = w.Write(kiroTestFrame("assistantResponseEvent", `{"content":"A中\ud83d\ude00Bé界"}`))
						data, _ := json.Marshal(map[string]any{"name": tools[0].ToolSpecification.Name, "toolUseId": toolID, "input": `{"city":"中😀é","escaped":"\\ud800","pair":"\ud83d\ude00"}`, "stop": true})
						_, _ = w.Write(kiroTestFrame("toolUseEvent", string(data)))
						_, _ = w.Write(kiroTestFrame("metadataEvent", `{"contextUsagePercentage":1}`))
					}))
					defer up.Close()
					name := "round29.工具." + up.URL
					target := resolve.ResolvedTarget{ModelID: "kiro-r29-unicode", ProviderID: kiro.ProviderID, Protocol: protocol, BaseURL: up.URL, NativeModel: "claude-sonnet-4.5", Headers: kiro.Headers("upstream-access", "")}
					h := NewHandler(testKey, fakeResolver{targets: map[string]resolve.ResolvedTarget{target.ModelID: target}}, nil)
					base := http.DefaultTransport.(*http.Transport).Clone()
					defer base.CloseIdleConnections()
					h.client.Transport = kiro.NewTransport(round29RoundTripFunc(func(r *http.Request) (*http.Response, error) {
						resp, err := base.RoundTrip(r)
						if err == nil {
							resp.Body = round29ByteBody{resp.Body}
						}
						return resp, err
					}))
					path := "/v1/messages"
					var choice any = map[string]any{"type": "auto"}
					if strict {
						choice = map[string]any{"type": "any"}
					}
					if protocol == provider.ProtocolChatCompletions {
						path, choice = "/v1/chat/completions", "auto"
						if strict {
							choice = "required"
						}
					}
					body, _ := json.Marshal(map[string]any{"model": target.ModelID, "max_tokens": 1024, "stream": stream, "tool_choice": choice, "messages": []any{map[string]any{"role": "user", "content": "q"}}, "tools": []any{map[string]any{"name": name, "input_schema": map[string]any{}}}})
					proxy := httptest.NewServer(h)
					defer proxy.Close()
					r, _ := http.NewRequest(http.MethodPost, proxy.URL+path, strings.NewReader(string(body)))
					r.Header.Set("Authorization", "Bearer "+testKey)
					resp, err := proxy.Client().Do(r)
					if err != nil {
						t.Fatal(err)
					}
					data, err := io.ReadAll(resp.Body)
					resp.Body.Close()
					if err != nil || resp.StatusCode != 200 || calls != 1 {
						t.Fatalf("status=%d calls=%d read=%v body=%s", resp.StatusCode, calls, err, data)
					}
					var gotText, gotThought, args strings.Builder
					var gotArgs map[string]any
					gotID, gotName := "", ""
					var visit func(any)
					visit = func(value any) {
						switch value := value.(type) {
						case []any:
							for _, part := range value {
								visit(part)
							}
						case map[string]any:
							for key, part := range value {
								s, _ := part.(string)
								switch key {
								case "content", "text":
									gotText.WriteString(s)
								case "thinking", "reasoning_content":
									gotThought.WriteString(s)
								case "id":
									if s == toolID {
										gotID = s
									}
								case "name":
									gotName = s
								case "arguments", "partial_json":
									args.WriteString(s)
								case "input":
									if input, ok := part.(map[string]any); ok && len(input) > 0 {
										gotArgs = input
									}
									continue
								}
								visit(part)
							}
						}
					}
					events := []string{string(data)}
					if stream {
						events = nil
						for _, line := range strings.Split(string(data), "\n") {
							if strings.HasPrefix(line, "data: ") && line != "data: [DONE]" {
								events = append(events, strings.TrimPrefix(line, "data: "))
							}
						}
					}
					for _, event := range events {
						var value any
						if err := json.Unmarshal([]byte(event), &value); err != nil {
							t.Fatal(err)
						}
						visit(value)
					}
					if args.Len() > 0 {
						if err := json.Unmarshal([]byte(args.String()), &gotArgs); err != nil {
							t.Fatal(err)
						}
					}
					if gotText.String() != text || gotThought.String() != thought || gotID != toolID || gotName != name || !reflect.DeepEqual(gotArgs, wantArgs) {
						t.Fatalf("text=%q thought=%q id=%q name=%q args=%v", gotText.String(), gotThought.String(), gotID, gotName, gotArgs)
					}
				})
			}
		}
	}
}

func TestRound29KiroAnthropicAuthenticationHTTP(t *testing.T) {
	for _, path := range []string{"/v1/messages", "/v1/messages/count_tokens"} {
		for _, tc := range []struct {
			name, bearer, key string
			allowed           bool
		}{
			{"native only", "", testKey, true},
			{"bearer only", testKey, "", true},
			{"both valid", testKey, testKey, true},
			{"valid native stale bearer", "stale-client-key", testKey, true},
			{"valid bearer stale native", testKey, "stale-client-key", true},
			{"both wrong", "stale-client-key", "wrong-key", false},
			{"missing", "", "", false},
		} {
			t.Run(path+"/"+tc.name, func(t *testing.T) {
				calls := 0
				up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					calls++
					if r.Header.Get("Authorization") != "Bearer upstream-access" || r.Header.Get("x-api-key") != "" {
						t.Error("client authentication escaped upstream")
					}
					_, _ = w.Write(kiroTestFrame("assistantResponseEvent", `{"content":"ok"}`))
					_, _ = w.Write(kiroTestFrame("metadataEvent", `{"contextUsagePercentage":1}`))
				}))
				defer up.Close()
				target := resolve.ResolvedTarget{ModelID: "kiro-r29-auth", ProviderID: kiro.ProviderID, Protocol: provider.ProtocolAnthropic, BaseURL: up.URL, NativeModel: "claude-sonnet-4.5", Headers: kiro.Headers("upstream-access", "")}
				h := NewHandler(testKey, fakeResolver{targets: map[string]resolve.ResolvedTarget{target.ModelID: target}}, nil)
				r := httptest.NewRequest(http.MethodPost, path, strings.NewReader(`{"model":"kiro-r29-auth","max_tokens":1024,"messages":[{"role":"user","content":"q"}]}`))
				if tc.bearer != "" {
					r.Header.Set("Authorization", "Bearer "+tc.bearer)
				}
				r.Header.Set("x-api-key", tc.key)
				w := httptest.NewRecorder()
				h.ServeHTTP(w, r)
				wantStatus, wantCalls := http.StatusUnauthorized, 0
				if tc.allowed {
					wantStatus = http.StatusOK
					if path == "/v1/messages" {
						wantCalls = 1
					}
				}
				if w.Code != wantStatus || calls != wantCalls {
					t.Fatalf("status=%d want=%d calls=%d want=%d", w.Code, wantStatus, calls, wantCalls)
				}
			})
		}
	}
	for _, path := range []string{"/v1/chat/completions", "/v1/responses", "/v1beta/models/example:generateContent"} {
		h := NewHandler(testKey, nil, nil)
		r := httptest.NewRequest(http.MethodPost, path, nil)
		r.Header.Set("Authorization", "Bearer wrong-key")
		r.Header.Set("x-api-key", testKey)
		r.Header.Set("x-goog-api-key", testKey)
		if h.authorized(r) {
			t.Errorf("non-Anthropic credential precedence changed: %s", path)
		}
	}
}

func TestRound28KiroSelectedAliasHTTP(t *testing.T) {
	for _, protocol := range []string{provider.ProtocolAnthropic, provider.ProtocolChatCompletions} {
		for _, stream := range []bool{false, true} {
			for _, mode := range []string{"named", "none"} {
				t.Run(fmt.Sprintf("%s/%v/%s", protocol, stream, mode), func(t *testing.T) {
					var name, alias string
					calls := 0
					up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
						calls++
						var payload struct {
							ConversationState struct {
								CurrentMessage struct {
									UserInputMessage struct {
										UserInputMessageContext struct {
											Tools []struct {
												ToolSpecification struct{ Name, Description string }
											}
										}
									}
								}
							}
						}
						if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
							t.Error(err)
							return
						}
						tools := payload.ConversationState.CurrentMessage.UserInputMessage.UserInputMessageContext.Tools
						if mode == "none" {
							if len(tools) != 0 {
								t.Error("none sent tools")
							}
							_, _ = w.Write(kiroTestFrame("assistantResponseEvent", `{"content":"ok"}`))
						} else {
							if len(tools) != 1 || tools[0].ToolSpecification.Name != alias || tools[0].ToolSpecification.Description != "Tool: "+alias {
								t.Errorf("selected native tools=%v want alias=%s", tools, alias)
							}
							data, _ := json.Marshal(map[string]any{"name": alias, "toolUseId": "r28", "input": "{}", "stop": true})
							_, _ = w.Write(kiroTestFrame("toolUseEvent", string(data)))
						}
						_, _ = w.Write(kiroTestFrame("metadataEvent", `{"contextUsagePercentage":1}`))
					}))
					defer up.Close()
					name = "round28.wire." + up.URL + "__selected"
					digest := sha256.Sum256([]byte(name))
					alias = fmt.Sprintf("t_%x_selected", digest[:6])
					var choice any = mode
					path := "/v1/chat/completions"
					if protocol == provider.ProtocolAnthropic {
						path = "/v1/messages"
						choice = map[string]any{"type": "none"}
						if mode == "named" {
							choice = map[string]any{"type": "tool", "name": name}
						}
					} else if mode == "named" {
						choice = map[string]any{"type": "function", "function": map[string]any{"name": name}}
					}
					target := resolve.ResolvedTarget{ModelID: "kiro-r28", ProviderID: kiro.ProviderID, Protocol: protocol, BaseURL: up.URL, NativeModel: "claude-sonnet-4.5", Headers: kiro.Headers("test-access", "")}
					h := NewHandler(testKey, fakeResolver{targets: map[string]resolve.ResolvedTarget{target.ModelID: target}}, nil)
					body, _ := json.Marshal(map[string]any{"model": target.ModelID, "max_tokens": 1024, "stream": stream, "messages": []any{map[string]any{"role": "user", "content": "q"}}, "tool_choice": choice,
						"tools": []any{map[string]any{"name": name, "input_schema": map[string]any{}}, map[string]any{"name": alias, "input_schema": map[string]any{}}}})
					r := httptest.NewRequest("POST", path, strings.NewReader(string(body)))
					r.Header.Set("Authorization", "Bearer "+testKey)
					w := httptest.NewRecorder()
					h.ServeHTTP(w, r)
					if w.Code != 200 || calls != 1 || mode == "named" && !strings.Contains(w.Body.String(), name) {
						t.Fatalf("status=%d calls=%d body=%s", w.Code, calls, w.Body)
					}
				})
			}
		}
	}
}

func TestRound28KiroLargeIntegerHTTP(t *testing.T) {
	for _, sign := range []string{"", "-"} {
		for _, source := range []string{"client", "defaults", "overrides"} {
			t.Run(sign+"/"+source, func(t *testing.T) {
				want := sign + "1" + strings.Repeat("0", 400)
				calls := 0
				up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					calls++
					var payload struct {
						ConversationState struct {
							CurrentMessage struct{ UserInputMessage struct{ Content string } }
						}
					}
					if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
						t.Error(err)
					}
					if !strings.HasSuffix(payload.ConversationState.CurrentMessage.UserInputMessage.Content, "[Tool Result (r28)]\n"+want) {
						t.Errorf("large integer lost at native HTTP boundary: %q", payload.ConversationState.CurrentMessage.UserInputMessage.Content)
					}
					_, _ = w.Write(kiroTestFrame("assistantResponseEvent", `{"content":"ok"}`))
					_, _ = w.Write(kiroTestFrame("metadataEvent", `{"contextUsagePercentage":1}`))
				}))
				defer up.Close()
				target := resolve.ResolvedTarget{ModelID: "kiro-r28-integer", ProviderID: kiro.ProviderID, Protocol: provider.ProtocolAnthropic, BaseURL: up.URL, NativeModel: "claude-sonnet-4.5", Headers: kiro.Headers("test-access", "")}
				messages := json.RawMessage(`[{"role":"user","content":[{"type":"tool_result","tool_use_id":"r28","content":` + want + `}]}]`)
				body := []byte(`{"model":"kiro-r28-integer","max_tokens":1024}`)
				part, _ := json.Marshal(map[string]any{"messages": messages})
				switch source {
				case "client":
					body, _ = json.Marshal(map[string]any{"model": target.ModelID, "max_tokens": 1024, "messages": messages})
				case "defaults":
					target.Defaults = part
				case "overrides":
					target.Overrides = part
				}
				h := NewHandler(testKey, fakeResolver{targets: map[string]resolve.ResolvedTarget{target.ModelID: target}}, nil)
				r := httptest.NewRequest("POST", "/v1/messages", strings.NewReader(string(body)))
				r.Header.Set("Authorization", "Bearer "+testKey)
				w := httptest.NewRecorder()
				h.ServeHTTP(w, r)
				if w.Code != 200 || calls != 1 {
					t.Fatalf("status=%d calls=%d body=%s", w.Code, calls, w.Body)
				}
			})
		}
	}
}

func TestRound28KiroSystemValidationHTTP(t *testing.T) {
	for _, field := range []string{"name", "tool_call_id", "tool_calls"} {
		t.Run(field, func(t *testing.T) {
			calls := 0
			up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls++
				_, _ = w.Write(kiroTestFrame("assistantResponseEvent", `{"content":"ok"}`))
				_, _ = w.Write(kiroTestFrame("metadataEvent", `{"contextUsagePercentage":1}`))
			}))
			defer up.Close()
			target := resolve.ResolvedTarget{ModelID: "kiro-r28-system", ProviderID: kiro.ProviderID, Protocol: provider.ProtocolChatCompletions, BaseURL: up.URL, NativeModel: "claude-sonnet-4.5", Headers: kiro.Headers("test-access", "")}
			h := NewHandler(testKey, fakeResolver{targets: map[string]resolve.ResolvedTarget{target.ModelID: target}}, nil)
			body, _ := json.Marshal(map[string]any{"model": target.ModelID, "messages": []any{map[string]any{"role": "system", "content": "s", field: 7}, map[string]any{"role": "user", "content": "q"}}})
			r := httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(string(body)))
			r.Header.Set("Authorization", "Bearer "+testKey)
			w := httptest.NewRecorder()
			h.ServeHTTP(w, r)
			if w.Code != 400 || calls != 0 {
				t.Fatalf("invalid system contacted upstream: status=%d calls=%d body=%s", w.Code, calls, w.Body)
			}
		})
	}
}

func TestKiroNativeProxy(t *testing.T) {
	for _, protocol := range []string{provider.ProtocolAnthropic, provider.ProtocolChatCompletions} {
		for _, stream := range []bool{false, true} {
			t.Run(protocol+map[bool]string{false: "/json", true: "/sse"}[stream], func(t *testing.T) {
				up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					if r.URL.Path != "/generateAssistantResponse" || r.Header.Get("Authorization") != "Bearer access-test" {
						t.Errorf("wrong native path/auth: %s", r.URL.Path)
					}
					if r.Header.Get(kiro.HeaderProfileARN) != "" || r.Header.Get(kiro.HeaderProvider) != "" || r.Header.Get("x-api-key") != "" {
						t.Error("client/internal authentication metadata escaped")
					}
					var payload map[string]any
					if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
						t.Error(err)
					}
					if payload["profileArn"] != "arn:aws:codewhisperer:us-east-1:123:profile/test" || payload["conversationState"] == nil {
						t.Error("Kiro payload/profile missing")
					}
					w.Header().Set("Content-Type", "application/vnd.amazon.eventstream")
					_, _ = w.Write(kiroTestFrame("assistantResponseEvent", `{"content":"native Go reply"}`))
					_, _ = w.Write(kiroTestFrame("messageStopEvent", `{"stopReason":"end_turn"}`))
				}))
				defer up.Close()
				target := resolve.ResolvedTarget{ModelID: "my-kiro", Account: "kiro-1", ProviderID: kiro.ProviderID,
					Protocol: protocol, BaseURL: up.URL, NativeModel: "claude-sonnet-4.5", Headers: kiro.Headers("access-test", "arn:aws:codewhisperer:us-east-1:123:profile/test")}
				h := NewHandler(testKey, fakeResolver{targets: map[string]resolve.ResolvedTarget{target.ModelID: target}}, nil)
				path := "/v1/messages"
				if protocol == provider.ProtocolChatCompletions {
					path = "/v1/chat/completions"
				}
				body, _ := json.Marshal(map[string]any{"model": target.ModelID, "messages": []map[string]any{{"role": "user", "content": "ping"}}, "max_tokens": 32, "stream": stream})
				r := httptest.NewRequest(http.MethodPost, path, strings.NewReader(string(body)))
				r.Header.Set("Authorization", "Bearer "+testKey)
				r.Header.Set("x-api-key", "client-key-must-not-escape")
				r.Header.Set(kiro.HeaderProvider, "client-forged")
				r.Header.Set(kiro.HeaderProfileARN, "client-forged-profile")
				w := httptest.NewRecorder()
				h.ServeHTTP(w, r)
				if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), "native Go reply") {
					t.Fatalf("response = %d %s", w.Code, w.Body)
				}
				if stream {
					if !strings.Contains(w.Header().Get("Content-Type"), "text/event-stream") {
						t.Fatal("stream content type missing")
					}
					end := "message_stop"
					if protocol == provider.ProtocolChatCompletions {
						end = "[DONE]"
					}
					if !strings.Contains(w.Body.String(), end) {
						t.Fatal("protocol stream did not finish")
					}
				} else if !json.Valid(w.Body.Bytes()) {
					t.Fatal("nonstream response is not JSON")
				}
			})
		}
	}
}

func TestRound30KiroManagementBoundary(t *testing.T) {
	for _, protocol := range []string{provider.ProtocolAnthropic, provider.ProtocolChatCompletions} {
		for _, source := range []string{"defaults", "overrides"} {
			for _, value := range []string{"0x1p-1", "1_.0"} {
				t.Run(protocol+"/"+source+"/"+value, func(t *testing.T) {
					calls := 0
					up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
						calls++
						if r.URL.Path != "/generateAssistantResponse" {
							t.Errorf("unexpected path=%s", r.URL.Path)
						}
						w.Header().Set("Content-Type", "application/vnd.amazon.eventstream")
						_, _ = w.Write(kiroTestFrame("assistantResponseEvent", `{"content":"management reply"}`))
						_, _ = w.Write(kiroTestFrame("messageStopEvent", `{"stopReason":"end_turn"}`))
					}))
					defer up.Close()
					target := resolve.ResolvedTarget{ModelID: "kiro-r30-management", ProviderID: kiro.ProviderID, Protocol: protocol, BaseURL: up.URL, NativeModel: "claude-sonnet-4.5", Headers: kiro.Headers("test-access", "")}
					params, _ := json.Marshal(map[string]any{"temperature": value})
					if source == "defaults" {
						target.Defaults = params
					} else {
						target.Overrides = params
					}
					reply, _, status, err := chat.Complete(context.Background(), target, "session", "", []chat.Message{{Role: "user", Content: "hello"}})
					wantCalls := 0
					if value == "1_.0" {
						wantCalls = 1
						if err != nil || status != 200 || reply != "management reply" {
							t.Errorf("chat reply=%q status=%d err=%v", reply, status, err)
						}
					} else if err == nil || status != 0 {
						t.Errorf("invalid chat status=%d err=%v", status, err)
					}
					if calls != wantCalls {
						t.Errorf("chat calls=%d want=%d", calls, wantCalls)
					}
					probe := modelcheck.Check(context.Background(), target)
					if !probe.OK || calls != wantCalls+1 {
						t.Errorf("probe=%+v calls=%d want=%d", probe, calls, wantCalls+1)
					}
				})
			}
		}
	}
}

func TestKiroManagementCalls(t *testing.T) {
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/generateAssistantResponse" {
			t.Errorf("unexpected path %s", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/vnd.amazon.eventstream")
		_, _ = w.Write(kiroTestFrame("assistantResponseEvent", `{"content":"management reply"}`))
		_, _ = w.Write(kiroTestFrame("messageStopEvent", `{"stopReason":"end_turn"}`))
	}))
	defer up.Close()
	for _, protocol := range []string{provider.ProtocolAnthropic, provider.ProtocolChatCompletions} {
		t.Run(protocol, func(t *testing.T) {
			target := resolve.ResolvedTarget{ModelID: "my-kiro", Account: "kiro-1", ProviderID: kiro.ProviderID,
				Protocol: protocol, BaseURL: up.URL, NativeModel: "claude-sonnet-4.5", Headers: kiro.Headers("access-test", "arn:aws:codewhisperer:us-east-1:123:profile/test")}
			result := modelcheck.Check(context.Background(), target)
			if !result.OK {
				t.Fatalf("model probe = %+v", result)
			}
			reply, _, status, err := chat.Complete(context.Background(), target, "session", "", []chat.Message{{Role: "user", Content: "hello"}})
			if err != nil || status != 200 || reply != "management reply" {
				t.Fatalf("chat reply=%q status=%d err=%v", reply, status, err)
			}
		})
	}
}

func TestKiroRetryUsesRefreshedEndpoint(t *testing.T) {
	old := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer old.Close()
	calls := 0
	fresh := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.URL.Path != "/generateAssistantResponse" || r.Header.Get("Authorization") != "Bearer refreshed-access" {
			t.Error("retry did not use refreshed native request")
		}
		w.Header().Set("Content-Type", "application/vnd.amazon.eventstream")
		_, _ = w.Write(kiroTestFrame("assistantResponseEvent", `{"content":"retry reply"}`))
		_, _ = w.Write(kiroTestFrame("messageStopEvent", `{"stopReason":"end_turn"}`))
	}))
	defer fresh.Close()
	target := resolve.ResolvedTarget{ModelID: "kiro-retry", Account: "kiro-test", ProviderID: "kiro.global.subscribe.standard",
		Protocol: provider.ProtocolChatCompletions, BaseURL: old.URL, NativeModel: "claude-sonnet-4.5",
		Headers: kiro.Headers("old-access", "")}
	updated := target
	updated.BaseURL = fresh.URL
	updated.Headers = kiro.Headers("refreshed-access", "arn:aws:codewhisperer:us-east-1:123:profile/test")
	resolver := &scriptedResolver{targets: []resolve.ResolvedTarget{target, updated}}
	inv := &fakeInvalidator{}
	h := NewHandler(testKey, resolver, nil).WithInvalidator(inv)
	r := httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(`{"model":"kiro-retry","messages":[{"role":"user","content":"hello"}]}`))
	r.Header.Set("Authorization", "Bearer "+testKey)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != 200 || !strings.Contains(w.Body.String(), "retry reply") || calls != 1 || inv.calls != 1 || inv.token != "old-access" {
		t.Fatalf("Kiro retry failed: status=%d calls=%d invalidations=%d body=%s", w.Code, calls, inv.calls, w.Body)
	}
}

// KiroaaS auth.py 的 reactive refresh 走 403:与 401 一样作废重放一次。
func TestKiroRetryOn403Refreshes(t *testing.T) {
	old := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
	}))
	defer old.Close()
	calls := 0
	fresh := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.Header.Get("Authorization") != "Bearer refreshed-access" {
			t.Error("403 retry did not use refreshed native request")
		}
		w.Header().Set("Content-Type", "application/vnd.amazon.eventstream")
		_, _ = w.Write(kiroTestFrame("assistantResponseEvent", `{"content":"retry reply"}`))
		_, _ = w.Write(kiroTestFrame("messageStopEvent", `{"stopReason":"end_turn"}`))
	}))
	defer fresh.Close()
	target := resolve.ResolvedTarget{ModelID: "kiro-403", Account: "kiro-test", ProviderID: "kiro.global.subscribe.standard",
		Protocol: provider.ProtocolChatCompletions, BaseURL: old.URL, NativeModel: "claude-sonnet-4.5",
		Headers: kiro.Headers("old-access", "")}
	updated := target
	updated.BaseURL = fresh.URL
	updated.Headers = kiro.Headers("refreshed-access", "arn:aws:codewhisperer:us-east-1:123:profile/test")
	resolver := &scriptedResolver{targets: []resolve.ResolvedTarget{target, updated}}
	inv := &fakeInvalidator{}
	h := NewHandler(testKey, resolver, nil).WithInvalidator(inv)
	r := httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(`{"model":"kiro-403","messages":[{"role":"user","content":"hello"}]}`))
	r.Header.Set("Authorization", "Bearer "+testKey)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != 200 || !strings.Contains(w.Body.String(), "retry reply") || calls != 1 || inv.calls != 1 || inv.token != "old-access" {
		t.Fatalf("Kiro 403 retry failed: status=%d calls=%d invalidations=%d body=%s", w.Code, calls, inv.calls, w.Body)
	}
}

// 非 Kiro 目标 403 不触发刷新重试。
func TestNonKiro403NotRetried(t *testing.T) {
	calls := 0
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.WriteHeader(http.StatusForbidden)
	}))
	defer up.Close()
	target := resolve.ResolvedTarget{ModelID: "gpt", Account: "openai-1", ProviderID: "openai", Protocol: provider.ProtocolChatCompletions,
		BaseURL: up.URL, NativeModel: "gpt-test", Headers: map[string]string{"Authorization": "Bearer account-key"}}
	h := NewHandler(testKey, fakeResolver{targets: map[string]resolve.ResolvedTarget{"gpt": target}}, nil).WithInvalidator(&fakeInvalidator{})
	r := httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(`{"model":"gpt","messages":[{"role":"user","content":"hello"}]}`))
	r.Header.Set("Authorization", "Bearer "+testKey)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != http.StatusForbidden || calls != 1 {
		t.Fatalf("non-Kiro 403 retried: status=%d calls=%d", w.Code, calls)
	}
}

func TestClientCannotSelectKiroAdapter(t *testing.T) {
	cap := &captured{}
	up := httptest.NewServer(cap.handler(200, `{"choices":[]}`))
	defer up.Close()
	target := resolve.ResolvedTarget{ModelID: "gpt", Account: "openai-1", ProviderID: "openai", Protocol: provider.ProtocolChatCompletions,
		BaseURL: up.URL, NativeModel: "gpt-test", Headers: map[string]string{"Authorization": "Bearer account-key"}}
	h := NewHandler(testKey, fakeResolver{targets: map[string]resolve.ResolvedTarget{"gpt": target}}, nil)
	r := httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(`{"model":"gpt","messages":[{"role":"user","content":"hello"}]}`))
	r.Header.Set("Authorization", "Bearer "+testKey)
	r.Header.Set(kiro.HeaderProvider, "kiro.global.subscribe.standard")
	r.Header.Set(kiro.HeaderProfileARN, "forged-profile")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	_, headers, path := cap.snapshot()
	if w.Code != 200 || path != "/v1/chat/completions" || headers.Get(kiro.HeaderProvider) != "" || headers.Get(kiro.HeaderProfileARN) != "" {
		t.Fatalf("client-selected native conversion escaped: status=%d path=%s", w.Code, path)
	}
	_, _ = io.Copy(io.Discard, w.Result().Body)
}

func TestRound26KiroCountTokensHTTP(t *testing.T) {
	calls := 0
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer up.Close()
	target := resolve.ResolvedTarget{ModelID: "kiro-count", ProviderID: kiro.ProviderID, Protocol: provider.ProtocolAnthropic, BaseURL: up.URL, NativeModel: "claude-sonnet-4.5", Headers: kiro.Headers("test-access", "")}
	h := NewHandler(testKey, fakeResolver{targets: map[string]resolve.ResolvedTarget{target.ModelID: target}}, nil)
	for _, tc := range []struct {
		content string
		status  int
	}{
		{`[{"type":"text"}]`, 200}, {`[{"type":"text","text":"x"}]`, 200}, {`[true]`, 400},
	} {
		r := httptest.NewRequest("POST", "/v1/messages/count_tokens", strings.NewReader(`{"model":"kiro-count","messages":[{"role":"user","content":`+tc.content+`}]}`))
		r.Header.Set("Authorization", "Bearer "+testKey)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code != tc.status {
			t.Errorf("content=%s status=%d body=%s", tc.content, w.Code, w.Body)
		}
		if tc.status == 200 {
			var result struct {
				InputTokens int `json:"input_tokens"`
			}
			if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil || result.InputTokens <= 0 {
				t.Errorf("invalid count result=%s err=%v", w.Body, err)
			}
		}
	}
	if calls != 0 {
		t.Fatalf("count_tokens contacted upstream %d times", calls)
	}
}

func TestRound26KiroNumericContentHTTP(t *testing.T) {
	for _, tc := range []struct{ raw, want string }{
		{"1e2", "100.0"}, {"1.00", "1.0"}, {"-0", "0"}, {"-0.0", "-0.0"},
		{`{}`, `{}`}, {`{"n":1e0}`, `{'n': 1.0}`}, {`{"x":"\u0000\u0085"}`, `{'x': '\x00\x85'}`},
		{`{"x":"\ud800"}`, `{'x': '\ud800'}`}, {`{"\ud800":1,"\ud801":2}`, `{'\ud800': 1, '\ud801': 2}`},
	} {
		t.Run(tc.raw, func(t *testing.T) {
			up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var payload struct {
					ConversationState struct {
						CurrentMessage struct{ UserInputMessage struct{ Content string } }
					}
				}
				if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
					t.Error(err)
					return
				}
				content := payload.ConversationState.CurrentMessage.UserInputMessage.Content
				if content != tc.want && !strings.HasSuffix(content, "\n\n"+tc.want) {
					t.Errorf("numeric content suffix differs, want=%s", tc.want)
				}
				_, _ = w.Write(kiroTestFrame("assistantResponseEvent", `{"content":"ok"}`))
				_, _ = w.Write(kiroTestFrame("metadataEvent", `{"contextUsagePercentage":0}`))
			}))
			defer up.Close()
			target := resolve.ResolvedTarget{ModelID: "kiro-number", ProviderID: kiro.ProviderID, Protocol: provider.ProtocolChatCompletions, BaseURL: up.URL, NativeModel: "claude-sonnet-4.5", Headers: kiro.Headers("test-access", "")}
			h := NewHandler(testKey, fakeResolver{targets: map[string]resolve.ResolvedTarget{target.ModelID: target}}, nil)
			r := httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(`{"model":"kiro-number","messages":[{"role":"user","content":`+tc.raw+`}]}`))
			r.Header.Set("Authorization", "Bearer "+testKey)
			w := httptest.NewRecorder()
			h.ServeHTTP(w, r)
			if w.Code != 200 {
				t.Fatalf("status=%d body=%s", w.Code, w.Body)
			}
		})
	}
}

func TestRound27KiroSurrogateContainersHTTP(t *testing.T) {
	for _, tc := range []struct {
		name, raw, want string
	}{
		{"high", `{"x":"\ud800"}`, `{'x': '\ud800'}`},
		{"low", `{"x":"\uDC00"}`, `{'x': '\udc00'}`},
		{"pair", `{"x":"\ud83d\ude00"}`, `{'x': '😀'}`},
		{"literal_escape", `{"x":"\\ud800"}`, `{'x': '\\ud800'}`},
		{"replacement", `{"x":"�"}`, `{'x': '�'}`},
		{"distinct_keys", `{"\ud800":1,"\ud801":2}`, `{'\ud800': 1, '\ud801': 2}`},
		{"duplicate_key", `{"\ud800":1,"\uD800":2}`, `{'\ud800': 2}`},
		{"nested", `{"x":["\ud800",{"y":"\udc00","n":1e0}]}`, `{'x': ['\ud800', {'n': 1.0, 'y': '\udc00'}]}`},
	} {
		for _, protocol := range []string{provider.ProtocolChatCompletions, provider.ProtocolAnthropic} {
			for _, source := range []string{"client", "defaults", "overrides"} {
				t.Run(tc.name+"/"+protocol+"/"+source, func(t *testing.T) {
					up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
						if r.URL.Path != "/generateAssistantResponse" {
							t.Errorf("unexpected native path %s", r.URL.Path)
						}
						var payload struct {
							ConversationState struct {
								CurrentMessage struct{ UserInputMessage struct{ Content string } }
							}
						}
						if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
							t.Error(err)
							return
						}
						content := payload.ConversationState.CurrentMessage.UserInputMessage.Content
						want := tc.want
						if protocol == provider.ProtocolAnthropic {
							want = "[Tool Result (i)]\n" + want
						}
						if content != want && !strings.HasSuffix(content, "\n\n"+want) {
							t.Errorf("surrogate content=%q, want suffix=%q", content, want)
						}
						w.Header().Set("Content-Type", "application/vnd.amazon.eventstream")
						_, _ = w.Write(kiroTestFrame("assistantResponseEvent", `{"content":"ok"}`))
						_, _ = w.Write(kiroTestFrame("messageStopEvent", `{"stopReason":"end_turn"}`))
					}))
					defer up.Close()
					content := tc.raw
					path := "/v1/chat/completions"
					if protocol == provider.ProtocolAnthropic {
						path = "/v1/messages"
						content = `[{"type":"tool_result","tool_use_id":"i","content":` + content + `}]`
					}
					messages := `,"messages":[{"role":"user","content":` + content + `}]`
					target := resolve.ResolvedTarget{ModelID: "kiro-surrogate", ProviderID: kiro.ProviderID,
						Protocol: protocol, BaseURL: up.URL, NativeModel: "claude-sonnet-4.5", Headers: kiro.Headers("test-access", "")}
					client := messages
					switch source {
					case "defaults":
						target.Defaults = json.RawMessage(`{` + strings.TrimPrefix(messages, ",") + `}`)
						client = ""
					case "overrides":
						target.Overrides = json.RawMessage(`{` + strings.TrimPrefix(messages, ",") + `}`)
						client = `,"messages":[{"role":"user","content":"must be overridden"}]`
					}
					h := NewHandler(testKey, fakeResolver{targets: map[string]resolve.ResolvedTarget{target.ModelID: target}}, nil)
					proxy := httptest.NewServer(h)
					defer proxy.Close()
					r, err := http.NewRequest(http.MethodPost, proxy.URL+path, strings.NewReader(`{"model":"kiro-surrogate","max_tokens":32`+client+`}`))
					if err != nil {
						t.Fatal(err)
					}
					r.Header.Set("Authorization", "Bearer "+testKey)
					r.Header.Set("Content-Type", "application/json")
					resp, err := proxy.Client().Do(r)
					if err != nil {
						t.Fatal(err)
					}
					defer resp.Body.Close()
					body, err := io.ReadAll(resp.Body)
					if err != nil || resp.StatusCode != http.StatusOK || !strings.Contains(string(body), "ok") {
						t.Fatalf("status=%d body=%s err=%v", resp.StatusCode, body, err)
					}
				})
			}
		}
	}
}

func TestRound27KiroRawObjectEmpty(t *testing.T) {
	for _, raw := range []string{"", "null", "{}", "[]", "1", `{"x":`} {
		t.Run(raw, func(t *testing.T) {
			if got := rawObject(json.RawMessage(raw), true); got == nil || len(got) != 0 {
				t.Fatalf("rawObject(%q)=%v, want non-nil empty object", raw, got)
			}
		})
	}
}

func TestRound25KiroReasoningNumbers(t *testing.T) {
	for _, raw := range []string{"2", "2.0", "2e0"} {
		level, ok := reasoningLevel(json.Number(raw))
		if !ok || level != "2" {
			t.Fatalf("number=%s level=%q ok=%v", raw, level, ok)
		}
	}
	for _, raw := range []string{"-1", "1.5", "1e400"} {
		if level, ok := reasoningLevel(json.Number(raw)); ok {
			t.Fatalf("number=%s accepted as %q", raw, level)
		}
	}
}

func TestRound25KiroBudgetNumberIdentity(t *testing.T) {
	for _, tc := range []struct {
		name, client, defaults, overrides, want string
	}{
		{"client_float", `,"thinking":{"budget_tokens":5000.0}`, "", "", "4000"},
		{"client_integer", `,"thinking":{"budget_tokens":5000}`, "", "", "5000"},
		{"default_float", "", `{"thinking":{"budget_tokens":5000.0}}`, "", "4000"},
		{"override_float", `,"thinking":{"budget_tokens":5000}`, "", `{"thinking":{"budget_tokens":5000.0}}`, "4000"},
		{"large_integer_beats_none", `,"thinking":{"budget_tokens":1099511627777},"reasoning_effort":"none"`, "", "", "10000"},
		{"huge_integer_beats_none", `,"thinking":{"budget_tokens":999999999999999999999999999999999999999},"reasoning_effort":"none"`, "", "", "10000"},
		{"unbounded_integer_beats_none", `,"thinking":{"budget_tokens":` + strings.Repeat("9", 400) + `},"reasoning_effort":"none"`, "", "", "10000"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var payload struct {
					ConversationState struct {
						CurrentMessage struct {
							UserInputMessage struct{ Content string }
						}
					}
				}
				if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
					t.Error(err)
					return
				}
				content := payload.ConversationState.CurrentMessage.UserInputMessage.Content
				if !strings.Contains(content, "<max_thinking_length>"+tc.want+"</max_thinking_length>") {
					t.Errorf("expected budget=%s", tc.want)
				}
				_, _ = w.Write(kiroTestFrame("assistantResponseEvent", `{"content":"ok"}`))
				_, _ = w.Write(kiroTestFrame("messageStopEvent", `{"stopReason":"end_turn"}`))
			}))
			defer up.Close()
			target := resolve.ResolvedTarget{ModelID: "kiro-budget", ProviderID: kiro.ProviderID,
				Protocol: provider.ProtocolAnthropic, BaseURL: up.URL, NativeModel: "claude-sonnet-4.5",
				Headers: kiro.Headers("test-access", ""), Defaults: json.RawMessage(tc.defaults), Overrides: json.RawMessage(tc.overrides)}
			h := NewHandler(testKey, fakeResolver{targets: map[string]resolve.ResolvedTarget{target.ModelID: target}}, nil)
			r := httptest.NewRequest("POST", "/v1/messages", strings.NewReader(`{"model":"kiro-budget","max_tokens":32,"messages":[{"role":"user","content":"hello"}]`+tc.client+`}`))
			r.Header.Set("Authorization", "Bearer "+testKey)
			w := httptest.NewRecorder()
			h.ServeHTTP(w, r)
			if w.Code != http.StatusOK {
				t.Fatalf("status=%d body=%s", w.Code, w.Body)
			}
		})
	}
}
