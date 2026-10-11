package effort

import "testing"

// TestCoerce 矫正矩阵:空表恒等 / 别名归一 / 精确命中 / 向下钳 / 低于最低档
// 取最低 / 未知值 medium 兜底 / 自定义档参与。
func TestCoerce(t *testing.T) {
	full := []Entry{{Name: "0", Value: "none"}, {Name: "1", Value: "low"}, {Name: "2", Value: "medium"}, {Name: "3", Value: "high"}}
	noNone := []Entry{{Name: "1", Value: "low"}, {Name: "2", Value: "high"}}
	kimi := []Entry{{Name: "1", Value: "low"}, {Name: "2", Value: "high"}, {Name: "3", Value: "max"}}
	custom := []Entry{{Name: "低", Value: "low"}, {Name: "ultra", Value: "ultra"}}
	onlyNone := []Entry{{Name: "0", Value: "none"}}

	for _, tc := range []struct {
		name        string
		value       string
		list        []Entry
		want        string
		wantChanged bool
	}{
		{name: "空表恒等透传", value: "whatever", list: nil, want: "whatever"},
		{name: "精确命中不算矫正", value: "high", list: full, want: "high"},
		{name: "大小写归一命中", value: "HIGH", list: full, want: "high", wantChanged: true},
		{name: "别名 off→none", value: "off", list: full, want: "none", wantChanged: true},
		{name: "别名 disabled→none 但表无 none 钳最低", value: "disabled", list: noNone, want: "low", wantChanged: true},
		{name: "别名 extra-high→xhigh 表内无 xhigh 钳 high", value: "extra-high", list: full, want: "high", wantChanged: true},
		{name: "未声明 medium 之外档向下钳:xhigh→high", value: "xhigh", list: full, want: "high", wantChanged: true},
		{name: "未声明档向下钳:kimi 表 medium→low", value: "medium", list: kimi, want: "low", wantChanged: true},
		{name: "kimi 表 max 精确命中", value: "max", list: kimi, want: "max"},
		{name: "低于最低档取最低:none→low(表无 none)", value: "none", list: noNone, want: "low", wantChanged: true},
		{name: "表声明 none 则 none 命中", value: "none", list: full, want: "none"},
		{name: "minimal 低于 low 钳最低", value: "minimal", list: full, want: "none", wantChanged: true},
		{name: "未知自定义值 medium 兜底", value: "ultra", list: full, want: "medium", wantChanged: true},
		{name: "未知值无 medium 取首个非 none", value: "ultra", list: noNone, want: "low", wantChanged: true},
		{name: "表只有 none 回 none", value: "ultra", list: onlyNone, want: "none", wantChanged: true},
		{name: "声明的自定义值精确命中", value: "ultra", list: custom, want: "ultra"},
		{name: "high 遇自定义表:强度成员只有 low 钳 low", value: "high", list: custom, want: "low", wantChanged: true},
		{name: "未知值遇自定义表 medium 兜底失败取首个非 none", value: "mega", list: custom, want: "low", wantChanged: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, changed := Coerce(tc.value, tc.list)
			if got != tc.want || changed != tc.wantChanged {
				t.Errorf("Coerce(%q) = %q,%v, want %q,%v", tc.value, got, changed, tc.want, tc.wantChanged)
			}
		})
	}
}

// TestFormatsForProtocol 协议同族约束表。
func TestFormatsForProtocol(t *testing.T) {
	for _, tc := range []struct {
		protocol     string
		wantEntry    []string
		wantUpstream []string
	}{
		{"chat_completions", []string{FormatOpenAIChat}, []string{FormatOpenAIChat}},
		{"responses", []string{FormatOpenAIResponses}, []string{FormatOpenAIResponses}},
		{"anthropic", []string{FormatAnthropicEffort, FormatAnthropicBudget, FormatAnthropicAdaptive, FormatAnthropicOff},
			[]string{FormatAnthropicEffort, FormatAnthropicBudget, FormatAnthropicAdaptive, FormatAnthropicOff}},
		{"gemini", nil, []string{FormatGeminiLevel, FormatGeminiBudget}},
		{"unknown", nil, nil},
	} {
		entry, upstream := FormatsForProtocol(tc.protocol)
		if !sameStrings(entry, tc.wantEntry) || !sameStrings(upstream, tc.wantUpstream) {
			t.Errorf("FormatsForProtocol(%q) = %v,%v, want %v,%v",
				tc.protocol, entry, upstream, tc.wantEntry, tc.wantUpstream)
		}
	}
}

func sameStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// TestCompatibleWithProtocol 同族判定:空串恒放行、旧值归一后判、跨族拒绝、
// 未知协议不拦。
func TestCompatibleWithProtocol(t *testing.T) {
	for _, tc := range []struct {
		format, protocol string
		want             bool
	}{
		{"", "chat_completions", true},
		{"", "gemini", true},
		{"openai_chat", "chat_completions", true},
		{"chat_completions", "chat_completions", true}, // 旧值归一
		{"openai_chat", "responses", false},
		{"openai_responses", "responses", true},
		{"anthropic_budget", "anthropic", true},
		{"anthropic", "anthropic", true}, // 旧值归一 anthropic_effort
		{"anthropic_effort", "chat_completions", false},
		{"gemini_level", "gemini", true},
		{"gemini", "gemini", true}, // 旧值归一 gemini_level
		{"gemini_level", "anthropic", false},
		{"openai_chat", "unknown_protocol", true}, // 未知协议不拦
		{"bogus_format", "unknown_protocol", true},
	} {
		if got := CompatibleWithProtocol(tc.format, tc.protocol); got != tc.want {
			t.Errorf("CompatibleWithProtocol(%q,%q) = %v, want %v", tc.format, tc.protocol, got, tc.want)
		}
	}
}
