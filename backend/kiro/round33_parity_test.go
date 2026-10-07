package kiro

import "testing"

// 三十三轮:响应侧 finishTool 空判定须与 parsers.py:441 的 str.strip() 同类。
// Python 对纯 \x1c-\x1f 参数走 else 分支:arguments="{}" 且不标 _arguments_invalid;
// Go strings.TrimSpace 不含 \x1c-\x1f,旧实现误入解析失败 → invalid=true。
func TestRound33PyStripFinishToolArgs(t *testing.T) {
	s := &responseState{}
	s.tool = &pendingTool{id: "call_r33", name: "round33_tool"}
	s.tool.args.WriteString("\x1c")
	if err := s.finishTool(); err != nil {
		t.Fatalf("finishTool: %v", err)
	}
	if len(s.tools) != 1 {
		t.Fatalf("tools=%d want 1", len(s.tools))
	}
	ft := s.tools[0]
	if ft.args != "{}" {
		t.Fatalf("args=%q want {}", ft.args)
	}
	if ft.invalid {
		t.Fatal("invalid=true want false(parsers.py:441 strip 后为空走 else 分支)")
	}
}
