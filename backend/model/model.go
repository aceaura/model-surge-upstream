// Package model 是模型仓储。模型归属账号，但本包不引用 account 包——
// 账号存在性与协议合法性由调用方传入的 Resolver 提供，依赖方向保持自上而下。
package model

import (
	"encoding/json"
	"time"

	"github.com/aceaura/model-surge-upstream/backend/effort"
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
	// Efforts 推理档支持列表的原始配置：null=自动（跟随上游 /models 声明的
	// supported_reasoning_levels），数组=管理员显式声明的 [{name,value}]
	// 条目（空数组即不支持；value 是发上游的档位字符串，不限固定词表）。
	Efforts json.RawMessage `json:"efforts"`
	// EffortFormat effort 写入格式(effort.FormatXxx 枚举):空=协议内置
	// 映射(按出站协议选字段);非空=显式格式压过协议外形,承接
	// 「协议外壳+自家字段」的厂商差异。
	EffortFormat string `json:"effort_format"`
	// Rectifier 整流器配置:{"enabled", "retries", "interval_seconds"}。
	// 目前只对 kiro 提供商生效:上游 200 但通篇无正文、以拒答
	// (refusal/content_filter)收尾时,转发面自动重发同一请求。
	// 空对象=关闭。
	Rectifier json.RawMessage `json:"rectifier"`
	// EffortsEffective 是算好的有效支持列表（不落库；仓储读出为 nil，
	// 由能访问上游的 httpapi/resolve 层现算填充），对话页按它渲染
	// 档位选择器（name 显示、value 上行），发送侧按 value 校验所选档位。
	EffortsEffective []effort.Entry `json:"efforts_effective"`
	Enabled          bool           `json:"enabled"`
	// SortOrder 模型页拖拽排序序号,小者在前;并列回落 id 序。不落视图:
	// 列表数组顺序即顺序。
	SortOrder int       `json:"-"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}
