package compact

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/aceaura/model-surge-upstream/backend/provider"
	"github.com/aceaura/model-surge-upstream/backend/resolve"
	"github.com/aceaura/model-surge-upstream/backend/usage"
)

// longAnthropicBody 构造一个估算必超窗口的 Anthropic 请求体。
func longAnthropicBody(turns int) map[string]any {
	msgs := []any{}
	pad := strings.Repeat("很长的中文内容用来撑估算。", 20)
	for i := 0; i < turns; i++ {
		msgs = append(msgs,
			map[string]any{"role": "user", "content": pad},
			map[string]any{"role": "assistant", "content": pad},
		)
	}
	return map[string]any{"model": "alias", "messages": msgs}
}

func autoTarget(baseURL string, window int) resolve.ResolvedTarget {
	return resolve.ResolvedTarget{
		ModelID:       "m1",
		Account:       "acc",
		Protocol:      provider.ProtocolAnthropic,
		BaseURL:       baseURL,
		NativeModel:   "claude-native",
		ContextWindow: window,
		Headers:       map[string]string{"x-api-key": "k"},
		Compact:       json.RawMessage(`{"mode":"auto"}`),
	}
}

// fakeSummaryUpstream 区分摘要调用（末条消息含压缩指令）与普通请求。
func fakeSummaryUpstream(t *testing.T, status int) (*httptest.Server, *int32) {
	t.Helper()
	var summaryCalls int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		if strings.Contains(string(raw), "压缩为一份详尽摘要") {
			atomic.AddInt32(&summaryCalls, 1)
			w.WriteHeader(status)
			if status == http.StatusOK {
				_, _ = w.Write([]byte(`{"content":[{"type":"text","text":"[SUMMARY]"}],"usage":{"input_tokens":500,"output_tokens":10}}`))
			}
			return
		}
		t.Error("压缩路径下不应出现非摘要调用")
	}))
	return srv, &summaryCalls
}

func TestRunPassiveNoop(t *testing.T) {
	r := NewRunner(DefaultConfig()) // 全局默认 passive
	tgt := autoTarget("http://unused", 1)
	tgt.Compact = nil // 模型未配置 → 全局默认
	body := longAnthropicBody(10)
	out, _, reject := r.Run(context.Background(), tgt, body, nil)
	if reject {
		t.Fatal("passive 不应拒绝")
	}
	if len(out["messages"].([]any)) != 20 {
		t.Fatal("passive 不应改 body")
	}
}

func TestRunBelowThreshold(t *testing.T) {
	r := NewRunner(DefaultConfig())
	tgt := autoTarget("http://unused", 1000000)
	out, _, reject := r.Run(context.Background(), tgt, longAnthropicBody(2), nil)
	if reject || len(out["messages"].([]any)) != 4 {
		t.Fatal("未超阈值应原样放行")
	}
}

func TestRunErrorModeRejects(t *testing.T) {
	r := NewRunner(DefaultConfig())
	tgt := autoTarget("http://unused", 100)
	tgt.Compact = json.RawMessage(`{"mode":"error"}`)
	_, _, reject := r.Run(context.Background(), tgt, longAnthropicBody(10), nil)
	if !reject {
		t.Fatal("error 模式超限应 reject")
	}
}

func TestRunAutoCompacts(t *testing.T) {
	srv, summaryCalls := fakeSummaryUpstream(t, http.StatusOK)
	defer srv.Close()
	r := NewRunner(DefaultConfig())
	r.logf = func(string, ...any) {}

	var recorded usage.Usage
	tgt := autoTarget(srv.URL, 100)
	out, _, reject := r.Run(context.Background(), tgt, longAnthropicBody(10),
		func(u usage.Usage, _ int, _, _ time.Duration) { recorded = u })
	if reject {
		t.Fatal("auto 模式不应 reject")
	}
	if atomic.LoadInt32(summaryCalls) != 1 {
		t.Fatalf("摘要调用次数 = %d, want 1", summaryCalls)
	}
	msgs := out["messages"].([]any)
	// [摘要user, 假assistant] + 最近 6 轮（12 条）
	if len(msgs) != 14 {
		t.Fatalf("压缩后 messages = %d, want 14", len(msgs))
	}
	sumBlock := msgs[0].(map[string]any)["content"].([]any)[0].(map[string]any)
	if !strings.Contains(sumBlock["text"].(string), "[SUMMARY]") {
		t.Fatalf("首条应为摘要, got %v", sumBlock["text"])
	}
	if recorded.InputTokens != 500 || recorded.OutputTokens != 10 {
		t.Fatalf("摘要用量未记录: %+v", recorded)
	}
}

func TestRunAutoFallbackOnUpstreamError(t *testing.T) {
	srv, _ := fakeSummaryUpstream(t, http.StatusInternalServerError)
	defer srv.Close()
	r := NewRunner(DefaultConfig())
	r.logf = func(string, ...any) {}
	tgt := autoTarget(srv.URL, 100)
	body := longAnthropicBody(10)
	out, _, reject := r.Run(context.Background(), tgt, body, nil)
	if reject {
		t.Fatal("fail-open 不应 reject")
	}
	if len(out["messages"].([]any)) != 20 {
		t.Fatal("摘要失败应原样转发")
	}
}

func TestRunAutoFallbackOnBadJSON(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`not json`))
	}))
	defer srv.Close()
	r := NewRunner(DefaultConfig())
	r.logf = func(string, ...any) {}
	tgt := autoTarget(srv.URL, 100)
	body := longAnthropicBody(10)
	out, _, _ := r.Run(context.Background(), tgt, body, nil)
	if len(out["messages"].([]any)) != 20 {
		t.Fatal("响应解析失败应原样转发")
	}
}

func TestRunUnsupportedProtocol(t *testing.T) {
	r := NewRunner(DefaultConfig())
	tgt := autoTarget("http://unused", 1)
	tgt.Protocol = provider.ProtocolGemini // 二期才覆盖
	body := longAnthropicBody(10)
	out, _, reject := r.Run(context.Background(), tgt, body, nil)
	if reject || len(out["messages"].([]any)) != 20 {
		t.Fatal("未覆盖协议应原样放行")
	}
}

func TestRunZeroWindow(t *testing.T) {
	r := NewRunner(DefaultConfig())
	tgt := autoTarget("http://unused", 0) // 未声明窗口
	body := longAnthropicBody(10)
	out, _, _ := r.Run(context.Background(), tgt, body, nil)
	if len(out["messages"].([]any)) != 20 {
		t.Fatal("context_window=0 不应触发压缩")
	}
}

func TestRunAutoFallbackOnNoCutPoint(t *testing.T) {
	srv, _ := fakeSummaryUpstream(t, http.StatusOK)
	defer srv.Close()
	r := NewRunner(DefaultConfig())
	r.logf = func(string, ...any) {}
	tgt := autoTarget(srv.URL, 100)
	// 一条真人发言 + 长 tool 链：无安全切点
	body := map[string]any{
		"model": "alias",
		"messages": []any{
			map[string]any{"role": "user", "content": strings.Repeat("长", 500)},
			map[string]any{"role": "assistant", "content": []any{map[string]any{"type": "tool_use", "id": "t1"}}},
			map[string]any{"role": "user", "content": []any{map[string]any{"type": "tool_result", "tool_use_id": "t1", "content": strings.Repeat("果", 500)}}},
		},
	}
	out, _, _ := r.Run(context.Background(), tgt, body, nil)
	if len(out["messages"].([]any)) != 3 {
		t.Fatal("无安全切点应原样转发")
	}
}
