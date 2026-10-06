// Package codex 收口 ChatGPT 订阅(openai/codex provider)的 backend-api
// 特殊策略:认证与身份头、请求体硬约束、路径映射、官方 instructions 内嵌。
// 口径取 codex CLI 官方实现与 sub2api/new-api/cc-switch 三家生产网关的交集;
// 这些约束是上游服务端强制(缺了 400/404),不是风格选择。
package codex

import (
	"crypto/sha256"
	"fmt"
	"strings"

	"github.com/aceaura/model-surge-upstream/backend/oauth"
)

// ProviderID 是本策略作用的内置 provider。
const ProviderID = "openai/codex"

// droppedFields 是 backend-api 不接受的采样参数:带了直接 400,
// 转发前剥掉(new-api/sub2api 实测清单)。
var droppedFields = []string{
	"max_output_tokens", "temperature", "top_p",
	"frequency_penalty", "presence_penalty",
}

// Headers 生成 backend-api 认证与身份头。accessToken 由 oauth.Manager
// 续期产出;session_id 依赖请求体,不在这里,由转发面补。
//
// originator/version 必须成对且 version 不能太旧:上游按这对值路由模型
// 队列,过旧版本请求新模型直接 404(sub2api 实测下限 0.144.0)。
//
// sec-fetch-*/priority 是过 Cloudflare 的硬条件:chatgpt.com 对 backend-api
// 开了挑战,裸 Go 请求恒 403 挑战页,带这组浏览器头才放行(sub2api
// buildCodexCommonHeaders 同款,2026-10-03 额度脚本实测验证)。
func Headers(accessToken, accountID string) map[string]string {
	return map[string]string{
		"Authorization":      "Bearer " + accessToken,
		"chatgpt-account-id": accountID,
		"originator":         oauth.Originator,
		"version":            oauth.ClientVersion,
		"User-Agent":         oauth.Originator + "/" + oauth.ClientVersion,
		"OpenAI-Beta":        "responses=experimental",
		"Accept":             "text/event-stream",
		"oai-language":       "zh-CN",
		"sec-fetch-site":     "none",
		"sec-fetch-mode":     "no-cors",
		"sec-fetch-dest":     "empty",
		"priority":           "u=4, i",
	}
}

// ShapeBody 应用 codex 请求体硬约束。必须在 defaults/overrides 合并之后
// 调用:这些约束压过一切可调参数。body 就地改写并返回。
//
//   - store=false、stream=true 是订阅端点的强制契约;
//   - 剥掉上游不认的采样参数;
//   - input 字符串规范化为单条 user 消息列表(Responses 规范允许字符串,
//     订阅端点只收列表,裸字符串恒 400 "Input must be a list");
//   - input 里的 system 消息:订阅端点拒收 role=system(恒 400
//     "System messages are not allowed"),把文本镜像进 instructions
//     并将该条改写为 developer(sub2api extractSystemMessagesFromInput
//     同款,Qoder 等 BYOK 客户端常把系统提示放在 input 里);
//   - 带 reasoning 时补 include: reasoning.encrypted_content(否则推理
//     内容不回传,多轮上下文断链);
//   - instructions 空缺时注入 codex 官方 prompt(上游要求非空)。
func ShapeBody(body map[string]any, nativeModel string) map[string]any {
	body["store"] = false
	body["stream"] = true
	for _, f := range droppedFields {
		delete(body, f)
	}
	if s, ok := body["input"].(string); ok {
		body["input"] = []any{map[string]any{
			"type": "message",
			"role": "user",
			"content": []any{
				map[string]any{"type": "input_text", "text": s},
			},
		}}
	}
	hoistSystemMessages(body)
	if _, ok := body["reasoning"]; ok {
		body["include"] = appendStringSet(body["include"], "reasoning.encrypted_content")
	}
	if s, _ := body["instructions"].(string); strings.TrimSpace(s) == "" {
		body["instructions"] = InstructionsForModel(nativeModel)
	}
	return body
}

// hoistSystemMessages 把 input 里 role=system 的条目改写为 developer,
// 文本按序拼接后前置进 instructions(已有非空 instructions 时垫底)。
func hoistSystemMessages(body map[string]any) {
	input, ok := body["input"].([]any)
	if !ok {
		return
	}
	var texts []string
	for _, item := range input {
		m, ok := item.(map[string]any)
		if !ok || m["role"] != "system" {
			continue
		}
		if t := contentText(m["content"]); t != "" {
			texts = append(texts, t)
		}
		m["role"] = "developer"
	}
	if len(texts) == 0 {
		return
	}
	joined := strings.Join(texts, "\n\n")
	if existing, _ := body["instructions"].(string); strings.TrimSpace(existing) != "" {
		body["instructions"] = joined + "\n\n" + existing
	} else {
		body["instructions"] = joined
	}
}

// contentText 提取消息内容文本:字符串直取,分段列表拼接 text/
// input_text/output_text 段的 text,其余形态返回空。
func contentText(content any) string {
	switch v := content.(type) {
	case string:
		return v
	case []any:
		var b strings.Builder
		for _, part := range v {
			m, ok := part.(map[string]any)
			if !ok {
				continue
			}
			switch m["type"] {
			case "text", "input_text", "output_text":
				if t, ok := m["text"].(string); ok {
					b.WriteString(t)
				}
			}
		}
		return b.String()
	}
	return ""
}

func appendStringSet(v any, s string) []any {
	out, _ := v.([]any)
	for _, item := range out {
		if item == s {
			return out
		}
	}
	return append(out, s)
}

// MapSuffix 把客户端的 OpenAI 标准路径映射到 backend-api 真实路径:
// provider BaseURL 已含 /backend-api/codex,/v1/responses 落 /responses。
func MapSuffix(suffix string) string {
	if suffix == "/v1/responses" {
		return "/responses"
	}
	return suffix
}

// ModelsURL 返回模型清单端点地址。client_version 查询参数是硬条件:
// 缺了上游恒 400(sub2api buildCodexModelsManifestURL 同款协商语义)。
// 响应按模型携带 supported_reasoning_levels,是推理档动态适配的数据源。
func ModelsURL(baseURL string) string {
	return strings.TrimRight(baseURL, "/") + "/models?client_version=" + oauth.ClientVersion
}

// SessionID 由账号与 prompt_cache_key 派生稳定 UUID:同账号同会话恒定,
// 跨账号/跨会话隔离(sub2api 同款隔离思路,防客户端自带 session 头串号)。
// 客户端传来的 session_id/conversation_id 一律不用,转发面先删后写。
func SessionID(account, promptCacheKey string) string {
	sum := sha256.Sum256([]byte("msu-codex\x00" + account + "\x00" + promptCacheKey))
	return fmt.Sprintf("%x-%x-%x-%x-%x", sum[0:4], sum[4:6], sum[6:8], sum[8:10], sum[10:16])
}
