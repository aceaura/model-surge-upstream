package kiro

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode/utf8"
)

const maxResponseBytes = 32 << 20
const maxToolBytes = 8 << 20
const maxBracketScanBytes = 8 << 20

// kiroMaxInputTokens 对齐 config.py DEFAULT_MAX_INPUT_TOKENS:从
// contextUsagePercentage 反推 token 数时的上下文上限。Python 按模型读
// ListAvailableModels 缓存的 tokenLimits,传输层无此缓存,用默认上限。
const kiroMaxInputTokens = 200000

// toolViolation 是严格 tool_choice 下的语义违规(streaming_core.py
// ToolChoiceViolation):传输层缓冲校验后注入恢复指令重试一次。
type toolViolation struct{ msg string }

func (e *toolViolation) Error() string { return "kiro: tool policy violation: " + e.msg }

type pendingTool struct {
	id, name string
	args     strings.Builder
}

// finishedTool 是收尾后的工具调用:args 为保序归一化的 JSON 文本(去重键
// 与上行参数都用它,parsers.py 的 json.dumps(json.loads(raw)) 保序语义),
// invalid 对应 parsers.py 的 _arguments_invalid。
type finishedTool struct {
	id, name string
	args     string
	input    any
	invalid  bool
}

// normalizeOrderedJSON 把合法 JSON 重编码为保序紧凑文本(对象键序按到达
// 顺序保留,数字原文保留);非法输入 ok=false。
func normalizeOrderedJSON(raw string) (string, bool) {
	dec := json.NewDecoder(strings.NewReader(raw))
	dec.UseNumber()
	var buf strings.Builder
	if !encodeOrderedValue(dec, &buf) {
		return "", false
	}
	if _, err := dec.Token(); err != io.EOF {
		return "", false // 顶层多值
	}
	return buf.String(), true
}

func encodeOrderedValue(dec *json.Decoder, buf *strings.Builder) bool {
	tok, err := dec.Token()
	if err != nil {
		return false
	}
	delim, ok := tok.(json.Delim)
	if !ok {
		b, err := json.Marshal(tok)
		if err != nil {
			return false
		}
		buf.Write(b)
		return true
	}
	switch delim {
	case '{':
		buf.WriteByte('{')
		first := true
		for dec.More() {
			if !first {
				buf.WriteByte(',')
			}
			first = false
			kt, err := dec.Token()
			if err != nil {
				return false
			}
			ks, ok := kt.(string)
			if !ok {
				return false
			}
			kb, _ := json.Marshal(ks)
			buf.Write(kb)
			buf.WriteByte(':')
			if !encodeOrderedValue(dec, buf) {
				return false
			}
		}
		if _, err := dec.Token(); err != nil {
			return false
		}
		buf.WriteByte('}')
	case '[':
		buf.WriteByte('[')
		first := true
		for dec.More() {
			if !first {
				buf.WriteByte(',')
			}
			first = false
			if !encodeOrderedValue(dec, buf) {
				return false
			}
		}
		if _, err := dec.Token(); err != nil {
			return false
		}
		buf.WriteByte(']')
	default:
		return false
	}
	return true
}

// dedupTools 对齐 parsers.py deduplicate_tool_calls:先按 id 原位替换为参数
// 更全者(合法非空参数压过无效/空/更短者),再按 name+args 保序去重;无 id
// 的调用只参与 name+args 阶段。
func dedupTools(in []*finishedTool) []*finishedTool {
	byID := map[string]*finishedTool{}
	var withID, noID []*finishedTool
	for _, tc := range in {
		if tc.id == "" {
			noID = append(noID, tc)
			continue
		}
		ex := byID[tc.id]
		if ex == nil {
			byID[tc.id] = tc
			withID = append(withID, tc)
			continue
		}
		if !tc.invalid && tc.args != "{}" && (ex.invalid || ex.args == "{}" || len(tc.args) > len(ex.args)) {
			for i, v := range withID {
				if v == ex {
					withID[i] = tc
					break
				}
			}
			byID[tc.id] = tc
		} else if tc.invalid && (ex.invalid || ex.args == "{}") {
			ex.invalid = true
		}
	}
	seen := map[string]*finishedTool{}
	var out []*finishedTool
	for _, tc := range append(withID, noID...) {
		key := tc.name + "-" + tc.args
		if ex := seen[key]; ex != nil {
			if tc.invalid && tc.args == "{}" {
				ex.invalid = true
			}
			continue
		}
		seen[key] = tc
		out = append(out, tc)
	}
	return out
}

type responseState struct {
	options                       requestOptions
	id                            string
	created                       int64
	blocks                        []any // Only collected for nonstream responses.
	block                         object
	blockType                     string
	textBuffer                    strings.Builder
	blockIndex                    int
	tool                          *pendingTool
	toolCount                     int
	tools                         []*finishedTool
	finalTools                    []*finishedTool
	credits                       float64
	hasCredits                    bool
	contextPct                    float64
	hasContextPct                 bool
	parser                        *thinkingParser
	fullText                      strings.Builder
	fullThinking                  strings.Builder
	fullTextOverflow              bool
	outputRunes, collectedBytes   int
	inputTokens, outputTokens     int
	inputAbsolute, outputAbsolute bool
	cacheRead, cacheCreation      int
	cacheReadAbs, cacheCreateAbs  bool
	terminal, meaningful          bool
	sentRole                      bool
	bracketFound                  bool
	stopReason                    string
	stopSequence                  string
	emit                          func(string, object)
}

func newResponseState(o requestOptions) *responseState {
	prefix := "chatcmpl-"
	if o.protocol == "anthropic" {
		prefix = "msg_"
	}
	s := &responseState{options: o, id: prefix + strings.ReplaceAll(newID(), "-", ""), created: time.Now().Unix(), blockIndex: -1, inputTokens: o.inputEstimate}
	// streaming_core.py:296-299: 思考解析只受全局 FAKE_REASONING_ENABLED
	// 门控,与本请求是否注入标签无关——模型自发输出 <thinking> 块同样
	// 被剥离进 reasoning 通道。
	s.parser = newThinkingParser()
	return s
}
func (s *responseState) send(name string, v object) {
	if s.emit != nil {
		s.emit(name, v)
	}
}
func (s *responseState) chunk(delta object, finish any) object {
	return object{"id": s.id, "object": "chat.completion.chunk", "created": s.created, "model": s.options.model, "choices": []any{object{"index": 0, "delta": delta, "finish_reason": finish}}}
}
func (s *responseState) start() {
	if s.options.protocol == "anthropic" {
		s.send("message_start", object{"type": "message_start", "message": object{"id": s.id, "type": "message", "role": "assistant", "model": s.options.model, "content": []any{}, "stop_reason": nil, "stop_sequence": nil, "usage": s.anthropicUsage()}})
	}
	// streaming_openai.py:140-142: openai 不发空 role 首帧,role 附在首个
	// 真实 content/reasoning delta 上。
}
func (s *responseState) closeBlock() {
	if s.blockType == "" {
		return
	}
	if !s.options.stream && (s.blockType == "text" || s.blockType == "thinking") {
		key := "text"
		if s.blockType == "thinking" {
			key = "thinking"
		}
		s.block[key] = s.textBuffer.String()
		s.textBuffer.Reset()
	}
	if s.options.protocol == "anthropic" {
		s.send("content_block_stop", object{"type": "content_block_stop", "index": s.blockIndex})
	}
	s.blockType = ""
	s.block = nil
}
func (s *responseState) openBlock(kind string, block object) {
	s.closeBlock()
	s.blockIndex++
	s.blockType = kind
	s.block = block
	if !s.options.stream {
		s.blocks = append(s.blocks, block)
	}
	if s.options.protocol == "anthropic" {
		s.send("content_block_start", object{"type": "content_block_start", "index": s.blockIndex, "content_block": block})
	}
}

// text routes assistant content through the fake-reasoning parser when tags
// were injected; native thinking events bypass it via emitBlock.
func (s *responseState) text(kind, text string) error {
	if kind == "text" && s.parser != nil {
		thinking, regular := s.parser.feed(text)
		if err := s.emitBlock("thinking", thinking, true); err != nil {
			return err
		}
		return s.emitBlock("text", regular, true)
	}
	return s.emitBlock(kind, text, false)
}

func fakeSignature() string { return "sig_" + strings.ReplaceAll(newID(), "-", "") }

func (s *responseState) emitBlock(kind, text string, fake bool) error {
	if text == "" {
		return nil
	}
	// streaming_openai.py:287-291 / streaming_anthropic.py:630-634: 截断判定
	// 只认正文 full_content;纯 thinking 被掐断在 Python 判正常收尾。
	if kind == "text" {
		s.meaningful = true
	}
	s.outputRunes += utf8.RuneCountInString(text)
	// 截断恢复哈希只取正文(streaming_anthropic.py:701 传 full_content 不
	// 含思考),因此思考单独累积,不能与正文混入同一缓冲;括号扫描输入
	// 在 finalize 按协议与流式与否选择(streaming_core.py:478-484)。
	if (kind == "text" || kind == "thinking") && !s.fullTextOverflow {
		if s.fullText.Len()+s.fullThinking.Len()+len(text) > maxBracketScanBytes {
			s.fullTextOverflow = true
			s.fullText.Reset()
			s.fullThinking.Reset()
		} else if kind == "thinking" {
			s.fullThinking.WriteString(text)
		} else {
			s.fullText.WriteString(text)
		}
	}
	if !s.options.stream {
		s.collectedBytes += len(text)
		if s.collectedBytes > maxResponseBytes {
			return fmt.Errorf("kiro: response exceeds collection limit")
		}
	}
	if s.blockType != kind {
		block := object{"type": kind}
		if kind == "thinking" {
			block["thinking"] = ""
			if fake && s.options.protocol == "anthropic" {
				block["signature"] = fakeSignature()
			} else {
				block["signature"] = ""
			}
		} else {
			block["text"] = ""
		}
		s.openBlock(kind, block)
	}
	field, deltaType := "text", "text_delta"
	if kind == "thinking" {
		field, deltaType = "thinking", "thinking_delta"
	}
	if s.options.protocol == "anthropic" {
		s.send("content_block_delta", object{"type": "content_block_delta", "index": s.blockIndex, "delta": object{"type": deltaType, field: text}})
	} else {
		field = "content"
		if kind == "thinking" {
			field = "reasoning_content"
		}
		delta := object{field: text}
		if !s.sentRole {
			delta["role"] = "assistant"
			s.sentRole = true
		}
		s.send("", s.chunk(delta, nil))
	}
	if !s.options.stream {
		s.textBuffer.WriteString(text)
	}
	return nil
}
func (s *responseState) finishTool() error {
	if s.tool == nil {
		return nil
	}
	t := s.tool
	s.tool = nil
	args := t.args.String()
	if strings.TrimSpace(args) == "" {
		args = "{}"
	}
	norm, ok := normalizeOrderedJSON(args)
	invalid := false
	if !ok {
		// parsers.py: truncated arguments are diagnosed and replaced with {};
		// the truncation notice is injected into the next request.
		if looksTruncatedJSON(args) {
			saveToolTruncation(t.id, t.name)
		}
		if s.options.policyMode != "" {
			return &toolViolation{msg: "tool '" + t.name + "' returned malformed JSON arguments"}
		}
		norm, invalid = "{}", true
	}
	if s.options.forbidTools || !s.options.allowedTools[t.name] {
		if s.options.policyMode != "" {
			return &toolViolation{msg: "response returned disallowed tool '" + t.name + "'"}
		}
		// parsers.py 无工具白名单校验:未声明的工具调用照常透传。
	}
	if s.options.policyMode == "named" && t.name != s.options.policyTool {
		return &toolViolation{msg: "response called a tool other than required tool '" + s.options.policyTool + "'"}
	}
	if s.toolCount >= 1024 {
		return fmt.Errorf("kiro: response exceeds 1024 tool calls")
	}
	input := any(object{})
	if decoded, err := decodeObject(norm); err == nil {
		input = decoded
	}
	// streaming_anthropic.py:505-540: anthropic 流式逐工具实时发块,全程不去重;
	// openai 流收尾去重后聚合单 chunk(streaming_openai.py:284);非流式仅在
	// 括号恢复命中时去重(streaming_core.py:499-501)。三者统一先入 tools,
	// 去重在 finalize 按矩阵进行。
	s.tools = append(s.tools, &finishedTool{id: t.id, name: t.name, args: norm, input: input, invalid: invalid})
	s.toolCount++
	if !s.options.stream {
		s.collectedBytes += len(norm)
		if s.collectedBytes > maxResponseBytes {
			return fmt.Errorf("kiro: response exceeds collection limit")
		}
	}
	if s.options.stream && s.options.protocol == "anthropic" {
		block := object{"type": "tool_use", "id": t.id, "name": t.name, "input": object{}}
		s.openBlock("tool_use", block)
		s.send("content_block_delta", object{"type": "content_block_delta", "index": s.blockIndex, "delta": object{"type": "input_json_delta", "partial_json": norm}})
		s.closeBlock()
	}
	return nil
}
func (s *responseState) toolEvent(d object) error {
	name := str(d["name"])
	id := str(d["toolUseId"])
	if name != "" {
		// A named frame starts one tool; only this tool is buffered for validation.
		if s.tool != nil {
			if err := s.finishTool(); err != nil {
				return err
			}
		}
		if id == "" {
			id = "call_" + strings.ReplaceAll(newID(), "-", "")
		}
		s.tool = &pendingTool{id: id, name: name}
	} else if s.tool == nil {
		// parsers.py:408-427: 无开启工具的碎片帧静默忽略。
		return nil
	}
	if id != "" && id != s.tool.id {
		// Python 单 current_tool_call 模型不追踪碎片 id,归属当前工具。
		id = s.tool.id
	}
	if v, exists := d["input"]; exists {
		piece := ""
		switch x := v.(type) {
		case string:
			piece = x
		case map[string]any:
			if len(x) > 0 {
				piece = jsonText(x)
			}
		case nil:
		default:
			piece = fmt.Sprint(x) // parsers.py: str(input_data)
		}
		if s.tool.args.Len()+len(piece) > maxToolBytes {
			return fmt.Errorf("kiro: tool arguments exceed %d bytes", maxToolBytes)
		}
		s.tool.args.WriteString(piece)
	}
	if stop, _ := d["stop"].(bool); stop {
		return s.finishTool()
	}
	return nil
}
func (s *responseState) updateUsage(d object) {
	// Credits, percentages, and fractions are not absolute token counts.
	for _, key := range []string{"inputTokens", "inputTokenCount", "input_tokens", "prompt_tokens"} {
		if n, ok := absoluteTokens(d[key]); ok {
			s.inputTokens = n
			s.inputAbsolute = true
			break
		}
	}
	for _, key := range []string{"outputTokens", "outputTokenCount", "output_tokens", "completion_tokens"} {
		if n, ok := absoluteTokens(d[key]); ok {
			s.outputTokens = n
			s.outputAbsolute = true
			break
		}
	}
	// Prompt-cache metering is forwarded verbatim (streaming_anthropic.py).
	for _, key := range []string{"cacheReadInputTokens", "cache_read_input_tokens"} {
		if n, ok := absoluteTokens(d[key]); ok {
			s.cacheRead = n
			s.cacheReadAbs = true
			break
		}
	}
	for _, key := range []string{"cacheCreationInputTokens", "cache_creation_input_tokens"} {
		if n, ok := absoluteTokens(d[key]); ok {
			s.cacheCreation = n
			s.cacheCreateAbs = true
			break
		}
	}
}
func (s *responseState) accept(e wireEvent) error {
	d := e.data
	// Terminal metering events are emitted at the end in the observed protocol;
	// clean EOF alone is insufficient evidence of a completed generation.
	if v, ok := d["usage"]; ok {
		// streaming_openai.py:270-279: openai 路径把任意真值 usage(credits
		// 计量或绝对 token 字典)当作完成信号;streaming_anthropic.py:543-547
		// 的 anthropic 路径只认 contextUsagePercentage,usage 帧仅取缓存字段。
		if n, ok := v.(json.Number); ok {
			if f, err := n.Float64(); err == nil && f != 0 {
				s.credits, s.hasCredits = f, true
				if s.options.protocol == "openai" {
					s.terminal = true
				}
			}
		} else {
			s.updateUsage(obj(v))
			if s.options.protocol == "openai" && len(obj(v)) > 0 {
				s.terminal = true
			}
		}
	}
	if v, ok := d["contextUsagePercentage"]; ok {
		s.terminal = true
		if n, ok := v.(json.Number); ok {
			if f, err := n.Float64(); err == nil {
				s.contextPct, s.hasContextPct = f, true
			}
		}
	}
	if strings.Contains(strings.ToLower(e.kind), "usage") || e.kind == "metadataEvent" {
		s.updateUsage(d)
		for _, key := range []string{"tokenUsage", "usage", "metrics"} {
			s.updateUsage(obj(d[key]))
		}
		if s.options.protocol == "openai" && (s.inputAbsolute || s.outputAbsolute) {
			s.terminal = true
		}
	}
	if reason := str(d["stopReason"]); reason != "" {
		// stopReason 只用于收尾原因映射;两条 Python 流式路径都不把它当
		// 完成信号,缺 contextUsagePercentage(openai 另认 usage)即判截断。
		s.stopReason = strings.ToLower(reason)
		switch s.stopReason {
		case "tooluse":
			s.stopReason = "tool_use"
		case "maxtokens":
			s.stopReason = "max_tokens"
		}
		s.stopSequence = str(d["stopSequence"])
	}
	if _, ok := d["followupPrompt"]; ok {
		return nil
	}
	if name := str(d["name"]); name != "" {
		return s.toolEvent(d)
	}
	if _, ok := d["input"]; ok {
		return s.toolEvent(d)
	}
	if stop, _ := d["stop"].(bool); stop {
		if s.tool == nil {
			return nil // parsers.py:421-427: 无开启工具的 stop 帧静默忽略
		}
		return s.finishTool()
	}
	if v, ok := d["content"]; ok {
		text, ok := v.(string)
		if !ok {
			return fmt.Errorf("kiro: assistant content is not text")
		}
		return s.text("text", text)
	}
	if v, ok := d["text"]; ok && (strings.Contains(strings.ToLower(e.kind), "reason") || strings.Contains(strings.ToLower(e.kind), "thinking")) {
		text, ok := v.(string)
		if !ok {
			return fmt.Errorf("kiro: reasoning content is not text")
		}
		return s.emitBlock("thinking", text, false)
	}
	if signature := str(d["signature"]); signature != "" {
		if s.blockType != "thinking" {
			// streaming_anthropic.py:329-344: 无开启思考块的签名帧静默忽略。
			return nil
		}
		if s.options.protocol == "anthropic" {
			s.send("content_block_delta", object{"type": "content_block_delta", "index": s.blockIndex, "delta": object{"type": "signature_delta", "signature": signature}})
		}
		if !s.options.stream {
			s.block["signature"] = signature
		}
		s.closeBlock()
	}
	return nil
}
func (s *responseState) finalize() error {
	if s.parser != nil {
		thinking, regular := s.parser.finalize()
		if err := s.emitBlock("thinking", thinking, true); err != nil {
			return err
		}
		if err := s.emitBlock("text", regular, true); err != nil {
			return err
		}
	}
	// Some models answer with [Called name with args: {...}] text instead of
	// native tool events; those become real tool blocks after the text.
	if !s.fullTextOverflow {
		// 括号扫描输入:流式两协议与 openai 非流式(复用流式生成器)只扫
		// 正文(streaming_openai.py:283、streaming_anthropic.py:550);
		// anthropic 非流式走 collect_stream_to_result,正文+思考一起扫
		// (streaming_core.py:478-484)。
		scan := s.fullText.String()
		if s.options.protocol == "anthropic" && !s.options.stream {
			scan += s.fullThinking.String()
		}
		calls := parseBracketToolCalls(scan)
		s.bracketFound = len(calls) > 0
		for _, call := range calls {
			if err := s.emitBracketTool(call); err != nil {
				return err
			}
		}
	}
	if err := s.finishTool(); err != nil {
		return err
	}
	// 去重矩阵(parsers.py:151-210 及各调用点):openai 恒去重——流式收尾
	// (streaming_openai.py:284),非流式复用流式生成器同一收口
	// (collect_stream_response);anthropic 流实时发块不去重;anthropic
	// 非流式仅在括号恢复命中时去重(streaming_core.py:499-501)。
	switch {
	case s.options.protocol == "anthropic" && (s.options.stream || !s.bracketFound):
		s.finalTools = s.tools
	default:
		s.finalTools = dedupTools(s.tools)
	}
	s.toolCount = len(s.finalTools)
	if !s.options.stream {
		// streaming_anthropic.py:761-786: 非流式内容块顺序恒为
		// thinking → text → tool_use。
		for _, ft := range s.finalTools {
			s.blocks = append(s.blocks, object{"type": "tool_use", "id": ft.id, "name": ft.name, "input": ft.input})
		}
	}
	if !s.terminal {
		// Clean EOF without completion/usage markers: the reference recovers a
		// nonempty generation as truncated output instead of failing the
		// request; the notice is injected into the next one.
		switch {
		case s.toolCount > 0:
			// Tool-call truncation was already recorded by finishTool.
		case s.meaningful:
			s.stopReason = "max_tokens"
			s.stopSequence = ""
			saveContentTruncation(s.fullText.String())
		default:
			// streaming_openai.py:787: 空流仍回 200(空 content、usage 归零)。
		}
	}
	switch s.stopReason {
	case "", "end_turn", "stop", "stop_sequence", "tool_use", "toolUse", "tool_calls", "max_tokens", "maxTokens", "length", "MAX_TOKENS", "content_filter", "content_filtered", "refusal":
	default:
		return fmt.Errorf("kiro: unsupported upstream stop reason %q", s.stopReason)
	}
	if (s.stopReason == "tool_use" || s.stopReason == "toolUse" || s.stopReason == "tool_calls") && s.toolCount == 0 {
		return fmt.Errorf("kiro: upstream signaled tool use without a tool call")
	}
	s.closeBlock()
	return nil
}

// emitBracketTool appends a tool call recovered from response text. Unlike
// native tool events these are not restricted to declared tools, matching the
// reference parser.
func (s *responseState) emitBracketTool(call object) error {
	if s.toolCount >= 1024 {
		return fmt.Errorf("kiro: response exceeds 1024 tool calls")
	}
	id, name := str(call["toolUseId"]), str(call["name"])
	input := obj(call["input"])
	canonical := jsonText(input)
	// 严格 tool_choice 下括号恢复的工具同样受政策约束(validate_tool_choice_result)。
	if s.options.policyMode != "" {
		if s.options.forbidTools || !s.options.allowedTools[name] {
			return &toolViolation{msg: "response returned disallowed tool '" + name + "'"}
		}
		if s.options.policyMode == "named" && name != s.options.policyTool {
			return &toolViolation{msg: "response called a tool other than required tool '" + s.options.policyTool + "'"}
		}
	}
	s.tools = append(s.tools, &finishedTool{id: id, name: name, args: canonical, input: input})
	s.toolCount++
	if !s.options.stream {
		s.collectedBytes += len(canonical)
		if s.collectedBytes > maxResponseBytes {
			return fmt.Errorf("kiro: response exceeds collection limit")
		}
	}
	// streaming_anthropic.py:571-613: 括号恢复的块在收尾实时追加,不去重。
	if s.options.stream && s.options.protocol == "anthropic" {
		block := object{"type": "tool_use", "id": id, "name": name, "input": object{}}
		s.openBlock("tool_use", block)
		s.send("content_block_delta", object{"type": "content_block_delta", "index": s.blockIndex, "delta": object{"type": "input_json_delta", "partial_json": canonical}})
		s.closeBlock()
	}
	return nil
}

// strictViolation 是 validate_tool_choice_result 的收尾检查:required/named
// 模式下一个工具都没调到同样违规。
func strictViolation(s *responseState) *toolViolation {
	switch s.options.policyMode {
	case "required":
		if s.toolCount == 0 {
			return &toolViolation{msg: "tool_choice required returned no tools"}
		}
	case "named":
		if s.toolCount == 0 {
			return &toolViolation{msg: "required tool '" + s.options.policyTool + "' was not called"}
		}
	}
	return nil
}

// recoveryDirective 复刻 add_tool_choice_recovery_directive: 在 currentMessage
// 末尾追加一次性恢复指令后重新编码负载。
func recoveryDirective(payload object, options requestOptions, v *toolViolation) ([]byte, error) {
	uim := obj(obj(obj(payload["conversationState"])["currentMessage"])["userInputMessage"])
	if uim == nil {
		return nil, fmt.Errorf("kiro: cannot inject tool policy recovery directive")
	}
	uim["content"] = str(uim["content"]) +
		"\n\n[Tool Policy Recovery] Your previous response violated the tool policy (" +
		v.msg + "). Retry THIS response now. " + strings.TrimSpace(options.policyDirective)
	encoded, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}
	if len(encoded) > maxPayloadBytes {
		return nil, fmt.Errorf("kiro: native payload exceeds %d bytes (history is not silently trimmed)", maxPayloadBytes)
	}
	return encoded, nil
}
func (s *responseState) outputCount() int {
	if s.outputAbsolute {
		return s.outputTokens
	}
	if s.outputRunes == 0 {
		return 0
	}
	n := s.outputRunes/4 + 1
	if strings.HasPrefix(nativeModel(s.options.model), "claude") {
		n = n * 115 / 100
	}
	return n
}
func (s *responseState) usageSource() object {
	in, out := "estimated:unicode_chars/4+1;message_overhead;image=100;claude=1.15", "estimated:unicode_chars/4+1;claude=1.15"
	if s.inputAbsolute {
		in = "upstream:absolute_tokens"
	} else if _, ok := s.contextDerivedInput(s.outputCount()); ok {
		in = "derived:context_usage_percentage"
	}
	if s.outputAbsolute {
		out = "upstream:absolute_tokens"
	}
	return object{"input_tokens": in, "output_tokens": out}
}

// contextDerivedInput 对齐 streaming_core.py:510-535 的反推:无绝对 input
// 值时,total=int(pct/100×上限),prompt=max(0, total-completion)。
func (s *responseState) contextDerivedInput(output int) (int, bool) {
	if !s.hasContextPct || s.contextPct <= 0 || s.inputAbsolute {
		return 0, false
	}
	total := int(s.contextPct / 100 * kiroMaxInputTokens)
	prompt := total - output
	if prompt < 0 {
		prompt = 0
	}
	return prompt, true
}
func (s *responseState) anthropicUsage() object {
	input := s.inputTokens
	if derived, ok := s.contextDerivedInput(s.outputCount()); ok {
		input = derived
	}
	usage := object{"input_tokens": input, "output_tokens": s.outputCount(), "kiro_usage_source": s.usageSource()}
	if s.cacheReadAbs {
		usage["cache_read_input_tokens"] = s.cacheRead
	}
	if s.cacheCreateAbs {
		usage["cache_creation_input_tokens"] = s.cacheCreation
	}
	if s.hasCredits {
		usage["credits_used"] = s.credits
	}
	return usage
}
func (s *responseState) openAIUsage() object {
	out := s.outputCount()
	prompt := s.inputTokens
	total := prompt + out
	if derived, ok := s.contextDerivedInput(out); ok {
		prompt = derived
		total = int(s.contextPct / 100 * kiroMaxInputTokens)
	}
	usage := object{"prompt_tokens": prompt, "completion_tokens": out, "total_tokens": total, "kiro_usage_source": s.usageSource()}
	if s.hasCredits {
		usage["credits_used"] = s.credits
	}
	return usage
}
func (s *responseState) reason() (string, string) {
	switch s.stopReason {
	case "max_tokens", "maxTokens", "length", "MAX_TOKENS":
		return "max_tokens", "length"
	case "content_filter", "content_filtered", "refusal":
		// 上游观测值 content_filtered(Kiro 内容过滤拒答)与 anthropic 原生
		// refusal 同类:anthropic 透 refusal,openai 透 content_filter。
		return "refusal", "content_filter"
	case "end_turn", "stop", "stop_sequence", "", "tool_use", "toolUse", "tool_calls":
		if s.toolCount > 0 {
			return "tool_use", "tool_calls"
		}
		if s.stopReason == "stop_sequence" {
			return "stop_sequence", "stop"
		}
		return "end_turn", "stop"
	default:
		return "end_turn", "stop"
	}
}
func (s *responseState) sequenceValue() any {
	if s.stopReason == "stop_sequence" && s.toolCount == 0 && s.stopSequence != "" {
		return s.stopSequence
	}
	return nil
}
func (s *responseState) finishStream() {
	anthropic, openai := s.reason()
	if s.options.protocol == "anthropic" {
		s.send("message_delta", object{"type": "message_delta", "delta": object{"stop_reason": anthropic, "stop_sequence": s.sequenceValue()}, "usage": s.anthropicUsage()})
		s.send("message_stop", object{"type": "message_stop"})
	} else {
		if len(s.finalTools) > 0 {
			// streaming_openai.py:340-368: 工具调用收尾前聚合为单个
			// tool_calls chunk(去重后的 finalTools),index 按序编号。
			toolCalls := make([]any, 0, len(s.finalTools))
			for i, ft := range s.finalTools {
				toolCalls = append(toolCalls, object{"index": i, "id": ft.id, "type": "function", "function": object{"name": ft.name, "arguments": ft.args}})
			}
			s.send("", s.chunk(object{"tool_calls": toolCalls}, nil))
		}
		// streaming_openai.py:396-411: finish_reason 与 usage 同一收尾帧,
		// 恒发,不看 stream_options。
		final := s.chunk(object{}, openai)
		final["usage"] = s.openAIUsage()
		s.send("", final)
	}
}
func (s *responseState) response() object {
	anthropic, openai := s.reason()
	if s.options.protocol == "anthropic" {
		return object{"id": s.id, "type": "message", "role": "assistant", "model": s.options.model, "content": s.blocks, "stop_reason": anthropic, "stop_sequence": s.sequenceValue(), "usage": s.anthropicUsage()}
	}
	var content, reasoning strings.Builder
	for _, v := range s.blocks {
		b := obj(v)
		switch str(b["type"]) {
		case "text":
			content.WriteString(str(b["text"]))
		case "thinking":
			reasoning.WriteString(str(b["thinking"]))
		}
	}
	// streaming_openai.py:521-533: 非流式 tool_calls 取去重后的 finalTools,
	// arguments 用保序归一化文本。
	calls := []any{}
	for _, ft := range s.finalTools {
		calls = append(calls, object{"id": ft.id, "type": "function", "function": object{"name": ft.name, "arguments": ft.args}})
	}
	// streaming_openai.py:763: content 恒为字符串,工具调用场景不回 null。
	message := object{"role": "assistant", "content": content.String()}
	if len(calls) > 0 {
		message["tool_calls"] = calls
	}
	if reasoning.Len() > 0 {
		message["reasoning_content"] = reasoning.String()
	}
	return object{"id": s.id, "object": "chat.completion", "created": s.created, "model": s.options.model, "choices": []any{object{"index": 0, "message": message, "finish_reason": openai}}, "usage": s.openAIUsage()}
}

func onceCloser(body io.Closer) func() error {
	var once sync.Once
	var err error
	return func() error { once.Do(func() { err = body.Close() }); return err }
}
func collectResponse(body io.Reader, s *responseState, ctx context.Context) error {
	p := eventReader{r: body}
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		e, err := p.next()
		if contextErr := ctx.Err(); contextErr != nil {
			return contextErr
		}
		if errors.Is(err, io.EOF) {
			return s.finalize()
		}
		if err != nil {
			return err
		}
		if err := s.accept(e); err != nil {
			return err
		}
	}
}

// streamBody is a pull-based adapter: no pump goroutine, pipe or full-body
// buffering. Read advances at most to the next output event. Close/cancellation
// interrupts upstream reads through managedBody, including mid-frame reads.
// A body supports one concurrent reader, as do normal net/http response bodies.
type streamBody struct {
	upstream      *managedBody
	parser        eventReader
	state         *responseState
	ctx           context.Context
	pending       bytes.Buffer
	err           error
	started, done bool
	closed        atomic.Bool
}

func newStreamBody(body *managedBody, s *responseState, ctx context.Context) *streamBody {
	b := &streamBody{upstream: body, parser: eventReader{r: body}, state: s, ctx: ctx}
	s.emit = func(name string, v object) {
		data, _ := json.Marshal(v)
		if name != "" {
			b.pending.WriteString("event: " + name + "\n")
		}
		b.pending.WriteString("data: ")
		b.pending.Write(data)
		b.pending.WriteString("\n\n")
	}
	return b
}
func (b *streamBody) fail(err error) {
	b.err = err
	b.done = true
	b.state.send("error", object{"type": "error", "error": object{"type": "upstream_error", "message": err.Error()}})
	b.upstream.Close()
}
func (b *streamBody) Read(p []byte) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}
	if b.closed.Load() {
		return 0, io.ErrClosedPipe
	}
	if err := b.ctx.Err(); err != nil {
		return 0, err
	}
	if !b.started {
		b.started = true
		b.state.start()
	}
	for b.pending.Len() == 0 && !b.done {
		if err := b.ctx.Err(); err != nil {
			b.fail(err)
			break
		}
		event, err := b.parser.next()
		if b.closed.Load() {
			return 0, io.ErrClosedPipe
		}
		if contextErr := b.ctx.Err(); contextErr != nil {
			b.fail(contextErr)
			break
		}
		if errors.Is(err, io.EOF) {
			if err := b.state.finalize(); err != nil {
				b.fail(err)
				break
			}
			b.state.finishStream()
			if b.state.options.protocol == "openai" {
				b.pending.WriteString("data: [DONE]\n\n")
			}
			b.done = true
			b.upstream.Close()
			break
		}
		if err != nil {
			b.fail(err)
			break
		}
		if err := b.state.accept(event); err != nil {
			b.fail(err)
			break
		}
	}
	if b.pending.Len() > 0 {
		return b.pending.Read(p)
	}
	if b.err != nil {
		return 0, b.err
	}
	return 0, io.EOF
}
func (b *streamBody) Close() error { b.closed.Store(true); return b.upstream.Close() }
