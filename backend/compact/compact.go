// Package compact 实现请求上下文超限时的压缩策略。转发前估算输入
// token，超过模型声明的 context_window × 阈值时按模型配置的压缩模式
// 处置：auto 向上游发一次非流式摘要调用，把早期历史压成摘要、保留
// 近期轮次原样；error 让调用方回协议原生的 400 让客户端自行压缩；
// passive 只记录不生效（context_window 纯作元数据）。
//
// 全程 fail-open：估算未超、未声明窗口、切不出安全点、摘要调用失败、
// 响应解析失败——一律原样转发，压缩是优化不是前提，不给转发链路
// 引入新的故障模式。Gemini 协议二期再覆盖（implFor 返回 nil）。
package compact

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/aceaura/model-surge-upstream/backend/codex"
	"github.com/aceaura/model-surge-upstream/backend/provider"
	"github.com/aceaura/model-surge-upstream/backend/resolve"
	"github.com/aceaura/model-surge-upstream/backend/usage"
)

// Mode 是模型的上下文压缩模式。
type Mode string

const (
	// ModePassive 被动元数据：只记录不生效，转发链路零行为。
	ModePassive Mode = "passive"
	// ModeError 超限时回协议原生 400，由客户端（如 Claude Code）自行压缩。
	ModeError Mode = "error"
	// ModeAuto 超限时网关自动向上游发摘要调用压缩历史。
	ModeAuto Mode = "auto"
)

// Defaults 是压缩的全局默认（环境变量注入），模型级 compact JSON
// 可逐项覆盖。见 config 包的 MSU_COMPACT_* 环境变量。
type Defaults struct {
	Mode             Mode
	Threshold        float64       // 触发阈值，占 context_window 的比例
	KeepTurns        int           // 压缩后保留的最近真人发言轮数
	MaxSummaryTokens int           // 摘要请求的 max_tokens
	Timeout          time.Duration // 摘要调用超时
}

// DefaultConfig 给出全部全局默认值：模式默认 passive（不启用行为），
// 阈值 0.85 给估算误差留缓冲。
func DefaultConfig() Defaults {
	return Defaults{
		Mode:             ModePassive,
		Threshold:        0.85,
		KeepTurns:        6,
		MaxSummaryTokens: 2048,
		Timeout:          90 * time.Second,
	}
}

// config 是模型级 JSON 压平到全局默认后的有效配置。
type config struct {
	Defaults
}

// effective 合并模型级 compact JSON 与全局默认：JSON 里出现的键覆盖
// 默认，缺项用默认。raw 为空或 {} 时整体用默认。
func effective(raw json.RawMessage, g Defaults) config {
	out := config{Defaults: g}
	if len(raw) == 0 {
		return out
	}
	var probe struct {
		Mode             Mode     `json:"mode"`
		Threshold        *float64 `json:"threshold"`
		KeepTurns        *int     `json:"keep_turns"`
		MaxSummaryTokens *int     `json:"max_summary_tokens"`
	}
	if err := json.Unmarshal(raw, &probe); err != nil {
		return out // 模型入库时已校验，这里防御性回退默认
	}
	if probe.Mode != "" {
		out.Mode = probe.Mode
	}
	if probe.Threshold != nil {
		out.Threshold = *probe.Threshold
	}
	if probe.KeepTurns != nil {
		out.KeepTurns = *probe.KeepTurns
	}
	if probe.MaxSummaryTokens != nil {
		out.MaxSummaryTokens = *probe.MaxSummaryTokens
	}
	return out
}

// protocolImpl 按协议族实现压缩的结构差异：消息切割、摘要请求构造、
// 响应提取、拼回。触发判定与失败回退是公共的，在 Run 里编排。
type protocolImpl interface {
	// Estimate 估算整个请求体的输入 token。
	Estimate(body map[string]any) int
	// Messages 取请求体里的消息/item 列表（Anthropic/Chat 是 messages，
	// Responses 是 input）。取不到时返回 nil，Cut 失败走 fail-open。
	Messages(body map[string]any) []any
	// Cut 找安全切点：prefix 进摘要，tail 原样保留。ok=false 表示
	// 没有可切的安全点（如整条会话就是一个工具链）。
	Cut(messages []any, keepTurns int) (prefix, tail []any, ok bool)
	// BuildSummaryRequest 构造发往上游的非流式摘要请求。
	BuildSummaryRequest(nativeModel string, body map[string]any, prefix []any, maxTokens int) (suffix string, reqBody map[string]any, err error)
	// ExtractSummary 从摘要响应抽出纯文本与用量。
	ExtractSummary(respBody []byte) (text string, u usage.Usage, err error)
	// Splice 把摘要与 tail 拼回请求体。
	Splice(body map[string]any, summary string, tail []any) map[string]any
}

func implFor(protocol string) protocolImpl {
	switch protocol {
	case provider.ProtocolAnthropic:
		return anthropicImpl{}
	case provider.ProtocolChatCompletions:
		return chatImpl{}
	case provider.ProtocolResponses:
		return responsesImpl{}
	default:
		// Gemini 二期覆盖。
		return nil
	}
}

// UsageRecorder 记录一次摘要调用的用量。实现方自己保证不阻塞。
type UsageRecorder func(u usage.Usage, status int, latency, duration time.Duration)

// summaryBodyLimit 是摘要响应的读取上限。摘要 max_tokens 默认 2048，
// 正常回包几十 KB，限制只为防异常上游。
const summaryBodyLimit = 8 << 20

// Runner 编排一次压缩判定与执行。零值不可用，用 NewRunner 构造。
type Runner struct {
	globals Defaults
	client  *http.Client
	// logf 记录回退原因（fail-open 路径必须可查），默认标准 log。
	logf func(format string, args ...any)
}

func NewRunner(g Defaults) *Runner {
	return &Runner{
		globals: g,
		// 不设整体超时：摘要调用时限由 Run 里的 context.WithTimeout 控制。
		client: &http.Client{Transport: http.DefaultTransport.(*http.Transport).Clone()},
		logf:   log.Printf,
	}
}

// Run 在请求体定稿后（model 重写、defaults/overrides 合并之后）执行
// 压缩策略。estimated 是请求体的估算输入 token（未估算时为 0）。
// reject=true 表示 error 模式判定超限，调用方应回协议原生 400 且不要
// 转发。其余情况 reject=false：压缩成功时返回压缩后的请求体，任何
// 失败回退时原样返回。
func (r *Runner) Run(ctx context.Context, target resolve.ResolvedTarget, body map[string]any, record UsageRecorder) (map[string]any, int, bool) {
	cfg := effective(target.Compact, r.globals)
	if cfg.Mode == ModePassive || target.ContextWindow <= 0 {
		return body, 0, false
	}
	impl := implFor(target.Protocol)
	if impl == nil {
		return body, 0, false
	}
	estimated := impl.Estimate(body)
	if float64(estimated) <= float64(target.ContextWindow)*cfg.Threshold {
		return body, estimated, false
	}

	if cfg.Mode == ModeError {
		r.logf("compact: model=%s estimated %d tokens > %.0f%% of context_window=%d, rejecting for client-side compact",
			target.ModelID, estimated, cfg.Threshold*100, target.ContextWindow)
		return nil, estimated, true
	}

	messages := impl.Messages(body)
	prefix, tail, ok := impl.Cut(messages, cfg.KeepTurns)
	if !ok {
		r.logf("compact: model=%s estimated %d tokens exceeds window but no safe cut point, forwarding as-is",
			target.ModelID, estimated)
		return body, estimated, false
	}

	suffix, reqBody, err := impl.BuildSummaryRequest(target.NativeModel, body, prefix, cfg.MaxSummaryTokens)
	if err != nil {
		r.logf("compact: model=%s build summary request failed: %v, forwarding as-is", target.ModelID, err)
		return body, estimated, false
	}
	// codex 订阅端点的硬约束(store/stream/剥采样参数/instructions)与
	// 路径映射同样适用于摘要调用:与转发面同一套 ShapeBody/MapSuffix。
	// stream 被强制为 true,ExtractSummary 按 SSE 解析。
	if target.ProviderID == codex.ProviderID {
		reqBody = codex.ShapeBody(reqBody, target.NativeModel)
		suffix = codex.MapSuffix(suffix)
	}

	// 摘要调用挂在请求上下文上：客户端断开则摘要一并取消，不泄漏连接。
	ctx, cancel := context.WithTimeout(ctx, cfg.Timeout)
	defer cancel()
	start := time.Now()
	raw, status, err := r.callSummary(ctx, target, suffix, reqBody)
	latency := time.Since(start)
	if err != nil {
		r.logf("compact: model=%s summary call failed: %v, forwarding as-is", target.ModelID, err)
		return body, estimated, false
	}
	if status < 200 || status >= 300 {
		r.logf("compact: model=%s summary call HTTP %d: %s, forwarding as-is",
			target.ModelID, status, snippet(raw))
		return body, estimated, false
	}
	summary, u, err := impl.ExtractSummary(raw)
	if err != nil {
		r.logf("compact: model=%s extract summary failed: %v, forwarding as-is", target.ModelID, err)
		return body, estimated, false
	}
	if record != nil {
		record(u, status, latency, latency)
	}
	r.logf("compact: model=%s compacted %d→%d messages (estimated %d tokens, window %d)",
		target.ModelID, len(messages), len(tail)+2, estimated, target.ContextWindow)
	return impl.Splice(body, summary, tail), estimated, false
}

// callSummary 向上游发摘要请求。范式同 modelcheck.Check：拼
// BaseURL+suffix、写 target.Headers（含认证），但时限更长——
// 压缩大上下文远慢于连通性探测。
func (r *Runner) callSummary(ctx context.Context, target resolve.ResolvedTarget, suffix string, body map[string]any) ([]byte, int, error) {
	encoded, err := json.Marshal(body)
	if err != nil {
		return nil, 0, err
	}
	url := strings.TrimRight(target.BaseURL, "/") + suffix
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(encoded))
	if err != nil {
		return nil, 0, err
	}
	req.Header.Set("Content-Type", "application/json")
	for k, v := range target.Headers {
		req.Header.Set(k, v)
	}
	resp, err := r.client.Do(req)
	if err != nil {
		return nil, 0, err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, summaryBodyLimit))
	return raw, resp.StatusCode, err
}

func snippet(raw []byte) string {
	s := strings.TrimSpace(string(raw))
	if r := []rune(s); len(r) > 200 {
		s = string(r[:200]) + "…"
	}
	return s
}
