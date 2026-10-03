package codex

import (
	_ "embed"
	"strings"
)

// 内嵌 codex CLI 官方 base instructions(文件取自 sub2api,与 codex-rs
// core/prompt.md 族同源)。订阅端点要求 instructions 非空,客户端没给时
// 注入官方版本,行为对齐 codex CLI 本体。
//
//go:embed prompts/gpt5_codex.txt
var gpt5Codex string

//go:embed prompts/gpt5_1.txt
var gpt51 string

//go:embed prompts/gpt5_2.txt
var gpt52 string

// InstructionsForModel 按 native 模型挑官方 prompt:
// 含 codex 的模型(gpt-5-codex/codex-max/spark 等)用 GPT-5-Codex prompt;
// gpt-5.2/5.1 系非 codex 模型各用各的;未知模型回退 GPT-5-Codex,
// 保证返回值恒非空。
func InstructionsForModel(model string) string {
	m := strings.ToLower(model)
	switch {
	case strings.Contains(m, "codex"):
		return gpt5Codex
	case strings.HasPrefix(m, "gpt-5.2"):
		return gpt52
	case strings.HasPrefix(m, "gpt-5.1"):
		return gpt51
	default:
		return gpt5Codex
	}
}
