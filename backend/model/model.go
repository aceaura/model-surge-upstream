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
	Enabled       bool            `json:"enabled"`
	CreatedAt     time.Time       `json:"created_at"`
	UpdatedAt     time.Time       `json:"updated_at"`
}
