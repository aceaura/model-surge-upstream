package chat

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/aceaura/model-surge-upstream/backend/apperr"
	"github.com/aceaura/model-surge-upstream/backend/codex"
	"github.com/aceaura/model-surge-upstream/backend/effort"
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
//
// codex(openai-codex)是例外：订阅端点强制 stream=true，响应恒为 SSE，
// 这里把整段事件流读完后聚合回整轮（见 extractReplySSE），对外仍是非流式语义。
// sessionKey 用于派生 codex 的 session_id/conversation_id 头（转发面同款，
// 按账号+会话稳定），其余协议忽略。effort 是对话页选定的推理档（空=默认），
// 见 applyEffort。
func Complete(ctx context.Context, target resolve.ResolvedTarget, sessionKey, effort string, history []Message) (string, usage.Usage, int, error) {
	suffix, body, err := buildRequest(target, history)
	if err != nil {
		return "", usage.Usage{}, 0, err
	}
	// 档位写进骨架+defaults 后的体,再由 overrides 合并压盖:
	// 优先级 overrides > 对话页选定档 > defaults,与转发面
	// (overrides > reasoning_level 映射 > 请求参数 > defaults)同一形态。
	// applyEffort 先于 codex 整形(ShapeBody 看到 reasoning 会补 include)。
	if effort != "" {
		if err := applyEffort(target, body, effort); err != nil {
			return "", usage.Usage{}, 0, err
		}
	}
	body = mergeParams(body, rawObject(target.Overrides))
	// codex 硬约束(store/stream/剥采样参数/instructions)与路径映射最后应用，
	// 压过 defaults/overrides——与转发面、连通性检测同一顺序。
	if target.ProviderID == codex.ProviderID {
		body = codex.ShapeBody(body, target.NativeModel)
		suffix = codex.MapSuffix(suffix)
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
	if target.ProviderID == codex.ProviderID {
		// 会话头由服务端按账号+会话派生（sub2api 同款隔离），不接收客户端值。
		sid := codex.SessionID(target.Account, sessionKey)
		req.Header.Set("session_id", sid)
		req.Header.Set("conversation_id", sid)
		req.Header.Set("x-client-request-id", randomUUID())
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
	var reply string
	// codex 强制 stream=true，响应恒为事件流——但它 Content-Type 报
	// text/plain（2026-10-03 实测），不能靠响应头判，按请求侧定型。
	if target.ProviderID == codex.ProviderID || isEventStream(resp.Header) {
		reply, u, err = extractReplySSE(target.Protocol, raw)
	} else {
		reply, err = extractReply(target.Protocol, raw)
	}
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
// 再叠模型的 defaults（缺失才填）；overrides 由 Complete 在档位写入后
// 合并，保证 overrides 恒可压盖对话页选定档。
func buildRequest(target resolve.ResolvedTarget, history []Message) (string, map[string]any, error) {
	var suffix string
	var body map[string]any
	switch target.Protocol {
	case provider.ProtocolAnthropic:
		suffix = "/v1/messages"
		body = map[string]any{
			"model":      target.NativeModel,
			"max_tokens": 2048,
			"messages":   messageList(history, anthropicContent),
		}
	case provider.ProtocolChatCompletions:
		suffix = "/v1/chat/completions"
		body = map[string]any{
			"model":    target.NativeModel,
			"messages": messageList(history, chatCompletionsContent), // 骨架同为 role+content，带图 part 形态不同
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
	return suffix, body, nil
}

// applyEffort 把对话页选定的推理档写进请求体,与转发面 reasoning_level
// 数字档共用同一份映射逻辑:模型配了映射脚本(effort_script)即由脚本
// 接管写入位置,否则走协议内置映射(effort.Apply:responses=
// reasoning.effort、chat_completions=reasoning_effort、anthropic=
// output_config.effort 或 none 时 thinking disabled、gemini=
// thinkingConfig.thinkingLevel)。
func applyEffort(target resolve.ResolvedTarget, body map[string]any, value string) error {
	if target.EffortScript == "" {
		effort.Apply(target.Protocol, body, value)
		return nil
	}
	// 对话页按值选档,反查档号喂脚本 ctx.level(声明按值唯一)。
	level := ""
	for _, e := range target.Efforts {
		if e.Value == value {
			level = e.Name
			break
		}
	}
	out, err := effort.RunScript(target.EffortScript, effort.ScriptContext{
		Level:    level,
		Value:    value,
		Protocol: target.Protocol,
		Efforts:  target.Efforts,
		Request:  body,
	})
	if err != nil {
		return err
	}
	for k := range body {
		delete(body, k)
	}
	for k, v := range out {
		body[k] = v
	}
	return nil
}

// messageList 生成 [{role, content}] 形态；content 由 perMessage 决定。
// user/assistant 交替由对话本身保证（发送一轮即追加两条），不做额外规整。
func messageList(history []Message, perMessage func(Message) any) []map[string]any {
	out := make([]map[string]any, 0, len(history))
	for _, m := range history {
		out = append(out, map[string]any{"role": m.Role, "content": perMessage(m)})
	}
	return out
}

// 各协议的 per-message content 构造共享同一条基线：
// 无附件严格保持字符串形态（现有行为即回归基线）；有附件时产出 part 数组，
// 图片在前、文本在后，文本为空则不产出文本 part。助手消息不携带附件，
// 天然不走 part 路径。
func anthropicContent(m Message) any {
	if len(m.Attachments) == 0 {
		return m.Content
	}
	parts := make([]map[string]any, 0, len(m.Attachments)+1)
	for _, img := range m.Attachments {
		parts = append(parts, map[string]any{
			"type": "image",
			"source": map[string]any{
				"type":       "base64",
				"media_type": img.Mime,
				"data":       img.Data,
			},
		})
	}
	if m.Content != "" {
		parts = append(parts, map[string]any{"type": "text", "text": m.Content})
	}
	return parts
}

func chatCompletionsContent(m Message) any {
	if len(m.Attachments) == 0 {
		return m.Content
	}
	parts := make([]map[string]any, 0, len(m.Attachments)+1)
	for _, img := range m.Attachments {
		parts = append(parts, map[string]any{
			"type":      "image_url",
			"image_url": map[string]any{"url": dataURL(img)},
		})
	}
	if m.Content != "" {
		parts = append(parts, map[string]any{"type": "text", "text": m.Content})
	}
	return parts
}

// dataURL 把内嵌图片编成 data URL（OpenAI 两族的图片引用形态）。
func dataURL(img ImageAttachment) string {
	return "data:" + img.Mime + ";base64," + img.Data
}

func responsesInput(history []Message) []map[string]any {
	out := make([]map[string]any, 0, len(history))
	for _, m := range history {
		out = append(out, map[string]any{
			"type":    "message",
			"role":    m.Role,
			"content": responsesContent(m),
		})
	}
	return out
}

func responsesContent(m Message) any {
	if len(m.Attachments) == 0 {
		return m.Content
	}
	parts := make([]map[string]any, 0, len(m.Attachments)+1)
	for _, img := range m.Attachments {
		parts = append(parts, map[string]any{
			"type":      "input_image",
			"image_url": dataURL(img),
		})
	}
	if m.Content != "" {
		parts = append(parts, map[string]any{"type": "input_text", "text": m.Content})
	}
	return parts
}

// geminiContents 的角色名是 user/model，assistant 需改写。
// 无附件保持单 text part 的现有形态；有附件时 inline_data 在前、文本在后。
func geminiContents(history []Message) []map[string]any {
	out := make([]map[string]any, 0, len(history))
	for _, m := range history {
		role := "user"
		if m.Role == RoleAssistant {
			role = "model"
		}
		parts := make([]map[string]any, 0, len(m.Attachments)+1)
		for _, img := range m.Attachments {
			parts = append(parts, map[string]any{
				"inline_data": map[string]any{"mime_type": img.Mime, "data": img.Data},
			})
		}
		if m.Content != "" || len(parts) == 0 {
			parts = append(parts, map[string]any{"text": m.Content})
		}
		out = append(out, map[string]any{
			"role":  role,
			"parts": parts,
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

// isEventStream 判定响应是否 SSE。codex 强制 stream=true，响应恒走这条。
func isEventStream(h http.Header) bool {
	return strings.Contains(strings.ToLower(h.Get("Content-Type")), "text/event-stream")
}

// extractReplySSE 把 responses 协议的 SSE 流聚合回整轮：回复文本优先取
// response.completed 里的完整响应（与非流式同一条 extractReply 路径），
// 没有 completed 时回落 output_text.delta 拼接；用量同样取自 completed。
func extractReplySSE(protocol string, raw []byte) (string, usage.Usage, error) {
	if protocol != provider.ProtocolResponses {
		return "", usage.Usage{}, apperr.New(apperr.UpstreamUnavailable,
			fmt.Sprintf("protocol %q returned an unexpected event stream", protocol))
	}
	var deltas strings.Builder
	var completed map[string]any
	for _, ev := range sseDataEvents(raw) {
		switch ev["type"] {
		case "response.output_text.delta":
			if d, _ := ev["delta"].(string); d != "" {
				deltas.WriteString(d)
			}
		case "response.completed":
			if resp, ok := ev["response"].(map[string]any); ok {
				completed = resp
			}
		}
	}
	if completed == nil {
		if deltas.Len() == 0 {
			return "", usage.Usage{}, apperr.New(apperr.UpstreamUnavailable,
				"上游事件流中没有 response.completed 事件："+snippet(raw))
		}
		return deltas.String(), usage.Usage{}, nil
	}
	enc, err := json.Marshal(completed)
	if err != nil {
		return "", usage.Usage{}, apperr.Wrap(apperr.InvalidJSON, "encode completed response", err)
	}
	u, _ := usage.FromResponse(protocol, enc)
	reply, err := extractReply(protocol, enc)
	if err != nil {
		return "", u, err
	}
	if reply == "" {
		reply = deltas.String()
	}
	return reply, u, nil
}

// sseDataEvents 抽出事件流里所有 data: 行的 JSON 对象；[DONE] 与
// 解析失败的行跳过（统计/回复只认完整事件，残行没有可用信息）。
func sseDataEvents(raw []byte) []map[string]any {
	var out []map[string]any
	for _, line := range strings.Split(string(raw), "\n") {
		data, ok := strings.CutPrefix(strings.TrimSpace(line), "data:")
		if !ok {
			continue
		}
		data = strings.TrimSpace(data)
		if data == "" || data == "[DONE]" {
			continue
		}
		var obj map[string]any
		if json.Unmarshal([]byte(data), &obj) == nil {
			out = append(out, obj)
		}
	}
	return out
}

// randomUUID 生成 v4 形态 UUID，供 x-client-request-id 使用（转发面同款）。
func randomUUID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return ""
	}
	b[6] = b[6]&0x0f | 0x40
	b[8] = b[8]&0x3f | 0x80
	return fmt.Sprintf("%s-%s-%s-%s-%s",
		hex.EncodeToString(b[0:4]), hex.EncodeToString(b[4:6]),
		hex.EncodeToString(b[6:8]), hex.EncodeToString(b[8:10]),
		hex.EncodeToString(b[10:16]))
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
