package kiro

import (
	"bytes"
	"compress/gzip"
	"crypto/tls"
	"crypto/x509"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/aceaura/model-surge-upstream/backend/apperr"
)

func TestRound28WireHTTPErrorLength(t *testing.T) {
	const raw = `{"message":"quota","reason":"MONTHLY_REQUEST_COUNT"}`
	var compressed bytes.Buffer
	zw := gzip.NewWriter(&compressed)
	if _, err := zw.Write([]byte(raw)); err != nil {
		t.Fatal(err)
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	for _, protocol := range []string{"openai", "anthropic"} {
		for _, stream := range []bool{false, true} {
			for _, mode := range []string{"plain", "identity", "chunked", "gzip", "decoded-gzip"} {
				t.Run(fmt.Sprintf("%s/stream=%v/%s", protocol, stream, mode), func(t *testing.T) {
					wire := []byte(raw)
					encoding := ""
					if mode == "identity" {
						encoding = "identity"
					}
					if mode == "gzip" || mode == "decoded-gzip" {
						wire, encoding = compressed.Bytes(), "gzip"
					}
					upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
						if r.URL.Path != "/generateAssistantResponse" || r.Header.Get("Accept-Encoding") != "identity" {
							t.Errorf("unexpected upstream request: path=%s headers=%v", r.URL.Path, r.Header)
						}
						w.Header().Set("Content-Type", "application/x-amz-json-1.0")
						w.Header().Set("ETag", `"upstream-body"`)
						w.Header().Set("Cache-Control", "no-store")
						w.Header().Set("Retry-After", "7")
						if encoding != "" {
							w.Header().Set("Content-Encoding", encoding)
						}
						if mode != "chunked" {
							w.Header().Set("Content-Length", fmt.Sprint(len(wire)))
						}
						w.WriteHeader(http.StatusBadRequest)
						if mode == "chunked" {
							w.(http.Flusher).Flush()
						}
						if _, err := w.Write(wire); err != nil {
							t.Error(err)
						}
					}))
					defer upstream.Close()
					base := http.DefaultTransport.(*http.Transport).Clone()
					defer base.CloseIdleConnections()
					var original *http.Response
					tr := NewTransport(roundTripFunc(func(r *http.Request) (*http.Response, error) {
						resp, err := base.RoundTrip(r)
						if err != nil {
							return nil, err
						}
						if mode == "decoded-gzip" {
							// A custom base may decode gzip while retaining its wire headers.
							zr, err := gzip.NewReader(resp.Body)
							if err != nil {
								resp.Body.Close()
								return nil, err
							}
							data, err := io.ReadAll(zr)
							zr.Close()
							resp.Body.Close()
							if err != nil {
								return nil, err
							}
							resp.Body = io.NopCloser(bytes.NewReader(data))
							resp.Uncompressed = true
						}
						original = resp
						return resp, nil
					}))
					want := []byte(jsonText(object{"error": object{"message": "Monthly request limit exceeded. Account has reached its monthly quota.", "type": "kiro_api_error", "code": 400}}))
					if protocol == "anthropic" {
						want = []byte(jsonText(object{"type": "error", "error": object{"type": "api_error", "message": "Monthly request limit exceeded. Account has reached its monthly quota."}}))
					}
					if mode == "gzip" {
						want = wire
					}
					check := func(resp *http.Response, downstream bool) {
						t.Helper()
						data, err := io.ReadAll(resp.Body)
						resp.Body.Close()
						if err != nil || !bytes.Equal(data, want) || resp.StatusCode != 400 {
							t.Errorf("downstream=%v status=%d read=%v body=%q want=%q", downstream, resp.StatusCode, err, data, want)
						}
						length := resp.Header.Get("Content-Length")
						if length != "" && length != fmt.Sprint(len(want)) || resp.ContentLength != int64(len(want)) {
							t.Errorf("downstream=%v stale length: header=%q field=%d body=%d", downstream, length, resp.ContentLength, len(want))
						}
						if resp.Header.Get("Cache-Control") != "no-store" || resp.Header.Get("Retry-After") != "7" {
							t.Errorf("lost error policy headers: %v", resp.Header)
						}
						if mode == "gzip" {
							if resp.Header.Get("Content-Encoding") != "gzip" || resp.Header.Get("ETag") != `"upstream-body"` {
								t.Errorf("compressed passthrough metadata changed: %v", resp.Header)
							}
						} else if resp.Header.Get("Content-Encoding") != "" || resp.Header.Get("ETag") != "" ||
							resp.Header.Get("Content-Type") != "application/json" || len(resp.TransferEncoding) != 0 || len(resp.Trailer) != 0 || resp.Uncompressed {
							t.Errorf("stale rewritten-body metadata: headers=%v transfer=%v trailer=%v uncompressed=%v", resp.Header, resp.TransferEncoding, resp.Trailer, resp.Uncompressed)
						}
					}
					resp, err := tr.RoundTrip(clientRequest(tbOf(t), upstream.URL, protocol, stream))
					if err != nil {
						t.Fatal(err)
					}
					check(resp, false)
					if original.Header.Get("ETag") != `"upstream-body"` || original.Header.Get("Content-Encoding") != encoding ||
						mode != "chunked" && original.Header.Get("Content-Length") != fmt.Sprint(len(wire)) {
						t.Errorf("upstream header clone was mutated: %v", original.Header)
					}
					// Forward the real non-2xx RoundTrip result through an actual HTTP server.
					proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
						forward := r.Clone(r.Context())
						forward.URL.Scheme, forward.URL.Host = "http", strings.TrimPrefix(upstream.URL, "http://")
						forward.RequestURI = ""
						resp, err := tr.RoundTrip(forward)
						if err != nil {
							t.Error(err)
							http.Error(w, "RoundTrip failed", http.StatusBadGateway)
							return
						}
						defer resp.Body.Close()
						for key, values := range resp.Header {
							w.Header()[key] = append([]string(nil), values...)
						}
						w.WriteHeader(resp.StatusCode)
						if _, err := io.Copy(w, resp.Body); err != nil {
							t.Errorf("downstream error-body write: %v", err)
						}
					}))
					defer proxy.Close()
					client := &http.Client{Transport: base}
					request := clientRequest(tbOf(t), proxy.URL, protocol, stream)
					request.Header.Set("Accept-Encoding", "identity")
					resp, err = client.Do(request)
					if err != nil {
						t.Fatal(err)
					}
					check(resp, true)
				})
			}
		}
	}
}

func TestRound28WireNetworkRetryBoundary(t *testing.T) {
	origBackoff := retryBackoff
	retryBackoff = func(int) time.Duration { return 0 }
	t.Cleanup(func() { retryBackoff = origBackoff })
	for _, tc := range []struct {
		name  string
		cause error
		retry bool
	}{
		{"handshake", errors.New("tls: handshake failure"), false},
		{"wrapped-handshake", fmt.Errorf("dial: %w", errors.New("remote error: TLS: handshake failure")), false},
		{"wrapped-certificate", fmt.Errorf("verify: %w", errors.New("x509: certificate signed by unknown authority")), false},
		{"wrapped-ssl", fmt.Errorf("connect: %w", errors.New("SSL handshake failed")), false},
		{"connection-reset", errors.New("connection reset by peer"), true},
		{"unexpected-eof", io.ErrUnexpectedEOF, true},
		{"timeout", &net.DNSError{Err: "timeout", IsTimeout: true}, true},
	} {
		for _, kiro := range []bool{true, false} {
			t.Run(fmt.Sprintf("%s/kiro=%v", tc.name, kiro), func(t *testing.T) {
				wrapped := fmt.Errorf("upstream: %w", &net.OpError{Op: "remote error", Net: "tcp", Err: tc.cause})
				calls := 0
				broken := roundTripFunc(func(*http.Request) (*http.Response, error) {
					calls++
					return nil, wrapped
				})
				request := clientRequest(tbOf(t), "http://unused", "openai", true)
				if !kiro {
					request.Header.Del(HeaderProvider)
				}
				resp, err := NewTransport(broken).RoundTrip(request)
				// 超时类错误被刻意附加 ErrUpstreamTimeout 标记(转发面据此
				// 判 504),不再保持原错误标识;其余错误仍须原样透传。
				timeoutMarked := kiro && tc.name == "timeout"
				if resp != nil || !errors.Is(err, tc.cause) || timeoutMarked != errors.Is(err, ErrUpstreamTimeout) {
					t.Fatalf("security/network error was changed or swallowed: resp=%v err=%v", resp, err)
				}
				if !timeoutMarked && err != wrapped {
					t.Fatalf("non-timeout error identity changed: %v", err)
				}
				wantCalls := 1
				if kiro && tc.retry {
					wantCalls = maxRetryAttempts
				}
				if calls != wantCalls {
					t.Errorf("calls=%d want=%d cause=%v", calls, wantCalls, tc.cause)
				}
			})
		}
	}
}

func TestRound27StrictOpenAIContentKey(t *testing.T) {
	for _, tc := range []struct {
		name, thinking string
		text, tools    bool
	}{
		{name: "only-fake-thinking", thinking: "fake"},
		{name: "only-native-thinking", thinking: "native"},
		{name: "fake-thinking+text", thinking: "fake", text: true},
		{name: "native-thinking+text", thinking: "native", text: true},
		{name: "text-only", text: true},
		{name: "empty"},
		{name: "tools-only", tools: true},
		{name: "fake-thinking+tools", thinking: "fake", tools: true},
		{name: "native-thinking+tools", thinking: "native", tools: true},
	} {
		for _, policy := range []string{"auto", "none", "required", "named"} {
			if policy == "none" && tc.tools || (policy == "required" || policy == "named") && !tc.tools {
				continue
			}
			for _, stream := range []bool{false, true} {
				for _, retry := range []bool{false, true} {
					if retry && policy == "auto" {
						continue
					}
					t.Run(fmt.Sprintf("%s/%s/stream=%v/retry=%v", tc.name, policy, stream, retry), func(t *testing.T) {
						var choice any = policy
						if policy == "named" {
							choice = object{"type": "function", "function": object{"name": "lookup"}}
						}
						root := object{"model": "claude-sonnet-4-6", "max_tokens": 32, "stream": stream,
							"messages": []any{object{"role": "user", "content": "round27-current-request"}},
							"tools":    toolDefinition("openai"), "tool_choice": choice}
						// NewRequest binds Body and GetBody to the same payload; replacing
						// clientRequest.Body alone would replay its stale auto request.
						request, err := http.NewRequest(http.MethodPost, "http://unused/v1/chat/completions", strings.NewReader(jsonText(root)))
						if err != nil {
							t.Fatal(err)
						}
						for k, v := range Headers("native-token", "arn:aws:codewhisperer:eu-west-1:123:profile/p") {
							request.Header.Set(k, v)
						}
						wire := []byte{}
						var wantReasonPieces []string
						switch tc.thinking {
						case "fake":
							wire = joinedFrames(frame("assistantResponseEvent", object{"content": "<think>reason-"}),
								frame("assistantResponseEvent", object{"content": "tail</think>"}))
							wantReasonPieces = []string{"reason-tail"}
						case "native":
							wire = joinedFrames(frame("assistantResponseEvent", object{"text": "reason-"}),
								frame("assistantResponseEvent", object{"text": "tail"}))
							wantReasonPieces = []string{"reason-", "tail"}
						}
						wantContent, wantReason, wantFinish := "", strings.Join(wantReasonPieces, ""), "stop"
						if tc.text {
							// Identical body frames remain two real deltas.
							wire = joinedFrames(wire, frame("assistantResponseEvent", object{"content": "ha"}), frame("assistantResponseEvent", object{"content": "ha"}))
							wantContent = "haha"
						}
						if tc.tools {
							wire = joinedFrames(wire, frame("toolUseEvent", object{"name": "lookup", "toolUseId": "round27-tool", "input": `{"x":`}),
								frame("toolUseEvent", object{"input": "1}", "stop": true}))
							wantFinish = "tool_calls"
						}
						wire = joinedFrames(wire, tokenFrame())
						calls := 0
						upstream := roundTripFunc(func(r *http.Request) (*http.Response, error) {
							calls++
							payload, err := io.ReadAll(r.Body)
							if err != nil {
								t.Fatal(err)
							}
							if r.URL.Path != "/generateAssistantResponse" || !strings.Contains(string(payload), "round27-current-request") ||
								strings.Contains(string(payload), "[Tool Policy Recovery]") != (retry && calls == 2) ||
								strings.Contains(string(payload), "[Tool Policy]") != (policy != "auto") {
								t.Fatalf("call=%d path=%s payload=%s", calls, r.URL.Path, payload)
							}
							body := wire
							if retry && calls == 1 {
								name := "unlisted"
								if policy == "none" {
									name = "lookup"
								}
								body = joinedFrames(frame("assistantResponseEvent", object{"text": "FIRST_RESPONSE_REASON_SECRET"}),
									frame("assistantResponseEvent", object{"content": "FIRST_RESPONSE_TEXT_SECRET"}),
									frame("toolUseEvent", object{"name": name, "toolUseId": "FIRST_RESPONSE_TOOL_SECRET", "input": object{}, "stop": true}), endFrame())
							}
							return &http.Response{StatusCode: 200, Header: http.Header{}, Body: io.NopCloser(bytes.NewReader(body)), Request: r}, nil
						})
						response, err := NewTransport(upstream).RoundTrip(request)
						if err != nil {
							t.Fatal(err)
						}
						data, err := io.ReadAll(response.Body)
						response.Body.Close()
						wantCalls := 1
						if retry {
							wantCalls = 2
						}
						if err != nil || response.StatusCode != 200 || calls != wantCalls || strings.Contains(string(data), "FIRST_RESPONSE_") {
							t.Fatalf("status=%d calls=%d want=%d err=%v body=%s", response.StatusCode, calls, wantCalls, err, data)
						}
						var message, usage object
						var tools []any
						if stream {
							if response.Header.Get("Content-Type") != "text/event-stream" || strings.Count(string(data), "data: [DONE]\n\n") != 1 {
								t.Fatalf("invalid SSE response: headers=%v body=%s", response.Header, data)
							}
							parsed := events(t, data)
							var reasonPieces, textPieces []string
							roles, emptyContents, finishes := 0, 0, 0
							message = object{}
							for i, event := range parsed {
								if event["done"] == true {
									if i != len(parsed)-1 {
										t.Fatal("DONE is not last")
									}
									continue
								}
								choices := list(event["choices"])
								if len(choices) != 1 || event["object"] != "chat.completion.chunk" {
									t.Fatalf("invalid chunk: %v", event)
								}
								c := obj(choices[0])
								delta := obj(c["delta"])
								if role, ok := delta["role"]; ok {
									roles++
									message["role"] = role
									if i != 0 || role != "assistant" {
										t.Fatalf("unexpected role delta: %v", delta)
									}
								}
								if v, ok := delta["content"]; ok {
									text, ok := v.(string)
									if !ok {
										t.Fatalf("content must be a string: %v", delta)
									}
									message["content"] = str(message["content"]) + text
									if text == "" {
										emptyContents++
										if i != 0 || policy == "auto" {
											t.Fatalf("unexpected empty content: %v", delta)
										}
									} else {
										textPieces = append(textPieces, text)
									}
								}
								if v, ok := delta["reasoning_content"]; ok {
									reasonPieces = append(reasonPieces, str(v))
									if i == 0 {
										content, exists := delta["content"]
										if exists != (policy != "auto") || exists && content != "" {
											t.Errorf("first reasoning delta content: policy=%s delta=%v", policy, delta)
										}
									}
								}
								tools = append(tools, list(delta["tool_calls"])...)
								if c["finish_reason"] != nil {
									finishes++
									usage = obj(event["usage"])
									if c["finish_reason"] != wantFinish || len(delta) != 0 || i != len(parsed)-2 {
										t.Fatalf("invalid finish chunk: %v", event)
									}
								}
							}
							message["reasoning_content"] = strings.Join(reasonPieces, "")
							wantRoles, wantEmpty := 1, 0
							if policy == "auto" && tc.thinking == "" && !tc.text {
								wantRoles = 0
							}
							if policy != "auto" && (tc.thinking != "" || !tc.text) {
								wantEmpty = 1
							}
							if roles != wantRoles || emptyContents != wantEmpty || finishes != 1 ||
								jsonText(reasonPieces) != jsonText(wantReasonPieces) || tc.text && jsonText(textPieces) != `["ha","ha"]` {
								t.Errorf("roles=%d emptyContents=%d finishes=%d reasonPieces=%v textPieces=%v body=%s", roles, emptyContents, finishes, reasonPieces, textPieces, data)
							}
							if policy != "auto" {
								if _, exists := message["content"]; !exists {
									t.Errorf("strict streamed message missing content key: %s", data)
								}
							}
						} else {
							if response.Header.Get("Content-Type") != "application/json" {
								t.Fatalf("headers=%v", response.Header)
							}
							result := parseResult(t, data)
							c := obj(list(result["choices"])[0])
							message, usage = obj(c["message"]), obj(result["usage"])
							tools = list(message["tool_calls"])
							if content, exists := message["content"]; !exists || content != wantContent || message["role"] != "assistant" || c["finish_reason"] != wantFinish {
								t.Fatalf("invalid completion: %s", data)
							}
						}
						if str(message["content"]) != wantContent || str(message["reasoning_content"]) != wantReason || number(usage["prompt_tokens"]) != 17 || number(usage["completion_tokens"]) != 9 {
							t.Fatalf("message=%v usage=%v body=%s", message, usage, data)
						}
						wantTools := 0
						if tc.tools {
							wantTools = 1
						}
						if len(tools) != wantTools {
							t.Fatalf("tools=%v", tools)
						}
						if tc.tools {
							tool := obj(tools[0])
							if tool["id"] != "round27-tool" || obj(tool["function"])["name"] != "lookup" || obj(tool["function"])["arguments"] != `{"x": 1}` {
								t.Fatalf("tool=%v", tool)
							}
						}
					})
				}
			}
		}
	}
}

func TestRound26AliasDedupWireMatrix(t *testing.T) {
	const original = "round26.wire.lookup"
	registerToolNames([]string{original})
	alias := aliasToolName(original)
	wire := joinedFrames(frame("toolUseEvent", object{"name": alias, "toolUseId": "a", "input": object{}, "stop": true}), frame("toolUseEvent", object{"name": original, "toolUseId": "b", "input": object{}, "stop": true}), endFrame())
	for _, protocol := range []string{"anthropic", "openai"} {
		for _, stream := range []bool{false, true} {
			for _, strict := range []bool{false, true} {
				t.Run(fmt.Sprintf("%s/stream=%v/strict=%v", protocol, stream, strict), func(t *testing.T) {
					var choice any = "auto"
					if strict {
						choice = "required"
					}
					if protocol == "anthropic" {
						choice = object{"type": "auto"}
						if strict {
							choice = object{"type": "any"}
						}
					}
					tools := toolDefinition(protocol)
					if protocol == "anthropic" {
						obj(tools[0])["name"] = original
					} else {
						obj(obj(tools[0])["function"])["name"] = original
					}
					root := object{"model": "claude-sonnet-4-6", "max_tokens": 32, "stream": stream, "messages": []any{object{"role": "user", "content": "hello"}}, "tools": tools, "tool_choice": choice}
					upstream := roundTripFunc(func(r *http.Request) (*http.Response, error) {
						return &http.Response{StatusCode: 200, Header: http.Header{}, Body: io.NopCloser(bytes.NewReader(wire)), Request: r}, nil
					})
					request := clientRequest(tbOf(t), "http://unused", protocol, stream)
					request.Body = io.NopCloser(strings.NewReader(jsonText(root)))
					request.GetBody = nil
					response, err := NewTransport(upstream).RoundTrip(request)
					if err != nil {
						t.Fatal(err)
					}
					data, err := io.ReadAll(response.Body)
					response.Body.Close()
					if err != nil {
						t.Fatal(err)
					}
					want := 2
					if protocol == "openai" && !strict {
						want = 1
					}
					if count := strings.Count(string(data), `"name":"`+original+`"`); count != want || strings.Contains(string(data), alias) {
						t.Fatalf("restored tool count=%d want=%d body=%s", count, want, data)
					}
				})
			}
		}
	}
}

func TestRound26FallbackToolIDRecoveryWire(t *testing.T) {
	for _, id := range []any{"", nil, false, json.Number("0")} {
		t.Run(fmt.Sprintf("%T/%v", id, id), func(t *testing.T) {
			calls := 0
			upstream := roundTripFunc(func(r *http.Request) (*http.Response, error) {
				calls++
				payload, err := io.ReadAll(r.Body)
				if err != nil {
					t.Fatal(err)
				}
				if strings.Contains(string(payload), "[API Limitation] Your tool call was truncated") != (calls == 2) {
					t.Errorf("recovery notice differs on call %d: payload=%s", calls, payload)
				}
				wire := joinedFrames(frame("assistantResponseEvent", object{"content": "fixed"}), endFrame())
				if calls == 1 {
					wire = frame("toolUseEvent", object{"name": "lookup", "toolUseId": id, "input": `{"x":`, "stop": true})
				}
				return &http.Response{StatusCode: 200, Header: http.Header{}, Body: io.NopCloser(bytes.NewReader(wire)), Request: r}, nil
			})
			transport := NewTransport(upstream)
			request := clientRequest(tbOf(t), "http://unused", "anthropic", true)
			response, err := transport.RoundTrip(request)
			if err != nil {
				t.Fatal(err)
			}
			data, err := io.ReadAll(response.Body)
			response.Body.Close()
			if err != nil {
				t.Fatal(err)
			}
			outputID := ""
			for _, event := range events(t, data) {
				if block := obj(event["content_block"]); block["type"] == "tool_use" {
					outputID = str(block["id"])
				}
			}
			if !strings.HasPrefix(outputID, "toolu_") {
				t.Fatalf("missing fallback id: %s", data)
			}
			t.Cleanup(func() { popToolTruncation(outputID) })
			root := object{"model": "claude-sonnet-4-6", "max_tokens": 32, "messages": []any{
				object{"role": "user", "content": "q"},
				object{"role": "assistant", "content": []any{object{"type": "tool_use", "id": outputID, "name": "lookup", "input": object{}}}},
				object{"role": "user", "content": []any{object{"type": "tool_result", "tool_use_id": outputID, "content": "retry"}}},
			}, "tools": toolDefinition("anthropic")}
			for attempt := 0; attempt < 2; attempt++ {
				retry := clientRequest(tbOf(t), "http://unused", "anthropic", false)
				retry.Body = io.NopCloser(strings.NewReader(jsonText(root)))
				retry.GetBody = nil
				response, err := transport.RoundTrip(retry)
				if err != nil {
					t.Fatal(err)
				}
				_, err = io.ReadAll(response.Body)
				response.Body.Close()
				if err != nil {
					t.Fatal(err)
				}
			}
			if calls != 3 {
				t.Fatalf("calls=%d", calls)
			}
		})
	}
}

func TestRound26WireBoundaryMatrix(t *testing.T) {
	for _, protocol := range []string{"anthropic", "openai"} {
		for _, stream := range []bool{false, true} {
			for _, policy := range []string{"auto", "required", "named"} {
				t.Run(fmt.Sprintf("%s/stream=%v/%s", protocol, stream, policy), func(t *testing.T) {
					rawFrame := func(raw string) []byte {
						headers := append(stringHeader(":message-type", "event"), stringHeader(":event-type", "assistantResponseEvent")...)
						return frameWithHeaders(headers, []byte(raw))
					}
					wire := joinedFrames(
						rawFrame(`{"content":"visible","input":"{}","usage":99}`),
						rawFrame(`{"text":"native reason"}`), rawFrame(`{"signature":"s1"}`), rawFrame(`{"signature":"s2"}`),
						rawFrame(`{"name":"lookup","toolUseId":25,"input":{"x":1},"stop":true}`),
						rawFrame(`{"name":"lookup","toolUseId":25.0,"input":{"x":2,"y":"中"},"stop":true}`),
						rawFrame(`{"usage":{"inputTokens":17,"outputTokens":29}}`),
						rawFrame(`{"usage":0.25}`), rawFrame(`{"contextUsagePercentage":0}`),
					)
					calls := 0
					upstream := roundTripFunc(func(r *http.Request) (*http.Response, error) {
						calls++
						return &http.Response{StatusCode: 200, Header: http.Header{}, Body: io.NopCloser(bytes.NewReader(wire)), Request: r}, nil
					})
					var choice any = policy
					if protocol == "anthropic" {
						choice = object{"type": "auto"}
						if policy == "required" {
							choice = object{"type": "any"}
						}
						if policy == "named" {
							choice = object{"type": "tool", "name": "lookup"}
						}
					} else if policy == "named" {
						choice = object{"type": "function", "function": object{"name": "lookup"}}
					}
					root := object{"model": "claude-sonnet-4-6", "max_tokens": 32, "stream": stream, "messages": []any{object{"role": "user", "content": "hello"}}, "tools": toolDefinition(protocol), "tool_choice": choice}
					request := clientRequest(tbOf(t), "http://unused", protocol, stream)
					request.Body = io.NopCloser(strings.NewReader(jsonText(root)))
					request.GetBody = nil
					response, err := NewTransport(upstream).RoundTrip(request)
					if err != nil {
						t.Fatal(err)
					}
					data, err := io.ReadAll(response.Body)
					response.Body.Close()
					if err != nil || response.StatusCode != 200 || calls != 1 {
						t.Fatalf("status=%d calls=%d err=%v body=%s", response.StatusCode, calls, err, data)
					}
					tools := []object{}
					var usage object
					if stream {
						for _, event := range events(t, data) {
							if protocol == "anthropic" {
								if event["type"] == "message_start" && number(obj(obj(event["message"])["usage"])["output_tokens"]) != 0 {
									t.Fatal("nonzero start usage")
								}
								if block := obj(event["content_block"]); block["type"] == "tool_use" {
									tools = append(tools, block)
								}
								if event["type"] == "message_delta" {
									usage = obj(event["usage"])
								}
							} else {
								for _, choice := range list(event["choices"]) {
									for _, tool := range list(obj(obj(choice)["delta"])["tool_calls"]) {
										tools = append(tools, obj(tool))
									}
								}
								if event["usage"] != nil {
									usage = obj(event["usage"])
								}
							}
						}
					} else {
						result := parseResult(t, data)
						usage = obj(result["usage"])
						if protocol == "anthropic" {
							for _, block := range list(result["content"]) {
								if obj(block)["type"] == "tool_use" {
									tools = append(tools, obj(block))
								}
							}
						} else {
							for _, tool := range list(obj(obj(list(result["choices"])[0])["message"])["tool_calls"]) {
								tools = append(tools, obj(tool))
							}
						}
					}
					if len(tools) != 1 || fmt.Sprint(tools[0]["id"]) != "25.0" || fmt.Sprint(usage["credits_used"]) != "0.25" || !strings.Contains(string(data), "visible") || !strings.Contains(string(data), "native reason") {
						t.Fatalf("tools=%v usage=%v body=%s", tools, usage, data)
					}
					if protocol == "openai" {
						want := `{"x": 2, "y": "中"}`
						if policy == "auto" {
							want = `{"x": 2, "y": "\u4e2d"}`
						}
						if str(obj(tools[0]["function"])["arguments"]) != want {
							t.Fatalf("arguments=%v want=%s", obj(tools[0]["function"])["arguments"], want)
						}
					}
					key := "output_tokens"
					if protocol == "openai" {
						key = "completion_tokens"
					}
					if number(usage[key]) != 29 {
						t.Fatalf("final usage=%v", usage)
					}
				})
			}
		}
	}
}

func TestRound25EmptyNativeThinkingLifecycle(t *testing.T) {
	for _, finish := range []string{"text", "finalize"} {
		t.Run(finish, func(t *testing.T) {
			s := newResponseState(requestOptions{protocol: "anthropic", stream: true})
			var emitted []object
			s.emit = func(_ string, event object) { emitted = append(emitted, event) }
			for _, data := range []object{{"text": ""}, {"text": ""}, {"signature": "real-signature"}} {
				if err := s.accept(wireEvent{kind: "assistantResponseEvent", data: data}); err != nil {
					t.Fatal(err)
				}
			}
			if len(emitted) != 2 || obj(emitted[0]["content_block"])["thinking"] != "" || obj(emitted[1]["delta"])["signature"] != "real-signature" || s.outputRunes != 0 {
				t.Fatalf("empty thinking lifecycle=%v runes=%d", emitted, s.outputRunes)
			}
			if finish == "text" {
				if err := s.accept(wireEvent{data: object{"content": "answer"}}); err != nil {
					t.Fatal(err)
				}
			}
			s.terminal = true
			if err := s.finalize(); err != nil {
				t.Fatal(err)
			}
			stops := 0
			for _, event := range emitted {
				if event["type"] == "content_block_stop" && event["index"] == 0 {
					stops++
				}
			}
			if stops != 1 {
				t.Fatalf("thinking stops=%d events=%v", stops, emitted)
			}
		})
	}
	for _, options := range []requestOptions{{protocol: "anthropic"}, {protocol: "openai", stream: true}, {protocol: "anthropic", stream: true, policyMode: "none"}} {
		s := newResponseState(options)
		if err := s.accept(wireEvent{data: object{"text": ""}}); err != nil {
			t.Fatal(err)
		}
		if s.blockIndex != -1 || len(s.blocks) != 0 {
			t.Fatalf("empty frame leaked into collect/OpenAI: %+v", options)
		}
	}
}

func TestRound25ThinkingPayloadDispatch(t *testing.T) {
	base := frame("assistantResponseEvent", object{})
	hlen := binary.BigEndian.Uint32(base[4:8])
	reader := eventReader{r: bytes.NewReader(frameWithHeaders(base[12:12+hlen], []byte(`{"text":"native reasoning"}`)))}
	e, err := reader.next()
	if err != nil {
		t.Fatal(err)
	}
	s := newResponseState(requestOptions{protocol: "anthropic"})
	if err := s.accept(e); err != nil {
		t.Fatal(err)
	}
	if s.fullThinking.String() != "native reasoning" {
		t.Fatalf("thinking=%q", s.fullThinking.String())
	}
}

func TestRound25SingleFrameDispatch(t *testing.T) {
	for _, tc := range []struct {
		raw, text string
		terminal  bool
	}{
		{`{"content":"visible","usage":0.25}`, "visible", false},
		{`{"usage":0.25,"content":"hidden"}`, "", true},
		{`{"content":"visible","input":"{}"}`, "visible", false},
		{`{"content":"visible","contextUsagePercentage":25}`, "visible", false},
		{`{"contextUsagePercentage":25,"content":"hidden"}`, "", true},
		{`{"followupPrompt":"suggestion","content":"hidden","usage":0.25}`, "", false},
	} {
		t.Run(tc.raw, func(t *testing.T) {
			base := frame("assistantResponseEvent", object{})
			hlen := binary.BigEndian.Uint32(base[4:8])
			reader := eventReader{r: bytes.NewReader(frameWithHeaders(base[12:12+hlen], []byte(tc.raw)))}
			e, err := reader.next()
			if err != nil {
				t.Fatal(err)
			}
			s := newResponseState(requestOptions{protocol: "openai"})
			if err := s.accept(e); err != nil {
				t.Fatal(err)
			}
			if s.fullText.String() != tc.text || s.terminal != tc.terminal {
				t.Fatalf("text=%q terminal=%v event=%v", s.fullText.String(), s.terminal, e.data)
			}
		})
	}
}

func TestRound25ThinkingBudgetIntegers(t *testing.T) {
	for _, tc := range []struct{ raw, want string }{
		{`5000`, "5000"}, {`5000.0`, "disabled"}, {`5e3`, "disabled"},
		{`1099511627777`, "10000"}, {`999999999999999999999999999999999999999`, "10000"},
		{`0`, "disabled"}, {`-1`, "disabled"}, {`true`, "disabled"},
	} {
		t.Run(tc.raw, func(t *testing.T) {
			root, err := decodeObject(`{"thinking":{"budget_tokens":` + tc.raw + `},"reasoning_effort":"none"}`)
			if err != nil {
				t.Fatal(err)
			}
			cfg := extractThinking(root, "anthropic")
			if tc.want == "disabled" {
				if !cfg.disabled {
					t.Fatalf("cfg=%+v", cfg)
				}
			} else if cfg.disabled || !strings.Contains(thinkingTagsPrefix(cfg), "<max_thinking_length>"+tc.want+"</max_thinking_length>") {
				t.Fatalf("cfg=%+v prefix=%s", cfg, thinkingTagsPrefix(cfg))
			}
		})
	}
}

// stubTimeout 把首 token 等待缩到毫秒级。
func stubTimeout(t *testing.T, d time.Duration) {
	t.Helper()
	orig := firstTokenTimeout
	firstTokenTimeout = func(string) time.Duration { return d }
	t.Cleanup(func() { firstTokenTimeout = orig })
}

func TestFirstTokenTimeoutRetry(t *testing.T) {
	stubTimeout(t, 50*time.Millisecond)
	var calls atomic.Int32
	wire := joinedFrames(frame("assistantResponseEvent", object{"content": "hi"}), endFrame())
	// 第一次请求 200 但不出字节,触发首 token 超时重发;第二次正常返回。
	slow := roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if calls.Add(1) == 1 {
			return newHangResponse(r), nil
		}
		return &http.Response{StatusCode: 200, Header: http.Header{}, Body: io.NopCloser(bytes.NewReader(wire)), Request: r}, nil
	})
	r := clientRequest(tbOf(t), "http://unused", "openai", true)
	resp, err := NewTransport(slow).RoundTrip(r)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(resp.Body)
	if err != nil || !strings.Contains(string(data), "hi") {
		t.Fatalf("data=%q err=%v", data, err)
	}
	if calls.Load() != 2 {
		t.Fatalf("calls=%d", calls.Load())
	}
}

func TestFirstTokenTimeoutExhausted(t *testing.T) {
	stubTimeout(t, 50*time.Millisecond)
	var calls atomic.Int32
	hang := roundTripFunc(func(r *http.Request) (*http.Response, error) {
		calls.Add(1)
		w := newHangResponse(r)
		return w, nil
	})
	r := clientRequest(tbOf(t), "http://unused", "openai", true)
	_, err := NewTransport(hang).RoundTrip(r)
	if err == nil || !strings.Contains(err.Error(), "did not respond") {
		t.Fatalf("err=%v", err)
	}
	if calls.Load() != firstTokenMaxAttempts {
		t.Fatalf("calls=%d", calls.Load())
	}
}

// newHangResponse 返回一个 200 但 body 永不出字节的响应。
func newHangResponse(r *http.Request) *http.Response {
	return &http.Response{
		StatusCode: 200,
		Header:     http.Header{},
		Body:       &hangBody{closed: make(chan struct{})},
		Request:    r,
	}
}

type hangBody struct {
	closed chan struct{}
	once   sync.Once
}

func (b *hangBody) Read([]byte) (int, error) {
	<-b.closed
	return 0, io.EOF
}
func (b *hangBody) Close() error {
	b.once.Do(func() { close(b.closed) })
	return nil
}

// dripBody 第一次 Read 交出数据,之后挂起直到 Close,模拟流中段停滞。
type dripBody struct {
	data   []byte
	sent   bool
	closed chan struct{}
	once   sync.Once
}

func (b *dripBody) Read(p []byte) (int, error) {
	if !b.sent {
		b.sent = true
		return copy(p, b.data), nil
	}
	<-b.closed
	return 0, io.EOF
}
func (b *dripBody) Close() error {
	b.once.Do(func() { close(b.closed) })
	return nil
}

func TestMidStreamStallTimeout(t *testing.T) {
	orig := streamStallTimeout
	streamStallTimeout = 50 * time.Millisecond
	t.Cleanup(func() { streamStallTimeout = orig })
	drip := &dripBody{data: joinedFrames(frame("assistantResponseEvent", object{"content": "hi"})), closed: make(chan struct{})}
	stalled := roundTripFunc(func(r *http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 200, Header: http.Header{}, Body: drip, Request: r}, nil
	})
	r := clientRequest(tbOf(t), "http://unused", "openai", true)
	resp, err := NewTransport(stalled).RoundTrip(r)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if _, err = io.ReadAll(resp.Body); err == nil || !strings.Contains(err.Error(), "stalled") {
		t.Fatalf("err=%v", err)
	}
}

func TestNetworkErrorRetry(t *testing.T) {
	var calls atomic.Int32
	wire := joinedFrames(frame("assistantResponseEvent", object{"content": "ok"}), endFrame())
	flaky := roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if calls.Add(1) <= 2 {
			return nil, &net.OpError{Op: "read", Err: errors.New("connection reset by peer")}
		}
		return &http.Response{StatusCode: 200, Header: http.Header{}, Body: io.NopCloser(bytes.NewReader(wire)), Request: r}, nil
	})
	origBackoff := retryBackoff
	retryBackoff = func(int) time.Duration { return time.Millisecond }
	t.Cleanup(func() { retryBackoff = origBackoff })
	r := clientRequest(tbOf(t), "http://unused", "openai", true)
	resp, err := NewTransport(flaky).RoundTrip(r)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if _, err := io.ReadAll(resp.Body); err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 3 {
		t.Fatalf("calls=%d", calls.Load())
	}
}

func TestNetworkErrorNotRetryable(t *testing.T) {
	var calls atomic.Int32
	// SSL 类错误不是 net.Error,不重试(network_errors.py)。
	broken := roundTripFunc(func(r *http.Request) (*http.Response, error) {
		calls.Add(1)
		return nil, errors.New("tls: handshake failure")
	})
	r := clientRequest(tbOf(t), "http://unused", "openai", true)
	if _, err := NewTransport(broken).RoundTrip(r); err == nil {
		t.Fatal("expected error")
	}
	if calls.Load() != 1 {
		t.Fatalf("calls=%d", calls.Load())
	}
}

func TestWrappedTLSFailureNotRetried(t *testing.T) {
	for _, cause := range []error{
		&tls.CertificateVerificationError{Err: x509.UnknownAuthorityError{}},
		x509.UnknownAuthorityError{},
		x509.HostnameError{Certificate: &x509.Certificate{}, Host: "invalid.test"},
		x509.CertificateInvalidError{Cert: &x509.Certificate{}, Reason: x509.Expired},
		tls.RecordHeaderError{Msg: "invalid TLS record"},
	} {
		t.Run(fmt.Sprintf("%T", cause), func(t *testing.T) {
			var calls atomic.Int32
			wrapped := &net.OpError{Op: "remote error", Net: "tcp", Err: cause}
			broken := roundTripFunc(func(*http.Request) (*http.Response, error) {
				calls.Add(1)
				return nil, wrapped
			})
			if _, err := NewTransport(broken).RoundTrip(clientRequest(tbOf(t), "http://unused", "openai", true)); !errors.Is(err, wrapped) {
				t.Fatalf("err=%v", err)
			}
			if calls.Load() != 1 {
				t.Fatalf("calls=%d", calls.Load())
			}
		})
	}
}

func TestStrictToolFinalValidationProtocols(t *testing.T) {
	for _, protocol := range []string{"openai", "anthropic"} {
		for _, stream := range []bool{false, true} {
			for _, args := range []string{`[]`, `null`, `1`, `"text"`, `true`, `{"x":`} {
				t.Run(fmt.Sprintf("%s/%v/%s", protocol, stream, args), func(t *testing.T) {
					id := "matrix-healed"
					wire := joinedFrames(frame("toolUseEvent", object{"name": "lookup", "toolUseId": id, "input": args, "stop": true}), endFrame())
					if args == `{"x":` {
						wire = joinedFrames(frame("toolUseEvent", object{"name": "lookup", "toolUseId": id, "input": args, "stop": true}),
							frame("toolUseEvent", object{"name": "lookup", "toolUseId": id, "input": `{"x":1}`, "stop": true}), endFrame())
					}
					good := joinedFrames(frame("toolUseEvent", object{"name": "lookup", "input": `{"x":1}`, "stop": true}), endFrame())
					var calls atomic.Int32
					server := seqStub(t, [][]byte{wire, good}, func(n int, _ object) { calls.Store(int32(n + 1)) })
					r := clientRequest(tbOf(t), server.URL, protocol, stream)
					body, err := io.ReadAll(r.Body)
					r.Body.Close()
					if err != nil {
						t.Fatal(err)
					}
					root, err := decodeObject(string(body))
					if err != nil {
						t.Fatal(err)
					}
					root["tool_choice"] = "required"
					if protocol == "anthropic" {
						root["tool_choice"] = object{"type": "any"}
					}
					request, err := http.NewRequest("POST", r.URL.String(), strings.NewReader(jsonText(root)))
					if err != nil {
						t.Fatal(err)
					}
					request.Header = r.Header.Clone()
					resp, err := NewTransport(nil).RoundTrip(request)
					if err != nil {
						t.Fatal(err)
					}
					data, err := io.ReadAll(resp.Body)
					resp.Body.Close()
					want := int32(2)
					if args == `{"x":` {
						want = 1
					}
					if err != nil || resp.StatusCode != 200 || calls.Load() != want || !strings.Contains(string(data), "lookup") {
						t.Fatalf("status=%d calls=%d want=%d data=%s err=%v", resp.StatusCode, calls.Load(), want, data, err)
					}
				})
			}
		}
	}
}

func TestStrictAnthropicThinkingReplay(t *testing.T) {
	for _, tc := range []struct {
		name   string
		frames [][]byte
		sig    string
	}{
		{"late-signature", [][]byte{
			frame("reasoningContentEvent", object{"text": "secret"}),
			frame("assistantResponseEvent", object{"content": "answer"}),
			frame("reasoningContentEvent", object{"signature": "s1"}),
			frame("reasoningContentEvent", object{"signature": "s2"}),
		}, "s2"},
		{"fallback", [][]byte{
			frame("reasoningContentEvent", object{"text": "secret"}),
			frame("assistantResponseEvent", object{"content": "answer"}),
		}, ""},
		{"merged-thinking-first", [][]byte{
			frame("assistantResponseEvent", object{"content": "ans"}),
			frame("reasoningContentEvent", object{"text": "sec"}),
			frame("assistantResponseEvent", object{"content": "wer"}),
			frame("reasoningContentEvent", object{"text": "ret"}),
			frame("reasoningContentEvent", object{"signature": "s1"}),
			frame("reasoningContentEvent", object{"signature": "s2"}),
		}, "s2"},
	} {
		for _, stream := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/stream=%v", tc.name, stream), func(t *testing.T) {
				wire := joinedFrames(append(tc.frames, endFrame())...)
				server := stub(t, wire, nil)
				root := object{
					"model": "claude-sonnet-4-6", "max_tokens": 1024, "stream": stream,
					"messages": []any{object{"role": "user", "content": "hello"}},
					"tools":    toolDefinition("anthropic"), "tool_choice": object{"type": "none"},
				}
				request, err := http.NewRequest("POST", server.URL+"/v1/messages", strings.NewReader(jsonText(root)))
				if err != nil {
					t.Fatal(err)
				}
				request.Header = clientRequest(tbOf(t), server.URL, "anthropic", stream).Header.Clone()
				resp, err := NewTransport(nil).RoundTrip(request)
				if err != nil {
					t.Fatal(err)
				}
				data, err := io.ReadAll(resp.Body)
				resp.Body.Close()
				if err != nil || resp.StatusCode != 200 {
					t.Fatalf("status=%d data=%s err=%v", resp.StatusCode, data, err)
				}
				var kinds []string
				var thinking, text, signature string
				var usage object
				if stream {
					stops := 0
					for _, event := range events(t, data) {
						switch str(event["type"]) {
						case "content_block_start":
							block := obj(event["content_block"])
							if number(event["index"]) != len(kinds) || str(block["thinking"]) != "" || str(block["text"]) != "" || str(block["signature"]) != "" {
								t.Fatalf("nonempty or misindexed block start: %v", event)
							}
							kinds = append(kinds, str(block["type"]))
						case "content_block_delta":
							delta := obj(event["delta"])
							thinking += str(delta["thinking"])
							text += str(delta["text"])
							if delta["type"] == "signature_delta" {
								if number(event["index"]) != 0 || signature != "" {
									t.Fatalf("signature must occur once in the first block: %v", event)
								}
								signature = str(delta["signature"])
							}
						case "content_block_stop":
							if number(event["index"]) != stops {
								t.Fatalf("misindexed block stop: %v", event)
							}
							stops++
						case "message_delta":
							usage = obj(event["usage"])
							if obj(event["delta"])["stop_reason"] != "end_turn" {
								t.Fatalf("unexpected stop reason: %v", event)
							}
						}
					}
					if stops != 2 || !strings.HasSuffix(string(data), "event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n") {
						t.Fatalf("incomplete replay: %s", data)
					}
				} else {
					result := parseResult(t, data)
					usage = obj(result["usage"])
					for _, value := range list(result["content"]) {
						block := obj(value)
						kinds = append(kinds, str(block["type"]))
						thinking += str(block["thinking"])
						text += str(block["text"])
						signature += str(block["signature"])
					}
				}
				if strings.Join(kinds, ",") != "thinking,text" || thinking != "secret" || text != "answer" {
					t.Fatalf("kinds=%v thinking=%q text=%q data=%s", kinds, thinking, text, data)
				}
				if (tc.sig != "" && signature != tc.sig) || (tc.sig == "" && !strings.HasPrefix(signature, "sig_")) {
					t.Fatalf("signature=%q want=%q (empty means sig_ fallback) data=%s", signature, tc.sig, data)
				}
				if number(usage["output_tokens"]) != 4 {
					t.Fatalf("replay changed token accounting: usage=%v", usage)
				}
			})
		}
	}
}

func TestOrdinaryAnthropicLateSignatureIgnored(t *testing.T) {
	wire := joinedFrames(
		frame("reasoningContentEvent", object{"text": "secret"}),
		frame("assistantResponseEvent", object{"content": "answer"}),
		frame("reasoningContentEvent", object{"signature": "s1"}),
		frame("reasoningContentEvent", object{"signature": "s2"}),
		endFrame(),
	)
	server := stub(t, wire, nil)
	_, data, err := do(t, server.URL, "anthropic", true)
	if err != nil {
		t.Fatal(err)
	}
	var thinking, text string
	for _, event := range events(t, data) {
		delta := obj(event["delta"])
		if delta["type"] == "signature_delta" {
			t.Fatalf("ordinary streaming must ignore late signatures: %s", data)
		}
		thinking += str(delta["thinking"])
		text += str(delta["text"])
	}
	if thinking != "secret" || text != "answer" {
		t.Fatalf("thinking=%q text=%q data=%s", thinking, text, data)
	}
}

func TestStrictAnthropicToolReplayArgumentOrder(t *testing.T) {
	wire := joinedFrames(
		frame("toolUseEvent", object{"name": "lookup", "toolUseId": "first", "input": `{"z":"\u4e2d","a":1}`, "stop": true}),
		frame("toolUseEvent", object{"name": "lookup", "toolUseId": "second", "input": `{"b":2,"a":3}`, "stop": true}),
		endFrame(),
	)
	server := stub(t, wire, nil)
	root := object{
		"model": "claude-sonnet-4-6", "max_tokens": 1024, "stream": true,
		"messages": []any{object{"role": "user", "content": "hello"}},
		"tools":    toolDefinition("anthropic"), "tool_choice": object{"type": "any"},
	}
	request, err := http.NewRequest("POST", server.URL+"/v1/messages", strings.NewReader(jsonText(root)))
	if err != nil {
		t.Fatal(err)
	}
	request.Header = clientRequest(tbOf(t), server.URL, "anthropic", true).Header.Clone()
	resp, err := NewTransport(nil).RoundTrip(request)
	if err != nil {
		t.Fatal(err)
	}
	data, err := io.ReadAll(resp.Body)
	resp.Body.Close()
	if err != nil || resp.StatusCode != 200 {
		t.Fatalf("status=%d data=%s err=%v", resp.StatusCode, data, err)
	}
	var ids, args []string
	stops := 0
	for _, event := range events(t, data) {
		switch str(event["type"]) {
		case "content_block_start":
			block := obj(event["content_block"])
			if block["type"] != "tool_use" || block["name"] != "lookup" || len(obj(block["input"])) != 0 || number(event["index"]) != len(ids) {
				t.Fatalf("unexpected tool block start: %v", event)
			}
			ids = append(ids, str(block["id"]))
		case "content_block_delta":
			delta := obj(event["delta"])
			if delta["type"] != "input_json_delta" || number(event["index"]) != len(args) {
				t.Fatalf("unexpected tool delta: %v", event)
			}
			args = append(args, str(delta["partial_json"]))
		case "content_block_stop":
			stops++
		case "message_delta":
			if obj(event["delta"])["stop_reason"] != "tool_use" {
				t.Fatalf("unexpected stop reason: %v", event)
			}
		}
	}
	if strings.Join(ids, ",") != "first,second" || len(args) != 2 || args[0] != `{"z": "中", "a": 1}` || args[1] != `{"b": 2, "a": 3}` || stops != 2 {
		t.Fatalf("ids=%v args=%v stops=%d data=%s", ids, args, stops, data)
	}
}

func TestOrdinaryScalarToolStream(t *testing.T) {
	for _, protocol := range []string{"openai", "anthropic"} {
		for _, args := range []string{`[1, 2]`, `null`, `1`, `"text"`, `true`} {
			t.Run(protocol+"/"+args, func(t *testing.T) {
				wire := joinedFrames(frame("toolUseEvent", object{"name": "lookup", "input": args, "stop": true}), endFrame())
				server := stub(t, wire, nil)
				_, data, err := do(t, server.URL, protocol, true)
				if err != nil {
					t.Fatal(err)
				}
				var got string
				for _, event := range events(t, data) {
					if protocol == "anthropic" {
						got += str(obj(event["delta"])["partial_json"])
					} else if choices := list(event["choices"]); len(choices) > 0 {
						for _, call := range list(obj(obj(choices[0])["delta"])["tool_calls"]) {
							got += str(obj(obj(call)["function"])["arguments"])
						}
					}
				}
				if got != args {
					t.Fatalf("got=%q want=%q data=%s", got, args, data)
				}
			})
		}
	}
}

func TestRound23ObjectToolArgumentOrder(t *testing.T) {
	for _, wrapped := range []bool{false, true} {
		for _, protocol := range []string{"openai", "anthropic"} {
			first := `{"name":"lookup","toolUseId":"first","input":{"b":1,"a":2},"stop":true}`
			second := `{"name":"lookup","toolUseId":"second","input":"{\"b\":1,\"a\":2}","stop":true}`
			if wrapped {
				first = `{"toolUseEvent":` + first + `}`
				second = `{"toolUseEvent":` + second + `}`
			}
			headers := append(stringHeader(":message-type", "event"), stringHeader(":event-type", "toolUseEvent")...)
			wire := joinedFrames(frameWithHeaders(headers, []byte(first)), frameWithHeaders(headers, []byte(second)), endFrame())
			server := stub(t, wire, nil)
			_, data, err := do(t, server.URL, protocol, true)
			if err != nil {
				t.Fatal(err)
			}
			calls := 0
			for _, event := range events(t, data) {
				if protocol == "anthropic" {
					if obj(event["content_block"])["type"] == "tool_use" {
						calls++
					}
				} else if choices := list(event["choices"]); len(choices) > 0 {
					calls += len(list(obj(obj(choices[0])["delta"])["tool_calls"]))
				}
			}
			if calls != 1 {
				t.Fatalf("protocol=%s wrapped=%v calls=%d", protocol, wrapped, calls)
			}
		}
	}
}

func TestRound22UnicodeThinkingStream(t *testing.T) {
	for _, protocol := range []string{"openai", "anthropic"} {
		text := strings.Repeat("中🙂é", 20)
		wire := joinedFrames(frame("assistantResponseEvent", object{"content": "<thinking>" + text}), frame("assistantResponseEvent", object{"content": "</thinking>OK"}), endFrame())
		server := stub(t, wire, nil)
		_, data, err := do(t, server.URL, protocol, true)
		if err != nil {
			t.Fatal(err)
		}
		thinking, regular := "", ""
		for _, event := range events(t, data) {
			if protocol == "anthropic" {
				delta := obj(event["delta"])
				thinking += str(delta["thinking"])
				regular += str(delta["text"])
			} else if choices := list(event["choices"]); len(choices) > 0 {
				delta := obj(obj(choices[0])["delta"])
				thinking += str(delta["reasoning_content"])
				regular += str(delta["content"])
			}
		}
		if thinking != text || regular != "OK" {
			t.Fatalf("protocol=%s thinking=%q regular=%q", protocol, thinking, regular)
		}
	}
}

func TestRound21MaxTokensHTTP(t *testing.T) {
	for _, tc := range []struct {
		value any
		valid bool
	}{
		{"16.0", true}, {"1_6", true}, {"9223372036854775808", true},
		{json.Number("9223372036854775808"), true}, {json.Number("1e2"), true}, {false, true},
		{"1e2", false}, {"16.", false}, {"16.1", false}, {nil, false},
		{json.Number("9223372036854775808.0"), false},
	} {
		root := object{"model": "model", "max_tokens": tc.value, "messages": []any{object{"role": "user", "content": "hi"}}}
		r, err := http.NewRequest("POST", "http://127.0.0.1/v1/messages", strings.NewReader(jsonText(root)))
		if err != nil {
			t.Fatal(err)
		}
		for k, v := range Headers("token", "") {
			r.Header.Set(k, v)
		}
		calls := 0
		tr := NewTransport(roundTripFunc(func(r *http.Request) (*http.Response, error) {
			calls++
			return &http.Response{StatusCode: 200, Header: http.Header{}, Body: io.NopCloser(bytes.NewReader(endFrame())), Request: r}, nil
		}))
		resp, err := tr.RoundTrip(r)
		if !tc.valid {
			if err == nil || calls != 0 {
				t.Fatalf("invalid value=%v calls=%d err=%v", tc.value, calls, err)
			}
			continue
		}
		if err != nil {
			t.Fatalf("valid value=%v err=%v", tc.value, err)
		}
		_, err = io.ReadAll(resp.Body)
		resp.Body.Close()
		if err != nil || calls != 1 {
			t.Fatalf("value=%v calls=%d err=%v", tc.value, calls, err)
		}
	}
}

func TestRound21CacheUsageStream(t *testing.T) {
	wire := joinedFrames(
		frame("assistantResponseEvent", object{"content": "hello"}),
		frame("usageEvent", object{"usage": object{"cache_read_input_tokens": 9, "cacheReadInputTokens": 7.9, "cacheCreationInputTokens": true}}),
		endFrame(),
	)
	server := stub(t, wire, nil)
	_, data, err := do(t, server.URL, "anthropic", true)
	if err != nil {
		t.Fatal(err)
	}
	var usage object
	for _, event := range events(t, data) {
		if event["type"] == "message_delta" {
			usage = obj(event["usage"])
		}
	}
	if usage == nil || number(usage["cache_read_input_tokens"]) != 7 || number(usage["cache_creation_input_tokens"]) != 1 {
		t.Fatalf("usage=%v data=%s", usage, data)
	}
}

func TestCreditsPassthrough(t *testing.T) {
	wire := joinedFrames(frame("assistantResponseEvent", object{"content": "hi"}), endFrame())
	server := stub(t, wire, nil)
	for _, protocol := range []string{"anthropic", "openai"} {
		_, data, err := do(t, server.URL, protocol, false)
		if err != nil {
			t.Fatal(err)
		}
		usage := obj(parseResult(t, data)["usage"])
		if fmt.Sprint(usage["credits_used"]) != "0.25" {
			t.Fatalf("%s usage=%v", protocol, usage)
		}
	}
}

func TestFlatToolDefinition(t *testing.T) {
	wire := joinedFrames(frame("assistantResponseEvent", object{"content": "hi"}), endFrame())
	var seen object
	server := stub(t, wire, func(_ *http.Request, p object) { seen = p })
	// converters_openai.py:290-296: Cursor 风格扁平工具,无 type/function 包裹。
	body := object{
		"model":    "claude-sonnet-4-6",
		"messages": []any{object{"role": "user", "content": "hello"}},
		"tools": []any{object{
			"name":         "lookup",
			"description":  "Look up x",
			"input_schema": object{"type": "object", "properties": object{"x": object{"type": "integer"}}},
		}},
	}
	r2, err := http.NewRequest("POST", server.URL+"/v1/chat/completions", strings.NewReader(jsonText(body)))
	if err != nil {
		t.Fatal(err)
	}
	for k, v := range Headers("native-token", "arn:aws:codewhisperer:eu-west-1:123:profile/p") {
		r2.Header.Set(k, v)
	}
	resp, err := NewTransport(nil).RoundTrip(r2)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if _, err := io.ReadAll(resp.Body); err != nil {
		t.Fatal(err)
	}
	ctx := obj(obj(obj(seen["conversationState"])["currentMessage"])["userInputMessage"])["userInputMessageContext"]
	tools := list(obj(ctx)["tools"])
	if len(tools) != 1 || str(obj(obj(tools[0])["toolSpecification"])["name"]) != "lookup" {
		t.Fatalf("tools=%v", tools)
	}
}

func TestToolNameArgsDedup(t *testing.T) {
	wire := joinedFrames(
		frame("toolUseEvent", object{"name": "lookup", "toolUseId": "call_1", "input": object{"x": 1}, "stop": true}),
		// 同 name+args 不同 id:parsers.py deduplicate_tool_calls 只发一次。
		frame("toolUseEvent", object{"name": "lookup", "toolUseId": "call_2", "input": object{"x": 1}, "stop": true}),
		frame("toolUseEvent", object{"name": "lookup", "toolUseId": "call_3", "input": object{"x": 2}, "stop": true}),
		// 括号文本恢复出与原生事件相同的调用:交叉去重。
		frame("assistantResponseEvent", object{"content": `done [Called lookup with args: {"x": 2}]`}),
		endFrame(),
	)
	server := stub(t, wire, nil)
	_, data, err := do(t, server.URL, "openai", false)
	if err != nil {
		t.Fatal(err)
	}
	m := parseResult(t, data)
	calls := list(obj(obj(list(m["choices"])[0])["message"])["tool_calls"])
	if len(calls) != 2 || str(obj(calls[0])["id"]) != "call_1" || str(obj(calls[1])["id"]) != "call_3" {
		t.Fatalf("calls=%v", calls)
	}
}

func tbOf(t *testing.T) *testing.TB {
	tb := testing.TB(t)
	return &tb
}

func TestAnthropicMaxTokensRequired(t *testing.T) {
	// models_anthropic.py:375: FastAPI 端点模型里 max_tokens 必填,缺失在
	// HTTP 边界直接拒绝,不进转换器也不触网。
	r := strictRequest(t, "http://unused", "anthropic", false, nil)
	_, err := NewTransport(nil).RoundTrip(r)
	if err == nil || !strings.Contains(err.Error(), "max_tokens is required") {
		t.Fatalf("err=%v", err)
	}
	// 与 FastAPI 422 同类:校验错误归为 invalid_request,proxyplane 映射 400。
	if !apperr.Is(err, apperr.InvalidRequest) {
		t.Fatalf("err=%v", err)
	}
}

func TestValidationErrorsClassified(t *testing.T) {
	r := clientRequest(tbOf(t), "http://unused", "openai", true)
	r.Method = http.MethodGet
	if _, err := NewTransport(nil).RoundTrip(r); !apperr.Is(err, apperr.InvalidRequest) {
		t.Fatalf("method err=%v", err)
	}
	r = clientRequest(tbOf(t), "http://unused", "openai", true)
	r.URL.Path = "/v1/unknown"
	if _, err := NewTransport(nil).RoundTrip(r); !apperr.Is(err, apperr.InvalidRequest) {
		t.Fatalf("path err=%v", err)
	}
}

func TestEnhanceKiroError(t *testing.T) {
	// kiro_errors.py enhance_kiro_error 的已知 reason 映射与兜底。
	cases := []struct{ body, want string }{
		{`{"message":"Input is too long.","reason":"CONTENT_LENGTH_EXCEEDS_THRESHOLD"}`,
			"Model context limit reached. Conversation size exceeds model capacity."},
		{`{"message":"quota","reason":"MONTHLY_REQUEST_COUNT"}`,
			"Monthly request limit exceeded. Account has reached its monthly quota."},
		{`{"message":"bad model","reason":"INVALID_MODEL_ID"}`,
			"Invalid model ID or insufficient subscription level to use it."},
		{`{"message":"Something went wrong.","reason":"WEIRD"}`, "Something went wrong. (reason: WEIRD)"},
		{`{"message":"plain"}`, "plain"},
		{`not json`, "not json"},
		{`{"reason":"X"}`, "Unknown error (reason: X)"},
	}
	for _, tc := range cases {
		if got := enhanceKiroError([]byte(tc.body)); got != tc.want {
			t.Errorf("%s → %q, want %q", tc.body, got, tc.want)
		}
	}
}

func TestCountTokens(t *testing.T) {
	// routes_anthropic.py count_tokens_endpoint: 纯本地估算,不打上游;
	// max_tokens 在此端点不要求(AnthropicCountTokensRequest 无此字段)。
	upstream := roundTripFunc(func(*http.Request) (*http.Response, error) {
		t.Error("count_tokens must not reach the network")
		return nil, errors.New("unreachable")
	})
	data := object{
		"model":    "claude-sonnet-4-6",
		"system":   "be terse",
		"messages": []any{object{"role": "user", "content": "hello world"}},
		"tools":    toolDefinition("anthropic"),
	}
	r, err := http.NewRequest("POST", "http://unused/v1/messages/count_tokens", strings.NewReader(jsonText(data)))
	if err != nil {
		t.Fatal(err)
	}
	for k, v := range Headers("native-token", "arn:aws:codewhisperer:eu-west-1:123:profile/p") {
		r.Header.Set(k, v)
	}
	resp, err := NewTransport(upstream).RoundTrip(r)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("status=%d", resp.StatusCode)
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	n, ok := absoluteTokens(parseResult(t, body)["input_tokens"])
	if !ok || n <= 0 {
		t.Fatalf("body=%s", body)
	}
}

func TestAutoKiroAlias(t *testing.T) {
	// config.py MODEL_ALIASES: auto-kiro 归一到 auto(规避 Cursor 内置冲突)。
	var seen object
	server := stub(t, joinedFrames(frame("assistantResponseEvent", object{"content": "hi"}), endFrame()),
		func(_ *http.Request, p object) { seen = p })
	body := object{
		"model":      "auto-kiro",
		"max_tokens": 1024,
		"messages":   []any{object{"role": "user", "content": "hello"}},
	}
	r, err := http.NewRequest("POST", server.URL+"/v1/messages", strings.NewReader(jsonText(body)))
	if err != nil {
		t.Fatal(err)
	}
	for k, v := range Headers("native-token", "arn:aws:codewhisperer:eu-west-1:123:profile/p") {
		r.Header.Set(k, v)
	}
	resp, err := NewTransport(nil).RoundTrip(r)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if _, err := io.ReadAll(resp.Body); err != nil {
		t.Fatal(err)
	}
	if model := str(obj(obj(obj(seen["conversationState"])["currentMessage"])["userInputMessage"])["modelId"]); model != "auto" {
		t.Fatalf("model=%q payload=%v", model, seen)
	}
}

// strictRequest 带 tool_choice 与工具声明的请求。
func strictRequest(t *testing.T, base, protocol string, stream bool, toolChoice any) *http.Request {
	t.Helper()
	path := "/v1/messages"
	if protocol == "openai" {
		path = "/v1/chat/completions"
	}
	data := object{
		"model": "claude-sonnet-4-6", "stream": stream,
		"messages": []any{object{"role": "user", "content": "hello"}},
		"tools":    toolDefinition(protocol), "tool_choice": toolChoice,
	}
	r, err := http.NewRequest("POST", base+path, strings.NewReader(jsonText(data)))
	if err != nil {
		t.Fatal(err)
	}
	for k, v := range Headers("native-token", "arn:aws:codewhisperer:eu-west-1:123:profile/p") {
		r.Header.Set(k, v)
	}
	return r
}

// seqStub 按调用序返回不同的帧序列,inspect 收到 (调用序号, 请求负载)。
func seqStub(t *testing.T, seq [][]byte, inspect func(int, object)) *httptest.Server {
	t.Helper()
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, err := io.ReadAll(r.Body)
		if err != nil {
			t.Error(err)
			return
		}
		m, err := decodeObject(string(b))
		if err != nil {
			t.Error(err)
			return
		}
		n := int(calls.Add(1)) - 1
		if inspect != nil {
			inspect(n, m)
		}
		frames := seq[len(seq)-1]
		if n < len(seq) {
			frames = seq[n]
		}
		w.Header().Set("Content-Type", "application/vnd.amazon.eventstream")
		w.Header().Set("Content-Encoding", "identity")
		w.WriteHeader(200)
		_, _ = w.Write(frames)
	}))
	t.Cleanup(server.Close)
	return server
}

func TestToolChoiceRecovery(t *testing.T) {
	textOnly := joinedFrames(frame("assistantResponseEvent", object{"content": "sorry no tool"}), endFrame())
	withTool := joinedFrames(
		frame("toolUseEvent", object{"name": "lookup", "toolUseId": "call_1", "input": object{"x": 1}, "stop": true}),
		endFrame(),
	)
	var calls atomic.Int32
	var recovered string
	server := seqStub(t, [][]byte{textOnly, withTool}, func(n int, p object) {
		calls.Store(int32(n) + 1)
		if n == 1 {
			recovered = currentContent(t, p)
		}
	})
	r := strictRequest(t, server.URL, "openai", false, "required")
	resp, err := NewTransport(nil).RoundTrip(r)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 2 {
		t.Fatalf("calls=%d", calls.Load())
	}
	// streaming_core.py add_tool_choice_recovery_directive: 违规详情 + 原政策指令。
	if !strings.Contains(recovered, "[Tool Policy Recovery]") ||
		!strings.Contains(recovered, "tool_choice required returned no tools") ||
		!strings.Contains(recovered, "MUST call at least one tool") {
		t.Fatalf("recovered=%q", recovered)
	}
	m := parseResult(t, data)
	toolCalls := list(obj(obj(list(m["choices"])[0])["message"])["tool_calls"])
	if len(toolCalls) != 1 || str(obj(toolCalls[0])["id"]) != "call_1" {
		t.Fatalf("tool_calls=%v", toolCalls)
	}
}

func TestToolChoiceRecoveryExhausted(t *testing.T) {
	textOnly := joinedFrames(frame("assistantResponseEvent", object{"content": "still no tool"}), endFrame())
	var calls atomic.Int32
	server := seqStub(t, [][]byte{textOnly}, func(n int, _ object) { calls.Store(int32(n) + 1) })
	r := strictRequest(t, server.URL, "openai", false, "required")
	// routes_openai.py:414-423: 恢复重试仍违规返回 502 + 协议错误体,
	// 消息带 "tool_choice_not_satisfied: " 前缀(streaming_core.py:126-130)。
	resp, err := NewTransport(nil).RoundTrip(r)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != 502 {
		t.Fatalf("status=%d body=%s", resp.StatusCode, data)
	}
	e := obj(parseResult(t, data)["error"])
	if e["type"] != "tool_choice_not_satisfied" || e["code"] != "tool_choice_not_satisfied" ||
		str(e["message"]) != "tool_choice_not_satisfied: tool_choice required returned no tools" {
		t.Fatalf("body=%s", data)
	}
	if calls.Load() != 2 {
		t.Fatalf("calls=%d", calls.Load())
	}
}

func TestToolChoiceRecoveryStream(t *testing.T) {
	textOnly := joinedFrames(frame("assistantResponseEvent", object{"content": "sorry no tool"}), endFrame())
	withTool := joinedFrames(
		frame("toolUseEvent", object{"name": "lookup", "toolUseId": "call_9", "input": object{"x": 2}, "stop": true}),
		endFrame(),
	)
	var calls atomic.Int32
	server := seqStub(t, [][]byte{textOnly, withTool}, func(n int, _ object) { calls.Store(int32(n) + 1) })
	r := strictRequest(t, server.URL, "openai", true, "required")
	resp, err := NewTransport(nil).RoundTrip(r)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	// 违规响应的 SSE 不得外泄;回放只含恢复后的成功流。
	if strings.Contains(string(data), "sorry no tool") ||
		!strings.Contains(string(data), `"tool_calls"`) || !strings.Contains(string(data), "call_9") ||
		!strings.Contains(string(data), "data: [DONE]") {
		t.Fatalf("data=%q", data)
	}
	if calls.Load() != 2 {
		t.Fatalf("calls=%d", calls.Load())
	}
	chunks := events(t, data)
	first := obj(obj(list(chunks[0]["choices"])[0])["delta"])
	if first["role"] != "assistant" || first["content"] != "" || len(list(first["tool_calls"])) != 1 {
		t.Fatalf("first strict tool delta=%v", first)
	}
}

func TestToolChoiceNamedWrongTool(t *testing.T) {
	wrong := joinedFrames(
		// 声明集里只有 lookup;named 模式指定 calc,模型却回了 lookup。
		frame("toolUseEvent", object{"name": "lookup", "toolUseId": "call_1", "input": object{"x": 1}, "stop": true}),
		endFrame(),
	)
	right := joinedFrames(
		frame("toolUseEvent", object{"name": "calc", "toolUseId": "call_2", "input": object{"x": 1}, "stop": true}),
		endFrame(),
	)
	var calls atomic.Int32
	server := seqStub(t, [][]byte{wrong, right}, func(n int, _ object) { calls.Store(int32(n) + 1) })
	data := object{
		"model":    "claude-sonnet-4-6",
		"messages": []any{object{"role": "user", "content": "hello"}},
		"tools": []any{
			obj(toolDefinition("openai")[0]),
			object{"type": "function", "function": object{"name": "calc", "description": "Calc x", "parameters": object{"type": "object", "properties": object{"x": object{"type": "integer"}}}}},
		},
		"tool_choice": object{"type": "function", "function": object{"name": "calc"}},
	}
	r, err := http.NewRequest("POST", server.URL+"/v1/chat/completions", strings.NewReader(jsonText(data)))
	if err != nil {
		t.Fatal(err)
	}
	for k, v := range Headers("native-token", "arn:aws:codewhisperer:eu-west-1:123:profile/p") {
		r.Header.Set(k, v)
	}
	resp, err := NewTransport(nil).RoundTrip(r)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	m := parseResult(t, body)
	toolCalls := list(obj(obj(list(m["choices"])[0])["message"])["tool_calls"])
	if len(toolCalls) != 1 || str(obj(toolCalls[0])["id"]) != "call_2" || calls.Load() != 2 {
		t.Fatalf("tool_calls=%v calls=%d", toolCalls, calls.Load())
	}
}

// 上游把工具参数的每个碎片帧都重复携带 name 与 toolUseId(2026-10-08 抓包
// 实录),只有首键为 name 的帧才是开启帧。把碎片帧也当开启会让每个碎片各自
// 成工具,再被同 id 去重压成单个碎片或 "{}",客户端报缺少必填参数。
func TestToolInputFragmentFramesRepeatName(t *testing.T) {
	const id = "toolu_bdrk_01HyyLJfKxNh7k4a3BqiTsw8"
	const want = `{"command": "ls -la", "description": "List directory contents with details"}`
	headers := append(stringHeader(":message-type", "event"), stringHeader(":event-type", "toolUseEvent")...)
	toolFrame := func(payload string) []byte { return frameWithHeaders(headers, []byte(payload)) }
	fragments := []string{`{"command"`, `: "ls`, ` -la"`, `, "d`, `escri`, `ption": `, `"List `, `directory`, ` contents `, `wit`, `h details"}`}
	wire := frame("assistantResponseEvent", object{"content": "I'll run that now."})
	wire = append(wire, toolFrame(`{"name":"Bash","toolUseId":"`+id+`"}`)...)
	for _, fragment := range fragments {
		encoded, err := json.Marshal(fragment)
		if err != nil {
			t.Fatal(err)
		}
		wire = append(wire, toolFrame(`{"input":`+string(encoded)+`,"name":"Bash","toolUseId":"`+id+`"}`)...)
	}
	wire = append(wire, toolFrame(`{"name":"Bash","stop":true,"toolUseId":"`+id+`"}`)...)
	wire = append(wire, frame("metadataEvent", object{"stopReason": "TOOL_USE"})...)
	wire = append(wire, endFrame()...)

	for _, protocol := range []string{"openai", "anthropic"} {
		t.Run(protocol, func(t *testing.T) {
			server := stub(t, wire, nil)
			_, data, err := do(t, server.URL, protocol, true)
			if err != nil {
				t.Fatal(err)
			}
			var text, args string
			ids, names, calls := 0, 0, 0
			for _, event := range events(t, data) {
				if protocol == "anthropic" {
					switch str(event["type"]) {
					case "content_block_start":
						if block := obj(event["content_block"]); str(block["type"]) == "tool_use" {
							calls++
							if str(block["id"]) == id {
								ids++
							}
							if str(block["name"]) == "Bash" {
								names++
							}
						}
					case "content_block_delta":
						delta := obj(event["delta"])
						if delta["type"] == "input_json_delta" {
							args += str(delta["partial_json"])
						} else {
							text += str(delta["text"])
						}
					}
					continue
				}
				choices := list(event["choices"])
				if len(choices) == 0 {
					continue
				}
				delta := obj(obj(choices[0])["delta"])
				text += str(delta["content"])
				for _, raw := range list(delta["tool_calls"]) {
					call := obj(raw)
					calls++
					function := obj(call["function"])
					if str(call["id"]) == id {
						ids++
					}
					if str(function["name"]) == "Bash" {
						names++
					}
					args += str(function["arguments"])
				}
			}
			if calls != 1 || ids != 1 || names != 1 || args != want || text != "I'll run that now." {
				t.Fatalf("calls=%d ids=%d names=%d args=%q text=%q data=%s", calls, ids, names, args, text, data)
			}
		})
	}
}
