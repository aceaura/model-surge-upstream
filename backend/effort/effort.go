// Package effort 定义推理档的统一词表与「声明式」支持列表判定。
// 词表与 new-api/sub2api/cc-switch 三家同构;某模型支持哪些档不靠猜——
// 自动模式跟随上游 /models 响应里声明的 supported_reasoning_levels
// (sub2api 同款动态适配),上游没声明即不支持;管理员也可在模型上
// 显式声明(efforts 列存数组),显式声明压过上游声明。
package effort

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/aceaura/model-surge-upstream/backend/apperr"
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

// Auto 判定原始配置是否为自动模式(null/缺省=跟随上游声明)。
func Auto(raw json.RawMessage) bool {
	s := strings.TrimSpace(string(raw))
	return s == "" || s == "null"
}

// Normalize 把上游声明的档位名折进词表:大小写与空白归一,常见别名映射
// (off/disabled→none、extra-high/extra_high→xhigh),词表外的名字回 ""
// (各家私有档位词表表达不了,丢弃而不是编造)。别名表与 sub2api 同款。
func Normalize(level string) string {
	switch strings.ToLower(strings.TrimSpace(level)) {
	case "off", "disabled":
		return "none"
	case "extra-high", "extra_high":
		return "xhigh"
	case "none", "minimal", "low", "medium", "high", "xhigh", "max":
		return strings.ToLower(strings.TrimSpace(level))
	default:
		return ""
	}
}

// NormalizeList 归一一份上游声明列表:逐项 Normalize、丢弃词表外项、
// 按 Levels 升序去重。
func NormalizeList(declared []string) []string {
	seen := map[string]bool{}
	for _, d := range declared {
		if n := Normalize(d); n != "" {
			seen[n] = true
		}
	}
	out := make([]string, 0, len(seen))
	for _, l := range Levels {
		if seen[l] {
			out = append(out, l)
		}
	}
	return out
}

// Effective 算出模型的有效支持列表:
//   - raw 为自动模式(null/缺省):跟随上游声明,declared 是上游 /models
//     声明的 supported_reasoning_levels(原始名字,此处归一);无声明即
//     不支持(回空列表,选择器不露面)。
//   - raw 为数组:管理员显式声明,逐项校验词表并按 Levels 升序去重,
//     显式空数组 [] 表示声明该模型不支持 effort。
func Effective(raw json.RawMessage, declared []string) ([]string, error) {
	if Auto(raw) {
		return NormalizeList(declared), nil
	}
	var list []string
	if err := json.Unmarshal(raw, &list); err != nil {
		return nil, apperr.New(apperr.InvalidJSON, "efforts must be a json array of strings")
	}
	for _, got := range list {
		if !Valid(got) {
			return nil, apperr.New(apperr.InvalidRequest,
				fmt.Sprintf("unknown reasoning effort %q (levels: %s)", got, strings.Join(Levels, ", ")))
		}
	}
	seen := map[string]bool{}
	for _, l := range list {
		seen[l] = true
	}
	out := make([]string, 0, len(seen))
	for _, l := range Levels {
		if seen[l] {
			out = append(out, l)
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
