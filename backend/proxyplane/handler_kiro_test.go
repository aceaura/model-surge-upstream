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
	target := resolve.ResolvedTarget{ModelID: "kiro-retry", Account: "kiro-test", ProviderID: "kiro",
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
	target := resolve.ResolvedTarget{ModelID: "kiro-403", Account: "kiro-test", ProviderID: "kiro",
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
	r.Header.Set(kiro.HeaderProvider, "kiro")
	r.Header.Set(kiro.HeaderProfileARN, "forged-profile")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	_, headers, path := cap.snapshot()
	if w.Code != 200 || path != "/v1/chat/completions" || headers.Get(kiro.HeaderProvider) != "" || headers.Get(kiro.HeaderProfileARN) != "" {
		t.Fatalf("client-selected native conversion escaped: status=%d path=%s", w.Code, path)
	}
	_, _ = io.Copy(io.Discard, w.Result().Body)
}
