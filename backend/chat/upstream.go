package chat

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/aceaura/model-surge-upstream/backend/apperr"
	"github.com/aceaura/model-surge-upstream/backend/provider"
	"github.com/aceaura/model-surge-upstream/backend/resolve"
	"github.com/aceaura/model-surge-upstream/backend/usage"
)

// completeTimeout 是单轮补全的上游超时。长回复模型可能跑满一分钟以上，
// 取宽松值；界面侧有自己的等待提示，不靠超时兜用户体验。
const completeTimeout = 180 * time.Second

// respSnippet 是错误信息里附带的上游响应截断长度。
const respSnippet = 300

// Complete 按目标模型的出站协议构造请求、调用上游并抽取回复文本与用量。
// history 含本轮用户消息（调用方已追加）。不做流式：对话页整轮等待，
// 换取四协议一套简单可靠的解析路径。返回上游 HTTP 状态码供用量统计记失败率。
func Complete(ctx context.Context, target resolve.ResolvedTarget, history []Message) (string, usage.Usage, int, error) {
	suffix, body, err := buildRequest(target, history)
	if err != nil {
		return "", usage.Usage{}, 0, err
	}
	encoded, err := json.Marshal(body)
	if err != nil {
		return "", usage.Usage{}, 0, apperr.Wrap(apperr.InvalidJSON, "encode upstream request", err)
	}

	ctx, cancel := context.WithTimeout(ctx, completeTimeout)
	defer cancel()
	url := strings.TrimRight(target.BaseURL, "/") + suffix
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(encoded))
	if err != nil {
		return "", usage.Usage{}, 0, apperr.Wrap(apperr.UpstreamUnavailable, "build upstream request", err)
	}
	req.Header.Set("Content-Type", "application/json")
	for k, v := range target.Headers {
		req.Header.Set(k, v)
	}

	client := &http.Client{}
	resp, err := client.Do(req)
	if err != nil {
		return "", usage.Usage{}, 0, apperr.Wrap(apperr.UpstreamUnavailable, "upstream request failed", err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return "", usage.Usage{}, resp.StatusCode, apperr.Wrap(apperr.UpstreamUnavailable, "read upstream response", err)
	}
	u, _ := usage.FromResponse(target.Protocol, raw)
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", u, resp.StatusCode, apperr.New(apperr.UpstreamUnavailable,
			fmt.Sprintf("上游返回 HTTP %d：%s", resp.StatusCode, snippet(raw)))
	}
	reply, err := extractReply(target.Protocol, raw)
	if err != nil {
		return "", u, resp.StatusCode, err
	}
	return reply, u, resp.StatusCode, nil
}

func snippet(raw []byte) string {
	s := strings.TrimSpace(string(raw))
	if r := []rune(s); len(r) > respSnippet {
		s = string(r[:respSnippet]) + "…"
	}
	return s
}

// buildRequest 按协议生成上游路径后缀与请求体。体先按协议形态构造，
// 再叠模型的 defaults（缺失才填）与 overrides（强制压盖），
// 与转发面给调用方的合并语义保持一致。
func buildRequest(target resolve.ResolvedTarget, history []Message) (string, map[string]any, error) {
	var suffix string
	var body map[string]any
	switch target.Protocol {
	case provider.ProtocolAnthropic:
		suffix = "/v1/messages"
		body = map[string]any{
			"model":      target.NativeModel,
			"max_tokens": 2048,
			"messages":   anthropicMessages(history),
		}
	case provider.ProtocolChatCompletions:
		suffix = "/v1/chat/completions"
		body = map[string]any{
			"model":    target.NativeModel,
			"messages": anthropicMessages(history), // 形态与 chat/completions 相同：role+content
		}
	case provider.ProtocolResponses:
		suffix = "/v1/responses"
		body = map[string]any{
			"model": target.NativeModel,
			"input": responsesInput(history),
		}
	case provider.ProtocolGemini:
		suffix = "/v1beta/models/" + target.NativeModel + ":generateContent"
		body = map[string]any{
			"contents": geminiContents(history),
		}
	default:
		return "", nil, apperr.New(apperr.InvalidProtocol,
			fmt.Sprintf("model %q speaks unknown protocol %q", target.ModelID, target.Protocol))
	}
	body = mergeParams(rawObject(target.Defaults), body)
	body = mergeParams(body, rawObject(target.Overrides))
	return suffix, body, nil
}

// anthropicMessages 生成 [{role, content}] 形态。user/assistant 交替
// 由对话本身保证（发送一轮即追加两条），不做额外规整。
func anthropicMessages(history []Message) []map[string]any {
	out := make([]map[string]any, 0, len(history))
	for _, m := range history {
		out = append(out, map[string]any{"role": m.Role, "content": m.Content})
	}
	return out
}

func responsesInput(history []Message) []map[string]any {
	out := make([]map[string]any, 0, len(history))
	for _, m := range history {
		out = append(out, map[string]any{
			"type":    "message",
			"role":    m.Role,
			"content": m.Content,
		})
	}
	return out
}

// geminiContents 的角色名是 user/model，assistant 需改写。
func geminiContents(history []Message) []map[string]any {
	out := make([]map[string]any, 0, len(history))
	for _, m := range history {
		role := "user"
		if m.Role == RoleAssistant {
			role = "model"
		}
		out = append(out, map[string]any{
			"role":  role,
			"parts": []map[string]any{{"text": m.Content}},
		})
	}
	return out
}

// extractReply 按协议从上游响应里抽回复文本。
func extractReply(protocol string, raw []byte) (string, error) {
	var obj map[string]any
	if err := json.Unmarshal(raw, &obj); err != nil {
		return "", apperr.New(apperr.UpstreamUnavailable,
			"上游响应不是合法 JSON："+snippet(raw))
	}
	switch protocol {
	case provider.ProtocolAnthropic:
		var sb strings.Builder
		if blocks, ok := obj["content"].([]any); ok {
			for _, b := range blocks {
				blk, _ := b.(map[string]any)
				if blk["type"] == "text" {
					if t, _ := blk["text"].(string); t != "" {
						sb.WriteString(t)
					}
				}
			}
		}
		return sb.String(), nil
	case provider.ProtocolChatCompletions:
		choices, _ := obj["choices"].([]any)
		if len(choices) == 0 {
			return "", apperr.New(apperr.UpstreamUnavailable, "上游响应缺少 choices")
		}
		first, _ := choices[0].(map[string]any)
		msg, _ := first["message"].(map[string]any)
		text, _ := msg["content"].(string)
		return text, nil
	case provider.ProtocolResponses:
		if t, _ := obj["output_text"].(string); t != "" {
			return t, nil
		}
		var sb strings.Builder
		if items, ok := obj["output"].([]any); ok {
			for _, it := range items {
				item, _ := it.(map[string]any)
				if item["type"] != "message" {
					continue
				}
				if parts, ok := item["content"].([]any); ok {
					for _, p := range parts {
						part, _ := p.(map[string]any)
						if t, _ := part["text"].(string); t != "" {
							sb.WriteString(t)
						}
					}
				}
			}
		}
		return sb.String(), nil
	case provider.ProtocolGemini:
		cands, _ := obj["candidates"].([]any)
		if len(cands) == 0 {
			return "", apperr.New(apperr.UpstreamUnavailable, "上游响应缺少 candidates")
		}
		first, _ := cands[0].(map[string]any)
		content, _ := first["content"].(map[string]any)
		var sb strings.Builder
		if parts, ok := content["parts"].([]any); ok {
			for _, p := range parts {
				part, _ := p.(map[string]any)
				if t, _ := part["text"].(string); t != "" {
					sb.WriteString(t)
				}
			}
		}
		return sb.String(), nil
	default:
		return "", apperr.New(apperr.InvalidProtocol, fmt.Sprintf("unknown protocol %q", protocol))
	}
}

// rawObject 把 defaults/overrides 读成对象；空或 null 视为 {}。
func rawObject(raw json.RawMessage) map[string]any {
	out := map[string]any{}
	if len(raw) == 0 {
		return out
	}
	_ = json.Unmarshal(raw, &out)
	if out == nil {
		return map[string]any{}
	}
	return out
}

// mergeParams 递归合并：overlay 压 base，对象深合并，数组与标量整体替换。
// 与转发面同语义；此处独立一份避免 chat 依赖 proxyplane 的私有实现。
func mergeParams(base, overlay map[string]any) map[string]any {
	out := make(map[string]any, len(base)+len(overlay))
	for k, v := range base {
		out[k] = v
	}
	for k, v := range overlay {
		if ov, ok := v.(map[string]any); ok {
			if bv, ok := out[k].(map[string]any); ok {
				out[k] = mergeParams(bv, ov)
				continue
			}
		}
		out[k] = v
	}
	return out
}
