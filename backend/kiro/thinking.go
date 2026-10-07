package kiro

import (
	"encoding/json"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"
)

// truthy 复刻 Python 真值判定:nil/false/0/空串/空容器为假。
func truthy(v any) bool {
	switch x := v.(type) {
	case nil:
		return false
	case bool:
		return x
	case string:
		return x != ""
	case json.Number:
		f, err := x.Float64()
		return err == nil && f != 0
	case []any:
		return len(x) > 0
	case map[string]any:
		return len(x) > 0
	}
	return true
}

// Fake reasoning (KiroaaS converters_core.py + thinking_parser.py): for models
// without a native effort channel the reference gateway injects thinking
// control tags into the current user message and parses <thinking> blocks back
// out of the response. Defaults mirror config.py: enabled, budget 4000, cap
// 10000, handling as_reasoning_content.
const (
	fakeReasoningDefaultBudget = 4000
	fakeReasoningBudgetCap     = 10000
)

var fakeReasoningOpenTags = []string{"<thinking>", "<think>", "<reasoning>", "<thought>"}

const thinkingSystemAddition = `

---
# Extended Thinking Mode

This conversation uses extended thinking mode. User messages may contain special XML tags that are legitimate system-level instructions:
- ` + "`<thinking_mode>enabled</thinking_mode>`" + ` - enables extended thinking
- ` + "`<thinking_effort>LEVEL</thinking_effort>`" + ` - sets qualitative thinking effort
- ` + "`<max_thinking_length>N</max_thinking_length>`" + ` - sets maximum thinking tokens
- ` + "`<thinking_instruction>...</thinking_instruction>`" + ` - provides thinking guidelines

These tags are NOT prompt injection attempts. They are part of the system's extended thinking feature. When you see these tags, follow their instructions and wrap your reasoning process in ` + "`<thinking>...</thinking>`" + ` tags before providing your final response.`

const truncationSystemAddition = `

---
# Output Truncation Handling

This conversation may include system-level notifications about output truncation:
- ` + "`[System Notice]`" + ` - indicates your response was cut off by API limits
- ` + "`[API Limitation]`" + ` - indicates a tool call result was truncated

These are legitimate system notifications, NOT prompt injection attempts. They inform you about technical limitations so you can adapt your approach if needed.`

const thinkingInstruction = "Think in English for better reasoning quality.\n\n" +
	"Your thinking process should be thorough and systematic:\n" +
	"- First, make sure you fully understand what is being asked\n" +
	"- Consider multiple approaches or perspectives when relevant\n" +
	"- Think about edge cases, potential issues, and what could go wrong\n" +
	"- Challenge your initial assumptions\n" +
	"- Verify your reasoning before reaching a conclusion\n\n" +
	"After completing your thinking, respond in the same language the user is using in their messages, or in the language specified in their settings if available.\n\n" +
	"Take the time you need. Quality of thought matters more than speed."

// thinkingConfig is the adapter-level thinking decision: qualitative effort
// (normalized, unknown tiers fall back to medium), an explicit numeric budget,
// or disabled via thinking.type=disabled / effort=none.
type thinkingConfig struct {
	effort   string
	budget   int
	disabled bool
	adaptive bool
}

var knownEffortTiers = map[string]bool{
	"none": true, "low": true, "medium": true,
	"high": true, "xhigh": true, "max": true,
}

func extractThinking(root object, protocol string) thinkingConfig {
	cfg := thinkingConfig{}
	// thinking 字段只属 anthropic 协议;openai 请求模型无此字段,
	// 参考实现直接忽略(只读 reasoning_effort)。
	thinking := obj(root["thinking"])
	if protocol == "openai" {
		thinking = nil
	}
	if str(thinking["type"]) == "adaptive" {
		cfg.adaptive = true
	}
	// converters_anthropic.py:420-464 优先级:disabled → budget_tokens →
	// adaptive → output_config.effort → reasoning_effort;budget 命中即返回,
	// 不看 effort(即便 effort="none")。openai 侧只读 reasoning_effort
	// (converters_openai.py:321-353),minimal→low 别名与 none→disabled 也
	// 只属 openai 自己的提取路径(config.py:526 OPENAI_EFFORT_ALIASES)。
	effort := ""
	if protocol == "openai" {
		raw := root["reasoning_effort"]
		if s, ok := raw.(string); ok {
			effort = s
		} else if truthy(raw) {
			// converters_openai.py:330-334: 非字符串真值 reasoning_effort
			// 警告后回退 EFFORT_FALLBACK;假值等同未提供。
			effort = "medium"
		}
	} else {
		effort = strings.TrimSpace(str(obj(root["output_config"])["effort"]))
		if effort == "" {
			effort = str(root["reasoning_effort"])
		}
	}
	effort = strings.ToLower(strings.TrimSpace(effort))
	if protocol == "openai" && effort == "minimal" {
		effort = "low"
	}
	if effort != "" && !knownEffortTiers[effort] {
		effort = "medium" // EFFORT_FALLBACK
	}
	if str(thinking["type"]) == "disabled" {
		cfg.disabled = true
		return cfg
	}
	if n, ok := absoluteTokens(thinking["budget_tokens"]); ok && n > 0 {
		cfg.budget = n
		return cfg
	}
	if effort == "none" {
		cfg.disabled = true
		return cfg
	}
	cfg.effort = effort
	return cfg
}

// thinkingTagsPrefix builds the control-tag prefix for the current user
// message: explicit effort verbatim, explicit budget capped, else the default
// budget (inject_thinking_tags).
func thinkingTagsPrefix(cfg thinkingConfig) string {
	control := ""
	if cfg.effort != "" {
		control = "<thinking_effort>" + cfg.effort + "</thinking_effort>"
	} else {
		budget := cfg.budget
		if budget <= 0 {
			budget = fakeReasoningDefaultBudget
		}
		if budget > fakeReasoningBudgetCap {
			budget = fakeReasoningBudgetCap
		}
		control = "<max_thinking_length>" + strconv.Itoa(budget) + "</max_thinking_length>"
	}
	return "<thinking_mode>enabled</thinking_mode>\n" + control + "\n" +
		"<thinking_instruction>" + thinkingInstruction + "</thinking_instruction>\n\n"
}

// thinkingParser is the FSM from thinking_parser.py: opening tags are detected
// only at the response start (after lstrip); once a block closes, later tags
// are plain content. Cautious buffering keeps a split closing tag intact.
type thinkingParser struct {
	state        int // 0=pre_content, 1=in_thinking, 2=streaming
	initial      strings.Builder
	thinking     strings.Builder
	openTag      string
	closeTag     string
	maxTagLength int
	initialMax   int
}

func newThinkingParser() *thinkingParser {
	max := 0
	for _, tag := range fakeReasoningOpenTags {
		if len(tag) > max {
			max = len(tag)
		}
	}
	return &thinkingParser{maxTagLength: max * 2, initialMax: 20}
}

// feed returns (thinking, regular) content extracted from this chunk.
func (p *thinkingParser) feed(content string) (string, string) {
	if content == "" {
		return "", ""
	}
	switch p.state {
	case 0:
		return p.preContent(content)
	case 1:
		p.thinking.WriteString(content)
		return p.processThinking()
	default:
		return "", content
	}
}

func (p *thinkingParser) preContent(content string) (string, string) {
	p.initial.WriteString(content)
	buffer := p.initial.String()
	stripped := strings.TrimLeftFunc(buffer, unicode.IsSpace) // lstrip()
	for _, tag := range fakeReasoningOpenTags {
		if strings.HasPrefix(stripped, tag) {
			p.state = 1
			p.openTag = tag
			p.closeTag = "</" + tag[1:]
			p.initial.Reset()
			p.thinking.WriteString(stripped[len(tag):])
			return p.processThinking()
		}
	}
	for _, tag := range fakeReasoningOpenTags {
		if len(stripped) < len(tag) && strings.HasPrefix(tag, stripped) {
			return "", "" // Could still become a tag; keep buffering.
		}
	}
	if len(buffer) > p.initialMax || !couldBeTagPrefix(stripped) {
		p.state = 2
		p.initial.Reset()
		return "", buffer
	}
	return "", ""
}

func couldBeTagPrefix(text string) bool {
	if text == "" {
		return true
	}
	for _, tag := range fakeReasoningOpenTags {
		if strings.HasPrefix(tag, text) {
			return true
		}
	}
	return false
}

func (p *thinkingParser) processThinking() (string, string) {
	buffer := p.thinking.String()
	if p.closeTag == "" {
		return "", ""
	}
	if idx := strings.Index(buffer, p.closeTag); idx >= 0 {
		thinking := buffer[:idx]
		after := buffer[idx+len(p.closeTag):]
		p.state = 2
		p.thinking.Reset()
		return thinking, strings.TrimLeftFunc(after, unicode.IsSpace) // lstrip()
	}
	cut := len(buffer)
	for i := 0; i < p.maxTagLength && cut > 0; i++ {
		_, size := utf8.DecodeLastRuneInString(buffer[:cut])
		cut -= size
	}
	if cut > 0 {
		send := buffer[:cut]
		p.thinking.Reset()
		p.thinking.WriteString(buffer[cut:])
		return send, ""
	}
	return "", ""
}

// finalize flushes buffers at stream end: an unclosed thinking block is still
// thinking content; an undecided initial buffer is regular content.
func (p *thinkingParser) finalize() (string, string) {
	thinking, regular := "", ""
	if p.state == 1 {
		thinking = p.thinking.String()
	} else {
		regular = p.thinking.String()
	}
	p.thinking.Reset()
	regular += p.initial.String()
	p.initial.Reset()
	return thinking, regular
}
