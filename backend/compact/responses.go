package compact

import (
	"bytes"
	"encoding/json"
	"errors"
	"strings"

	"github.com/aceaura/model-surge-upstream/backend/provider"
	"github.com/aceaura/model-surge-upstream/backend/usage"
)

// responsesImpl 处理 OpenAI Responses 协议：
// {instructions: 顶层字段, input: [message|function_call|function_call_output|reasoning, ...]}。
// 与 Chat 的差异：消息只是 input 的一种 item（type=message），工具链以
// function_call/function_call_output 平铺；安全切点仍只落在 user 消息上。
type responsesImpl struct{}

// Estimate 复用通用文本估算，另按 input item 数补结构开销
// （estimateBody 只认 messages 键）。
func (responsesImpl) Estimate(body map[string]any) int {
	n := estimateBody(body)
	if items, ok := body["input"].([]any); ok {
		n += messageOverhead * len(items)
	}
	return n
}

// Messages 取 input 列表。input 是字符串形态时返回 nil——Cut 失败
// 走 fail-open 原样转发（ShapeBody 稍后才规范化字符串 input）。
func (responsesImpl) Messages(body map[string]any) []any {
	items, _ := body["input"].([]any)
	return items
}

// Cut 从后往前数第 keepTurns 条 user 消息，其下标为切点。tail 首条必为
// user 消息——不会出现引用了 prefix 里 function_call 的悬空
// function_call_output，也不会有断了上下文的 reasoning item。
// i 扫到 1，保证 prefix 非空。
func (responsesImpl) Cut(items []any, keepTurns int) (prefix, tail []any, ok bool) {
	if keepTurns < 1 {
		keepTurns = 1
	}
	count := 0
	for i := len(items) - 1; i >= 1; i-- {
		if isResponsesUserMessage(items[i]) {
			count++
			if count == keepTurns {
				return items[:i], items[i:], true
			}
		}
	}
	return nil, nil, false
}

// isResponsesUserMessage 判定 user 消息 item：role=user 且 type 空缺
// （旧形态）或为 message。function_call_output 等工具 item 永不算切点。
func isResponsesUserMessage(item any) bool {
	m, ok := item.(map[string]any)
	if !ok || m["role"] != "user" {
		return false
	}
	t, _ := m["type"].(string)
	return t == "" || t == "message"
}

// BuildSummaryRequest 构造非流式摘要请求：instructions 随历史带入，
// 压缩指令作为最后一条 user 消息追加。不合并 defaults/overrides。
// codex 订阅端点的硬约束（stream/store/剥参数）由 Run 在拿到请求后
// 统一套 ShapeBody，这里只产出协议标准形态。
func (responsesImpl) BuildSummaryRequest(nativeModel string, body map[string]any, prefix []any, maxTokens int) (string, map[string]any, error) {
	history, ok := deepCopy(prefix).([]any)
	if !ok {
		return "", nil, errors.New("deep copy input failed")
	}
	full := append(history, responsesUserMessage(instruction))

	req := map[string]any{
		"model":             nativeModel,
		"max_output_tokens": maxTokens,
		"input":             full,
	}
	if instr, ok := body["instructions"]; ok {
		req["instructions"] = deepCopy(instr)
	}
	return "/v1/responses", req, nil
}

// responsesUserMessage 构造一条 Responses user 消息 item。
func responsesUserMessage(text string) map[string]any {
	return map[string]any{
		"type": "message",
		"role": "user",
		"content": []any{
			map[string]any{"type": "input_text", "text": text},
		},
	}
}

// ExtractSummary 从摘要响应抽出纯文本与用量。优先按非流式 JSON 解析；
// 失败后按 SSE 解析——codex 订阅端点强制 stream=true，响应体是事件流，
// 取 response.completed 事件里的完整 response 对象按 JSON 路径复用。
func (responsesImpl) ExtractSummary(raw []byte) (string, usage.Usage, error) {
	var obj map[string]any
	if err := json.Unmarshal(raw, &obj); err == nil {
		return extractResponsesSummary(obj, raw)
	}
	completed, err := responsesCompletedEvent(raw)
	if err != nil {
		return "", usage.Usage{}, err
	}
	encoded, err := json.Marshal(completed)
	if err != nil {
		return "", usage.Usage{}, err
	}
	return extractResponsesSummary(completed, encoded)
}

// extractResponsesSummary 从 response 对象抽文本与用量。
func extractResponsesSummary(obj map[string]any, raw []byte) (string, usage.Usage, error) {
	text, err := responsesOutputText(obj)
	if err != nil {
		return "", usage.Usage{}, err
	}
	u, _ := usage.FromResponse(provider.ProtocolResponses, raw)
	return text, u, nil
}

// responsesOutputText 拼接 output 里所有 message item 的 output_text 段；
// output 缺失时回退顶层 output_text 便捷字段。
func responsesOutputText(obj map[string]any) (string, error) {
	var sb strings.Builder
	if output, ok := obj["output"].([]any); ok {
		for _, item := range output {
			m, ok := item.(map[string]any)
			if !ok || m["type"] != "message" {
				continue
			}
			parts, _ := m["content"].([]any)
			for _, p := range parts {
				pm, ok := p.(map[string]any)
				if !ok || pm["type"] != "output_text" {
					continue
				}
				if t, ok := pm["text"].(string); ok {
					sb.WriteString(t)
				}
			}
		}
	}
	if sb.Len() == 0 {
		if t, ok := obj["output_text"].(string); ok {
			sb.WriteString(t)
		}
	}
	text := strings.TrimSpace(sb.String())
	if text == "" {
		return "", errors.New("summary response has no output text")
	}
	return text, nil
}

// responsesCompletedEvent 在 SSE 事件流里找 response.completed 事件，
// 返回其 response 对象。
func responsesCompletedEvent(raw []byte) (map[string]any, error) {
	for _, line := range bytes.Split(raw, []byte("\n")) {
		line = bytes.TrimSpace(line)
		data, ok := bytes.CutPrefix(line, []byte("data:"))
		if !ok {
			continue
		}
		var event map[string]any
		if err := json.Unmarshal(bytes.TrimSpace(data), &event); err != nil {
			continue
		}
		if event["type"] != "response.completed" {
			continue
		}
		if resp, ok := event["response"].(map[string]any); ok {
			return resp, nil
		}
	}
	return nil, errors.New("summary stream has no response.completed event")
}

// Splice 把摘要与 tail 拼回请求体：摘要作为一条 user 消息插在 tail 之前。
// Responses 无交替约束，不需要补假 assistant 应答；instructions 顶层
// 字段原样保留。
func (responsesImpl) Splice(body map[string]any, summary string, tail []any) map[string]any {
	out := make(map[string]any, len(body))
	for k, v := range body {
		out[k] = v
	}
	merged := make([]any, 0, len(tail)+1)
	merged = append(merged, responsesUserMessage(summaryPreamble+summary))
	merged = append(merged, tail...)
	out["input"] = merged
	return out
}
