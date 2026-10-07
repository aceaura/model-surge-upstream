package kiro

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// 三十二轮:Python str.strip() 的空白类含 FS/GS/RS/US 四个控制字符
// (str.isspace() 为真),Go strings.TrimSpace 不含。四个用户输入触点须与
// Python 一致钳制。

const (
	round32FS = "\x1c"
	round32US = "\x1f"
)

func TestRound32PyStripEffort(t *testing.T) {
	// converters_anthropic.py:392 / converters_openai.py:336:
	// value.strip().lower() 后控制字符包裹的 high 钳制为 high;旧 Go 代码
	// 不识别该空白,落入 EFFORT_FALLBACK medium。
	for _, protocol := range []string{"anthropic", "openai"} {
		t.Run(protocol, func(t *testing.T) {
			root := object{"model": "claude-sonnet-4.6", "max_tokens": 1,
				"messages": []any{object{"role": "user", "content": "q"}}}
			if protocol == "openai" {
				root["reasoning_effort"] = round32FS + "high" + round32US
			} else {
				root["output_config"] = object{"effort": round32FS + "high" + round32US}
			}
			payload, _, err := convertRequest([]byte(jsonText(root)), protocol, "")
			if err != nil {
				t.Fatal(err)
			}
			got := str(obj(obj(payload["additionalModelRequestFields"])["output_config"])["effort"])
			if got != "high" {
				t.Fatalf("effort=%q want high", got)
			}
		})
	}
}

func TestRound32PyStripSystem(t *testing.T) {
	// converters_openai.py:175: system_prompt.strip() 后拼入系统段。
	root := object{"model": "claude-sonnet-4.6", "messages": []any{
		object{"role": "system", "content": round32FS + "round32 system" + round32US},
		object{"role": "user", "content": "hi"},
	}}
	payload, _, err := convertRequest([]byte(jsonText(root)), "openai", "")
	if err != nil {
		t.Fatal(err)
	}
	content := str(obj(obj(obj(payload["conversationState"])["currentMessage"])["userInputMessage"])["content"])
	if strings.ContainsAny(content, round32FS+round32US) || !strings.Contains(content, "round32 system") {
		t.Fatalf("system not python-stripped: %q", content)
	}
}

func TestRound32PyStripToolDescription(t *testing.T) {
	// converters_core.py:703: not description.strip() → 占位 "Tool: <name>"。
	root := object{"model": "claude-sonnet-4.6", "max_tokens": 1,
		"messages": []any{object{"role": "user", "content": "q"}},
		"tools":    []any{object{"name": "round32_tool", "description": round32FS, "input_schema": object{}}}}
	payload, _, err := convertRequest([]byte(jsonText(root)), "anthropic", "")
	if err != nil {
		t.Fatal(err)
	}
	tools := list(obj(obj(obj(obj(payload["conversationState"])["currentMessage"])["userInputMessage"])["userInputMessageContext"])["tools"])
	if len(tools) != 1 {
		t.Fatalf("tools=%v", tools)
	}
	if got := str(obj(obj(tools[0])["toolSpecification"])["description"]); got != "Tool: round32_tool" {
		t.Fatalf("description=%q want placeholder", got)
	}
}

func TestRound32PyStripToolArguments(t *testing.T) {
	// converters_core.py:872: coerce_tool_input_to_dict 先 strip 再
	// json.loads;旧 Go 直接解析,控制字符前缀使 JSON 解析失败退化 {}。
	u := toolUse("round32-call", "f", round32FS+`{"a":1}`+round32US)
	input, ok := u["input"].(object)
	if !ok || input["a"] != json.Number("1") {
		t.Fatalf("input=%v want map[a:1]", u["input"])
	}
}

func TestRound32FirstTokenTimeoutMarked(t *testing.T) {
	// streaming_openai.py:655-660: 首 token 耗尽是超时类,转发面须能据此
	// 判 504;旧代码只给普通错误。
	stubTimeout(t, 20*time.Millisecond)
	var calls atomic.Int32
	hang := roundTripFunc(func(r *http.Request) (*http.Response, error) {
		calls.Add(1)
		return newHangResponse(r), nil
	})
	r := clientRequest(tbOf(t), "http://unused", "openai", true)
	_, err := NewTransport(hang).RoundTrip(r)
	if !errors.Is(err, ErrUpstreamTimeout) {
		t.Fatalf("err=%v not marked as upstream timeout", err)
	}
	if !strings.Contains(err.Error(), "did not respond") {
		t.Fatalf("message lost: %v", err)
	}
	if calls.Load() != firstTokenMaxAttempts {
		t.Fatalf("calls=%d", calls.Load())
	}
}

type round32TimeoutErr struct{}

func (round32TimeoutErr) Error() string   { return "simulated dial timeout" }
func (round32TimeoutErr) Timeout() bool   { return true }
func (round32TimeoutErr) Temporary() bool { return true }

func TestRound32NetworkTimeoutMarked(t *testing.T) {
	// network_errors.py: TimeoutException → 504,其余网络错误 → 502;
	// 耗尽后的超时类网络错误须带标记,非超时类不带。
	origBackoff := retryBackoff
	retryBackoff = func(int) time.Duration { return 0 }
	t.Cleanup(func() { retryBackoff = origBackoff })
	t.Run("timeout", func(t *testing.T) {
		flaky := roundTripFunc(func(r *http.Request) (*http.Response, error) {
			return nil, round32TimeoutErr{}
		})
		r := clientRequest(tbOf(t), "http://unused", "openai", true)
		_, err := NewTransport(flaky).RoundTrip(r)
		if !errors.Is(err, ErrUpstreamTimeout) || !strings.Contains(err.Error(), "simulated dial timeout") {
			t.Fatalf("err=%v", err)
		}
	})
	t.Run("non-timeout", func(t *testing.T) {
		flaky := roundTripFunc(func(r *http.Request) (*http.Response, error) {
			return nil, errors.New("connection refused")
		})
		r := clientRequest(tbOf(t), "http://unused", "openai", true)
		_, err := NewTransport(flaky).RoundTrip(r)
		if err == nil || errors.Is(err, ErrUpstreamTimeout) {
			t.Fatalf("err=%v", err)
		}
	})
}
