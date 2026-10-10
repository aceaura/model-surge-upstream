package effort

import (
	"reflect"
	"testing"
)

func TestNormalizeAndValidFormat(t *testing.T) {
	cases := map[string]string{
		"chat_completions": FormatOpenAIChat,
		"responses":        FormatOpenAIResponses,
		"anthropic":        FormatAnthropicEffort,
		"gemini":           FormatGeminiLevel,
		"":                 "",
		"openai_chat":      FormatOpenAIChat,
		"anthropic_budget": FormatAnthropicBudget,
	}
	for in, want := range cases {
		if got := NormalizeFormat(in); got != want {
			t.Errorf("NormalizeFormat(%q) = %q, want %q", in, got, want)
		}
	}
	for _, f := range []string{"", FormatIndex, FormatOpenAIChat, FormatOpenAIResponses,
		FormatAnthropicEffort, FormatAnthropicBudget, FormatAnthropicAdaptive,
		FormatAnthropicOff, FormatGeminiLevel, FormatGeminiBudget,
		"chat_completions", "responses", "anthropic", "gemini"} {
		if !ValidFormat(f) {
			t.Errorf("ValidFormat(%q) 应为真", f)
		}
	}
	for _, f := range []string{"bogus", "skip_none", "reasoning_effort"} {
		if ValidFormat(f) {
			t.Errorf("ValidFormat(%q) 应为假", f)
		}
	}
	// 入口词表不收 gemini(下游适配已删):七种+auto+归一到非 gemini 的别名收;
	// gemini_level/gemini_budget 及 legacy "gemini" 均被入口拒绝。
	for _, f := range []string{"", FormatIndex, FormatOpenAIChat, FormatOpenAIResponses,
		FormatAnthropicEffort, FormatAnthropicBudget, FormatAnthropicAdaptive, FormatAnthropicOff,
		"chat_completions", "responses", "anthropic"} {
		if !ValidEntryFormat(f) {
			t.Errorf("ValidEntryFormat(%q) 应为真", f)
		}
	}
	for _, f := range []string{FormatGeminiLevel, FormatGeminiBudget, "gemini", "bogus"} {
		if ValidEntryFormat(f) {
			t.Errorf("ValidEntryFormat(%q) 应为假", f)
		}
	}
	if !ValidOff(OffDisabled) || !ValidOff(OffBetweenTools) || !ValidOff(OffOmit) || ValidOff("nope") {
		t.Error("ValidOff 判定错误")
	}
}

func TestBudgetAndClamp(t *testing.T) {
	if got := BudgetOf("high", nil); got != 10000 {
		t.Errorf("BudgetOf(high) = %d, want 10000", got)
	}
	if got := BudgetOf("high", map[string]int{"high": 12000}); got != 12000 {
		t.Errorf("覆盖应优先: %d", got)
	}
	if got := BudgetOf("ultra", nil); got != 4000 {
		t.Errorf("表外档回 4000: %d", got)
	}
	if got := ClampBudget(10000, 1024); got != 10000 {
		t.Errorf("max_tokens≤1024 不钳: %d", got)
	}
	if got := ClampBudget(10000, 1025); got != 1024 {
		t.Errorf("钳到 max-1: %d", got)
	}
	if got := ClampBudget(500, 16000); got != 500 {
		t.Errorf("未超不钳: %d", got)
	}
}

var idxList = []Entry{
	{Name: "0", Value: "none"},
	{Name: "1", Value: "low"},
	{Name: "2", Value: "medium"},
	{Name: "3", Value: "high"},
}

func TestRead(t *testing.T) {
	t.Run("index", func(t *testing.T) {
		v, ok := Read(FormatIndex, map[string]any{"reasoning_level": float64(2)}, idxList, nil)
		if !ok || v != "medium" {
			t.Errorf("index 读 = %q,%v", v, ok)
		}
		v, ok = Read(FormatIndex, map[string]any{"reasoning_level": 0}, idxList, nil)
		if !ok || v != "none" {
			t.Errorf("index 0 = %q,%v", v, ok)
		}
		if _, ok := Read(FormatIndex, map[string]any{}, idxList, nil); ok {
			t.Error("缺字段应未命中")
		}
	})
	t.Run("openai", func(t *testing.T) {
		v, ok := Read(FormatOpenAIChat, map[string]any{"reasoning_effort": "high"}, nil, nil)
		if !ok || v != "high" {
			t.Errorf("chat 读 = %q,%v", v, ok)
		}
		v, ok = Read(FormatOpenAIResponses, map[string]any{"reasoning": map[string]any{"effort": "low", "summary": "auto"}}, nil, nil)
		if !ok || v != "low" {
			t.Errorf("responses 读 = %q,%v", v, ok)
		}
	})
	t.Run("anthropic", func(t *testing.T) {
		v, ok := Read(FormatAnthropicEffort, map[string]any{"output_config": map[string]any{"effort": "max"}}, nil, nil)
		if !ok || v != "max" {
			t.Errorf("effort 读 = %q,%v", v, ok)
		}
		v, ok = Read(FormatAnthropicBudget, map[string]any{"thinking": map[string]any{"type": "enabled", "budget_tokens": float64(10000)}}, idxList, nil)
		if !ok || v != "high" {
			t.Errorf("预算反查 = %q,%v", v, ok)
		}
		if _, ok := Read(FormatAnthropicBudget, map[string]any{"thinking": map[string]any{"type": "enabled", "budget_tokens": float64(9999)}}, idxList, nil); ok {
			t.Error("预算未精确命中应未命中")
		}
		v, ok = Read(FormatAnthropicAdaptive, map[string]any{"thinking": map[string]any{"type": "adaptive"}, "output_config": map[string]any{"effort": "high"}}, nil, nil)
		if !ok || v != "high" {
			t.Errorf("adaptive 读 = %q,%v", v, ok)
		}
		if _, ok := Read(FormatAnthropicAdaptive, map[string]any{"thinking": map[string]any{"type": "adaptive"}}, nil, nil); ok {
			t.Error("adaptive 无 effort 应未命中")
		}
		for _, typ := range []string{"disabled", "between_tools"} {
			v, ok := Read(FormatAnthropicOff, map[string]any{"thinking": map[string]any{"type": typ}}, nil, nil)
			if !ok || v != "none" {
				t.Errorf("off 读 %s = %q,%v", typ, v, ok)
			}
		}
	})
}

func TestWriteNoneSemantics(t *testing.T) {
	t.Run("openai 原样", func(t *testing.T) {
		body := map[string]any{}
		Write(FormatOpenAIChat, "chat_completions", nil, body, "none", OffDisabled, nil, 0)
		if body["reasoning_effort"] != "none" {
			t.Errorf("body = %v", body)
		}
	})
	t.Run("gemini 不动体", func(t *testing.T) {
		for _, f := range []string{FormatGeminiLevel, FormatGeminiBudget, FormatAnthropicOff} {
			body := map[string]any{"keep": 1}
			Write(f, "gemini", idxList, body, "high", OffDisabled, nil, 0)
			if f == FormatAnthropicOff {
				if len(body) != 1 {
					t.Errorf("anthropic_off 载档不应写: %v", body)
				}
				continue
			}
			body2 := map[string]any{"keep": 1}
			Write(f, "gemini", idxList, body2, "none", OffDisabled, nil, 0)
			if !reflect.DeepEqual(body2, map[string]any{"keep": 1}) {
				t.Errorf("%s none 不应动体: %v", f, body2)
			}
		}
	})
	t.Run("anthropic 载档按 off 落定", func(t *testing.T) {
		body := map[string]any{}
		Write(FormatAnthropicEffort, "anthropic", nil, body, "none", OffDisabled, nil, 0)
		if body["thinking"].(map[string]any)["type"] != "disabled" {
			t.Errorf("disabled 落定: %v", body)
		}
		body = map[string]any{"thinking": map[string]any{"display": "summarized"}}
		Write(FormatAnthropicBudget, "anthropic", nil, body, "none", OffBetweenTools, nil, 0)
		thinking := body["thinking"].(map[string]any)
		if thinking["type"] != "between_tools" || len(thinking) != 1 {
			t.Errorf("between_tools 应整对象替换: %v", body)
		}
		body = map[string]any{}
		Write(FormatAnthropicAdaptive, "anthropic", nil, body, "none", OffOmit, nil, 0)
		if len(body) != 0 {
			t.Errorf("omit 不应写: %v", body)
		}
	})
}

func TestWriteCarriers(t *testing.T) {
	body := map[string]any{}
	Write(FormatAnthropicBudget, "anthropic", nil, body, "high", OffDisabled, map[string]int{"high": 12000}, 16000)
	thinking := body["thinking"].(map[string]any)
	if thinking["type"] != "enabled" || thinking["budget_tokens"] != 12000 {
		t.Errorf("预算写 = %v", body)
	}
	body = map[string]any{}
	Write(FormatAnthropicBudget, "anthropic", nil, body, "max", OffDisabled, nil, 16000)
	if body["thinking"].(map[string]any)["budget_tokens"] != 15999 {
		t.Errorf("钳制 = %v", body)
	}
	body = map[string]any{"thinking": map[string]any{"display": "omitted"}}
	Write(FormatAnthropicAdaptive, "anthropic", nil, body, "high", OffDisabled, nil, 0)
	thinking = body["thinking"].(map[string]any)
	if thinking["type"] != "adaptive" || thinking["display"] != "omitted" {
		t.Errorf("adaptive 应保留兄弟键: %v", body)
	}
	if body["output_config"].(map[string]any)["effort"] != "high" {
		t.Errorf("adaptive 档位 = %v", body)
	}
	body = map[string]any{}
	Write(FormatGeminiBudget, "gemini", nil, body, "low", OffDisabled, nil, 0)
	got := body["generationConfig"].(map[string]any)["thinkingConfig"].(map[string]any)["thinkingBudget"]
	if got != 1024 {
		t.Errorf("gemini 预算 = %v", body)
	}
	body = map[string]any{}
	Write(FormatIndex, "chat_completions", idxList, body, "medium", OffDisabled, nil, 0)
	if body["reasoning_level"] != 2 {
		t.Errorf("index 写 = %v", body)
	}
	body = map[string]any{}
	Write(FormatIndex, "chat_completions", idxList, body, "none", OffDisabled, nil, 0)
	if body["reasoning_level"] != 0 {
		t.Errorf("index none 写 = %v", body)
	}
}

func TestStripKeys(t *testing.T) {
	body := map[string]any{"reasoning_level": 2, "model": "x"}
	StripKeys(FormatIndex, body)
	if _, ok := body["reasoning_level"]; ok {
		t.Errorf("index 剥离失败: %v", body)
	}
	body = map[string]any{"reasoning": map[string]any{"effort": "high", "summary": "auto"}}
	StripKeys(FormatOpenAIResponses, body)
	reasoning := body["reasoning"].(map[string]any)
	if _, ok := reasoning["effort"]; ok || reasoning["summary"] != "auto" {
		t.Errorf("responses 应保留兄弟键: %v", body)
	}
	body = map[string]any{"reasoning": map[string]any{"effort": "high"}}
	StripKeys(FormatOpenAIResponses, body)
	if _, ok := body["reasoning"]; ok {
		t.Errorf("删空中层应清理: %v", body)
	}
	body = map[string]any{"reasoning_effort": "high"}
	StripKeys(FormatAuto, body)
	if body["reasoning_effort"] != "high" {
		t.Errorf("auto 不应剥离: %v", body)
	}
}

// TestRoundTrip 入口可读格式 Write→Read 往返一致。gemini 只作上游(入口读
// 已删),不在往返集内;anthropic_off 只承载 none,单独验。
func TestRoundTrip(t *testing.T) {
	for _, f := range []string{FormatOpenAIChat, FormatOpenAIResponses, FormatAnthropicEffort,
		FormatAnthropicBudget, FormatAnthropicAdaptive, FormatIndex} {
		body := map[string]any{}
		Write(f, "anthropic", idxList, body, "high", OffDisabled, nil, 0)
		v, ok := Read(f, body, idxList, nil)
		if !ok || v != "high" {
			t.Errorf("%s 往返 = %q,%v (body %v)", f, v, ok, body)
		}
	}
	body := map[string]any{}
	Write(FormatAnthropicOff, "anthropic", idxList, body, "none", OffDisabled, nil, 0)
	v, ok := Read(FormatAnthropicOff, body, idxList, nil)
	if !ok || v != "none" {
		t.Errorf("off 往返 = %q,%v", v, ok)
	}
}

// TestAutoDelegatesApply auto 与协议内置逐字同。
func TestAutoDelegatesApply(t *testing.T) {
	for _, proto := range []string{"chat_completions", "responses", "anthropic", "gemini"} {
		for _, value := range []string{"high", "none"} {
			a := map[string]any{}
			b := map[string]any{}
			Apply(proto, a, value)
			Write(FormatAuto, proto, nil, b, value, OffDisabled, nil, 0)
			if !reflect.DeepEqual(a, b) {
				t.Errorf("%s/%s auto 与 Apply 不同: %v vs %v", proto, value, a, b)
			}
			c := map[string]any{}
			ApplyFormat("", proto, c, value)
			if !reflect.DeepEqual(a, c) {
				t.Errorf("ApplyFormat auto = %v, want %v", c, a)
			}
		}
	}
}

// TestAutoNoneYieldsToOffPolicy auto+anthropic+none 时 off 落定压过
// Apply 的 disabled 默认:omit 不写、between_tools 整对象顶替;
// off 为空(=disabled)与 Apply 逐字同,旧行为不变。
func TestAutoNoneYieldsToOffPolicy(t *testing.T) {
	omit := map[string]any{}
	Write(FormatAuto, "anthropic", nil, omit, "none", OffOmit, nil, 0)
	if len(omit) != 0 {
		t.Errorf("off=omit 不应写任何字段: %v", omit)
	}
	// omit 也不动体里既有的 thinking(不写≠删除)。
	keep := map[string]any{"thinking": map[string]any{"type": "enabled"}}
	Write(FormatAuto, "anthropic", nil, keep, "none", OffOmit, nil, 0)
	if !reflect.DeepEqual(keep["thinking"], map[string]any{"type": "enabled"}) {
		t.Errorf("off=omit 不应动既有 thinking: %v", keep)
	}

	bt := map[string]any{"thinking": map[string]any{"type": "enabled", "budget_tokens": 1000}}
	Write(FormatAuto, "anthropic", nil, bt, "none", OffBetweenTools, nil, 0)
	if !reflect.DeepEqual(bt["thinking"], map[string]any{"type": "between_tools"}) {
		t.Errorf("off=between_tools 应整对象顶替: %v", bt)
	}

	// 非 none 档不受 off 影响,照旧写 output_config.effort。
	carry := map[string]any{}
	Write(FormatAuto, "anthropic", nil, carry, "high", OffOmit, nil, 0)
	output, _ := carry["output_config"].(map[string]any)
	if output["effort"] != "high" {
		t.Errorf("非 none 档应照常写 output_config: %v", carry)
	}
}
