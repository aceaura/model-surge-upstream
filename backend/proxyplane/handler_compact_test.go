package proxyplane

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/aceaura/model-surge-upstream/backend/compact"
	"github.com/aceaura/model-surge-upstream/backend/resolve"
)

// compactUpstream 只服务主请求：回 SSE 流并记录请求体。
type compactUpstream struct {
	mu           sync.Mutex
	forwardBody  []byte
	forwardCalls int
}

func (u *compactUpstream) handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		u.mu.Lock()
		defer u.mu.Unlock()
		u.forwardCalls++
		u.forwardBody = raw
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"id\":\"msg_1\",\"usage\":{\"input_tokens\":20,\"output_tokens\":1}}}\n\n"+
			"event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"delta\":{\"type\":\"text_delta\",\"text\":\"hello\"}}\n\n"+
			"event: message_delta\ndata: {\"type\":\"message_delta\",\"usage\":{\"output_tokens\":5}}\n\n")
	})
}

// longHistory 构造估算必超小窗口的 n 轮中文历史。
func longHistory(n int) string {
	var sb strings.Builder
	sb.WriteString(`{"model":"c-auto","max_tokens":100,"stream":true,"messages":[`)
	pad := strings.Repeat("很长的中文内容用来撑估算。", 20)
	for i := 0; i < n; i++ {
		if i > 0 {
			sb.WriteString(",")
		}
		round, _ := json.Marshal([]any{
			map[string]any{"role": "user", "content": pad},
			map[string]any{"role": "assistant", "content": pad},
		})
		sb.WriteString(strings.TrimSuffix(strings.TrimPrefix(string(round), "["), "]"))
	}
	sb.WriteString(`]}`)
	return sb.String()
}

func compactResolver(upURL string, mode string) fakeResolver {
	return fakeResolver{targets: map[string]resolve.ResolvedTarget{
		"c-auto": {
			ModelID: "c-auto", Account: "acc", Protocol: "anthropic",
			BaseURL: upURL, NativeModel: "claude-native", ContextWindow: 100,
			Headers: map[string]string{"x-api-key": "k"},
			Compact: json.RawMessage(`{"mode":"` + mode + `"}`),
		},
	}}
}

type recordSink struct {
	mu   sync.Mutex
	recs []UsageRecord
}

func (s *recordSink) add(_ context.Context, rec UsageRecord) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.recs = append(s.recs, rec)
}

func (s *recordSink) all() []UsageRecord {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]UsageRecord{}, s.recs...)
}

func TestErrorModeRejectsWithNativeShape(t *testing.T) {
	up := &compactUpstream{}
	srv := httptest.NewServer(up.handler())
	defer srv.Close()

	sink := &recordSink{}
	h := NewHandler(testKey, compactResolver(srv.URL, "error"), sink.add).
		WithCompactor(compact.NewRunner(compact.DefaultConfig()))

	rec := doRequest(t, h, http.MethodPost, "/v1/messages",
		map[string]string{"x-api-key": testKey}, longHistory(10))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
	var env struct {
		Type  string `json:"type"`
		Error struct {
			Type    string `json:"type"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &env); err != nil {
		t.Fatalf("bad error body: %v", err)
	}
	// 对齐真实上游文案，客户端按既有逻辑触发 compact。
	if env.Error.Type != "invalid_request_error" || !strings.Contains(env.Error.Message, "prompt is too long") {
		t.Fatalf("error = %+v", env.Error)
	}
	if up.forwardCalls != 0 {
		t.Fatal("error 模式不应触达上游")
	}
	// 失败也记一条用量（状态 400、用量为 0）。
	recs := sink.all()
	if len(recs) != 1 || recs[0].StatusCode != http.StatusBadRequest {
		t.Fatalf("records = %+v", recs)
	}
}

func TestPassiveModePassesThrough(t *testing.T) {
	up := &compactUpstream{}
	srv := httptest.NewServer(up.handler())
	defer srv.Close()

	h := NewHandler(testKey, compactResolver(srv.URL, "passive"), nil).
		WithCompactor(compact.NewRunner(compact.DefaultConfig()))

	rec := doRequest(t, h, http.MethodPost, "/v1/messages",
		map[string]string{"x-api-key": testKey}, longHistory(10))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	var fwd map[string]any
	_ = json.Unmarshal(up.forwardBody, &fwd)
	if len(fwd["messages"].([]any)) != 20 {
		t.Fatal("passive 应原样转发 20 条历史")
	}
}
