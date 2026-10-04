package compact

import (
	"encoding/json"
	"errors"
	"strings"

	"github.com/aceaura/model-surge-upstream/backend/provider"
	"github.com/aceaura/model-surge-upstream/backend/usage"
)

// chatImpl 处理 OpenAI Chat Completions 协议：
// {messages: [{role: system|user|assistant|tool, ...}]}。
// 与 Anthropic 的差异：system 在 messages 数组头部；assistant 的
// tool_calls 由后续 role=tool 消息应答；无交替约束、无 cache_control。
type chatImpl struct{}

func (chatImpl) Estimate(body map[string]any) int { return estimateBody(body) }

func (chatImpl) Messages(body map[string]any) []any {
	msgs, _ := body["messages"].([]any)
	return msgs
}

// Cut 切点只落在 role=user 的消息之前（头部连续 system 不参与切割）。
// tail 首条必为 user——不会出现引用了 prefix 里 tool_calls 的悬空
// role=tool 消息。i 扫到 start+1，保证 prefix（不含 system）非空。
func (chatImpl) Cut(messages []any, keepTurns int) (prefix, tail []any, ok bool) {
	start := leadingSystems(messages)
	middle := messages[start:]
	if keepTurns < 1 {
		keepTurns = 1
	}
	count := 0
	for i := len(middle) - 1; i >= 1; i-- {
		if m, ok := middle[i].(map[string]any); ok && m["role"] == "user" {
			count++
			if count == keepTurns {
				return middle[:i], middle[i:], true
			}
		}
	}
	return nil, nil, false
}

// leadingSystems 返回 messages 头部连续 system 消息的条数。
func leadingSystems(messages []any) int {
	n := 0
	for n < len(messages) {
		m, ok := messages[n].(map[string]any)
		if !ok || m["role"] != "system" {
			break
		}
		n++
	}
	return n
}

// BuildSummaryRequest 构造摘要请求：system 消息随历史一起带入，
// 压缩指令追加为最后一条 user 消息。不设 stream（非流式），不合并
// defaults/overrides。
func (chatImpl) BuildSummaryRequest(nativeModel string, body map[string]any, prefix []any, maxTokens int) (string, map[string]any, error) {
	messages, _ := body["messages"].([]any)
	history, ok := deepCopy(prefix).([]any)
	if !ok {
		return "", nil, errors.New("deep copy messages failed")
	}
	full := make([]any, 0, leadingSystems(messages)+len(history)+1)
	full = append(full, messages[:leadingSystems(messages)]...)
	full = append(full, history...)
	full = append(full, map[string]any{"role": "user", "content": instruction})

	req := map[string]any{
		"model":      nativeModel,
		"max_tokens": maxTokens,
		"messages":   full,
	}
	return "/v1/chat/completions", req, nil
}

// ExtractSummary 从 Chat Completions 非流式响应抽出摘要文本与用量。
// content 兼容字符串与内容块数组两种形态（部分兼容实现返回后者）。
func (chatImpl) ExtractSummary(raw []byte) (string, usage.Usage, error) {
	var resp struct {
		Choices []struct {
			Message struct {
				Content json.RawMessage `json:"content"`
			} `json:"message"`
		} `json:"choices"`
	}
	if err := json.Unmarshal(raw, &resp); err != nil {
		return "", usage.Usage{}, err
	}
	if len(resp.Choices) == 0 {
		return "", usage.Usage{}, errors.New("summary response has no choices")
	}
	text, err := chatContentText(resp.Choices[0].Message.Content)
	if err != nil {
		return "", usage.Usage{}, err
	}
	u, _ := usage.FromResponse(provider.ProtocolChatCompletions, raw)
	return text, u, nil
}

func chatContentText(raw json.RawMessage) (string, error) {
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		if strings.TrimSpace(s) == "" {
			return "", errors.New("summary content is empty")
		}
		return strings.TrimSpace(s), nil
	}
	var parts []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	if err := json.Unmarshal(raw, &parts); err != nil {
		return "", errors.New("summary content has unknown shape")
	}
	var sb strings.Builder
	for _, p := range parts {
		if p.Type == "text" {
			sb.WriteString(p.Text)
		}
	}
	if strings.TrimSpace(sb.String()) == "" {
		return "", errors.New("summary content is empty")
	}
	return strings.TrimSpace(sb.String()), nil
}

// Splice 把摘要与 tail 拼回请求体：头部 system 原样保留，摘要作为
// 一条 user 消息插在 tail 之前（OpenAI 无交替约束；用 user 角色对
// 第三方兼容网关最稳，部分严格实现拒绝对话中部出现 system）。
func (chatImpl) Splice(body map[string]any, summary string, tail []any) map[string]any {
	out := make(map[string]any, len(body))
	for k, v := range body {
		out[k] = v
	}
	messages, _ := body["messages"].([]any)
	systems := messages[:leadingSystems(messages)]

	merged := make([]any, 0, len(systems)+1+len(tail))
	merged = append(merged, systems...)
	merged = append(merged, map[string]any{
		"role":    "user",
		"content": summaryPreamble + summary,
	})
	merged = append(merged, tail...)
	out["messages"] = merged
	return out
}
