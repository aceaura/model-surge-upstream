package effort

import (
	"encoding/json"
	"slices"
	"testing"
)

// TestNormalize 锁定上游档位名的归一与别名映射(sub2api 同款别名表)。
func TestNormalize(t *testing.T) {
	cases := map[string]string{
		"low":        "low",
		" High ":     "high",
		"MAX":        "max",
		"off":        "none",
		"disabled":   "none",
		"extra-high": "xhigh",
		"extra_high": "xhigh",
		"ultra":      "", // 各家私有档词表表达不了,丢弃
		"turbo":      "",
		"":           "",
	}
	for in, want := range cases {
		if got := Normalize(in); got != want {
			t.Errorf("Normalize(%q) = %q, want %q", in, got, want)
		}
	}
}

// TestNormalizeList 校验声明列表归一:别名折叠、词表外丢弃、升序去重。
func TestNormalizeList(t *testing.T) {
	got := NormalizeList([]string{"high", "Low", "ultra", "extra-high", "high", "minimal"})
	if !slices.Equal(got, []string{"minimal", "low", "high", "xhigh"}) {
		t.Errorf("NormalizeList = %v, want [minimal low high xhigh]", got)
	}
	if got := NormalizeList(nil); len(got) != 0 {
		t.Errorf("NormalizeList(nil) = %v, want 空", got)
	}
}

// TestAuto 判定自动模式(null/缺省/空白=跟随上游声明)。
func TestAuto(t *testing.T) {
	for _, raw := range []json.RawMessage{nil, {}, json.RawMessage(`null`), json.RawMessage(" null ")} {
		if !Auto(raw) {
			t.Errorf("Auto(%q) = false, want true", string(raw))
		}
	}
	for _, raw := range []json.RawMessage{json.RawMessage(`[]`), json.RawMessage(`["low"]`)} {
		if Auto(raw) {
			t.Errorf("Auto(%q) = true, want false", string(raw))
		}
	}
}

// TestEffective 校验:自动模式跟随上游声明(无声明即不支持);显式数组按
// 词表升序去重;非法档位与非数组形态报错;显式空数组=不支持。
func TestEffective(t *testing.T) {
	// 自动 + 有声明:归一后的声明即有效列表。
	got, err := Effective(nil, []string{"high", "low", "extra_high"})
	if err != nil || !slices.Equal(got, []string{"low", "high", "xhigh"}) {
		t.Errorf("自动+声明 = %v, %v, want [low high xhigh]", got, err)
	}
	// 自动 + 无声明:不支持,选择器不露面。
	got, err = Effective(json.RawMessage(`null`), nil)
	if err != nil || len(got) != 0 {
		t.Errorf("自动+无声明 = %v, %v, want 空(不支持)", got, err)
	}
	// 显式数组压过上游声明。
	got, err = Effective(json.RawMessage(`["high","low","high","xhigh"]`), []string{"minimal"})
	if err != nil || !slices.Equal(got, []string{"low", "high", "xhigh"}) {
		t.Errorf("显式数组 = %v, %v, want 升序去重 [low high xhigh]", got, err)
	}

	got, err = Effective(json.RawMessage(`[]`), []string{"low"})
	if err != nil || len(got) != 0 {
		t.Errorf("显式空数组 = %v, %v, want 空(不支持)", got, err)
	}

	if _, err = Effective(json.RawMessage(`["turbo"]`), nil); err == nil {
		t.Error("词表外档位应报错")
	}
	if _, err = Effective(json.RawMessage(`{"a":1}`), nil); err == nil {
		t.Error("非数组形态应报错")
	}
}
