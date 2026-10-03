// Package effort 定义推理档的统一词表与「自动」支持列表规则。
// 词表与 new-api/sub2api/cc-switch 三家同构;各家模型支持哪些档并不统一,
// Defaults 用协议默认+模型名规则给出免维护的初始答案,
// 规则失灵时管理员在模型上显式覆盖(efforts 列存数组即压过本规则)。
package effort

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/aceaura/model-surge-upstream/backend/apperr"
	"github.com/aceaura/model-surge-upstream/backend/provider"
)

// Levels 是全部合法档位,按强度升序。UI 与校验都以这份词表为准。
var Levels = []string{"none", "minimal", "low", "medium", "high", "xhigh", "max"}

// Valid 判定是否为词表内档位。
func Valid(s string) bool {
	for _, l := range Levels {
		if s == l {
			return true
		}
	}
	return false
}

// Defaults 给出协议×模型名的自动支持列表:
//   - responses(codex 订阅族):实测 minimal~high 可用;xhigh/max 词表里有,
//     需管理员确认上游支持后手动加,不放默认。
//   - chat_completions(o 系列/推理型兼容端点):low~high。
//   - anthropic/gemini 协议:没有通用 effort 语义(thinking 走 budget 数值),
//     不支持。
//
// 模型名规则只在有 effort 语义的协议内收窄:gemini 系只分低/高两档;
// kimi/deepseek 的「思考」是开关不是档位,塞 effort 只会被上游拒,一律置空。
func Defaults(protocol, nativeModel string) []string {
	name := strings.ToLower(nativeModel)
	for _, noEffort := range []string{"kimi", "deepseek"} {
		if strings.Contains(name, noEffort) {
			return []string{}
		}
	}
	switch protocol {
	case provider.ProtocolResponses:
		if strings.Contains(name, "gemini") {
			return []string{"low", "high"}
		}
		return []string{"minimal", "low", "medium", "high"}
	case provider.ProtocolChatCompletions:
		if strings.Contains(name, "gemini") {
			return []string{"low", "high"}
		}
		return []string{"low", "medium", "high"}
	default:
		return []string{}
	}
}

// Effective 算出模型的有效支持列表:raw 为 null/空(未显式配置)走
// Defaults;raw 为数组时逐项校验词表并按 Levels 升序去重返回,
// 显式空数组 [] 表示管理员声明该模型不支持 effort。
func Effective(protocol, nativeModel string, raw json.RawMessage) ([]string, error) {
	if len(raw) == 0 || string(raw) == "null" {
		return Defaults(protocol, nativeModel), nil
	}
	var list []string
	if err := json.Unmarshal(raw, &list); err != nil {
		return nil, apperr.New(apperr.InvalidJSON, "efforts must be a json array of strings")
	}
	seen := map[string]bool{}
	out := make([]string, 0, len(list))
	for _, l := range Levels {
		for _, got := range list {
			if got == l && !seen[l] {
				seen[l] = true
				out = append(out, l)
			}
		}
	}
	for _, got := range list {
		if !Valid(got) {
			return nil, apperr.New(apperr.InvalidRequest,
				fmt.Sprintf("unknown reasoning effort %q (levels: %s)", got, strings.Join(Levels, ", ")))
		}
	}
	return out, nil
}

// Contains 判定列表是否含某档位。
func Contains(list []string, s string) bool {
	for _, l := range list {
		if l == s {
			return true
		}
	}
	return false
}
