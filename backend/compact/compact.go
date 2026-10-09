// Package compact 实现请求上下文超限时的拦截策略。转发前估算输入
// token，超过模型声明的 context_window × 阈值时按模型配置的压缩模式
// 处置：error 回协议原生的 400 让客户端（harness）自行压缩；passive
// 只记录不生效（context_window 纯作元数据），超限请求原样转发、由
// 上游自己报错。
//
// 网关不拥有会话历史：harness 每轮重发全量转录，网关的改写只作用于
// 单个请求、无法被客户端采纳，故不做代压——压缩由持有转录的一方持久
// 地完成，网关只用上游的错误方言充当触发器。估算未超或未声明窗口时
// 零行为。
package compact

import (
	"encoding/json"
	"log"

	"github.com/aceaura/model-surge-upstream/backend/resolve"
)

// Mode 是模型的上下文压缩模式。
type Mode string

const (
	// ModePassive 被动元数据：只记录不生效，转发链路零行为。
	ModePassive Mode = "passive"
	// ModeError 超限时回协议原生 400，由客户端（如 Claude Code）自行压缩。
	ModeError Mode = "error"
)

// Defaults 是压缩的全局默认（环境变量注入），模型级 compact JSON
// 可逐项覆盖。见 config 包的 MSU_COMPACT_* 环境变量。
type Defaults struct {
	Mode      Mode
	Threshold float64 // 触发阈值，占 context_window 的比例
}

// DefaultConfig 给出全部全局默认值：模式默认 passive（不启用行为），
// 阈值 0.85 给估算误差留缓冲。
func DefaultConfig() Defaults {
	return Defaults{
		Mode:      ModePassive,
		Threshold: 0.85,
	}
}

// config 是模型级 JSON 压平到全局默认后的有效配置。
type config struct {
	Defaults
}

// effective 合并模型级 compact JSON 与全局默认：JSON 里出现的键覆盖
// 默认，缺项用默认。raw 为空或 {} 时整体用默认。存量 auto（网关代压，
// 已废）归一为 error：保留「超限要主动处置」的配置意图。
func effective(raw json.RawMessage, g Defaults) config {
	out := config{Defaults: g}
	if len(raw) == 0 {
		return out
	}
	var probe struct {
		Mode      Mode     `json:"mode"`
		Threshold *float64 `json:"threshold"`
	}
	if err := json.Unmarshal(raw, &probe); err != nil {
		return out // 模型入库时已校验，这里防御性回退默认
	}
	switch probe.Mode {
	case ModePassive, ModeError:
		out.Mode = probe.Mode
	case Mode("auto"):
		out.Mode = ModeError
	}
	if probe.Threshold != nil {
		out.Threshold = *probe.Threshold
	}
	return out
}

// Runner 编排一次超限判定。零值不可用，用 NewRunner 构造。
type Runner struct {
	globals Defaults
	// logf 记录拦截原因，默认标准 log。
	logf func(format string, args ...any)
}

func NewRunner(g Defaults) *Runner {
	return &Runner{
		globals: g,
		logf:    log.Printf,
	}
}

// Run 在请求体定稿后（model 重写、defaults/overrides 合并之后）执行
// 超限判定。reject=true 表示 error 模式判定超限，调用方应回协议原生
// 400 且不要转发；其余情况 reject=false。estimated 为请求体的估算输入
// token，仅在 reject 时有意义。
func (r *Runner) Run(target resolve.ResolvedTarget, body map[string]any) (estimated int, reject bool) {
	cfg := effective(target.Compact, r.globals)
	if cfg.Mode != ModeError || target.ContextWindow <= 0 {
		return 0, false
	}
	estimated = estimateBody(body)
	if float64(estimated) <= float64(target.ContextWindow)*cfg.Threshold {
		return estimated, false
	}
	r.logf("compact: model=%s account=%s estimated %d tokens > %.0f%% of context_window=%d, rejecting for client-side compact",
		target.ModelID, target.Account, estimated, cfg.Threshold*100, target.ContextWindow)
	return estimated, true
}
