package compact

import (
	"encoding/json"
	"testing"
)

func TestEstimateTokens(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want int
	}{
		{"empty", "", 0},
		{"pure ascii", "abcdefghijklmnop", 4}, // 16/4
		{"pure cjk", "你好世界", 4},              // 每字 1 token
		{"mixed", "hello 世界", 1 + 2},          // "hello " 6 ascii → 1，两汉字 → 2
	}
	for _, tt := range tests {
		if got := estimateTokens(tt.in); got != tt.want {
			t.Errorf("%s: estimateTokens = %d, want %d", tt.name, got, tt.want)
		}
	}
}

func TestEstimateBody(t *testing.T) {
	body := map[string]any{
		"model":  "my-alias-ignored", // model 是网关别名，不计
		"stream": true,               // 标量不计
		"messages": []any{
			map[string]any{"role": "user", "content": "abcdefghijklmnop"},   // 4 + 4 开销
			map[string]any{"role": "assistant", "content": "你好世界"},        // 4 + 4 开销
			map[string]any{ // 含 tool_use 的 JSON input：字符串值被递归计入
				"role": "assistant",
				"content": []any{
					map[string]any{"type": "tool_use", "input": map[string]any{"path": "abcdefghijklmnop"}},
				},
			}, // input 里字符串 4 + 4 开销
		},
	}
	// 文本（含 role/type 等结构字符串）：msg1 "user"1+16/4=5，msg2
	// "assistant"2+4=6，msg3 "assistant"2+"tool_use"2+input 值 16/4=8，
	// 小计 19；结构开销 3 条 × 4 = 12；合计 31。
	if got := estimateBody(body); got != 31 {
		t.Fatalf("estimateBody = %d, want 31", got)
	}
}

func TestEstimateBodyNoMessages(t *testing.T) {
	body := map[string]any{"model": "x", "max_tokens": json.Number("1")}
	if got := estimateBody(body); got != 0 {
		t.Fatalf("estimateBody = %d, want 0", got)
	}
}
