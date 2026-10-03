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
// 词表外私有档不丢),名取 Label,去空去重;无声明即不支持。
func TestEffectiveAuto(t *testing.T) {
	got, err := Effective(nil, []string{"low", "ultra", "extra-high", "low", " "})
	want := []Entry{
		{Name: "低", Value: "low"},
		{Name: "ultra", Value: "ultra"},
		{Name: "超高", Value: "extra-high"}, // 值原样上行,别名只用于取名
	}
	if err != nil || !slices.Equal(got, want) {
		t.Errorf("自动+声明 = %v, %v, want %v(原值声明序)", got, err, want)
	}

	got, err = Effective(json.RawMessage(`null`), nil)
	if err != nil || len(got) != 0 {
		t.Errorf("自动+无声明 = %v, %v, want 空(不支持)", got, err)
	}
}

// TestEffectiveExplicit 校验显式声明:[{name,value}] 条目,name 留空按值
// 自动命名,按 value 去重(保留首个,顺序不动),压过上游声明;存量纯
// 字符串元素兼容;空值/坏形态报错;显式空数组=不支持。
func TestEffectiveExplicit(t *testing.T) {
	got, err := Effective(
		json.RawMessage(`[{"name":"超","value":"high"},{"name":"","value":"ultra"},{"name":"重","value":"high"}]`),
		[]string{"low"})
	want := []Entry{
		{Name: "超", Value: "high"},
		{Name: "ultra", Value: "ultra"},
	}
	if err != nil || !slices.Equal(got, want) {
		t.Errorf("显式条目 = %v, %v, want %v(顺序不动,按值去重)", got, err, want)
	}

	// 存量纯字符串元素:名按 Label 推导。
	got, err = Effective(json.RawMessage(`["low","high"]`), nil)
	want = []Entry{{Name: "低", Value: "low"}, {Name: "高", Value: "high"}}
	if err != nil || !slices.Equal(got, want) {
		t.Errorf("存量字符串数组 = %v, %v, want %v", got, err, want)
	}

	got, err = Effective(json.RawMessage(`[]`), []string{"low"})
	if err != nil || len(got) != 0 {
		t.Errorf("显式空数组 = %v, %v, want 空(不支持)", got, err)
	}

	if _, err = Effective(json.RawMessage(`[{"name":"空值","value":" "}]`), nil); err == nil {
		t.Error("空 value 应报错")
	}
	if _, err = Effective(json.RawMessage(`[1]`), nil); err == nil {
		t.Error("非字符串/对象元素应报错")
	}
	if _, err = Effective(json.RawMessage(`{"a":1}`), nil); err == nil {
		t.Error("非数组形态应报错")
	}
}

// TestContainsValue 与 Values 锁定发送侧校验的两个辅助。
func TestContainsValue(t *testing.T) {
	list := []Entry{{Name: "低", Value: "low"}, {Name: "私有", Value: "ultra"}}
	if !ContainsValue(list, "ultra") || ContainsValue(list, "high") {
		t.Errorf("ContainsValue 判定错误: %v", list)
	}
	if got := Values(list); !slices.Equal(got, []string{"low", "ultra"}) {
		t.Errorf("Values = %v, want [low ultra]", got)
	}
}
