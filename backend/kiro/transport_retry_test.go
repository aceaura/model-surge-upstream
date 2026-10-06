package kiro

import (
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
	server := stub(t, wire, nil)
	// 第一次请求 200 但不出字节,触发首 token 超时重发;第二次正常返回。
	slow := roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if calls.Add(1) == 1 {
			return newHangResponse(r), nil
		}
		return http.DefaultTransport.RoundTrip(r)
	})
	r := clientRequest(tbOf(t), server.URL, "openai", true)
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
	server := stub(t, wire, nil)
	flaky := roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if calls.Add(1) <= 2 {
			return nil, &net.OpError{Op: "read", Err: errors.New("connection reset by peer")}
		}
		return http.DefaultTransport.RoundTrip(r)
	})
	origBackoff := retryBackoff
	retryBackoff = func(int) time.Duration { return time.Millisecond }
	t.Cleanup(func() { retryBackoff = origBackoff })
	r := clientRequest(tbOf(t), server.URL, "openai", true)
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
	_, err := NewTransport(nil).RoundTrip(r)
	if err == nil || !strings.Contains(err.Error(), "required returned no tools") {
		t.Fatalf("err=%v", err)
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
