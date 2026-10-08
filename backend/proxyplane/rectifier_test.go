package proxyplane

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/aceaura/model-surge-upstream/backend/kiro"
	"github.com/aceaura/model-surge-upstream/backend/provider"
	"github.com/aceaura/model-surge-upstream/backend/resolve"
)

func TestParseRectifier(t *testing.T) {
	for _, tc := range []struct {
		name     string
		raw      string
		enabled  bool
		retries  int
		interval float64
	}{
		{"empty object", `{}`, false, 0, 0},
		{"absent", ``, false, 0, 0},
		{"enabled defaults", `{"enabled":true}`, true, 2, 2},
		{"enabled explicit", `{"enabled":true,"retries":5,"interval_seconds":0.5}`, true, 5, 0.5},
		{"disabled keeps params inert", `{"enabled":false,"retries":9,"interval_seconds":9}`, false, 9, 9},
		{"garbage", `not json`, false, 0, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := parseRectifier(json.RawMessage(tc.raw))
			if cfg.Enabled != tc.enabled || cfg.Retries != tc.retries || cfg.IntervalSeconds != tc.interval {
				t.Fatalf("got %+v", cfg)
			}
		})
	}
}

func TestSniffRefusal(t *testing.T) {
	openAIRole := `data: {"choices":[{"index":0,"delta":{"role":"assistant"},"finish_reason":null}]}`
	openAIFilter := `data: {"choices":[{"index":0,"delta":{},"finish_reason":"content_filter"}]}`
	openAIText := `data: {"choices":[{"index":0,"delta":{"content":"hi"},"finish_reason":null}]}`
	openAIThinking := `data: {"choices":[{"index":0,"delta":{"reasoning_content":"hmm"},"finish_reason":null}]}`
	openAITool := `data: {"choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"function":{"name":"f"}}]},"finish_reason":null}]}`
	anthStop := `data: {"type":"message_delta","delta":{"stop_reason":"refusal"}}`
	anthText := `data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"hi"}}`
	for _, tc := range []struct {
		name        string
		contentType string
		body        string
		want        sniffVerdict
	}{
		{"openai stream refusal", "text/event-stream", openAIRole + "\n\n" + openAIFilter + "\n\ndata: [DONE]\n\n", verdictRefusal},
		{"openai stream text then filter", "text/event-stream", openAIText + "\n\n" + openAIFilter + "\n\ndata: [DONE]\n\n", verdictPass},
		{"openai stream thinking then filter", "text/event-stream", openAIThinking + "\n\n" + openAIFilter + "\n\ndata: [DONE]\n\n", verdictPass},
		{"openai stream tool then filter", "text/event-stream", openAITool + "\n\n" + openAIFilter + "\n\ndata: [DONE]\n\n", verdictPass},
		{"openai stream filter no done", "text/event-stream", openAIRole + "\n\n" + openAIFilter + "\n\n", verdictRefusal},
		{"openai stream normal", "text/event-stream", openAIText + "\n\ndata: {\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n", verdictPass},
		{"anthropic stream refusal", "text/event-stream", "data: {\"type\":\"message_start\"}\n\n" + anthStop + "\n\ndata: {\"type\":\"message_stop\"}\n\n", verdictRefusal},
		{"anthropic stream text then refusal", "text/event-stream", anthText + "\n\n" + anthStop + "\n\n", verdictPass},
		{"openai json refusal", "application/json", `{"choices":[{"message":{"role":"assistant","content":""},"finish_reason":"content_filter"}]}`, verdictRefusal},
		{"openai json refusal null content", "application/json", `{"choices":[{"message":{"role":"assistant","content":null},"finish_reason":"content_filter"}]}`, verdictRefusal},
		{"openai json normal", "application/json", `{"choices":[{"message":{"role":"assistant","content":"hi"},"finish_reason":"stop"}]}`, verdictPass},
		{"openai json filter with tool calls", "application/json", `{"choices":[{"message":{"role":"assistant","content":"","tool_calls":[{"id":"t"}]},"finish_reason":"content_filter"}]}`, verdictPass},
		{"anthropic json refusal", "application/json", `{"content":[],"stop_reason":"refusal"}`, verdictRefusal},
		{"anthropic json normal", "application/json", `{"content":[{"type":"text","text":"hi"}],"stop_reason":"end_turn"}`, verdictPass},
		{"json garbage", "application/json", `not json`, verdictPass},
	} {
		t.Run(tc.name, func(t *testing.T) {
			verdict, replay := sniffRefusal(io.NopCloser(strings.NewReader(tc.body)), tc.contentType)
			if verdict != tc.want {
				t.Fatalf("verdict=%v want %v", verdict, tc.want)
			}
			data, err := io.ReadAll(replay)
			replay.Close()
			if err != nil {
				t.Fatalf("replay read err=%v", err)
			}
			if tc.want == verdictRefusal {
				// 拒答重放只含预读部分(调用方会丢弃),须是原文前缀。
				if !strings.HasPrefix(tc.body, string(data)) {
					t.Fatalf("refusal replay=%q not prefix of %q", data, tc.body)
				}
			} else if string(data) != tc.body {
				t.Fatalf("pass replay=%q want original %q", data, tc.body)
			}
		})
	}
}

// rectifierKiroHandler 搭一个 kiro 目标+事件stream桩上游的完整链路。
// respond 按上游调用次序(从 1 起)产出事件帧。
func rectifierKiroHandler(t *testing.T, protocol, rectifier string, respond func(call int, w http.ResponseWriter)) (*Handler, *int) {
	t.Helper()
	calls := new(int)
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		*calls++
		_, _ = io.Copy(io.Discard, r.Body)
		w.Header().Set("Content-Type", "application/vnd.amazon.eventstream")
		respond(*calls, w)
	}))
	t.Cleanup(up.Close)
	target := resolve.ResolvedTarget{
		ModelID: "kiro-rect", ProviderID: kiro.ProviderID, Protocol: protocol,
		BaseURL: up.URL, NativeModel: "claude-sonnet-4.5", Headers: kiro.Headers("test-access", ""),
		Rectifier: json.RawMessage(rectifier),
	}
	h := NewHandler(testKey, fakeResolver{targets: map[string]resolve.ResolvedTarget{target.ModelID: target}}, nil)
	base := http.DefaultTransport.(*http.Transport).Clone()
	t.Cleanup(base.CloseIdleConnections)
	h.client.Transport = kiro.NewTransport(base)
	return h, calls
}

func rectifierRoundTrip(t *testing.T, h *Handler, protocol string, stream bool) (int, string) {
	t.Helper()
	path := "/v1/messages"
	if protocol == provider.ProtocolChatCompletions {
		path = "/v1/chat/completions"
	}
	body, _ := json.Marshal(map[string]any{
		"model": "kiro-rect", "max_tokens": 1024, "stream": stream,
		"messages": []any{map[string]any{"role": "user", "content": "q"}},
	})
	r := httptest.NewRequest(http.MethodPost, path, strings.NewReader(string(body)))
	r.Header.Set("Authorization", "Bearer "+testKey)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w.Code, w.Body.String()
}

func kiroRefusalFrames(w http.ResponseWriter) {
	_, _ = w.Write(kiroTestFrame("messageStopEvent", `{"stopReason":"content_filtered"}`))
	_, _ = w.Write(kiroTestFrame("metadataEvent", `{"contextUsagePercentage":1}`))
}

func kiroAnswerFrames(w http.ResponseWriter, content string) {
	data, _ := json.Marshal(map[string]any{"content": content})
	_, _ = w.Write(kiroTestFrame("assistantResponseEvent", string(data)))
	_, _ = w.Write(kiroTestFrame("metadataEvent", `{"contextUsagePercentage":1}`))
}

// 开启整流器:两协议×流式/非流式,首次空拒答自动重发,客户端只见成功响应。
func TestRectifierKiroRetryHTTP(t *testing.T) {
	for _, protocol := range []string{provider.ProtocolAnthropic, provider.ProtocolChatCompletions} {
		for _, stream := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/stream=%v", protocol, stream), func(t *testing.T) {
				h, calls := rectifierKiroHandler(t, protocol, `{"enabled":true,"retries":2,"interval_seconds":0.01}`,
					func(call int, w http.ResponseWriter) {
						if call == 1 {
							kiroRefusalFrames(w)
							return
						}
						kiroAnswerFrames(w, "rectified answer")
					})
				code, body := rectifierRoundTrip(t, h, protocol, stream)
				if code != 200 || *calls != 2 || !strings.Contains(body, "rectified answer") {
					t.Fatalf("status=%d calls=%d body=%s", code, *calls, body)
				}
				if strings.Contains(body, "content_filter") || strings.Contains(body, "refusal") {
					t.Fatalf("refusal leaked to client: %s", body)
				}
			})
		}
	}
}

// 未开启(空对象):空拒答原样透传,上游只调一次。
func TestRectifierKiroDisabledHTTP(t *testing.T) {
	h, calls := rectifierKiroHandler(t, provider.ProtocolChatCompletions, `{}`,
		func(call int, w http.ResponseWriter) { kiroRefusalFrames(w) })
	code, body := rectifierRoundTrip(t, h, provider.ProtocolChatCompletions, true)
	if code != 200 || *calls != 1 || !strings.Contains(body, "content_filter") {
		t.Fatalf("status=%d calls=%d body=%s", code, *calls, body)
	}
}

// 重试耗尽:retries=1 两次皆拒答,第二次拒答透传给客户端。
func TestRectifierKiroExhaustedHTTP(t *testing.T) {
	h, calls := rectifierKiroHandler(t, provider.ProtocolChatCompletions, `{"enabled":true,"retries":1,"interval_seconds":0.01}`,
		func(call int, w http.ResponseWriter) { kiroRefusalFrames(w) })
	code, body := rectifierRoundTrip(t, h, provider.ProtocolChatCompletions, true)
	if code != 200 || *calls != 2 || !strings.Contains(body, "content_filter") {
		t.Fatalf("status=%d calls=%d body=%s", code, *calls, body)
	}
}

// 中途拒答:正文已流出后才以 content_filter 收尾,字节收不回,不重试。
func TestRectifierKiroMidStreamRefusalHTTP(t *testing.T) {
	h, calls := rectifierKiroHandler(t, provider.ProtocolChatCompletions, `{"enabled":true,"retries":2,"interval_seconds":0.01}`,
		func(call int, w http.ResponseWriter) {
			kiroAnswerFrames(w, "partial")
			_, _ = w.Write(kiroTestFrame("messageStopEvent", `{"stopReason":"content_filtered"}`))
		})
	code, body := rectifierRoundTrip(t, h, provider.ProtocolChatCompletions, true)
	if code != 200 || *calls != 1 || !strings.Contains(body, "partial") || !strings.Contains(body, "content_filter") {
		t.Fatalf("status=%d calls=%d body=%s", code, *calls, body)
	}
}

// 非 kiro 提供商:即使模型带了 rectifier 配置也不参与拒答重试。
func TestRectifierNonKiroIgnoredHTTP(t *testing.T) {
	calls := 0
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		_, _ = io.Copy(io.Discard, r.Body)
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: {\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\"},\"finish_reason\":null}]}\n\n" +
			"data: {\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"content_filter\"}]}\n\ndata: [DONE]\n\n"))
	}))
	defer up.Close()
	target := resolve.ResolvedTarget{
		ModelID: "oss-rect", ProviderID: "openai.global.api.standard", Protocol: provider.ProtocolChatCompletions,
		BaseURL: up.URL, NativeModel: "gpt-x", Headers: map[string]string{"Authorization": "Bearer upstream"},
		Rectifier: json.RawMessage(`{"enabled":true,"retries":2,"interval_seconds":0.01}`),
	}
	h := NewHandler(testKey, fakeResolver{targets: map[string]resolve.ResolvedTarget{target.ModelID: target}}, nil)
	body, _ := json.Marshal(map[string]any{
		"model": target.ModelID, "stream": true,
		"messages": []any{map[string]any{"role": "user", "content": "q"}},
	})
	r := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(string(body)))
	r.Header.Set("Authorization", "Bearer "+testKey)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != 200 || calls != 1 || !strings.Contains(w.Body.String(), "content_filter") {
		t.Fatalf("status=%d calls=%d body=%s", w.Code, calls, w.Body)
	}
}
