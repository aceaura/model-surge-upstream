// 转发面调试日志的格式化:最终发给上游的请求体与上游响应体各记一条进程
// 日志。请求体的长文本字段(对话正文)整体换占位说明,其余 JSON 参数原样
// 输出;响应体原样输出。任何单字符串超上限截断、响应捕获限长双兜底,
// 保证单条日志有界,环形缓冲不会被一条大请求撑爆。
package proxyplane

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
)

// longTextKeys 是各协议承载对话正文的顶层字段:调试看的是参数与合并结果,
// 正文本身只会刷屏。命中即整体换占位,不再展开内部结构。
var longTextKeys = map[string]bool{
	"messages":           true, // anthropic / chat_completions
	"input":              true, // responses(字符串或消息数组)
	"instructions":       true, // responses 系统指令(codex 注入的很长)
	"contents":           true, // gemini
	"system":             true, // anthropic 系统提示
	"system_instruction": true, // gemini 系统提示
	"prompt":             true, // 老式 completions 形态
}

const (
	// stringClamp 是日志里单个字符串值的展示上限(字符),超出保留头部并标注原长。
	stringClamp = 4000
	// stringHead 是截断时保留的头部长度(字符)。
	stringHead = 2000
	// captureLimit 是响应体旁路捕获的字节上限:非流式响应远小于此,
	// 流式响应超出后只记头部,总量由日志里的字节数交代。
	captureLimit = 64 << 10
)

// requestDebugView 返回请求体的调试视图:长文本字段换占位说明,工具清单
// 压成名字列表,其余参数原样保留,藏在他处的超长字符串截断兜底。
func requestDebugView(body map[string]any) map[string]any {
	out := make(map[string]any, len(body))
	for k, v := range body {
		if longTextKeys[k] {
			out[k] = omittedNote(v)
			continue
		}
		if k == "tools" {
			out[k] = toolsNote(v)
			continue
		}
		out[k] = clampStrings(v)
	}
	return out
}

// omittedNote 给被略去的长文本字段一句形状说明:数组记条数、字符串记长度。
func omittedNote(v any) string {
	switch t := v.(type) {
	case []any:
		return fmt.Sprintf("(%d items omitted)", len(t))
	case string:
		return fmt.Sprintf("(%d chars omitted)", len([]rune(t)))
	default:
		return "(omitted)"
	}
}

// toolsNote 把工具清单压成名字列表:调试要确认「给了哪些工具」,每个工具的
// description/input_schema 展开可达几十 KB,原样输出只会刷屏。名字取两种
// 原生位置:anthropic/gemini 的顶层 name、openai 的 function.name。
func toolsNote(v any) any {
	items, ok := v.([]any)
	if !ok {
		return clampStrings(v)
	}
	names := make([]string, 0, len(items))
	for _, it := range items {
		m, ok := it.(map[string]any)
		if !ok {
			continue
		}
		if name, ok := m["name"].(string); ok {
			names = append(names, name)
			continue
		}
		if fn, ok := m["function"].(map[string]any); ok {
			if name, ok := fn["name"].(string); ok {
				names = append(names, name)
			}
		}
	}
	return fmt.Sprintf("(%d tools: %s)", len(items), strings.Join(names, ", "))
}

// clampStrings 深遍历并截断超长字符串,其余类型原样。
func clampStrings(v any) any {
	switch t := v.(type) {
	case string:
		return clampString(t)
	case map[string]any:
		out := make(map[string]any, len(t))
		for k, sub := range t {
			out[k] = clampStrings(sub)
		}
		return out
	case []any:
		out := make([]any, len(t))
		for i, sub := range t {
			out[i] = clampStrings(sub)
		}
		return out
	default:
		return v
	}
}

func clampString(s string) string {
	r := []rune(s)
	if len(r) <= stringClamp {
		return s
	}
	return fmt.Sprintf("%s…(%d chars total)", string(r[:stringHead]), len(r))
}

// marshalDebug 序列化调试视图。map 元素类型受限于 JSON 反序列化产物,
// 正常不会失败;失败时给一句说明,不让日志整条消失。
func marshalDebug(v any) string {
	b, err := json.Marshal(v)
	if err != nil {
		return fmt.Sprintf("(marshal failed: %v)", err)
	}
	return string(b)
}

// responseDebugText 把旁路捕获的响应字节格式化成日志文本:JSON 原样输出
// (超长字符串截断),SSE/纯文本等非 JSON 给原文头部;捕获被上限截断时
// 标注实际总字节数。
func responseDebugText(captured []byte, total int64) string {
	if len(captured) == 0 {
		return "(empty)"
	}
	var v any
	var text string
	if err := json.Unmarshal(captured, &v); err == nil {
		text = marshalDebug(clampStrings(v))
	} else {
		text = clampString(string(captured))
	}
	if int64(len(captured)) < total {
		text += fmt.Sprintf(" …(%d bytes total)", total)
	}
	return text
}

// cappedWriter 旁路记录响应字节,超出上限后丢弃但照常应答(配 TeeReader 用)。
type cappedWriter struct {
	buf bytes.Buffer
}

func (w *cappedWriter) Write(p []byte) (int, error) {
	if w.buf.Len() < captureLimit {
		n := captureLimit - w.buf.Len()
		if n > len(p) {
			n = len(p)
		}
		w.buf.Write(p[:n])
	}
	return len(p), nil
}
