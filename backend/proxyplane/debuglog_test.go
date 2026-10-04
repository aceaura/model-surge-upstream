package proxyplane

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/aceaura/model-surge-upstream/backend/ringlog"
)

func TestRequestDebugViewOmitsLongTextKeepsParams(t *testing.T) {
	longSystem := strings.Repeat("系", 100)
	body := map[string]any{
		"model":       "k3-256k",
		"messages":    []any{map[string]any{"role": "user", "content": "hello"}, map[string]any{"role": "assistant", "content": "hi"}},
		"system":      longSystem,
		"max_tokens":  float64(8192),
		"stream":      true,
		"temperature": 0.6,
		"thinking":    map[string]any{"type": "enabled", "budget_tokens": float64(2000)},
	}
	view := requestDebugView(body)

	if got := view["messages"]; got != "(2 items omitted)" {
		t.Fatalf("messages placeholder = %v", got)
	}
	if got := view["system"]; got != "(100 chars omitted)" {
		t.Fatalf("system placeholder = %v", got)
	}
	if got := view["max_tokens"]; got != float64(8192) {
		t.Fatalf("max_tokens = %v", got)
	}
	if got := view["stream"]; got != true {
		t.Fatalf("stream = %v", got)
	}
	thinking := view["thinking"].(map[string]any)
	if thinking["budget_tokens"] != float64(2000) || thinking["type"] != "enabled" {
		t.Fatalf("thinking = %v", thinking)
	}
	// 视图不得改动原体:merged 还要继续发往上游。
	if _, ok := body["messages"].([]any); !ok {
		t.Fatal("original body mutated")
	}
}

func TestClampStringBoundaries(t *testing.T) {
	at := strings.Repeat("a", stringClamp)
	if got := clampString(at); got != at {
		t.Fatal("at-limit string should pass through")
	}
	over := strings.Repeat("汉", stringClamp+1)
	got := clampString(over)
	if !strings.HasPrefix(got, strings.Repeat("汉", stringHead)) {
		t.Fatal("clamped string should keep rune head")
	}
	if !strings.HasSuffix(got, fmt.Sprintf("(%d chars total)", stringClamp+1)) {
		t.Fatalf("clamped string should note total runes, got suffix %q", got[len(got)-20:])
	}
}

func TestResponseDebugText(t *testing.T) {
	// JSON 响应:原样输出,内部超长字符串截断。
	big := strings.Repeat("x", stringClamp+10)
	in := fmt.Sprintf(`{"id":"m1","content":[{"text":%q}]}`, big)
	out := responseDebugText([]byte(in), int64(len(in)))
	var decoded map[string]any
	if err := json.Unmarshal([]byte(out), &decoded); err != nil {
		t.Fatalf("JSON response should stay JSON, got %q…: %v", out[:40], err)
	}
	text := decoded["content"].([]any)[0].(map[string]any)["text"].(string)
	if !strings.HasSuffix(text, fmt.Sprintf("(%d chars total)", stringClamp+10)) {
		t.Fatalf("nested long string not clamped: %q", text[len(text)-30:])
	}

	// SSE 原文:不是 JSON,给原文头部。
	sse := "data: {\"type\":\"ping\"}\n\ndata: {\"type\":\"delta\"}\n\n"
	if got := responseDebugText([]byte(sse), int64(len(sse))); got != sse {
		t.Fatalf("SSE text should pass through, got %q", got)
	}

	// 捕获被截断:标注实际总量。
	part := `{"a":"` + strings.Repeat("y", 100)
	got := responseDebugText([]byte(part), 999999)
	if !strings.HasSuffix(got, "…(999999 bytes total)") {
		t.Fatalf("truncated capture should note total, got %q", got[len(got)-30:])
	}

	if got := responseDebugText(nil, 0); got != "(empty)" {
		t.Fatalf("empty body = %q", got)
	}
}

func TestCappedWriter(t *testing.T) {
	w := &cappedWriter{}
	chunk := make([]byte, captureLimit)
	for i := range chunk {
		chunk[i] = 'a'
	}
	if n, _ := w.Write(chunk); n != captureLimit {
		t.Fatalf("write n = %d", n)
	}
	if n, _ := w.Write([]byte("overflow")); n != len("overflow") {
		t.Fatalf("overflow write must still report full length, n = %d", n)
	}
	if w.buf.Len() != captureLimit {
		t.Fatalf("buffer should cap at %d, got %d", captureLimit, w.buf.Len())
	}
}

func TestForwardLogsRequestAndResponse(t *testing.T) {
	h, _, cleanup := newTestHandler(t, "")
	defer cleanup()
	ringlog.Clear()

	body := `{"model":"my-claude","max_tokens":100,"messages":[{"role":"user","content":"hi"}]}`
	rec := doRequest(t, h, "POST", "/v1/messages", map[string]string{"x-api-key": testKey}, body)
	if rec.Code != 200 {
		t.Fatalf("status = %d", rec.Code)
	}

	var reqLine, respLine string
	for _, e := range ringlog.Since(0) {
		if e.Source != "proxy" {
			continue
		}
		if strings.HasPrefix(e.Msg, "→ ") {
			reqLine = e.Msg
		}
		if strings.HasPrefix(e.Msg, "← ") {
			respLine = e.Msg
		}
	}
	if reqLine == "" || respLine == "" {
		t.Fatalf("missing log lines, request=%q response=%q", reqLine, respLine)
	}
	for _, want := range []string{"POST", "native=kimi-k2", `"messages":"(1 items omitted)"`, `"max_tokens":8192`} {
		if !strings.Contains(reqLine, want) {
			t.Fatalf("request line missing %q: %s", want, reqLine)
		}
	}
	// overrides 把 max_tokens 钉到 8192:日志里出现 100 说明记的不是最终体。
	if strings.Contains(reqLine, `"max_tokens":100`) {
		t.Fatalf("request line should show final merged body: %s", reqLine)
	}
	for _, want := range []string{"← 200", "model=my-claude", `{"ok":true}`} {
		if !strings.Contains(respLine, want) {
			t.Fatalf("response line missing %q: %s", want, respLine)
		}
	}
}

func TestForwardLogsUpstreamFailureAsWarn(t *testing.T) {
	h, _, cleanup := newTestHandler(t, "http://127.0.0.1:1") // 不可达
	defer cleanup()
	ringlog.Clear()

	body := `{"model":"my-claude","max_tokens":100,"messages":[]}`
	rec := doRequest(t, h, "POST", "/v1/messages", map[string]string{"x-api-key": testKey}, body)
	if rec.Code != 502 {
		t.Fatalf("status = %d", rec.Code)
	}
	found := false
	for _, e := range ringlog.Since(0) {
		if e.Source == "proxy" && e.Level == ringlog.LevelWarn && strings.HasPrefix(e.Msg, "← error model=my-claude") {
			found = true
		}
	}
	if !found {
		t.Fatal("upstream dial failure should log a warn line")
	}
}
