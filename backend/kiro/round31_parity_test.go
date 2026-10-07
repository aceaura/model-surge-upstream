package kiro

import "testing"

// parsers.py:361-370 + thinking_parser.py:150:显式 null/false 的 content 帧
// 经 data.get('content','') 得假值,dedup 跳过或 feed 空返回,静默忽略;
// Go 不得整流报错。真值非字符串(如数字)在 Python 侧是崩溃路径,属既定
// 不复制类;此处只钉死假值帧的静默忽略。
func TestRound31NullContentFrame(t *testing.T) {
	for _, protocol := range []string{"anthropic", "openai"} {
		for _, stream := range []bool{true, false} {
			for _, bad := range []any{nil, false} {
				s := newResponseState(requestOptions{protocol: protocol, stream: stream})
				if err := s.accept(wireEvent{kind: "assistantResponseEvent", data: object{"content": "a"}}); err != nil {
					t.Fatalf("protocol=%s stream=%v: first content: %v", protocol, stream, err)
				}
				if err := s.accept(wireEvent{kind: "assistantResponseEvent", data: object{"content": bad}}); err != nil {
					t.Fatalf("protocol=%s stream=%v bad=%v: %v", protocol, stream, bad, err)
				}
				if err := s.accept(wireEvent{kind: "assistantResponseEvent", data: object{"content": "b"}}); err != nil {
					t.Fatalf("protocol=%s stream=%v: trailing content: %v", protocol, stream, err)
				}
			}
		}
	}
}
