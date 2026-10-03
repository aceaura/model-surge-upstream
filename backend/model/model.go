// Package model 是模型仓储。模型归属账号，但本包不引用 account 包——
// 账号存在性与协议合法性由调用方传入的 Resolver 提供，依赖方向保持自上而下。
package model

import (
	"encoding/json"
	"time"
)

type Model struct {
	ID          string `json:"id"`
	Account     string `json:"account"`
	NativeModel string `json:"native_model"`
	Protocol    string `json:"protocol"`
	// ContextWindow 为 0 表示未声明。
	ContextWindow int             `json:"context_window"`
	Defaults      json.RawMessage `json:"defaults"`
	Overrides     json.RawMessage `json:"overrides"`
	// Compact 上下文压缩配置：{"mode":"passive|error|auto", "threshold",
	// "keep_turns", "max_summary_tokens"}，缺项回落到全局 env 默认。
	Compact json.RawMessage `json:"compact"`
	// Efforts 推理档支持列表的原始配置：null=自动（按协议+模型名规则
	// 推导），数组=管理员显式声明（空数组即不支持）。
	Efforts json.RawMessage `json:"efforts"`
	// EffortsEffective 是算好的有效支持列表（不落库），对话页按它渲染
	// 档位选择器，发送侧按它校验所选档位。
	EffortsEffective []string `json:"efforts_effective"`
	Enabled          bool     `json:"enabled"`
	CreatedAt     time.Time       `json:"created_at"`
	UpdatedAt     time.Time       `json:"updated_at"`
}
