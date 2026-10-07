package kiro

import "testing"

// 三十五轮:anthropic tool_choice={} 边界。pydantic smart union 对空对象
// 落 Dict[str,Any] 兜底(models_anthropic.py:390,实测选型),parse_tool_choice_
// policy(converters_core.py:209-223) 得 type=None → ValueError 400;
// Go 旧码对空对象注入 type="auto" 放行,须拒绝。
// {"name":"x"} 无 type 仍命中 ToolChoiceTool(type 缺省 "tool"),保持合法。
func TestRound35AnthropicEmptyToolChoiceRejected(t *testing.T) {
	root := object{
		"model": "model", "max_tokens": 32,
		"messages": []any{object{"role": "user", "content": "x"}},
		"tools":    []any{object{"name": "f", "input_schema": object{}}},
	}
	root["tool_choice"] = object{}
	if _, _, err := convertRequest([]byte(jsonText(root)), "anthropic", ""); err == nil {
		t.Fatal("empty anthropic tool_choice object accepted")
	}
	root["tool_choice"] = object{"name": "f"}
	if _, _, err := convertRequest([]byte(jsonText(root)), "anthropic", ""); err != nil {
		t.Fatalf("name-only tool_choice rejected: %v", err)
	}
}
