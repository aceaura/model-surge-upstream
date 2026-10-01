package compact

// token 估算：不引入 tokenizer 依赖（tiktoken 词表运行时需外网下载，
// 且只覆盖 OpenAI 系；Anthropic/Gemini 无公开 Go 词表）。分段启发式：
// ASCII 约 4 字符 1 token，非 ASCII（CJK 等）约 1 字符 1 token。
// 触发判定不需要精确值——阈值（默认 0.85）留的缓冲吸收估算误差：
// 估高了多压一次（损失一点缓存命中），估低了上游自己报错（与现状一致）。

// messageOverhead 是每条消息的固定结构开销（role、JSON 骨架等约 4 token）。
const messageOverhead = 4

// estimateTokens 估算一段文本的 token 数。
func estimateTokens(s string) int {
	ascii, nonASCII := 0, 0
	for _, r := range s {
		if r < 128 {
			ascii++
		} else {
			nonASCII++
		}
	}
	return ascii/4 + nonASCII
}

// estimateValue 递归汇总 JSON 值中所有字符串的估算 token。
// 标量（数字/布尔/null）不产生文本，计 0；map 的键是字段名，不计。
func estimateValue(v any) int {
	switch t := v.(type) {
	case string:
		return estimateTokens(t)
	case []any:
		total := 0
		for _, item := range t {
			total += estimateValue(item)
		}
		return total
	case map[string]any:
		total := 0
		for _, val := range t {
			total += estimateValue(val)
		}
		return total
	default:
		return 0
	}
}

// estimateBody 估算整个请求体的输入 token：所有文本字段（messages、
// system、tools 描述等）+ 每条消息的结构开销。model 字段是网关别名，
// 长度与上游计费无关，跳过。两族协议共用——字段名差异不影响「把
// 文本加起来」这个估算语义。
func estimateBody(body map[string]any) int {
	total := 0
	for k, v := range body {
		if k == "model" {
			continue
		}
		total += estimateValue(v)
	}
	if msgs, ok := body["messages"].([]any); ok {
		total += messageOverhead * len(msgs)
	}
	return total
}
