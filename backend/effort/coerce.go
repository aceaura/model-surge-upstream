package effort

import "strings"

// 矫正逻辑(2026-10-10 映射重设计):下游请求携带的档位值未必在模型声明表内
// (未声明的私有值、别名、大小写差异、budget 数偏差),此前原样写给上游会撞
// unsupported_value 或静默丢字段。统一原则:就低不就高——请求档超出声明表时
// 钳到「强度不超过请求值」的最高档,保守不超档,避免悄悄放大思考预算。

// strength 内置档位强度序(与 budgetTable 同口径);表外自定义档(ultra 等)
// 无强度语义,不参与钳制比较,走位置取中兜底。
var strength = map[string]int{
	"none":    0,
	"minimal": 1,
	"low":     2,
	"medium":  3,
	"high":    4,
	"xhigh":   5,
	"max":     6,
}

// Coerce 把请求档位值矫正到声明表内,返回矫正后的**上行值**(命中行的
// Value)与「是否发生改写」。声明行是「Name=下游档 → Value=上行值」
// 映射对(2026-10-11 行式映射对设计),匹配与强度比较都按 Name,产出
// 一律是行的 Value。管线:
//  1. 空声明表 → 原样透传,不算矫正(与现状逐字一致);
//  2. 大小写/空白/别名归一(off/disabled→none、extra-high/extra_high→xhigh,
//     与 Label 共用 aliases 表);
//  3. 精确命中某行 Name → 回该行 Value;
//  4. 归一值在内置强度序 → 向下钳:Name 强度不超过请求值的最高档行,
//     回其 Value;请求强度低于全部声明档 → 取最低档行的 Value;
//  5. 完全未知且无强度语义 → 声明行排除 none 后按位置取中,序号
//     (n-1)/2 下取整(4 行选 2、3 行选 2、5 行选 3),回该行 Value;
//     仅 none 行(n=0)落 none。
func Coerce(value string, list []Entry) (string, bool) {
	if len(list) == 0 {
		return value, false
	}
	norm := strings.ToLower(strings.TrimSpace(value))
	if a, ok := aliases[norm]; ok {
		norm = a
	}
	for _, e := range list {
		if strings.ToLower(strings.TrimSpace(e.Name)) == norm {
			return e.Value, e.Value != value
		}
	}
	if s, ok := strength[norm]; ok {
		best, bestS := "", -1
		lowest, lowestS := "", len(strength)+1
		for _, e := range list {
			es, ok := strength[strings.ToLower(strings.TrimSpace(e.Name))]
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
	mid := make([]Entry, 0, len(list))
	for _, e := range list {
		if !strings.EqualFold(strings.TrimSpace(e.Name), "none") {
			mid = append(mid, e)
		}
	}
	if len(mid) == 0 {
		return list[0].Value, list[0].Value != value
	}
	pick := mid[(len(mid)-1)/2].Value
	return pick, pick != value
}
