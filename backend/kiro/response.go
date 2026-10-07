package kiro

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/big"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode/utf16"
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
	id   any
	name string
	args strings.Builder
}

// finishedTool 是收尾后的工具调用:args 为保序归一化的 JSON 文本(去重键
// 与上行参数都用它,parsers.py 的 json.dumps(json.loads(raw)) 保序语义),
// invalid 对应 parsers.py 的 _arguments_invalid。
type finishedTool struct {
	id        any
	name      string
	args      string
	input     any
	invalid   bool
	truncated bool
	bracket   bool
}

// normalizeOrderedJSON 把合法 JSON 重编码为 Python json.dumps 风格文本:
// 默认分隔符 ", "/": "、对象键序按到达顺序保留、数字按 Python repr 归一
// (parsers.py:445 的 json.dumps(json.loads(raw)) 语义);asciiOnly 对应
// ensure_ascii(openai arguments 存 ASCII 形式,anthropic partial_json 用
// ensure_ascii=False 的 UTF-8 形式)。非法输入 ok=false。
func normalizeOrderedJSON(raw string, asciiOnly bool) (string, bool) {
	dec := json.NewDecoder(strings.NewReader(raw))
	dec.UseNumber()
	var buf strings.Builder
	if !encodeOrderedValue(dec, &buf, raw, asciiOnly) {
		return "", false
	}
	if _, err := dec.Token(); err != io.EOF {
		return "", false // 顶层多值
	}
	return buf.String(), true
}

// pyJSONString 按 Python json.dumps 转义字符串:只转引号、反斜杠与常见控制
// 符,不转 <>&;asciiOnly 时非 ASCII 字符转 \uXXXX(星平面用代理对)。
func pyJSONString(s string, asciiOnly bool) string {
	var buf strings.Builder
	buf.WriteByte('"')
	for _, r := range s {
		writeJSONRune(&buf, r, asciiOnly)
	}
	buf.WriteByte('"')
	return buf.String()
}

func writeJSONRune(buf *strings.Builder, r rune, asciiOnly bool) {
	switch r {
	case '"':
		buf.WriteString(`\"`)
	case '\\':
		buf.WriteString(`\\`)
	case '\n':
		buf.WriteString(`\n`)
	case '\r':
		buf.WriteString(`\r`)
	case '\t':
		buf.WriteString(`\t`)
	case '\b':
		buf.WriteString(`\b`)
	case '\f':
		buf.WriteString(`\f`)
	default:
		if r < 0x20 || (r >= 0xd800 && r <= 0xdfff) {
			fmt.Fprintf(buf, `\u%04x`, r)
		} else if asciiOnly && r > 0x7f {
			if r > 0xffff {
				r1, r2 := utf16.EncodeRune(r)
				fmt.Fprintf(buf, `\u%04x\u%04x`, r1, r2)
			} else {
				fmt.Fprintf(buf, `\u%04x`, r)
			}
		} else {
			buf.WriteRune(r)
		}
	}
}

// Go's JSON decoder replaces unpaired surrogates; preserve their escaped identity.
func pyJSONTokenString(raw string, asciiOnly bool) string {
	raw = raw[strings.IndexByte(raw, '"')+1 : len(raw)-1]
	var buf strings.Builder
	buf.WriteByte('"')
	for i := 0; i < len(raw); {
		r, size := utf8.DecodeRuneInString(raw[i:])
		i += size
		if r == '\\' {
			r = rune(raw[i])
			i++
			switch r {
			case 'u':
				n, _ := strconv.ParseUint(raw[i:i+4], 16, 16)
				r, i = rune(n), i+4
				if r >= 0xd800 && r <= 0xdbff && strings.HasPrefix(raw[i:], `\u`) {
					next, _ := strconv.ParseUint(raw[i+2:i+6], 16, 16)
					if next >= 0xdc00 && next <= 0xdfff {
						r, i = utf16.DecodeRune(r, rune(next)), i+6
					}
				}
			case 'b':
				r = '\b'
			case 'f':
				r = '\f'
			case 'n':
				r = '\n'
			case 'r':
				r = '\r'
			case 't':
				r = '\t'
			}
		}
		writeJSONRune(&buf, r, asciiOnly)
	}
	buf.WriteByte('"')
	return buf.String()
}

// pyNumber 按 Python repr 归一数字:整数原文保留(任意精度),浮点走最短
// 表示且恒带小数点或指数(json "1e2"→"100.0"、"1.50"→"1.5")。
func pyNumber(n json.Number) string {
	s := n.String()
	if strings.IndexAny(s, ".eEnN") < 0 {
		if s == "-0" {
			return "0"
		}
		return s
	}
	f, err := n.Float64()
	if err != nil {
		return s
	}
	out := strconv.FormatFloat(f, 'e', -1, 64)
	_, exponent, _ := strings.Cut(out, "e")
	if exp, _ := strconv.Atoi(exponent); exp >= -4 && exp < 16 {
		out = strconv.FormatFloat(f, 'f', -1, 64)
	}
	if strings.IndexAny(out, ".en") < 0 {
		out += ".0"
	}
	return out
}

func encodeOrderedValue(dec *json.Decoder, buf *strings.Builder, raw string, asciiOnly bool) bool {
	start := int(dec.InputOffset())
	tok, err := dec.Token()
	if err != nil {
		return false
	}
	delim, ok := tok.(json.Delim)
	if !ok {
		switch x := tok.(type) {
		case string:
			buf.WriteString(pyJSONTokenString(raw[start:int(dec.InputOffset())], asciiOnly))
		case json.Number:
			buf.WriteString(pyNumber(x))
		case bool:
			if x {
				buf.WriteString("true")
			} else {
				buf.WriteString("false")
			}
		case nil:
			buf.WriteString("null")
		default:
			return false
		}
		return true
	}
	switch delim {
	case '{':
		keys, values := []string{}, []string{}
		indices := map[string]int{}
		for dec.More() {
			keyStart := int(dec.InputOffset())
			kt, err := dec.Token()
			if err != nil {
				return false
			}
			if _, ok := kt.(string); !ok {
				return false
			}
			keyRaw := raw[keyStart:int(dec.InputOffset())]
			key := pyJSONTokenString(keyRaw, true)
			var value strings.Builder
			if !encodeOrderedValue(dec, &value, raw, asciiOnly) {
				return false
			}
			if i, exists := indices[key]; exists {
				values[i] = value.String()
			} else {
				indices[key] = len(keys)
				keys = append(keys, pyJSONTokenString(keyRaw, asciiOnly))
				values = append(values, value.String())
			}
		}
		if _, err := dec.Token(); err != nil {
			return false
		}
		buf.WriteByte('{')
		for i, key := range keys {
			if i > 0 {
				buf.WriteString(", ")
			}
			buf.WriteString(key)
			buf.WriteString(": ")
			buf.WriteString(values[i])
		}
		buf.WriteByte('}')
	case '[':
		buf.WriteByte('[')
		first := true
		for dec.More() {
			if !first {
				buf.WriteString(", ")
			}
			first = false
			if !encodeOrderedValue(dec, buf, raw, asciiOnly) {
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

type toolIDKey struct {
	kind  byte
	value string
}

func toolIDTruthy(id any) bool {
	switch x := id.(type) {
	case json.Number:
		if strings.IndexAny(x.String(), ".eE") < 0 {
			return strings.Trim(x.String(), "-0") != ""
		}
	case float64:
		return x != 0
	case int:
		return x != 0
	case int64:
		return x != 0
	}
	return truthy(id)
}

func comparableToolID(id any) (toolIDKey, bool) {
	var number *big.Rat
	switch x := id.(type) {
	case string:
		return toolIDKey{kind: 's', value: x}, x != ""
	case bool:
		if x {
			number = big.NewRat(1, 1)
		}
	case json.Number:
		if strings.IndexAny(x.String(), ".eE") < 0 {
			if integer, ok := new(big.Int).SetString(x.String(), 10); ok {
				number = new(big.Rat).SetInt(integer)
			}
		} else if f, err := x.Float64(); err == nil {
			number = new(big.Rat).SetFloat64(f)
		}
	case float64:
		number = new(big.Rat).SetFloat64(x)
	case int:
		number = new(big.Rat).SetInt64(int64(x))
	case int64:
		number = new(big.Rat).SetInt64(x)
	}
	if number == nil || number.Sign() == 0 {
		return toolIDKey{}, false
	}
	// Python hashes equal int/float/bool IDs together, without rounding arbitrary-size ints.
	return toolIDKey{kind: 'n', value: number.RatString()}, true
}

// dedupTools 对齐 parsers.py deduplicate_tool_calls:先按 id 原位替换为参数
// 更全者(合法非空参数压过无效/空/更短者),再按 name+args 保序去重;无 id
// 的调用只参与 name+args 阶段。
func dedupTools(in []*finishedTool) []*finishedTool {
	byID := map[toolIDKey]*finishedTool{}
	var withID, noID []*finishedTool
	for _, tc := range in {
		key, hasID := comparableToolID(tc.id)
		if !hasID {
			noID = append(noID, tc)
			continue
		}
		ex := byID[key]
		if ex == nil {
			byID[key] = tc
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
			byID[key] = tc
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
	fullContentForBracket         strings.Builder
	fullTextOverflow              bool
	outputRunes, collectedBytes   int
	inputTokens, outputTokens     int
	inputAbsolute, outputAbsolute bool
	cacheRead, cacheCreation      int
	cacheReadAbs, cacheCreateAbs  bool
	terminal, meaningful          bool
	sentRole                      bool
	stopReason                    string
	stopSequence                  string
	thinkingSignature             string
	emit                          func(string, object)
}

func newResponseState(o requestOptions) *responseState {
	// utils.py: chatcmpl- 用完整 32 hex,msg_ 用前 24 hex,call_ 用前 8 hex。
	hex := strings.ReplaceAll(newID(), "-", "")
	id := "chatcmpl-" + hex
	if o.protocol == "anthropic" {
		id = "msg_" + hex[:24]
	}
	s := &responseState{options: o, id: id, created: time.Now().Unix(), blockIndex: -1, inputTokens: o.inputEstimate}
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
		usage := s.anthropicUsage()
		usage["output_tokens"] = 0
		s.send("message_start", object{"type": "message_start", "message": object{"id": s.id, "type": "message", "role": "assistant", "model": s.options.model, "content": []any{}, "stop_reason": nil, "stop_sequence": nil, "usage": usage}})
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
			s.fullContentForBracket.Reset()
		} else {
			if kind == "thinking" {
				s.fullThinking.WriteString(text)
			} else {
				s.fullText.WriteString(text)
			}
			if s.options.policyMode != "" || (s.options.protocol == "anthropic" && !s.options.stream) {
				s.fullContentForBracket.WriteString(text)
			}
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
	norm, ok := normalizeOrderedJSON(args, true)
	truncated := !ok && looksTruncatedJSON(args)
	invalid := false
	if !ok {
		norm, invalid = "{}", true
	}
	if s.toolCount >= 1024 {
		return fmt.Errorf("kiro: response exceeds 1024 tool calls")
	}
	var input any
	decoder := json.NewDecoder(strings.NewReader(norm))
	decoder.UseNumber()
	if err := decoder.Decode(&input); err != nil {
		return err
	}
	// streaming_core.py:362-368: 原生工具事件在参考实现里只在全部字节
	// 消费完后由 get_tool_calls(parsers.py:589-591,无条件去重)统一交出,
	// 发块在 finalize 按协议矩阵进行。
	s.tools = append(s.tools, &finishedTool{id: t.id, name: t.name, args: norm, input: input, invalid: invalid, truncated: truncated})
	s.toolCount++
	if !s.options.stream {
		s.collectedBytes += len(norm)
		if s.collectedBytes > maxResponseBytes {
			return fmt.Errorf("kiro: response exceeds collection limit")
		}
	}
	return nil
}
func (s *responseState) toolEvent(d object) error {
	_, hasName := d["name"]
	if hasName {
		// parsers.py:330/376-401: name 键出现即开启新工具,空名同样建
		// 工具(参考实现不校验,交由上游判定)。
		if s.tool != nil {
			if err := s.finishTool(); err != nil {
				return err
			}
		}
		id := d["toolUseId"]
		if _, hasID := d["toolUseId"]; !hasID {
			// parsers.py:395: 仅键缺省时生成 call_+8 hex;空串原样保留。
			id = "call_" + strings.ReplaceAll(newID(), "-", "")[:8]
		}
		s.tool = &pendingTool{id: id, name: restoreToolName(str(d["name"]))}
	} else if s.tool == nil {
		// parsers.py:408-427: 无开启工具的碎片帧静默忽略。
		return nil
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
		case []any:
			if len(x) > 0 {
				piece = pyRepr(x)
			}
		default:
			// parsers.py: str(input_data) if input_data else ''——假值静默为空。
			if truthy(x) {
				piece = pythonicString(x)
			}
		}
		if s.tool.args.Len()+len(piece) > maxToolBytes {
			return fmt.Errorf("kiro: tool arguments exceed %d bytes", maxToolBytes)
		}
		s.tool.args.WriteString(piece)
	}
	if hasName && truthy(d["stop"]) {
		return s.finishTool()
	}
	return nil
}
func cacheTokens(v any) (int, bool) {
	switch x := v.(type) {
	case bool:
		if x {
			return 1, true
		}
		return 0, true
	case json.Number:
		f, err := x.Float64()
		if err == nil && f >= -(1<<40) && f <= 1<<40 {
			return int(f), true
		}
	}
	return 0, false
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
	for _, key := range []string{"cache_read_input_tokens", "cacheReadInputTokens"} {
		if n, ok := cacheTokens(d[key]); ok {
			s.cacheRead = n
			s.cacheReadAbs = true
		}
	}
	for _, key := range []string{"cache_creation_input_tokens", "cacheCreationInputTokens"} {
		if n, ok := cacheTokens(d[key]); ok {
			s.cacheCreation = n
			s.cacheCreateAbs = true
		}
	}
}
func (s *responseState) accept(e wireEvent) error {
	d := e.data
	// Terminal metering events are emitted at the end in the observed protocol;
	// clean EOF alone is insufficient evidence of a completed generation.
	if v, ok := d["usage"]; ok && v != nil {
		if s.options.policyMode != "" || (s.options.protocol == "anthropic" && !s.options.stream) {
			s.cacheRead, s.cacheCreation = 0, 0
			s.cacheReadAbs, s.cacheCreateAbs = false, false
		}
		// 普通 OpenAI 两种输出均走生成器语义；仅 collect 路径认假值 usage。
		if s.options.policyMode != "" || (s.options.protocol == "anthropic" && !s.options.stream) || (s.options.protocol == "openai" && truthy(v)) {
			s.terminal = true
		}
		if n, ok := v.(json.Number); ok {
			if f, err := n.Float64(); err == nil && f != 0 {
				s.credits, s.hasCredits = f, true
			}
		} else {
			s.updateUsage(obj(v))
		}
	}
	if v, ok := d["contextUsagePercentage"]; ok && v != nil {
		// streaming_core.py:494-496 / streaming_anthropic.py:543 /
		// streaming_openai.py:274: 三条消费路径都要求非 None,显式 null
		// 不算完成信号(与 usage 分支同规)。
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
	if truthy(d["followupPrompt"]) {
		return nil
	}
	if _, has := d["name"]; has {
		return s.toolEvent(d)
	}
	if _, ok := d["input"]; ok {
		return s.toolEvent(d)
	}
	if truthy(d["stop"]) {
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
	if v, ok := d["text"]; ok {
		text, ok := v.(string)
		if !ok {
			return nil
		}
		if text == "" && s.options.protocol == "anthropic" && s.options.stream && s.options.policyMode == "" && s.blockType != "thinking" {
			s.openBlock("thinking", object{"type": "thinking", "thinking": "", "signature": ""})
		}
		return s.emitBlock("thinking", text, false)
	}
	if signature := str(d["signature"]); signature != "" {
		if !s.options.stream {
			s.thinkingSignature = signature
		}
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
	if err := s.finishTool(); err != nil {
		return err
	}
	s.tools = dedupTools(s.tools)
	// Some models answer with [Called name with args: {...}] text instead of
	// native tool events; those become real tool blocks after the text.
	if !s.fullTextOverflow {
		// 收集路径按事件顺序扫描正文与思考，普通生成器只扫描正文。
		scan := s.fullText.String()
		if s.options.policyMode != "" || (s.options.protocol == "anthropic" && !s.options.stream) {
			scan = s.fullContentForBracket.String()
		}
		calls := parseBracketToolCalls(scan)
		for _, call := range calls {
			if err := s.emitBracketTool(call); err != nil {
				return err
			}
		}
	}
	// 去重矩阵(parsers.py:151-211 及各调用点):原生工具经 get_tool_calls
	// (parsers.py:589-591)无条件去重,四路径皆然;openai 流收尾与括号恢复
	// 合并后再去重(streaming_openai.py:283-284、streaming_core.py:499-501);
	// 仅 anthropic 流式的括号恢复块追加不去重(streaming_anthropic.py:570-612)。
	if s.options.protocol == "anthropic" && s.options.stream && s.options.policyMode == "" {
		var native, bracket []*finishedTool
		for _, ft := range s.tools {
			if ft.bracket {
				bracket = append(bracket, ft)
			} else {
				native = append(native, ft)
			}
		}
		s.finalTools = append(dedupTools(native), bracket...)
	} else {
		s.finalTools = dedupTools(s.tools)
	}
	s.toolCount = len(s.finalTools)
	for _, ft := range s.finalTools {
		if s.options.policyMode != "" {
			if ft.name == "" || s.options.forbidTools || !s.options.allowedTools[ft.name] ||
				(s.options.policyMode == "named" && ft.name != s.options.policyTool) {
				return &toolViolation{msg: "response returned disallowed tool '" + ft.name + "'"}
			}
			if ft.invalid {
				return &toolViolation{msg: "tool '" + ft.name + "' returned malformed JSON arguments"}
			}
			if obj(ft.input) == nil {
				return &toolViolation{msg: "tool '" + ft.name + "' arguments must be a JSON object"}
			}
		}
		if ft.truncated && s.registersTruncation() {
			if id, ok := ft.id.(string); ok {
				saveToolTruncation(id, ft.name)
			}
		}
	}
	if s.options.stream && s.options.protocol == "anthropic" {
		// streaming_core.py:362-368 + streaming_anthropic.py:346-363/505-539:
		// anthropic 流式的工具块在正文全部流完之后统一发出;openBlock 先闭
		// 合未闭合的 thinking/text 块。空 id 回退 toolu_<24hex>
		// (streaming_anthropic.py:366);partial_json 用 ensure_ascii=False
		// 的 UTF-8 形式(streaming_anthropic.py:518)。
		for _, ft := range s.finalTools {
			id := ft.id
			if !toolIDTruthy(id) {
				id = "toolu_" + strings.ReplaceAll(newID(), "-", "")[:24]
			}
			partial, _ := normalizeOrderedJSON(ft.args, false)
			s.openBlock("tool_use", object{"type": "tool_use", "id": id, "name": ft.name, "input": object{}})
			s.send("content_block_delta", object{"type": "content_block_delta", "index": s.blockIndex, "delta": object{"type": "input_json_delta", "partial_json": partial}})
			s.closeBlock()
		}
	}
	if !s.options.stream {
		s.closeBlock()
		if s.options.protocol == "anthropic" {
			// streaming_anthropic.py:761-766: 非流式把全部 thinking 合并为
			// 单块置首、全部 text 合并为单块,thinking 无签名帧时生成
			// sig_ 占位;块顺序恒 thinking → text → tool_use。多个签名帧
			// 最后者胜(streaming_core.py:487-488 覆盖赋值)。
			var thinking, text strings.Builder
			sig := ""
			thinkingSeen, textSeen := false, false
			merged := make([]any, 0, len(s.blocks)+2)
			for _, b := range s.blocks {
				switch str(obj(b)["type"]) {
				case "thinking":
					thinkingSeen = true
					thinking.WriteString(str(obj(b)["thinking"]))
					if sg := str(obj(b)["signature"]); sg != "" {
						sig = sg
					}
				case "text":
					textSeen = true
					text.WriteString(str(obj(b)["text"]))
				default:
					merged = append(merged, b)
				}
			}
			s.blocks = make([]any, 0, len(merged)+2)
			if thinkingSeen {
				if s.thinkingSignature != "" {
					sig = s.thinkingSignature
				}
				if sig == "" {
					sig = fakeSignature()
				}
				s.blocks = append(s.blocks, object{"type": "thinking", "thinking": thinking.String(), "signature": sig})
			}
			if textSeen {
				s.blocks = append(s.blocks, object{"type": "text", "text": text.String()})
			}
			s.blocks = append(s.blocks, merged...)
		}
		// streaming_anthropic.py:761-786: 非流式内容块顺序恒为
		// thinking → text → tool_use。
		for _, ft := range s.finalTools {
			id := ft.id
			if !toolIDTruthy(id) && s.options.protocol == "anthropic" {
				// streaming_anthropic.py:785: 空 id 回退 toolu_<24hex>。
				id = "toolu_" + strings.ReplaceAll(newID(), "-", "")[:24]
			}
			s.blocks = append(s.blocks, object{"type": "tool_use", "id": id, "name": ft.name, "input": ft.input})
		}
	}
	if !s.terminal {
		// Clean EOF without completion/usage markers: the reference recovers a
		// nonempty generation as truncated output instead of failing the
		// request; the notice is injected into the next one.
		switch {
		case s.toolCount > 0:
		case s.meaningful:
			s.stopReason = "max_tokens"
			s.stopSequence = ""
			if s.registersTruncation() {
				saveContentTruncation(s.fullText.String())
			}
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
	id, name := str(call["toolUseId"]), restoreToolName(str(call["name"]))
	input := obj(call["input"])
	canonical := jsonText(input)
	// parsers.py:142: 括号工具 arguments 同走 json.dumps(json.loads) 归一;
	// 有原文时保序重编码,缺原文退回 map 序列化。
	if raw := str(call["raw"]); raw != "" {
		if norm, ok := normalizeOrderedJSON(raw, true); ok {
			canonical = norm
		}
	}
	s.tools = append(s.tools, &finishedTool{id: id, name: name, args: canonical, input: input, bracket: true})
	s.toolCount++
	if !s.options.stream {
		s.collectedBytes += len(canonical)
		if s.collectedBytes > maxResponseBytes {
			return fmt.Errorf("kiro: response exceeds collection limit")
		}
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

// registersTruncation 对齐参考实现的截断登记覆盖面:只在 anthropic 流式
// 生成器(streaming_anthropic.py:685-701)与 openai 流式生成器
// (streaming_openai.py:368-394,非流式经 collect_stream_response 复用同一
// 生成器)登记;anthropic 非流式与两条严格 tool_choice 路径
// (format_*_from_result)检测截断并改 stop_reason 但不登记,下请求不注入。
func (s *responseState) registersTruncation() bool {
	return s.options.policyMode == "" && (s.options.protocol == "openai" || s.options.stream)
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
	} else if s.options.policyMode != "" && s.hasContextPct && s.contextPct == 0 {
		// format_openai_response_from_result(streaming_openai.py:502-512)
		// 只在 pct 为 None 时回退请求估算;pct=0 经 streaming_core.py:528
		// 的 pct>0 守卫落 unknown 分支,prompt=0、total=completion。
		prompt, total = 0, out
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
		delta := object{}
		if len(s.finalTools) > 0 {
			// streaming_openai.py:340-368: 工具调用收尾前聚合为单个
			// tool_calls chunk(去重后的 finalTools),index 按序编号。
			toolCalls := make([]any, 0, len(s.finalTools))
			for i, ft := range s.finalTools {
				toolCalls = append(toolCalls, object{"index": i, "id": ft.id, "type": "function", "function": object{"name": ft.name, "arguments": ft.args}})
			}
			delta["tool_calls"] = toolCalls
		}
		if s.options.policyMode != "" && !s.sentRole {
			delta["role"], delta["content"] = "assistant", ""
			s.sentRole = true
		}
		if len(delta) > 0 {
			s.send("", s.chunk(delta, nil))
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
	// streaming_openai.py:794: 非流式 created 取收集完成后的 format 时刻;
	// 流式 chunk 用流开始时刻(s.created,streaming_openai.py:119)。
	return object{"id": s.id, "object": "chat.completion", "created": time.Now().Unix(), "model": s.options.model, "choices": []any{object{"index": 0, "message": message, "finish_reason": openai}}, "usage": s.openAIUsage()}
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
	if b.state.options.protocol == "anthropic" {
		// streaming_anthropic.py:726-732: 中段错误以 api_error 事件发出。
		b.state.send("error", object{"type": "error", "error": object{"type": "api_error", "message": "Internal error: " + err.Error()}})
	} else {
		// streaming_openai.py:431-441 + routes_openai.py:862-866: openai 不发
		// 错误事件,尽力补 [DONE] 后中断。
		b.pending.WriteString("data: [DONE]\n\n")
	}
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
