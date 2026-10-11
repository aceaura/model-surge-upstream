package effort

import "testing"

// TestCoerce 矫正矩阵(2026-10-11 行式映射对语义):匹配与强度比较按行
// Name(下游档),产出恒为该行 Value(上行值)。空表恒等 / 别名归一 /
// Name 精确命中 / 就低钳 / 低于最低档取最低 / 未知值排除 none 后位置
// 取中 (n-1)/2。
func TestCoerce(t *testing.T) {
	// 映射对表:下游档 → 上行值(非恒等,验证产出取 Value)。
	full := []Entry{
		{Name: "none", Value: "none"},
		{Name: "low", Value: "low"},
		{Name: "medium", Value: "medium"},
		{Name: "high", Value: "high"},
	}
	mapped := []Entry{
		{Name: "none", Value: "none"},
		{Name: "low", Value: "medium"},
		{Name: "high", Value: "max"},
	}
	noNone := []Entry{{Name: "low", Value: "low"}, {Name: "high", Value: "high"}}
	kimi := []Entry{{Name: "low", Value: "low"}, {Name: "high", Value: "high"}, {Name: "max", Value: "max"}}
	custom := []Entry{{Name: "low", Value: "low"}, {Name: "ultra", Value: "ultra"}}
	onlyNone := []Entry{{Name: "none", Value: "none"}}

	for _, tc := range []struct {
		name        string
		value       string
		list        []Entry
		want        string
		wantChanged bool
	}{
		{name: "空表恒等透传", value: "whatever", list: nil, want: "whatever"},
		{name: "Name 精确命中回该行 Value(恒等)", value: "high", list: full, want: "high"},
		{name: "Name 精确命中回该行 Value(映射改写)", value: "low", list: mapped, want: "medium", wantChanged: true},
		{name: "大小写归一命中", value: "HIGH", list: mapped, want: "max", wantChanged: true},
		{name: "别名 off→none 命中 none 行", value: "off", list: full, want: "none", wantChanged: true},
		{name: "别名 disabled→none 但表无 none 取最低", value: "disabled", list: noNone, want: "low", wantChanged: true},
		{name: "别名 extra-high→xhigh 表内无 xhigh 钳 high", value: "extra-high", list: full, want: "high", wantChanged: true},
		{name: "未声明档向下钳:xhigh→high", value: "xhigh", list: full, want: "high", wantChanged: true},
		{name: "未声明档向下钳:kimi 表 medium→low", value: "medium", list: kimi, want: "low", wantChanged: true},
		{name: "kimi 表 max 精确命中", value: "max", list: kimi, want: "max"},
		{name: "低于最低档取最低:none→low(表无 none)", value: "none", list: noNone, want: "low", wantChanged: true},
		{name: "minimal 低于 low 钳最低(表含 none)", value: "minimal", list: full, want: "none", wantChanged: true},
		{name: "声明的自定义值精确命中", value: "ultra", list: custom, want: "ultra"},
		{name: "high 遇自定义表:强度成员只有 low 钳 low", value: "high", list: custom, want: "low", wantChanged: true},

		// 未知值(无强度语义):排除 none 行后按位置取中 (n-1)/2。
		{name: "未知值取中:n=3 选 2(含 none 行)", value: "ultra", list: full, want: "medium", wantChanged: true},
		{name: "未知值取中:n=2 选 1", value: "ultra", list: noNone, want: "low", wantChanged: true},
		{name: "未知值取中:n=3 选 2", value: "mega", list: kimi, want: "high", wantChanged: true},
		{name: "未知值取中:n=1 选 1", value: "mega", list: []Entry{{Name: "low", Value: "low"}}, want: "low", wantChanged: true},
		{name: "未知值取中:n=5 选 3", value: "mega", list: []Entry{
			{Name: "none", Value: "none"},
			{Name: "minimal", Value: "minimal"},
			{Name: "low", Value: "low"},
			{Name: "medium", Value: "medium"},
			{Name: "high", Value: "high"},
			{Name: "max", Value: "max"},
		}, want: "medium", wantChanged: true},
		{name: "未知值取中:映射表产出该行 Value", value: "mega", list: mapped, want: "medium", wantChanged: true},
		{name: "表只有 none 落 none", value: "ultra", list: onlyNone, want: "none", wantChanged: true},
		{name: "未知值遇自定义表取中", value: "mega", list: custom, want: "low", wantChanged: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, changed := Coerce(tc.value, tc.list)
			if got != tc.want || changed != tc.wantChanged {
				t.Errorf("Coerce(%q) = %q,%v, want %q,%v", tc.value, got, changed, tc.want, tc.wantChanged)
			}
		})
	}
}
