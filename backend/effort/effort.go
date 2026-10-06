// Package effort 定义推理档的「名+值」条目与声明式支持列表判定。
// 某模型支持哪些档不靠猜——自动模式跟随上游 /models 响应里声明的
// supported_reasoning_levels(sub2api 同款动态适配),上游没声明即不支持;
// 管理员也可在模型上显式声明(efforts 列存 [{name,value}] 数组),
// 显式声明压过上游声明。档位值不设固定词表:各家上游的私有档
// (ultra 等)也能声明,上行时按声明原样发送。
package effort

import (
	"encoding/json"
	"strings"

	"github.com/aceaura/model-surge-upstream/backend/apperr"
	"github.com/aceaura/model-surge-upstream/backend/provider"
)

// Entry 是一个推理档:Name 是显示名(对话页档位菜单),Value 是
// 发上游的档位字符串。
type Entry struct {
	Name  string `json:"name"`
	Value string `json:"value"`
}

// labels 常见档位值的中文显示名;表外的值直接用原值当名。
var labels = map[string]string{
	"none":    "无",
	"minimal": "最小",
	"low":     "低",
	"medium":  "中",
	"high":    "高",
	"xhigh":   "超高",
	"max":     "最大",
}

// aliases 常见别名(sub2api 同款),归一只用于取显示名,不改写上行的值。
var aliases = map[string]string{
	"off":        "none",
	"disabled":   "none",
	"extra-high": "xhigh",
	"extra_high": "xhigh",
}

// Label 给档位值取显示名:别名归一后命中常见词表回中文名,否则回原值。
func Label(value string) string {
	key := strings.ToLower(strings.TrimSpace(value))
	if a, ok := aliases[key]; ok {
		key = a
	}
	if l, ok := labels[key]; ok {
		return l
	}
	return strings.TrimSpace(value)
}

// Auto 判定原始配置是否为自动模式(null/缺省=跟随上游声明)。
func Auto(raw json.RawMessage) bool {
	s := strings.TrimSpace(string(raw))
	return s == "" || s == "null"
}

// Effective 算出模型的有效支持列表:
//   - raw 为自动模式(null/缺省):跟随上游声明,值按上游原样保留
//     (声明序,去空去重),名取 Label;无声明即不支持(回空列表,
//     选择器不露面)。
//   - raw 为数组:管理员显式声明 [{name,value}](兼容存量纯字符串元素);
//     value 去空白后必须非空,按 value 去重(保留首个),name 留空按
//     Label 自动命名;显式空数组 [] 表示声明该模型不支持 effort。
func Effective(raw json.RawMessage, declared []string) ([]Entry, error) {
	if Auto(raw) {
		return entriesFromValues(declared), nil
	}
	var items []any
	if err := json.Unmarshal(raw, &items); err != nil {
		return nil, apperr.New(apperr.InvalidJSON,
			"efforts must be a json array of {name,value} entries")
	}
	out := make([]Entry, 0, len(items))
	seen := map[string]bool{}
	for _, item := range items {
		var name, value string
		switch v := item.(type) {
		case string:
			value = strings.TrimSpace(v)
		case map[string]any:
			if s, ok := v["value"].(string); ok {
				value = strings.TrimSpace(s)
			}
			if s, ok := v["name"].(string); ok {
				name = strings.TrimSpace(s)
			}
		default:
			return nil, apperr.New(apperr.InvalidJSON,
				"effort entries must be {name,value} objects")
		}
		if value == "" {
			return nil, apperr.New(apperr.InvalidRequest,
				"effort entry value must not be empty")
		}
		if seen[value] {
			continue
		}
		seen[value] = true
		if name == "" {
			name = Label(value)
		}
		out = append(out, Entry{Name: name, Value: value})
	}
	return out, nil
}

// entriesFromValues 声明值列表 → 条目:去空、按值去重、保留原顺序,名取 Label。
func entriesFromValues(values []string) []Entry {
	out := make([]Entry, 0, len(values))
	seen := map[string]bool{}
	for _, v := range values {
		v = strings.TrimSpace(v)
		if v == "" || seen[v] {
			continue
		}
		seen[v] = true
		out = append(out, Entry{Name: Label(v), Value: v})
	}
	return out
}

// ContainsValue 判定条目列表是否含某档位值。
func ContainsValue(list []Entry, value string) bool {
	for _, e := range list {
		if e.Value == value {
			return true
		}
	}
	return false
}

// LevelOf 按档位名(数字档)查映射出的上行值:命中返回值与 true。
// 数字档是下游请求顶层 reasoning_level 的合法取值,名即档号。
func LevelOf(list []Entry, level string) (string, bool) {
	for _, e := range list {
		if e.Name == level {
			return e.Value, true
		}
	}
	return "", false
}

// Apply 把映射出的档位值按协议写进上行请求体(2026-10-04 逐协议核对
// 官方 SDK/文档):
//   - responses:reasoning.effort(不动其他 reasoning 键),值域含 none;
//   - chat_completions:顶层 reasoning_effort,值域含 none;
//   - anthropic:档位写 output_config.effort(官方值域 low/medium/high/
//     xhigh/max);none 没有档位语义,改写为 thinking {type:disabled}
//     (关闭思考);不动 output_config/thinking 里的其他键;
//   - gemini:档位写 generationConfig.thinkingConfig.thinkingLevel(线协议
//     枚举大写 MINIMAL/LOW/MEDIUM/HIGH);3.x 全系不可关思考、2.5 走
//     thinkingBudget 预算制,none 无通用映射,不动体。
func Apply(protocol string, body map[string]any, value string) {
	switch protocol {
	case provider.ProtocolResponses:
		reasoning, _ := body["reasoning"].(map[string]any)
		if reasoning == nil {
			reasoning = map[string]any{}
			body["reasoning"] = reasoning
		}
		reasoning["effort"] = value
	case provider.ProtocolChatCompletions:
		body["reasoning_effort"] = value
	case provider.ProtocolAnthropic:
		if value == "none" {
			thinking, _ := body["thinking"].(map[string]any)
			if thinking == nil {
				thinking = map[string]any{}
				body["thinking"] = thinking
			}
			thinking["type"] = "disabled"
			return
		}
		output, _ := body["output_config"].(map[string]any)
		if output == nil {
			output = map[string]any{}
			body["output_config"] = output
		}
		output["effort"] = value
	case provider.ProtocolGemini:
		if value == "none" {
			return
		}
		generation, _ := body["generationConfig"].(map[string]any)
		if generation == nil {
			generation = map[string]any{}
			body["generationConfig"] = generation
		}
		thinking, _ := generation["thinkingConfig"].(map[string]any)
		if thinking == nil {
			thinking = map[string]any{}
			generation["thinkingConfig"] = thinking
		}
		thinking["thinkingLevel"] = strings.ToUpper(value)
	}
}

// Values 取出条目列表的全部档位值(错误提示用)。
func Values(list []Entry) []string {
	out := make([]string, 0, len(list))
	for _, e := range list {
		out = append(out, e.Value)
	}
	return out
}

// 写入格式(effort_format 列的合法取值):各映射脚本内容大量雷同,
// 2026-10-05 把脚本配置收进通用底层——选档后先按档号在模型声明里查值
// (查不到/值为空不落字段),再按此处选定的格式把值写进请求体。空串=
// 协议内置(Apply 按出站协议选字段);显式格式按目标协议命名(与
// provider.ProtocolXxx 标识一致),承接「协议外壳+自家字段」的厂商差异。
// none 的两种处理都按官方文档口径(2026-10-06 复核):
//   - chat_completions:OpenAI Chat 协议格式,顶层 reasoning_effort,none
//     原样上发——OpenAI chat 值域含 none;百炼 qwen3.8 官方文档:none 映射
//     enable_thinking=False(关闭思考),删字段反而吃默认 xhigh;
//   - chat_completions_skip_none:OpenAI Chat 协议格式的 none 变体,none
//     不落字段(上游吃自家默认)——kimi 官方端点口径:思考恒开不可关,
//     非法值静默忽略,none 发了也白发;
//   - responses:OpenAI Responses 协议格式,嵌套 reasoning.effort;
//   - anthropic:Anthropic 协议格式,output_config.effort(none=关闭思考);
//   - gemini:Gemini 协议格式,generationConfig.thinkingConfig.thinkingLevel
//     (大写枚举,none 不动体)。
const (
	FormatAuto                    = ""
	FormatChatCompletions         = "chat_completions"
	FormatChatCompletionsSkipNone = "chat_completions_skip_none"
	FormatResponses               = "responses"
	FormatAnthropic               = "anthropic"
	FormatGemini                  = "gemini"
)

// ValidFormat 判定 effort_format 是否为合法取值(空串=协议内置)。
func ValidFormat(format string) bool {
	switch format {
	case FormatAuto, FormatChatCompletions, FormatChatCompletionsSkipNone,
		FormatResponses, FormatAnthropic, FormatGemini:
		return true
	}
	return false
}

// ApplyFormat 按选定格式把映射出的档位值写进请求体:内置(空)跟随出站
// 协议,显式格式压过协议外形。none 语义随格式定义(见常量注释)。
func ApplyFormat(format, protocol string, body map[string]any, value string) {
	switch format {
	case FormatChatCompletions:
		Apply(provider.ProtocolChatCompletions, body, value)
	case FormatChatCompletionsSkipNone:
		if value == "none" {
			delete(body, "reasoning_effort")
			return
		}
		Apply(provider.ProtocolChatCompletions, body, value)
	case FormatResponses:
		Apply(provider.ProtocolResponses, body, value)
	case FormatAnthropic:
		Apply(provider.ProtocolAnthropic, body, value)
	case FormatGemini:
		Apply(provider.ProtocolGemini, body, value)
	default:
		Apply(protocol, body, value)
	}
}
