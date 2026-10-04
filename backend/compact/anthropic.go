package compact

import (
	"encoding/json"
	"errors"
	"strings"

	"github.com/aceaura/model-surge-upstream/backend/provider"
	"github.com/aceaura/model-surge-upstream/backend/usage"
)

// 压缩指令：作为摘要请求的最后一条 user 消息追加在早期历史之后。
const instruction = "以上是一段人机对话的早期历史。请将其压缩为一份详尽摘要，必须保留：" +
	"用户的目标与约束、已做出的决定与理由、关键事实/代码/数据/文件路径、" +
	"每次工具调用的要点与结果、未解决的问题与下一步。" +
	"使用历史中的主要语言输出，直接给出摘要正文，不要客套。"

// summaryPreamble 是拼回主请求时摘要块的前缀说明。
const summaryPreamble = "[此前对话的早期历史已被代理压缩为以下摘要，请基于摘要与后续消息继续]\n"

// anthropicAck 是摘要轮之后补的假 assistant 应答：Anthropic 要求消息
// 严格 user/assistant 交替，tail 首条必为 user，摘要 user 轮之后必须
// 垫一条 assistant 才合法。
const anthropicAck = "了解。我将基于以上摘要与后续对话继续。"

// anthropicImpl 处理 Anthropic Messages 协议：
// {system: 顶层字段, messages: [{role: user|assistant, content: string|blocks}]}。
type anthropicImpl struct{}

func (anthropicImpl) Estimate(body map[string]any) int { return estimateBody(body) }

func (anthropicImpl) Messages(body map[string]any) []any {
	msgs, _ := body["messages"].([]any)
	return msgs
}

// Cut 从后往前数第 keepTurns 个「真人发言」（不含 tool_result 块的 user
// 消息），其下标为切点。tail 首条因此必为真人发言——不会出现引用了
// prefix 里 tool_use 的悬空 tool_result。prefix 里未闭合的工具链
// 被消化进摘要文本，不透传给上游。i 从 len-1 扫到 1，保证 prefix 非空。
func (anthropicImpl) Cut(messages []any, keepTurns int) (prefix, tail []any, ok bool) {
	if keepTurns < 1 {
		keepTurns = 1
	}
	count := 0
	for i := len(messages) - 1; i >= 1; i-- {
		if isRealUserMessage(messages[i]) {
			count++
			if count == keepTurns {
				return messages[:i], messages[i:], true
			}
		}
	}
	return nil, nil, false
}

// isRealUserMessage 判定真人发言：role=user 且 content 里没有
// tool_result 块（纯字符串 content 必为真人发言）。
func isRealUserMessage(m any) bool {
	msg, ok := m.(map[string]any)
	if !ok || msg["role"] != "user" {
		return false
	}
	blocks, ok := msg["content"].([]any)
	if !ok {
		return true
	}
	for _, b := range blocks {
		if blk, ok := b.(map[string]any); ok && blk["type"] == "tool_result" {
			return false
		}
	}
	return true
}

// BuildSummaryRequest 构造发往上游的摘要请求：保留 system，历史为
// prefix 的深拷贝（避免污染 fail-open 时要原样转发的 body），压缩指令
// 追加在末尾。不合并 defaults/overrides——摘要是网关内部行为，
// 不能带上模型配置里的 thinking/temperature 等强制覆盖。
func (anthropicImpl) BuildSummaryRequest(nativeModel string, body map[string]any, prefix []any, maxTokens int) (string, map[string]any, error) {
	req := map[string]any{
		"model":      nativeModel,
		"max_tokens": maxTokens,
	}
	if sys, ok := body["system"]; ok {
		req["system"] = deepCopy(sys)
	}
	history, ok := deepCopy(prefix).([]any)
	if !ok {
		return "", nil, errors.New("deep copy messages failed")
	}
	req["messages"] = appendAnthropicInstruction(history)
	return "/v1/messages", req, nil
}

// appendAnthropicInstruction 把压缩指令追加到历史末尾，保持 user/assistant
// 交替：末条是 user 则并入其 content，否则新增一条 user 消息。
func appendAnthropicInstruction(messages []any) []any {
	if len(messages) > 0 {
		if last, ok := messages[len(messages)-1].(map[string]any); ok && last["role"] == "user" {
			switch c := last["content"].(type) {
			case string:
				last["content"] = c + "\n\n" + instruction
			case []any:
				last["content"] = append(c, map[string]any{"type": "text", "text": instruction})
			default:
				last["content"] = instruction
			}
			return messages
		}
	}
	return append(messages, map[string]any{
		"role":    "user",
		"content": []any{map[string]any{"type": "text", "text": instruction}},
	})
}

// ExtractSummary 从 Anthropic 非流式响应抽出摘要文本与用量。
func (anthropicImpl) ExtractSummary(raw []byte) (string, usage.Usage, error) {
	var resp struct {
		Content []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
	}
	if err := json.Unmarshal(raw, &resp); err != nil {
		return "", usage.Usage{}, err
	}
	var sb strings.Builder
	for _, b := range resp.Content {
		if b.Type == "text" {
			sb.WriteString(b.Text)
		}
	}
	text := strings.TrimSpace(sb.String())
	if text == "" {
		return "", usage.Usage{}, errors.New("summary response has no text content")
	}
	u, _ := usage.FromResponse(provider.ProtocolAnthropic, raw)
	return text, u, nil
}

// Splice 把摘要与 tail 拼回请求体。Anthropic 约束：首条必须 user 且
// 严格交替——tail 首条是真人发言（Cut 保证），故前置一对
// 「摘要 user 轮 + 假 assistant 应答」。
//
// tail 里的 cache_control 断点全部剥离：历史改写后 Anthropic 按前缀
// 匹配缓存，旧断点必不命中且白占配额（上限 4 个，超限 400）；摘要块
// 打一个新 ephemeral 断点，让 system+摘要成为新的稳定缓存前缀。
func (anthropicImpl) Splice(body map[string]any, summary string, tail []any) map[string]any {
	out := make(map[string]any, len(body))
	for k, v := range body {
		out[k] = v
	}
	cleanTail, _ := deepCopy(tail).([]any)
	stripCacheControl(cleanTail)

	summaryMsg := map[string]any{
		"role": "user",
		"content": []any{map[string]any{
			"type":          "text",
			"text":          summaryPreamble + summary,
			"cache_control": map[string]any{"type": "ephemeral"},
		}},
	}
	ack := map[string]any{
		"role":    "assistant",
		"content": []any{map[string]any{"type": "text", "text": anthropicAck}},
	}
	messages := make([]any, 0, len(cleanTail)+2)
	messages = append(messages, summaryMsg, ack)
	messages = append(messages, cleanTail...)
	out["messages"] = messages
	return out
}

// stripCacheControl 递归剥离消息结构里的所有 cache_control 键。
func stripCacheControl(v any) {
	switch t := v.(type) {
	case map[string]any:
		delete(t, "cache_control")
		for _, val := range t {
			stripCacheControl(val)
		}
	case []any:
		for _, val := range t {
			stripCacheControl(val)
		}
	}
}

// deepCopy 走 JSON 往返做深拷贝。请求体来自 json.Unmarshal，
// 必然可再序列化；失败时返回原值（调用方随后会失败回退）。
func deepCopy(v any) any {
	raw, err := json.Marshal(v)
	if err != nil {
		return v
	}
	var out any
	if err := json.Unmarshal(raw, &out); err != nil {
		return v
	}
	return out
}
