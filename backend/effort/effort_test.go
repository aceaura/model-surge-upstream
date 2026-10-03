package effort

import (
	"encoding/json"
	"slices"
	"testing"

	"github.com/aceaura/model-surge-upstream/backend/provider"
)

// TestDefaults 锁定协议默认与模型名规则的推导结果。
func TestDefaults(t *testing.T) {
	cases := []struct {
		name     string
		protocol string
		native   string
		want     []string
	}{
		{"responses 默认四档", provider.ProtocolResponses, "gpt-6.1-sol", []string{"minimal", "low", "medium", "high"}},
		{"chat_completions 默认三档", provider.ProtocolChatCompletions, "o4-mini", []string{"low", "medium", "high"}},
		{"anthropic 协议无 effort", provider.ProtocolAnthropic, "claude-opus-4", []string{}},
		{"gemini 协议无 effort", provider.ProtocolGemini, "gemini-2.5-pro", []string{}},
		{"cc 协议的 gemini 模型只分两档", provider.ProtocolChatCompletions, "gemini-2.5-flash", []string{"low", "high"}},
		{"kimi 思考是开关不是档", provider.ProtocolChatCompletions, "kimi-k2-thinking", []string{}},
		{"anthropic 协议的 kimi 同样没有", provider.ProtocolAnthropic, "kimi-k3-256k", []string{}},
		{"deepseek 拒收 reasoning_effort", provider.ProtocolChatCompletions, "deepseek-v4", []string{}},
		{"模型名大小写不敏感", provider.ProtocolChatCompletions, "Kimi-K2", []string{}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := Defaults(tc.protocol, tc.native)
			if !slices.Equal(got, tc.want) {
				t.Errorf("Defaults(%q, %q) = %v, want %v", tc.protocol, tc.native, got, tc.want)
			}
		})
	}
}

// TestEffective 校验:null/空走自动推导;数组按词表升序去重;
// 非法档位与非数组形态报错;显式空数组=不支持。
func TestEffective(t *testing.T) {
	got, err := Effective(provider.ProtocolResponses, "gpt-6.1-sol", nil)
	if err != nil || !slices.Equal(got, []string{"minimal", "low", "medium", "high"}) {
		t.Errorf("null = %v, %v, want 自动四档", got, err)
	}

	got, err = Effective(provider.ProtocolResponses, "gpt-6.1-sol", json.RawMessage(`["high","low","high","xhigh"]`))
	if err != nil || !slices.Equal(got, []string{"low", "high", "xhigh"}) {
		t.Errorf("显式数组 = %v, %v, want 升序去重 [low high xhigh]", got, err)
	}

	got, err = Effective(provider.ProtocolResponses, "gpt-6.1-sol", json.RawMessage(`[]`))
	if err != nil || len(got) != 0 {
		t.Errorf("显式空数组 = %v, %v, want 空(不支持)", got, err)
	}

	if _, err = Effective(provider.ProtocolResponses, "x", json.RawMessage(`["turbo"]`)); err == nil {
		t.Error("词表外档位应报错")
	}
	if _, err = Effective(provider.ProtocolResponses, "x", json.RawMessage(`{"a":1}`)); err == nil {
		t.Error("非数组形态应报错")
	}
}
