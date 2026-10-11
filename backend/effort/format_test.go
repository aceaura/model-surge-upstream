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
	for _, f := range []string{"", FormatOpenAIChat, FormatOpenAIResponses,
		FormatAnthropicEffort, FormatAnthropicBudget, FormatAnthropicAdaptive,
		FormatAnthropicOff, FormatGeminiLevel, FormatGeminiBudget,
		"chat_completions", "responses", "anthropic", "gemini"} {
		if !ValidFormat(f) {
			t.Errorf("ValidFormat(%q) 应为真", f)
		}
	}
	for _, f := range []string{"bogus", "skip_none", "reasoning_effort", "effort_index"} {
		if ValidFormat(f) {
			t.Errorf("ValidFormat(%q) 应为假", f)
		}
	}
	// 入口词表不收 gemini(下游适配已删):六种+auto+归一到非 gemini 的别名收;
	// gemini_level/gemini_budget 及 legacy "gemini" 均被入口拒绝;effort_index
	// 已废(数字档只走 auto 扩展字段),双端词表均不收。
	for _, f := range []string{"", FormatOpenAIChat, FormatOpenAIResponses,
		FormatAnthropicEffort, FormatAnthropicBudget, FormatAnthropicAdaptive, FormatAnthropicOff,
		"chat_completions", "responses", "anthropic"} {
		if !ValidEntryFormat(f) {
			t.Errorf("ValidEntryFormat(%q) 应为真", f)
		}
	}
	for _, f := range []string{FormatGeminiLevel, FormatGeminiBudget, "gemini", "bogus", "effort_index"} {
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

// pairList 映射对表(2026-10-11 行式映射对):Name=下游档,Value=上行值。
var pairList = []Entry{
	{Name: "none", Value: "none"},
	{Name: "low", Value: "low"},
	{Name: "medium", Value: "medium"},
	{Name: "high", Value: "high"},
}

// thresholdList 下游 budget 阈值行:Name=阈值数字串,Value=规范档。
var thresholdList = []Entry{
	{Name: "none", Value: "none"},
	{Name: "1024", Value: "low"},
	{Name: "4000", Value: "medium"},
	{Name: "10000", Value: "high"},
}

func TestLevelsFor(t *testing.T) {
	if got := LevelsFor(FormatOpenAIChat); len(got) != 7 || got[0] != "none" || got[6] != "max" {
		t.Errorf("openai 词表 = %v, want none..max 7 值", got)
	}
	if got := LevelsFor(FormatAnthropicEffort); len(got) != 6 || got[1] != "low" || got[5] != "max" {
		t.Errorf("anthropic 词表 = %v, want none+官方 5 档", got)
	}
	if got := LevelsFor("anthropic"); len(got) != 6 {
		t.Errorf("旧值别名应归一后命中: %v", got)
	}
	if got := LevelsFor(FormatGeminiLevel); !reflect.DeepEqual(got, []string{"none", "LOW", "HIGH"}) {
		t.Errorf("gemini 词表 = %v", got)
	}
	for _, f := range []string{"", FormatAnthropicBudget, FormatAnthropicOff, FormatGeminiBudget, "bogus"} {
		if LevelsFor(f) != nil {
			t.Errorf("LevelsFor(%q) 应回 nil", f)
		}
	}
}

func TestRead(t *testing.T) {
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
		v, ok = Read(FormatAnthropicBudget, map[string]any{"thinking": map[string]any{"type": "enabled", "budget_tokens": float64(10000)}}, pairList, nil)
		if !ok || v != "high" {
			t.Errorf("预算反查 = %q,%v", v, ok)
		}
		// 矫正版区间反查:9999 钳到预算不超过它的最高档 medium(4000)。
		v, ok = Read(FormatAnthropicBudget, map[string]any{"thinking": map[string]any{"type": "enabled", "budget_tokens": float64(9999)}}, pairList, nil)
		if !ok || v != "medium" {
			t.Errorf("预算 9999 应钳到 medium = %q,%v", v, ok)
		}
		// 低于最小档取最小档(low 1024)。
		v, ok = Read(FormatAnthropicBudget, map[string]any{"thinking": map[string]any{"type": "enabled", "budget_tokens": float64(500)}}, pairList, nil)
		if !ok || v != "low" {
			t.Errorf("预算 500 应钳到 low = %q,%v", v, ok)
		}
		// 阈值行(Name=数字串):Name 直接作阈值,返回 Name 由 Coerce 映射出
		// 上行值;档名行回退 BudgetOf(上面三例已覆盖)。
		v, ok = Read(FormatAnthropicBudget, map[string]any{"thinking": map[string]any{"type": "enabled", "budget_tokens": float64(9999)}}, thresholdList, nil)
		if !ok || v != "4000" {
			t.Errorf("阈值行预算 9999 应回 Name 4000 = %q,%v", v, ok)
		}
		if got, changed := Coerce(v, thresholdList); got != "medium" || !changed {
			t.Errorf("Coerce(4000) = %q,%v, want medium,true", got, changed)
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
		Write(FormatOpenAIChat, "chat_completions", body, "none", OffDisabled, nil, 0)
		if body["reasoning_effort"] != "none" {
			t.Errorf("body = %v", body)
		}
	})
	t.Run("gemini 不动体", func(t *testing.T) {
		for _, f := range []string{FormatGeminiLevel, FormatGeminiBudget} {
			body2 := map[string]any{"keep": 1}
			Write(f, "gemini", body2, "none", OffDisabled, nil, 0)
			if !reflect.DeepEqual(body2, map[string]any{"keep": 1}) {
				t.Errorf("%s none 不应动体: %v", f, body2)
			}
		}
	})
	t.Run("anthropic 载档按 off 落定", func(t *testing.T) {
		body := map[string]any{}
		Write(FormatAnthropicEffort, "anthropic", body, "none", OffDisabled, nil, 0)
		if body["thinking"].(map[string]any)["type"] != "disabled" {
			t.Errorf("disabled 落定: %v", body)
		}
		body = map[string]any{"thinking": map[string]any{"display": "summarized"}}
		Write(FormatAnthropicBudget, "anthropic", body, "none", OffBetweenTools, nil, 0)
		thinking := body["thinking"].(map[string]any)
		if thinking["type"] != "between_tools" || len(thinking) != 1 {
			t.Errorf("between_tools 应整对象替换: %v", body)
		}
		body = map[string]any{}
		Write(FormatAnthropicAdaptive, "anthropic", body, "none", OffOmit, nil, 0)
		if len(body) != 0 {
			t.Errorf("omit 不应写: %v", body)
		}
	})
}

// TestAnthropicOffAlwaysOff 无档位类(anthropic_off)模型思考恒关:命中任何
// 档(含矫正后的非 none 值)都落关思考形态;off 恒为禁用写法。
func TestAnthropicOffAlwaysOff(t *testing.T) {
	for _, value := range []string{"none", "low", "high", "max"} {
		body := map[string]any{"keep": 1}
		Write(FormatAnthropicOff, "anthropic", body, value, OffDisabled, nil, 0)
		thinking, ok := body["thinking"].(map[string]any)
		if !ok || thinking["type"] != "disabled" {
			t.Errorf("anthropic_off 命中 %q 应恒落 disabled: %v", value, body)
		}
		if body["keep"] != 1 {
			t.Errorf("兄弟键不应动: %v", body)
		}
	}
	body := map[string]any{"thinking": map[string]any{"display": "summarized"}}
	Write(FormatAnthropicOff, "anthropic", body, "high", OffBetweenTools, nil, 0)
	thinking := body["thinking"].(map[string]any)
	if thinking["type"] != "between_tools" || len(thinking) != 1 {
		t.Errorf("between_tools 应整对象替换: %v", body)
	}
}

func TestCategoryOf(t *testing.T) {
	cases := map[string]string{
		FormatOpenAIChat: CategoryEffort, FormatOpenAIResponses: CategoryEffort,
		FormatAnthropicEffort: CategoryEffort, FormatAnthropicAdaptive: CategoryEffort,
		FormatGeminiLevel: CategoryEffort,
		FormatAnthropicBudget: CategoryBudget, FormatGeminiBudget: CategoryBudget,
		FormatAnthropicOff: CategoryOff,
		// 旧值别名归一后同分类;auto/未知回空。
		FormatChatCompletions: CategoryEffort, FormatAnthropic: CategoryEffort,
		FormatGemini: CategoryEffort,
		FormatAuto:  "",
		"nonsense": "",
	}
	for format, want := range cases {
		if got := CategoryOf(format); got != want {
			t.Errorf("CategoryOf(%q) = %q, want %q", format, got, want)
		}
	}
}

func TestWriteCarriers(t *testing.T) {
	body := map[string]any{}
	Write(FormatAnthropicBudget, "anthropic", body, "high", OffDisabled, map[string]int{"high": 12000}, 16000)
	thinking := body["thinking"].(map[string]any)
	if thinking["type"] != "enabled" || thinking["budget_tokens"] != 12000 {
		t.Errorf("预算写 = %v", body)
	}
	body = map[string]any{}
	Write(FormatAnthropicBudget, "anthropic", body, "max", OffDisabled, nil, 16000)
	if body["thinking"].(map[string]any)["budget_tokens"] != 15999 {
		t.Errorf("钳制 = %v", body)
	}
	body = map[string]any{"thinking": map[string]any{"display": "omitted"}}
	Write(FormatAnthropicAdaptive, "anthropic", body, "high", OffDisabled, nil, 0)
	thinking = body["thinking"].(map[string]any)
	if thinking["type"] != "adaptive" || thinking["display"] != "omitted" {
		t.Errorf("adaptive 应保留兄弟键: %v", body)
	}
	if body["output_config"].(map[string]any)["effort"] != "high" {
		t.Errorf("adaptive 档位 = %v", body)
	}
	body = map[string]any{}
	Write(FormatGeminiBudget, "gemini", body, "low", OffDisabled, nil, 0)
	got := body["generationConfig"].(map[string]any)["thinkingConfig"].(map[string]any)["thinkingBudget"]
	if got != 1024 {
		t.Errorf("gemini 预算 = %v", body)
	}
	// 数字串上行值(映射对右列 budget 编辑框直填)不经 BudgetOf 直写。
	body = map[string]any{}
	Write(FormatAnthropicBudget, "anthropic", body, "8000", OffDisabled, nil, 0)
	if body["thinking"].(map[string]any)["budget_tokens"] != 8000 {
		t.Errorf("数字串预算直写 = %v", body)
	}
	body = map[string]any{}
	Write(FormatGeminiBudget, "gemini", body, "2048", OffDisabled, nil, 0)
	got = body["generationConfig"].(map[string]any)["thinkingConfig"].(map[string]any)["thinkingBudget"]
	if got != 2048 {
		t.Errorf("gemini 数字串预算直写 = %v", body)
	}
}

func TestStripKeys(t *testing.T) {
	body := map[string]any{"reasoning": map[string]any{"effort": "high", "summary": "auto"}}
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

// TestStripAll 映射写入前的统一清场(2026-10-11 用户硬性要求:先清原档位
// 参数再写新档):八格式键并集全清,兄弟键保留,删空的中层对象一并清理。
func TestStripAll(t *testing.T) {
	body := map[string]any{
		"reasoning_effort": "high",
		"reasoning":        map[string]any{"effort": "low", "summary": "auto"},
		"output_config":    map[string]any{"effort": "max", "format": "json"},
		"thinking":         map[string]any{"type": "enabled", "budget_tokens": 10000, "display": "summarized"},
		"generationConfig": map[string]any{
			"thinkingConfig": map[string]any{"thinkingLevel": "HIGH", "thinkingBudget": 1024},
			"temperature":    0.6,
		},
		"keep": 1,
	}
	StripAll(body)
	if _, ok := body["reasoning_effort"]; ok {
		t.Errorf("reasoning_effort 应清: %v", body)
	}
	reasoning := body["reasoning"].(map[string]any)
	if _, ok := reasoning["effort"]; ok || reasoning["summary"] != "auto" {
		t.Errorf("reasoning 应只留兄弟键: %v", body)
	}
	output := body["output_config"].(map[string]any)
	if _, ok := output["effort"]; ok || output["format"] != "json" {
		t.Errorf("output_config 应只留兄弟键: %v", body)
	}
	thinking := body["thinking"].(map[string]any)
	if _, ok := thinking["type"]; ok {
		t.Errorf("thinking.type 应清: %v", body)
	}
	if _, ok := thinking["budget_tokens"]; ok || thinking["display"] != "summarized" {
		t.Errorf("thinking 应只留兄弟键: %v", body)
	}
	generation := body["generationConfig"].(map[string]any)
	if _, ok := generation["thinkingConfig"]; ok || generation["temperature"] != 0.6 {
		t.Errorf("thinkingConfig 删空应整键清,兄弟键保留: %v", body)
	}
	if body["keep"] != 1 {
		t.Errorf("无关键不应动: %v", body)
	}

	// 删空的中层对象整键清理,不留空壳。
	shell := map[string]any{
		"reasoning":        map[string]any{"effort": "low"},
		"thinking":         map[string]any{"type": "disabled"},
		"generationConfig": map[string]any{"thinkingConfig": map[string]any{"thinkingLevel": "LOW"}},
	}
	StripAll(shell)
	if len(shell) != 0 {
		t.Errorf("空壳中层应整键清: %v", shell)
	}

	// 清场后写入只存目标字段,旧形态残留不并存。
	Write(FormatAnthropicEffort, "anthropic", body, "high", OffDisabled, nil, 0)
	if _, ok := body["reasoning_effort"]; ok {
		t.Errorf("清场后写入不应有旧形态键: %v", body)
	}
	if body["output_config"].(map[string]any)["effort"] != "high" {
		t.Errorf("写入目标字段 = %v", body)
	}
}


// TestRoundTrip 入口可读格式 Write→Read 往返一致。gemini 只作上游(入口读
// 已删),不在往返集内;anthropic_off 只承载 none,单独验。
func TestRoundTrip(t *testing.T) {
	for _, f := range []string{FormatOpenAIChat, FormatOpenAIResponses, FormatAnthropicEffort,
		FormatAnthropicBudget, FormatAnthropicAdaptive} {
		body := map[string]any{}
		Write(f, "anthropic", body, "high", OffDisabled, nil, 0)
		v, ok := Read(f, body, pairList, nil)
		if !ok || v != "high" {
			t.Errorf("%s 往返 = %q,%v (body %v)", f, v, ok, body)
		}
	}
	body := map[string]any{}
	Write(FormatAnthropicOff, "anthropic", body, "none", OffDisabled, nil, 0)
	v, ok := Read(FormatAnthropicOff, body, pairList, nil)
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
			Write(FormatAuto, proto, b, value, OffDisabled, nil, 0)
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
	Write(FormatAuto, "anthropic", omit, "none", OffOmit, nil, 0)
	if len(omit) != 0 {
		t.Errorf("off=omit 不应写任何字段: %v", omit)
	}
	// omit 也不动体里既有的 thinking(不写≠删除)。
	keep := map[string]any{"thinking": map[string]any{"type": "enabled"}}
	Write(FormatAuto, "anthropic", keep, "none", OffOmit, nil, 0)
	if !reflect.DeepEqual(keep["thinking"], map[string]any{"type": "enabled"}) {
		t.Errorf("off=omit 不应动既有 thinking: %v", keep)
	}

	bt := map[string]any{"thinking": map[string]any{"type": "enabled", "budget_tokens": 1000}}
	Write(FormatAuto, "anthropic", bt, "none", OffBetweenTools, nil, 0)
	if !reflect.DeepEqual(bt["thinking"], map[string]any{"type": "between_tools"}) {
		t.Errorf("off=between_tools 应整对象顶替: %v", bt)
	}

	// 非 none 档不受 off 影响,照旧写 output_config.effort。
	carry := map[string]any{}
	Write(FormatAuto, "anthropic", carry, "high", OffOmit, nil, 0)
	output, _ := carry["output_config"].(map[string]any)
	if output["effort"] != "high" {
		t.Errorf("非 none 档应照常写 output_config: %v", carry)
	}
}
