package effort

import (
	"encoding/json"
	"strconv"
	"strings"

	"github.com/aceaura/model-surge-upstream/backend/provider"
)

// 双端格式词表(2026-10-10 双端转换设计):入口与上游以规范档(efforts 列表
// 的 value,含 none)桥接——Read 把入口形态读成规范档,Write 把规范档写成
// 上游形态,StripKeys 剥离入口残留键。九种格式覆盖各家线形态:自有数字档、
// openai 两族(纯路径差异)、anthropic 四族(effort/预算/自适应/关思考)、
// gemini 两族(枚举/预算)。入口(下游)词表不收 gemini——gemini 仅作上游
// 协议与上游格式保留,下游适配已删;入口合法集见 ValidEntryFormat。
const (
	FormatIndex             = "effort_index"
	FormatOpenAIChat        = "openai_chat"
	FormatOpenAIResponses   = "openai_responses"
	FormatAnthropicEffort   = "anthropic_effort"
	FormatAnthropicBudget   = "anthropic_budget"
	FormatAnthropicAdaptive = "anthropic_adaptive"
	FormatAnthropicOff      = "anthropic_off"
	FormatGeminiLevel       = "gemini_level"
	FormatGeminiBudget      = "gemini_budget"
)

// formatAliases 存量 effort_format 旧值 → 新词表:读路径归一,落库原值
// 不动(下次保存才写新值),存量配置零迁移。
var formatAliases = map[string]string{
	FormatChatCompletions: FormatOpenAIChat,
	FormatResponses:       FormatOpenAIResponses,
	FormatAnthropic:       FormatAnthropicEffort,
	FormatGemini:          FormatGeminiLevel,
}

// NormalizeFormat 旧值别名映射,新值与空串原样返回。
func NormalizeFormat(raw string) string {
	if a, ok := formatAliases[raw]; ok {
		return a
	}
	return raw
}

// ValidFormat 判定归一后是否属双端词表(空串=auto)。上游(effort_format)
// 用此函数,九种全收(含 gemini)。
func ValidFormat(format string) bool {
	switch NormalizeFormat(format) {
	case FormatAuto, FormatIndex, FormatOpenAIChat, FormatOpenAIResponses,
		FormatAnthropicEffort, FormatAnthropicBudget, FormatAnthropicAdaptive,
		FormatAnthropicOff, FormatGeminiLevel, FormatGeminiBudget:
		return true
	}
	return false
}

// ValidEntryFormat 圈定入口(下游,effort_in)词表:auto + 七种非 gemini。
// gemini 不作入口——下游适配已删,gemini 仅保留为上游协议与上游格式;
// 旧值别名归一后判定(legacy "gemini"→gemini_level 同样被入口拒绝)。
func ValidEntryFormat(format string) bool {
	switch NormalizeFormat(format) {
	case FormatAuto, FormatIndex, FormatOpenAIChat, FormatOpenAIResponses,
		FormatAnthropicEffort, FormatAnthropicBudget, FormatAnthropicAdaptive,
		FormatAnthropicOff:
		return true
	}
	return false
}

// 关思考落定(0 档在 anthropic 族上游的写法):空=disabled 标准写法;
// between_tools=Sonnet 5.5 顶替 disabled(整对象替换,不收附加字段);
// omit=不写(上游思考恒开不可关,0 档由不声明承担)。
const (
	OffDisabled     = ""
	OffBetweenTools = "between_tools"
	OffOmit         = "omit"
)

// ValidOff 判定关思考落定取值。
func ValidOff(off string) bool {
	switch off {
	case OffDisabled, OffBetweenTools, OffOmit:
		return true
	}
	return false
}

// budgetTable 档位值 → 预算 token 内置映射;表外档位回 4000(medium 档),
// 与 kiro fake-reasoning 默认预算同口径。
var budgetTable = map[string]int{
	"low":    1024,
	"medium": 4000,
	"high":   10000,
	"xhigh":  20000,
	"max":    32000,
}

// BudgetOf 取档位的预算 token:模型覆盖优先,否则内置表,未命中回 4000。
func BudgetOf(value string, overrides map[string]int) int {
	if n, ok := overrides[value]; ok && n > 0 {
		return n
	}
	if n, ok := budgetTable[value]; ok {
		return n
	}
	return 4000
}

// ClampBudget 预算钳到 < max_tokens;max_tokens 未声明或 ≤1024 时不钳
// (钳了会低于协议下限 1024)。
func ClampBudget(n, maxTokens int) int {
	if maxTokens > 1024 && n >= maxTokens {
		return maxTokens - 1
	}
	return n
}

// Read 按入口格式从上行体读规范档;读不到回 "",false(回退链由调用方接)。
// 数字档按档号在 list 里查值;预算类按 BudgetOf 反查精确命中(不猜最近档);
// anthropic_off 认 disabled/between_tools 为 none。入口词表不收 gemini
// (见 ValidEntryFormat),gemini 形态不再有入口读取路径。
func Read(format string, body map[string]any, list []Entry, budgets map[string]int) (string, bool) {
	switch NormalizeFormat(format) {
	case FormatIndex:
		level, ok := numberOrString(body["reasoning_level"])
		if !ok {
			return "", false
		}
		return LevelOf(list, level)
	case FormatOpenAIChat:
		s, ok := body["reasoning_effort"].(string)
		return s, ok && s != ""
	case FormatOpenAIResponses:
		s, ok := existing(body, "reasoning")["effort"].(string)
		return s, ok && s != ""
	case FormatAnthropicEffort:
		s, ok := existing(body, "output_config")["effort"].(string)
		return s, ok && s != ""
	case FormatAnthropicBudget:
		thinking := existing(body, "thinking")
		if thinking["type"] != "enabled" {
			return "", false
		}
		n, ok := asInt(thinking["budget_tokens"])
		if !ok {
			return "", false
		}
		return tierByBudget(n, list, budgets)
	case FormatAnthropicAdaptive:
		if existing(body, "thinking")["type"] != "adaptive" {
			return "", false
		}
		s, ok := existing(body, "output_config")["effort"].(string)
		return s, ok && s != ""
	case FormatAnthropicOff:
		switch existing(body, "thinking")["type"] {
		case "disabled", "between_tools":
			return "none", true
		}
		return "", false
	}
	return "", false
}

// Write 按上游格式把规范档写进上行体;none 语义随格式:openai 族原样写、
// gemini 族与 anthropic_off 不动体、anthropic 载档三格式按 off 落定。
// format=auto 委托 Apply(出站协议内置映射),与改动前逐字一致。
func Write(format, protocol string, list []Entry, body map[string]any, value string, off string, budgets map[string]int, maxTokens int) {
	switch NormalizeFormat(format) {
	case FormatAuto:
		// 旧行为逐字保留(none→thinking disabled);仅当显式配置了关思考
		// 落定(off 为新字段,空=disabled 与旧行为等价)才按 off 接管 none。
		if protocol == provider.ProtocolAnthropic && value == "none" && off != OffDisabled {
			writeOff(body, off)
			return
		}
		Apply(protocol, body, value)
	case FormatIndex:
		WriteIndex(body, list, value)
	case FormatOpenAIChat:
		body["reasoning_effort"] = value
	case FormatOpenAIResponses:
		sub(body, "reasoning")["effort"] = value
	case FormatAnthropicEffort:
		if value == "none" {
			writeOff(body, off)
			return
		}
		sub(body, "output_config")["effort"] = value
	case FormatAnthropicBudget:
		if value == "none" {
			writeOff(body, off)
			return
		}
		thinking := sub(body, "thinking")
		thinking["type"] = "enabled"
		thinking["budget_tokens"] = ClampBudget(BudgetOf(value, budgets), maxTokens)
	case FormatAnthropicAdaptive:
		if value == "none" {
			writeOff(body, off)
			return
		}
		sub(body, "thinking")["type"] = "adaptive"
		sub(body, "output_config")["effort"] = value
	case FormatAnthropicOff:
		if value == "none" {
			writeOff(body, off)
		}
	case FormatGeminiLevel:
		if value == "none" {
			return
		}
		thinkingConfig(body)["thinkingLevel"] = strings.ToUpper(value)
	case FormatGeminiBudget:
		if value == "none" {
			return
		}
		thinkingConfig(body)["thinkingBudget"] = ClampBudget(BudgetOf(value, budgets), maxTokens)
	}
}

// WriteIndex 把规范档按档号写回 reasoning_level(effort_index 上游格式):
// 档在 list 里的 Name 即档号;查不到(如未声明 0 档的 none)不写。
func WriteIndex(body map[string]any, list []Entry, value string) {
	for _, e := range list {
		if e.Value != value {
			continue
		}
		if n, err := strconv.Atoi(e.Name); err == nil {
			body["reasoning_level"] = n
			return
		}
		body["reasoning_level"] = e.Name
		return
	}
}

// StripKeys 剥离入口格式写过的键;openai_responses 只删 reasoning.effort
// 保留兄弟键(summary 等);删空的中层对象一并清理;auto 为 no-op
// (现状透传不破坏)。
func StripKeys(format string, body map[string]any) {
	switch NormalizeFormat(format) {
	case FormatIndex:
		delete(body, "reasoning_level")
	case FormatOpenAIChat:
		delete(body, "reasoning_effort")
	case FormatOpenAIResponses:
		reasoning := existing(body, "reasoning")
		delete(reasoning, "effort")
		cleanEmpty(body, "reasoning")
	case FormatAnthropicEffort:
		output := existing(body, "output_config")
		delete(output, "effort")
		cleanEmpty(body, "output_config")
	case FormatAnthropicBudget:
		thinking := existing(body, "thinking")
		delete(thinking, "type")
		delete(thinking, "budget_tokens")
		cleanEmpty(body, "thinking")
	case FormatAnthropicAdaptive:
		thinking := existing(body, "thinking")
		delete(thinking, "type")
		cleanEmpty(body, "thinking")
		output := existing(body, "output_config")
		delete(output, "effort")
		cleanEmpty(body, "output_config")
	case FormatAnthropicOff:
		thinking := existing(body, "thinking")
		delete(thinking, "type")
		cleanEmpty(body, "thinking")
	}
}

// writeOff 0 档在 anthropic 族的落定写法。
func writeOff(body map[string]any, off string) {
	switch off {
	case OffOmit:
		return
	case OffBetweenTools:
		// between_tools 不收附加字段:整对象替换而非合并。
		body["thinking"] = map[string]any{"type": "between_tools"}
	default:
		sub(body, "thinking")["type"] = "disabled"
	}
}

// tierByBudget 预算数反查规范档:精确命中才回,未命中视为未携带该字段。
func tierByBudget(n int, list []Entry, budgets map[string]int) (string, bool) {
	for _, e := range list {
		if e.Value == "none" {
			continue
		}
		if BudgetOf(e.Value, budgets) == n {
			return e.Value, true
		}
	}
	return "", false
}

// sub 取(必要时建)body 下的对象键,供写路径深合并。
func sub(body map[string]any, key string) map[string]any {
	if m, ok := body[key].(map[string]any); ok {
		return m
	}
	m := map[string]any{}
	body[key] = m
	return m
}

// existing 取 body 下已存在的对象键;不存在回 nil map(读/剥离路径不建键)。
func existing(body map[string]any, key string) map[string]any {
	m, _ := body[key].(map[string]any)
	return m
}

// cleanEmpty 中层对象删空后整键清理,避免上行留空壳。
func cleanEmpty(body map[string]any, key string) {
	if m, ok := body[key].(map[string]any); ok && len(m) == 0 {
		delete(body, key)
	}
}

// thinkingConfig 取(必要时建)generationConfig.thinkingConfig,供 gemini 上游写路径。
func thinkingConfig(body map[string]any) map[string]any {
	generation, ok := body["generationConfig"].(map[string]any)
	if !ok {
		generation = map[string]any{}
		body["generationConfig"] = generation
	}
	thinking, ok := generation["thinkingConfig"].(map[string]any)
	if !ok {
		thinking = map[string]any{}
		generation["thinkingConfig"] = thinking
	}
	return thinking
}

// numberOrString 数字档取值兼容 JSON 数/json.Number/字符串三种形态,回档号字符串。
func numberOrString(v any) (string, bool) {
	switch t := v.(type) {
	case string:
		return t, t != ""
	case float64:
		return strconv.Itoa(int(t)), true
	case int:
		return strconv.Itoa(t), true
	case json.Number:
		if f, err := t.Float64(); err == nil {
			return strconv.Itoa(int(f)), true
		}
	}
	return "", false
}

// asInt JSON 数(float64/int/json.Number)取整值;非数回 false。
func asInt(v any) (int, bool) {
	switch t := v.(type) {
	case float64:
		return int(t), true
	case int:
		return t, true
	case json.Number:
		if f, err := t.Float64(); err == nil {
			return int(f), true
		}
	}
	return 0, false
}

// BodyMaxTokens 从上行体取输出上限字段供预算钳制:anthropic/openai 族看
// 顶层 max_tokens,gemini 看 generationConfig.maxOutputTokens;未声明回 0
// (不钳)。
func BodyMaxTokens(protocol string, body map[string]any) int {
	if protocol == provider.ProtocolGemini {
		generation, _ := body["generationConfig"].(map[string]any)
		n, _ := asInt(generation["maxOutputTokens"])
		return n
	}
	n, _ := asInt(body["max_tokens"])
	return n
}
