package effort

import (
	"encoding/json"
	"slices"
	"testing"
)

// TestLabel 锁定显示名推导:常见档回中文名,别名归一取名,词表外的
// 私有档直接用原值当名(不丢弃不编造)。
func TestLabel(t *testing.T) {
	cases := map[string]string{
		"low":        "低",
		" High ":     "高",
		"MAX":        "最大",
		"off":        "无",
		"disabled":   "无",
		"extra-high": "超高",
		"extra_high": "超高",
		"ultra":      "ultra", // 私有档原名上阵
		"turbo":      "turbo",
	}
	for in, want := range cases {
		if got := Label(in); got != want {
			t.Errorf("Label(%q) = %q, want %q", in, got, want)
		}
	}
}

// TestAuto 判定自动模式(null/缺省/空白=跟随上游声明)。
func TestAuto(t *testing.T) {
	for _, raw := range []json.RawMessage{nil, {}, json.RawMessage(`null`), json.RawMessage(" null ")} {
		if !Auto(raw) {
			t.Errorf("Auto(%q) = false, want true", string(raw))
		}
	}
	for _, raw := range []json.RawMessage{json.RawMessage(`[]`), json.RawMessage(`[{"name":"低","value":"low"}]`)} {
		if Auto(raw) {
			t.Errorf("Auto(%q) = true, want false", string(raw))
		}
	}
}

// TestEffectiveAuto 校验自动模式:跟随上游声明,值按原样保留(声明序,
// 词表外私有档不丢),生成恒等映射对(下游档=上行值),去空去重;
// 无声明即不支持。
func TestEffectiveAuto(t *testing.T) {
	got, err := Effective(nil, []string{"low", "ultra", "extra-high", "low", " "}, "")
	want := []Entry{
		{Name: "low", Value: "low"},
		{Name: "ultra", Value: "ultra"},
		{Name: "extra-high", Value: "extra-high"},
	}
	if err != nil || !slices.Equal(got, want) {
		t.Errorf("自动+声明 = %v, %v, want %v(恒等映射对,声明序)", got, err, want)
	}

	got, err = Effective(json.RawMessage(`null`), nil, "")
	if err != nil || len(got) != 0 {
		t.Errorf("自动+无声明 = %v, %v, want 空(不支持)", got, err)
	}
}

// TestEffectiveExplicit 校验显式声明:[{name,value}] 条目,name 留空取
// value(恒等),按 name 去重(保留首个,顺序不动——name 是下游档,两行
// 同 value 不同 name 是合法映射),压过上游声明;存量纯字符串元素兼容;
// 存量数字档号迁移为映射对;空值/坏形态报错;显式空数组=不支持。
func TestEffectiveExplicit(t *testing.T) {
	got, err := Effective(
		json.RawMessage(`[{"name":"low","value":"high"},{"name":"","value":"ultra"},{"name":"medium","value":"high"}]`),
		[]string{"low"}, "")
	want := []Entry{
		{Name: "low", Value: "high"},
		{Name: "ultra", Value: "ultra"},
		{Name: "medium", Value: "high"},
	}
	if err != nil || !slices.Equal(got, want) {
		t.Errorf("显式条目 = %v, %v, want %v(顺序不动,按 name 去重)", got, err, want)
	}

	// 存量纯字符串元素:恒等映射对。
	got, err = Effective(json.RawMessage(`["low","high"]`), nil, "")
	want = []Entry{{Name: "low", Value: "low"}, {Name: "high", Value: "high"}}
	if err != nil || !slices.Equal(got, want) {
		t.Errorf("存量字符串数组 = %v, %v, want %v", got, err, want)
	}

	// 存量数字档号(行号即档号)迁移:0/none→none→none,其余恒等。
	got, err = Effective(json.RawMessage(`[{"name":"0","value":"none"},{"name":"1","value":"low"},{"name":"2","value":"max"}]`), nil, "")
	want = []Entry{{Name: "none", Value: "none"}, {Name: "low", Value: "low"}, {Name: "max", Value: "max"}}
	if err != nil || !slices.Equal(got, want) {
		t.Errorf("存量档号迁移 = %v, %v, want %v", got, err, want)
	}

	// 下游 budget 格式:纯数字 name 是预算阈值行,不做档号迁移。
	got, err = Effective(json.RawMessage(`[{"name":"1024","value":"low"},{"name":"8000","value":"high"}]`), nil, FormatAnthropicBudget)
	want = []Entry{{Name: "1024", Value: "low"}, {Name: "8000", Value: "high"}}
	if err != nil || !slices.Equal(got, want) {
		t.Errorf("budget 阈值行 = %v, %v, want %v(数字 name 原样保留)", got, err, want)
	}

	got, err = Effective(json.RawMessage(`[]`), []string{"low"}, "")
	if err != nil || len(got) != 0 {
		t.Errorf("显式空数组 = %v, %v, want 空(不支持)", got, err)
	}

	if _, err = Effective(json.RawMessage(`[{"name":"空值","value":" "}]`), nil, ""); err == nil {
		t.Error("空 value 应报错")
	}
	if _, err = Effective(json.RawMessage(`[1]`), nil, ""); err == nil {
		t.Error("非字符串/对象元素应报错")
	}
	if _, err = Effective(json.RawMessage(`{"a":1}`), nil, ""); err == nil {
		t.Error("非数组形态应报错")
	}
}

// TestContainsName 与 Names 锁定发送侧校验的两个辅助。
func TestContainsName(t *testing.T) {
	list := []Entry{{Name: "low", Value: "medium"}, {Name: "ultra", Value: "ultra"}}
	if !ContainsName(list, "ultra") || ContainsName(list, "medium") {
		t.Errorf("ContainsName 判定错误: %v", list)
	}
	if got := Names(list); !slices.Equal(got, []string{"low", "ultra"}) {
		t.Errorf("Names = %v, want [low ultra]", got)
	}
}

// TestLevelOf 锁定数字档查映射:档号按行序("1"=第 1 行,"0"=none 行),
// 命中回该行上行值,越界/空表回 false。
func TestLevelOf(t *testing.T) {
	list := []Entry{{Name: "none", Value: "none"}, {Name: "low", Value: "medium"}, {Name: "high", Value: "xhigh"}}
	if v, ok := LevelOf(list, "2"); !ok || v != "medium" {
		t.Errorf("LevelOf(2) = %q, %v, want medium, true", v, ok)
	}
	if v, ok := LevelOf(list, "0"); !ok || v != "none" {
		t.Errorf("LevelOf(0) = %q, %v, want none, true", v, ok)
	}
	if _, ok := LevelOf(list, "9"); ok {
		t.Error("LevelOf(9) 应未命中")
	}
	if _, ok := LevelOf(nil, "1"); ok {
		t.Error("空列表应未命中")
	}
	// 未声明 none 时 "0" 不命中。
	if _, ok := LevelOf([]Entry{{Name: "low", Value: "low"}}, "0"); ok {
		t.Error("无 none 行时 LevelOf(0) 应未命中")
	}
}

// TestApply 锁定映射值写进上行体的协议差异(2026-10-04 核对官方
// SDK/文档):responses 深合并进 reasoning;chat_completions 顶层直写;
// anthropic 档位进 output_config.effort、none 改写 thinking disabled;
// gemini 档位进 thinkingConfig.thinkingLevel(大写)、none 不动体。
func TestApply(t *testing.T) {
	body := map[string]any{"reasoning": map[string]any{"summary": "auto"}}
	Apply("responses", body, "none")
	reasoning := body["reasoning"].(map[string]any)
	if reasoning["effort"] != "none" || reasoning["summary"] != "auto" {
		t.Errorf("responses body = %v, want effort=none 且保留 summary", body)
	}

	chat := map[string]any{}
	Apply("chat_completions", chat, "high")
	if chat["reasoning_effort"] != "high" {
		t.Errorf("chat_completions body = %v, want reasoning_effort=high", chat)
	}

	anthropic := map[string]any{"output_config": map[string]any{"format": "json"}}
	Apply("anthropic", anthropic, "high")
	output := anthropic["output_config"].(map[string]any)
	if output["effort"] != "high" || output["format"] != "json" {
		t.Errorf("anthropic 档位 body = %v, want output_config.effort=high 且保留 format", anthropic)
	}
	Apply("anthropic", anthropic, "none")
	thinking := anthropic["thinking"].(map[string]any)
	if thinking["type"] != "disabled" {
		t.Errorf("anthropic none body = %v, want thinking.type=disabled", anthropic)
	}

	gemini := map[string]any{"generationConfig": map[string]any{"temperature": 0.6}}
	Apply("gemini", gemini, "medium")
	generation := gemini["generationConfig"].(map[string]any)
	thinkingConfig := generation["thinkingConfig"].(map[string]any)
	if thinkingConfig["thinkingLevel"] != "MEDIUM" || generation["temperature"] != 0.6 {
		t.Errorf("gemini 档位 body = %v, want thinkingLevel=MEDIUM 且保留 temperature", gemini)
	}
	Apply("gemini", gemini, "none")
	if generation["thinkingConfig"].(map[string]any)["thinkingLevel"] != "MEDIUM" {
		t.Errorf("gemini none 不应动体: %v", gemini)
	}
}

// TestApplyFormat 锁定显式格式压过协议外形:OpenAI Chat 顶层
// reasoning_effort(none 原样上发,是否上行由 0 档声明决定),Responses
// 嵌套 reasoning.effort、Anthropic output_config.effort、Gemini
// thinkingLevel 复用对应协议分支;空格式=内置。
func TestApplyFormat(t *testing.T) {
	body := map[string]any{}
	ApplyFormat(FormatChatCompletions, "anthropic", body, "high")
	if body["reasoning_effort"] != "high" {
		t.Errorf("chat_completions 格式 body = %v, want 顶层 high", body)
	}
	ApplyFormat(FormatChatCompletions, "anthropic", body, "none")
	if body["reasoning_effort"] != "none" {
		t.Errorf("chat_completions 格式 none 应原样上发: %v", body)
	}

	nested := map[string]any{}
	ApplyFormat(FormatResponses, "anthropic", nested, "low")
	if nested["reasoning"].(map[string]any)["effort"] != "low" {
		t.Errorf("responses 格式 body = %v, want reasoning.effort=low", nested)
	}

	ant := map[string]any{}
	ApplyFormat(FormatAnthropic, "chat_completions", ant, "max")
	if ant["output_config"].(map[string]any)["effort"] != "max" {
		t.Errorf("anthropic 格式 body = %v, want output_config.effort=max", ant)
	}

	gem := map[string]any{}
	ApplyFormat(FormatGemini, "chat_completions", gem, "xhigh")
	got := gem["generationConfig"].(map[string]any)["thinkingConfig"].(map[string]any)["thinkingLevel"]
	if got != "XHIGH" {
		t.Errorf("gemini 格式 body = %v, want XHIGH", gem)
	}

	auto := map[string]any{}
	ApplyFormat(FormatAuto, "chat_completions", auto, "low")
	if auto["reasoning_effort"] != "low" {
		t.Errorf("空格式应走协议内置: %v", auto)
	}

	if !ValidFormat("") || !ValidFormat(FormatGemini) || ValidFormat("bogus") {
		t.Error("ValidFormat 判定错误")
	}
}
