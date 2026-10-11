package effort

import "strings"

// 矫正逻辑(2026-10-10 映射重设计):下游请求携带的档位值未必在模型声明表内
// (未声明的私有值、别名、大小写差异、budget 数偏差),此前原样写给上游会撞
// unsupported_value 或静默丢字段。统一原则:就低不就高——请求档超出声明表时
// 钳到「强度不超过请求值」的最高档,保守不超档,避免悄悄放大思考预算。

// strength 内置档位强度序(与 budgetTable 同口径);表外自定义档(ultra 等)
// 无强度语义,不参与钳制比较,走 medium 兜底。
var strength = map[string]int{
	"none":    0,
	"minimal": 1,
	"low":     2,
	"medium":  3,
	"high":    4,
	"xhigh":   5,
	"max":     6,
}

// Coerce 把请求档位值矫正到声明表内,返回矫正后的值与「是否发生改写」
// (out != value 即 changed,调用方据此写矫正日志)。管线:
//  1. 空声明表 → 原样透传,不算矫正(与现状逐字一致);
//  2. 大小写/空白/别名归一(off/disabled→none、extra-high/extra_high→xhigh,
//     与 Label 共用 aliases 表);
//  3. 精确命中声明表 → 回声明原值(仅归一命中也算矫正,值被改写了);
//  4. 归一值在内置强度序 → 向下钳:声明表内取强度不超过请求值的最高档;
//     请求强度低于全部声明档 → 取声明表最低档(none 参与比较,声明了 none
//     就是最低档);
//  5. 完全未知且无强度语义 → 回落声明表中的 medium;无 medium 取首个
//     非 none 档;表只有 none 回 none。
func Coerce(value string, list []Entry) (string, bool) {
	if len(list) == 0 {
		return value, false
	}
	norm := strings.ToLower(strings.TrimSpace(value))
	if a, ok := aliases[norm]; ok {
		norm = a
	}
	for _, e := range list {
		if strings.ToLower(strings.TrimSpace(e.Value)) == norm {
			return e.Value, e.Value != value
		}
	}
	if s, ok := strength[norm]; ok {
		best, bestS := "", -1
		lowest, lowestS := "", len(strength)+1
		for _, e := range list {
			es, ok := strength[strings.ToLower(strings.TrimSpace(e.Value))]
			if !ok {
				continue
			}
			if es <= s && es > bestS {
				best, bestS = e.Value, es
			}
			if es < lowestS {
				lowest, lowestS = e.Value, es
			}
		}
		if best != "" {
			return best, best != value
		}
		if lowest != "" {
			return lowest, lowest != value
		}
	}
	for _, e := range list {
		if strings.EqualFold(strings.TrimSpace(e.Value), "medium") {
			return e.Value, e.Value != value
		}
	}
	for _, e := range list {
		if !strings.EqualFold(strings.TrimSpace(e.Value), "none") {
			return e.Value, e.Value != value
		}
	}
	return list[0].Value, list[0].Value != value
}
