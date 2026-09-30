// Package usage 从四协议的上游响应里抽取 token 用量，供用量统计落库。
//
// 只做抽取与归一，不做协议转化、不做计价：统计面关心的是「这一请求上游
// 实际报了多少 token」，响应体本身仍由转发面原样透传给客户端。
//
// 输入 token 有两种上游语义，必须带着语义一起存，聚合时才能归一：
//   - fresh（Anthropic）：input_tokens 已不含缓存读取；
//   - total（OpenAI 两族 / Gemini）：input_tokens 含缓存读取（OpenAI 还含
//     缓存写入对应的部分），展示「新增输入」前要扣掉缓存两桶。
package usage

import (
	"bytes"
	"encoding/json"
	"strings"

	"github.com/aceaura/model-surge-upstream/backend/provider"
)

// Semantics 输入 token 的存储语义。
type Semantics int

const (
	// SemanticsTotal 输入含缓存读取与缓存写入，聚合时需扣减。
	SemanticsTotal Semantics = 1
	// SemanticsFresh 输入已是扣除缓存后的净输入。
	SemanticsFresh Semantics = 2
)

// Usage 一次请求的四桶 token 用量。
type Usage struct {
	InputTokens      int64 `json:"input_tokens"`
	OutputTokens     int64 `json:"output_tokens"`
	CacheReadTokens  int64 `json:"cache_read_tokens"`
	CacheWriteTokens int64 `json:"cache_creation_tokens"`
	Semantics        Semantics
	Model            string `json:"model,omitempty"`
	MessageID        string `json:"-"`
}

// HasBillable 是否产生了任一计费维度的 token。全 0 的空 usage 不落库：
// 流式被截断或上游省略 usage 时宁可少记一行，也不虚增请求数。
func (u Usage) HasBillable() bool {
	return u.InputTokens > 0 || u.OutputTokens > 0 ||
		u.CacheReadTokens > 0 || u.CacheWriteTokens > 0
}

// FreshInput 归一后的净输入：total 语义扣掉缓存两桶，fresh 原样返回。
func (u Usage) FreshInput() int64 {
	if u.Semantics != SemanticsTotal {
		return u.InputTokens
	}
	fresh := u.InputTokens - u.CacheReadTokens - u.CacheWriteTokens
	if fresh < 0 {
		return 0
	}
	return fresh
}

// RealTotal 真实消耗 token：净输入 + 输出 + 缓存写入 + 缓存读取。
// 与 CC Switch 口径一致：缓存读取虽打折计费，仍是真实过模型的 token。
func (u Usage) RealTotal() int64 {
	return u.FreshInput() + u.OutputTokens + u.CacheWriteTokens + u.CacheReadTokens
}

// HitRate 缓存命中率：读取 /（净输入 + 写入 + 读取）。分母为 0 返回 0。
func (u Usage) HitRate() float64 {
	den := u.FreshInput() + u.CacheWriteTokens + u.CacheReadTokens
	if den <= 0 {
		return 0
	}
	return float64(u.CacheReadTokens) / float64(den)
}

// num 从对象里取整数字段；缺失或非数返回 0。
func num(obj map[string]any, key string) int64 {
	v, ok := obj[key]
	if !ok || v == nil {
		return 0
	}
	switch n := v.(type) {
	case float64:
		return int64(n)
	case json.Number:
		i, _ := n.Int64()
		return i
	}
	return 0
}

// has 字段是否存在且非 null。
func has(obj map[string]any, key string) bool {
	v, ok := obj[key]
	return ok && v != nil
}

func str(obj map[string]any, key string) string {
	s, _ := obj[key].(string)
	return s
}

func sub(obj map[string]any, key string) map[string]any {
	m, _ := obj[key].(map[string]any)
	return m
}

// FromResponse 按协议解析非流式响应体。解析不出用量返回 ok=false。
func FromResponse(protocol string, body []byte) (Usage, bool) {
	var obj map[string]any
	if err := json.Unmarshal(body, &obj); err != nil {
		return Usage{}, false
	}
	switch protocol {
	case provider.ProtocolAnthropic:
		return fromAnthropicObject(obj)
	case provider.ProtocolChatCompletions:
		return fromOpenAIObject(obj)
	case provider.ProtocolResponses:
		return fromResponsesObject(obj)
	case provider.ProtocolGemini:
		return fromGeminiObject(obj)
	}
	return Usage{}, false
}

// fromAnthropicObject：usage.input_tokens 已是净输入。
func fromAnthropicObject(obj map[string]any) (Usage, bool) {
	u := sub(obj, "usage")
	if u == nil || !has(u, "input_tokens") || !has(u, "output_tokens") {
		return Usage{}, false
	}
	return Usage{
		InputTokens:      num(u, "input_tokens"),
		OutputTokens:     num(u, "output_tokens"),
		CacheReadTokens:  num(u, "cache_read_input_tokens"),
		CacheWriteTokens: num(u, "cache_creation_input_tokens"),
		Semantics:        SemanticsFresh,
		Model:            str(obj, "model"),
		MessageID:        str(obj, "id"),
	}, true
}

// fromOpenAIObject：chat/completions 形态，usage.prompt_tokens 含缓存。
func fromOpenAIObject(obj map[string]any) (Usage, bool) {
	u := sub(obj, "usage")
	if u == nil || !has(u, "prompt_tokens") || !has(u, "completion_tokens") {
		return Usage{}, false
	}
	return Usage{
		InputTokens:     num(u, "prompt_tokens"),
		OutputTokens:    num(u, "completion_tokens"),
		CacheReadTokens: openaiCachedTokens(u),
		Semantics:       SemanticsTotal,
		Model:           str(obj, "model"),
		MessageID:       str(obj, "id"),
	}, true
}

// fromResponsesObject：responses 形态，usage.input_tokens 含缓存。
func fromResponsesObject(obj map[string]any) (Usage, bool) {
	u := sub(obj, "usage")
	if u == nil || !has(u, "input_tokens") || !has(u, "output_tokens") {
		return Usage{}, false
	}
	return Usage{
		InputTokens:     num(u, "input_tokens"),
		OutputTokens:    num(u, "output_tokens"),
		CacheReadTokens: openaiCachedTokens(u),
		Semantics:       SemanticsTotal,
		Model:           str(obj, "model"),
		MessageID:       str(obj, "id"),
	}, true
}

// openaiCachedTokens 取缓存读取数：chat 族在 prompt_tokens_details.cached_tokens，
// responses 族在 input_tokens_details.cached_tokens，两处都认。
func openaiCachedTokens(u map[string]any) int64 {
	for _, key := range []string{"prompt_tokens_details", "input_tokens_details"} {
		if d := sub(u, key); d != nil {
			if c := num(d, "cached_tokens"); c > 0 {
				return c
			}
		}
	}
	return num(u, "cached_tokens")
}

// fromGeminiObject：usageMetadata.promptTokenCount 含缓存读取；
// 输出 = total - prompt（含 thoughts），与 CC Switch 口径一致。
func fromGeminiObject(obj map[string]any) (Usage, bool) {
	m := sub(obj, "usageMetadata")
	if m == nil || !has(m, "promptTokenCount") {
		return Usage{}, false
	}
	prompt := num(m, "promptTokenCount")
	total := num(m, "totalTokenCount")
	out := total - prompt
	if out < 0 {
		out = num(m, "candidatesTokenCount")
	}
	model := str(obj, "modelVersion")
	return Usage{
		InputTokens:     prompt,
		OutputTokens:    out,
		CacheReadTokens: num(m, "cachedContentTokenCount"),
		Semantics:       SemanticsTotal,
		Model:           model,
		MessageID:       str(obj, "responseId"),
	}, true
}

// Accumulator 流式（SSE）用量的增量累加器：逐事件喂入，不保留事件本身。
type Accumulator struct {
	protocol string
	u        Usage
	// anthropic 的 message_delta 可能报修正后的净输入，标记输入来源。
	inputFromDelta bool
	seen           bool
}

func NewAccumulator(protocol string) *Accumulator {
	return &Accumulator{protocol: protocol}
}

// Event 喂入一个 SSE data: 行的 JSON 对象。
func (a *Accumulator) Event(obj map[string]any) {
	switch a.protocol {
	case provider.ProtocolAnthropic:
		a.anthropicEvent(obj)
	case provider.ProtocolChatCompletions:
		a.openaiEvent(obj)
	case provider.ProtocolResponses:
		a.responsesEvent(obj)
	case provider.ProtocolGemini:
		a.geminiEvent(obj)
	}
}

func (a *Accumulator) anthropicEvent(obj map[string]any) {
	switch str(obj, "type") {
	case "message_start":
		msg := sub(obj, "message")
		if msg == nil {
			return
		}
		if a.u.Model == "" {
			a.u.Model = str(msg, "model")
		}
		if a.u.MessageID == "" {
			a.u.MessageID = str(msg, "id")
		}
		if mu := sub(msg, "usage"); mu != nil {
			if has(mu, "input_tokens") {
				a.u.InputTokens = num(mu, "input_tokens")
			}
			a.u.CacheReadTokens = num(mu, "cache_read_input_tokens")
			a.u.CacheWriteTokens = num(mu, "cache_creation_input_tokens")
			a.u.Semantics = SemanticsFresh
			a.seen = true
		}
	case "message_delta":
		du := sub(obj, "usage")
		if du == nil {
			return
		}
		if has(du, "output_tokens") {
			a.u.OutputTokens = num(du, "output_tokens")
		}
		// 部分 Anthropic 兼容上游在 message_delta 报修正后的净输入：
		// 更小的正数即采用，缓存计数若同帧带上则一并采用。
		deltaInput, hasInput := du["input_tokens"]
		if hasInput && deltaInput != nil {
			di := num(du, "input_tokens")
			use := di > 0 && (a.u.InputTokens == 0 || di < a.u.InputTokens ||
				(a.inputFromDelta && di <= a.u.InputTokens))
			if use {
				a.u.InputTokens = di
				a.inputFromDelta = true
				a.u.Semantics = SemanticsFresh
				if has(du, "cache_read_input_tokens") {
					a.u.CacheReadTokens = num(du, "cache_read_input_tokens")
				}
				if has(du, "cache_creation_input_tokens") {
					a.u.CacheWriteTokens = num(du, "cache_creation_input_tokens")
				}
			}
		}
		if a.u.CacheReadTokens == 0 && has(du, "cache_read_input_tokens") {
			a.u.CacheReadTokens = num(du, "cache_read_input_tokens")
		}
		if a.u.CacheWriteTokens == 0 && has(du, "cache_creation_input_tokens") {
			a.u.CacheWriteTokens = num(du, "cache_creation_input_tokens")
		}
		a.seen = true
	}
}

func (a *Accumulator) openaiEvent(obj map[string]any) {
	// 用量只在带 stream_options.include_usage 的末尾 chunk 出现。
	if !has(obj, "usage") {
		if id := str(obj, "id"); id != "" && a.u.MessageID == "" {
			a.u.MessageID = id
		}
		return
	}
	if parsed, ok := fromOpenAIObject(obj); ok {
		if parsed.MessageID == "" {
			parsed.MessageID = a.u.MessageID
		}
		a.u = parsed
		a.seen = true
	}
}

func (a *Accumulator) responsesEvent(obj map[string]any) {
	if str(obj, "type") != "response.completed" {
		return
	}
	if resp := sub(obj, "response"); resp != nil {
		if parsed, ok := fromResponsesObject(resp); ok {
			a.u = parsed
			a.seen = true
		}
	}
}

func (a *Accumulator) geminiEvent(obj map[string]any) {
	// 每个 chunk 都可能带 usageMetadata，取最后一个（累计值）。
	if parsed, ok := fromGeminiObject(obj); ok {
		if parsed.MessageID == "" {
			parsed.MessageID = a.u.MessageID
		}
		a.u = parsed
		a.seen = true
		return
	}
	if id := str(obj, "responseId"); id != "" && a.u.MessageID == "" {
		a.u.MessageID = id
	}
}

// Result 流结束时的用量。无计费 token 返回 ok=false。
func (a *Accumulator) Result() (Usage, bool) {
	if !a.seen || !a.u.HasBillable() {
		return Usage{}, false
	}
	return a.u, true
}

// maxJSONBody 非流式响应体的嗅探上限。聊天补全的响应体远小于此；
// 超限即放弃统计（仍原样透传），不为统计吃内存。
const maxJSONBody = 8 << 20

// maxSSELine 单条 SSE 行的上限，防异常长行撑爆行缓冲。
const maxSSELine = 1 << 20

// Sniffer 旁路嗅探器：转发面把响应字节原样写给客户端的同时喂一份进来，
// 流结束时取出用量。SSE 逐行解析不缓存全量；非流式缓存到上限再整包解析。
type Sniffer struct {
	protocol    string
	sse         bool
	acc         *Accumulator
	body        bytes.Buffer
	line        bytes.Buffer
	lineDropped bool
}

// NewSniffer 按协议与响应 Content-Type 建嗅探器。
func NewSniffer(protocol, contentType string) *Sniffer {
	return &Sniffer{
		protocol: protocol,
		sse:      strings.Contains(strings.ToLower(contentType), "text/event-stream"),
		acc:      NewAccumulator(protocol),
	}
}

// Write 喂入一段响应字节。永不失败：统计是旁路，不能影响透传。
func (s *Sniffer) Write(p []byte) {
	if s.sse {
		s.writeSSE(p)
		return
	}
	if s.body.Len() < maxJSONBody {
		rest := maxJSONBody - s.body.Len()
		if len(p) > rest {
			p = p[:rest]
		}
		s.body.Write(p)
	}
}

func (s *Sniffer) writeSSE(p []byte) {
	s.line.Write(p)
	for {
		idx := bytes.IndexByte(s.line.Bytes(), '\n')
		if idx < 0 {
			break
		}
		raw := s.line.Next(idx + 1)
		s.consumeLine(bytes.TrimRight(raw, "\r\n"))
	}
	// 行缓冲超上限：丢弃已积内容，避免异常流撑爆内存。
	if s.line.Len() > maxSSELine {
		s.line.Reset()
		s.lineDropped = true
	}
}

func (s *Sniffer) consumeLine(line []byte) {
	trimmed := bytes.TrimSpace(line)
	if !bytes.HasPrefix(trimmed, []byte("data:")) {
		return
	}
	payload := bytes.TrimSpace(trimmed[len("data:"):])
	if len(payload) == 0 || bytes.Equal(payload, []byte("[DONE]")) {
		return
	}
	var obj map[string]any
	if err := json.Unmarshal(payload, &obj); err != nil || obj == nil {
		return
	}
	s.acc.Event(obj)
}

// Result 流结束取用量。
func (s *Sniffer) Result() (Usage, bool) {
	if s.sse {
		// 末尾可能还有未换行的最后一行。
		if s.line.Len() > 0 && !s.lineDropped {
			s.consumeLine(bytes.TrimSpace(s.line.Bytes()))
			s.line.Reset()
		}
		return s.acc.Result()
	}
	return FromResponse(s.protocol, s.body.Bytes())
}
