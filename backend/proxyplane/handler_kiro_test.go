package proxyplane

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"hash/crc32"
	"io"
	"net/http"
	"net/http/httptest"
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
	for _, tc := range []struct{ raw, want string }{{"1e2", "100.0"}, {"1.00", "1.0"}, {"-0", "0"}, {"-0.0", "-0.0"}} {
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
