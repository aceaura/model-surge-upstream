package effort

import "testing"

// TestCoerce 矫正矩阵:空表恒等 / 别名归一 / 精确命中 / 向下钳 / 低于最低档
// 取最低 / 未知值 medium 兜底 / 自定义档参与。
func TestCoerce(t *testing.T) {
	full := []Entry{{Name: "0", Value: "none"}, {Name: "1", Value: "low"}, {Name: "2", Value: "medium"}, {Name: "3", Value: "high"}}
	noNone := []Entry{{Name: "1", Value: "low"}, {Name: "2", Value: "high"}}
	kimi := []Entry{{Name: "1", Value: "low"}, {Name: "2", Value: "high"}, {Name: "3", Value: "max"}}
	custom := []Entry{{Name: "低", Value: "low"}, {Name: "ultra", Value: "ultra"}}
	onlyNone := []Entry{{Name: "0", Value: "none"}}

	for _, tc := range []struct {
		name        string
		value       string
		list        []Entry
		want        string
		wantChanged bool
	}{
		{name: "空表恒等透传", value: "whatever", list: nil, want: "whatever"},
		{name: "精确命中不算矫正", value: "high", list: full, want: "high"},
		{name: "大小写归一命中", value: "HIGH", list: full, want: "high", wantChanged: true},
		{name: "别名 off→none", value: "off", list: full, want: "none", wantChanged: true},
		{name: "别名 disabled→none 但表无 none 钳最低", value: "disabled", list: noNone, want: "low", wantChanged: true},
		{name: "别名 extra-high→xhigh 表内无 xhigh 钳 high", value: "extra-high", list: full, want: "high", wantChanged: true},
		{name: "未声明 medium 之外档向下钳:xhigh→high", value: "xhigh", list: full, want: "high", wantChanged: true},
		{name: "未声明档向下钳:kimi 表 medium→low", value: "medium", list: kimi, want: "low", wantChanged: true},
		{name: "kimi 表 max 精确命中", value: "max", list: kimi, want: "max"},
		{name: "低于最低档取最低:none→low(表无 none)", value: "none", list: noNone, want: "low", wantChanged: true},
		{name: "表声明 none 则 none 命中", value: "none", list: full, want: "none"},
		{name: "minimal 低于 low 钳最低", value: "minimal", list: full, want: "none", wantChanged: true},
		{name: "未知自定义值 medium 兜底", value: "ultra", list: full, want: "medium", wantChanged: true},
		{name: "未知值无 medium 取首个非 none", value: "ultra", list: noNone, want: "low", wantChanged: true},
		{name: "表只有 none 回 none", value: "ultra", list: onlyNone, want: "none", wantChanged: true},
		{name: "声明的自定义值精确命中", value: "ultra", list: custom, want: "ultra"},
		{name: "high 遇自定义表:强度成员只有 low 钳 low", value: "high", list: custom, want: "low", wantChanged: true},
		{name: "未知值遇自定义表 medium 兜底失败取首个非 none", value: "mega", list: custom, want: "low", wantChanged: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, changed := Coerce(tc.value, tc.list)
			if got != tc.want || changed != tc.wantChanged {
				t.Errorf("Coerce(%q) = %q,%v, want %q,%v", tc.value, got, changed, tc.want, tc.wantChanged)
			}
		})
	}
}
