package kiro

import (
	"encoding/json"
	"fmt"
	"io"
	"regexp"
	"strings"
	"unicode/utf8"
)

type object = map[string]any

func obj(v any) object      { m, _ := v.(map[string]any); return m }
func str(v any) string      { s, _ := v.(string); return s }
func list(v any) []any      { a, _ := v.([]any); return a }
func jsonText(v any) string { b, _ := json.Marshal(v); return string(b) }
func decodeObject(s string) (object, error) {
	var m object
	d := json.NewDecoder(strings.NewReader(s))
	d.UseNumber()
	if err := d.Decode(&m); err != nil {
		return nil, err
	}
	if m == nil {
		return nil, fmt.Errorf("expected JSON object")
	}
	var extra any
	if err := d.Decode(&extra); err != io.EOF {
		return nil, fmt.Errorf("unexpected trailing JSON")
	}
	return m, nil
}

type requestOptions struct {
	protocol, model string
	stream          bool
	inputEstimate   int
	allowedTools    map[string]bool
	forbidTools     bool
	fakeReasoning   bool
	effort          string
	// 严格 tool_choice(streaming_core.py validate_tool_choice_result):
	// policyMode 非空时整体缓冲校验,违规注入恢复指令重试一次。
	policyMode      string
	policyTool      string
	policyDirective string
}
type message struct {
	role, text            string
	images, uses, results []any
}

var (
	contextSuffix = regexp.MustCompile(`\[\d+[mk]\]$`)
	standardModel = regexp.MustCompile(`^(claude-(?:haiku|sonnet|opus)-\d+)-(\d{1,2})(?:-(?:\d{8}|latest|\d+))?$`)
	noMinorModel  = regexp.MustCompile(`^(claude-(?:haiku|sonnet|opus)-\d+)(?:-\d{8})?$`)
	legacyModel   = regexp.MustCompile(`^(claude)-(\d+)-(\d+)-(haiku|sonnet|opus)(?:-(?:\d{8}|latest|\d+))?$`)
	dotDateModel  = regexp.MustCompile(`^(claude-(?:\d+\.\d+-)?(?:haiku|sonnet|opus)(?:-\d+\.\d+)?)-\d{8}$`)
	invertedModel = regexp.MustCompile(`^claude-(\d+)\.(\d+)-(haiku|sonnet|opus)-(.+)$`)
)

// nativeModel normalizes client model names to Kiro IDs, following
// model_resolver.py:normalize_model_name (context suffix, dash/dot versions,
// date/latest suffixes, legacy and inverted orders). Unknown names pass
// through unchanged; the upstream is the arbiter (INVALID_MODEL_ID).
func nativeModel(s string) string {
	// config.py MODEL_ALIASES: auto-kiro 是 auto 的别名(规避 Cursor 内置
	// auto 模型冲突);别名解析先于名称归一(model_resolver.py Layer 0)。
	if s == "auto-kiro" {
		return "auto"
	}
	s = contextSuffix.ReplaceAllString(s, "")
	lower := strings.ToLower(s)
	if m := standardModel.FindStringSubmatch(lower); m != nil {
		return m[1] + "." + m[2]
	}
	if m := noMinorModel.FindStringSubmatch(lower); m != nil {
		return m[1]
	}
	if m := legacyModel.FindStringSubmatch(lower); m != nil {
		return m[1] + "-" + m[2] + "." + m[3] + "-" + m[4]
	}
	if m := dotDateModel.FindStringSubmatch(lower); m != nil {
		return m[1]
	}
	if m := invertedModel.FindStringSubmatch(lower); m != nil {
		return "claude-" + m[3] + "-" + m[1] + "." + m[2]
	}
	return s
}

// estimateText follows the reference's tokenizer-free fallback: four Unicode
// characters per token, plus one, with a 1.15 Claude correction. It is not BPE.
func estimateText(s string, claude bool) int {
	if s == "" {
		return 0
	}
	n := utf8.RuneCountInString(s)/4 + 1
	if claude {
		n = n * 115 / 100
	}
	return n
}

func convertRequest(raw []byte, protocol, profile string) (object, requestOptions, error) {
	opts := requestOptions{protocol: protocol, allowedTools: map[string]bool{}}
	fail := func(err error) (object, requestOptions, error) {
		return nil, opts, fmt.Errorf("kiro: invalid request: %w", err)
	}
	root, err := decodeObject(string(raw))
	if err != nil {
		return fail(err)
	}
	for _, key := range []string{"messages", "tools"} {
		if v := root[key]; v != nil {
			if _, ok := v.([]any); !ok {
				return fail(fmt.Errorf("%s must be an array", key))
			}
		}
	}
	opts.model = str(root["model"])
	if opts.model == "" {
		return fail(fmt.Errorf("model is required"))
	}
	opts.stream, _ = root["stream"].(bool)
	if v, ok := root["stream"]; ok {
		if _, ok := v.(bool); !ok {
			return fail(fmt.Errorf("stream must be boolean"))
		}
	}
	// streaming_openai.py: 收尾 usage 块恒发,stream_options.include_usage 忽略。
	if n, ok := root["n"]; ok && jsonText(n) != "1" {
		return fail(fmt.Errorf("multiple completions are not supported"))
	}
	system, err := textOnly(root["system"])
	if err != nil {
		return fail(err)
	}
	var tools []any
	for _, v := range list(root["tools"]) {
		t := obj(v)
		flat := false
		if protocol == "openai" {
			if typ := str(t["type"]); typ != "" && typ != "function" {
				return fail(fmt.Errorf("only function tools are supported"))
			}
			if fn := obj(t["function"]); fn != nil {
				t = fn
			} else if str(t["name"]) != "" {
				// converters_openai.py:290-296: Cursor 风格扁平定义
				// {name,description,input_schema},无 function 包裹。
				flat = true
			} else {
				return fail(fmt.Errorf("only function tools are supported"))
			}
		}
		name := str(t["name"])
		if name == "" || utf8.RuneCountInString(name) > 64 {
			return fail(fmt.Errorf("tool name must contain 1..64 characters"))
		}
		if opts.allowedTools[name] {
			return fail(fmt.Errorf("duplicate tool %q", name))
		}
		opts.allowedTools[name] = true
		schema := t["input_schema"]
		if protocol == "openai" && !flat {
			schema = t["parameters"]
		}
		if schema == nil {
			schema = object{"type": "object", "properties": object{}}
		}
		if obj(schema) == nil {
			return fail(fmt.Errorf("tool %q schema must be an object", name))
		}
		description := str(t["description"])
		if description == "" {
			description = "Tool: " + name
		}
		if utf8.RuneCountInString(description) > 10000 {
			system = joinText(system, "Tool documentation for "+name+":\n"+description)
			description = "See system instructions for documentation of " + name
		}
		tools = append(tools, object{"toolSpecification": object{"name": name, "description": description, "inputSchema": object{"json": sanitizeSchema(schema)}}})
	}
	// Kiro has no tool-choice field: auto/none map to tool presence, while
	// required/named are expressed as a [Tool Policy] system directive with
	// tool filtering (converters_core.py:ToolChoicePolicy).
	directive := ""
	if tc := root["tool_choice"]; tc != nil {
		mode, named := str(tc), ""
		if protocol == "openai" {
			if m := obj(tc); m != nil {
				if str(m["type"]) != "function" || obj(m["function"]) == nil {
					return fail(fmt.Errorf("OpenAI named tool_choice must contain type='function' and a function object"))
				}
				if named = str(obj(m["function"])["name"]); named == "" {
					return fail(fmt.Errorf("OpenAI named tool_choice requires a non-empty function.name"))
				}
				mode = "named"
			} else if mode != "auto" && mode != "none" && mode != "required" {
				return fail(fmt.Errorf("invalid OpenAI tool_choice value %q", mode))
			}
		} else {
			m := obj(tc)
			if m == nil {
				return fail(fmt.Errorf("invalid Anthropic tool_choice structure"))
			}
			switch t := str(m["type"]); t {
			case "auto", "none":
				if _, has := m["name"]; has {
					return fail(fmt.Errorf("Anthropic tool_choice type=%q must not include name", t))
				}
				mode = t
			case "any":
				if _, has := m["name"]; has {
					return fail(fmt.Errorf("Anthropic tool_choice type='any' must not include name"))
				}
				mode = "required"
			case "tool":
				if named = str(m["name"]); named == "" {
					return fail(fmt.Errorf("Anthropic tool_choice type='tool' requires a non-empty name"))
				}
				mode = "named"
			default:
				return fail(fmt.Errorf("invalid Anthropic tool_choice type %q", t))
			}
		}
		switch mode {
		case "auto":
		case "none":
			opts.forbidTools = true
			tools = nil
			directive = "\n\n[Tool Policy] For THIS response you must NOT call any tool. Reply with plain content only."
		case "required":
			if len(tools) == 0 {
				return fail(fmt.Errorf("tool_choice requires at least one tool, but no tools were provided"))
			}
			directive = "\n\n[Tool Policy] For THIS response you MUST call at least one tool. Do not reply with text only."
		case "named":
			if !opts.allowedTools[named] {
				return fail(fmt.Errorf("tool_choice references unknown tool %q", named))
			}
			filtered := []any{}
			for _, t := range tools {
				if str(obj(obj(t)["toolSpecification"])["name"]) == named {
					filtered = append(filtered, t)
				}
			}
			tools = filtered
			directive = "\n\n[Tool Policy] For THIS response you MUST call the tool named '" + named + "'. Do not call any other tool and do not reply with text only."
		}
		if mode != "" && mode != "auto" {
			opts.policyMode, opts.policyTool, opts.policyDirective = mode, named, directive
		}
	}
	var messages []message
	for _, v := range list(root["messages"]) {
		m := obj(v)
		role := str(m["role"])
		// converters_openai.py:169-173: 只有 system 进系统提示;developer 等
		// 其他角色经 normalize_message_roles 归一为 user 消息。
		if role == "system" {
			s, e := textOnly(m["content"])
			if e != nil {
				return fail(e)
			}
			system = joinText(system, s)
			continue
		}
		if role != "user" && role != "assistant" && !(protocol == "openai" && role == "tool") {
			// normalize_message_roles: unknown roles become user turns.
			role = "user"
			m["role"] = "user"
		}
		msg, e := parseMessage(m, protocol)
		if e != nil {
			return fail(e)
		}
		if len(messages) > 0 && messages[len(messages)-1].role == msg.role {
			last := &messages[len(messages)-1]
			last.text = joinText(last.text, msg.text)
			last.images = append(last.images, msg.images...)
			last.uses = append(last.uses, msg.uses...)
			last.results = append(last.results, msg.results...)
		} else {
			messages = append(messages, msg)
		}
	}
	if len(messages) == 0 {
		return fail(fmt.Errorf("messages must contain a user or assistant turn"))
	}
	if messages[0].role != "user" {
		messages = append([]message{{role: "user", text: "(empty placeholder)"}}, messages...)
	}
	if messages[len(messages)-1].role == "assistant" {
		messages = append(messages, message{role: "user", text: "(empty placeholder)"})
	}
	// Truncation recovery (truncation_state.py): a previous response that was
	// cut mid-stream earns a one-time synthetic notice in this request.
	messages = injectTruncationNotices(messages)
	// Historical tools without usable definitions are retained as text, not
	// silently discarded or sent as invalid native tool context.
	historyAsText := len(tools) == 0
	for _, m := range messages {
		for _, u := range m.uses {
			if !opts.allowedTools[str(obj(u)["name"])] {
				historyAsText = true
			}
		}
	}
	if historyAsText {
		for i := range messages {
			m := &messages[i]
			for _, u := range m.uses {
				m.text = joinText(m.text, "[Previous tool call] "+jsonText(u))
			}
			for _, r := range m.results {
				m.text = joinText(m.text, "[Previous tool result] "+jsonText(r))
			}
			m.uses = nil
			m.results = nil
		}
	} else {
		// Kiro 400s the whole request on unpaired tool context, so the
		// reference repairs instead of rejecting: orphan tool results (no
		// preceding assistant) become text; every assistant toolUse without a
		// result gets a synthetic placeholder (repair_unpaired_tool_uses).
		// Results with unknown/duplicate ids pass through for the upstream.
		for i := range messages {
			m := &messages[i]
			if len(m.results) == 0 || (i > 0 && messages[i-1].role == "assistant") {
				continue
			}
			for _, r := range m.results {
				m.text = joinText(m.text, "[Previous tool result] "+jsonText(r))
			}
			m.results = nil
		}
		for i := range messages {
			m := &messages[i]
			if len(m.uses) == 0 || i+1 >= len(messages) {
				continue
			}
			next := &messages[i+1]
			seen := map[string]bool{}
			for _, r := range next.results {
				seen[str(obj(r)["toolUseId"])] = true
			}
			for _, u := range m.uses {
				id := str(obj(u)["toolUseId"])
				if id != "" && !seen[id] {
					next.results = append(next.results, toolResult(id,
						"[gateway: tool result was not delivered by the client; "+
							"the tool likely produced an image or other media that was "+
							"moved into an adjacent user message.]"))
				}
			}
		}
	}
	cfg := extractThinking(root)
	opts.effort = cfg.effort
	model := nativeModel(opts.model)
	fields, e := effortFields(cfg, obj(root["thinking"]), model)
	if e != nil {
		return fail(e)
	}
	// effort_schema.py:171-175: 首 token 超时按钳制后的生效档位计算,
	// 不用请求原值。
	for _, key := range []string{"output_config", "reasoning"} {
		if eff := str(obj(fields[key])["effort"]); eff != "" {
			opts.effort = eff
		}
	}
	// Native effort/adaptive thinking wins; fake-reasoning tags are injected
	// only when no native channel is in play (NATIVE_EFFORT_SUPPRESS_TAGS).
	suppressTags := len(fields) > 0 || cfg.adaptive
	system += directive
	if !suppressTags {
		system += thinkingSystemAddition
	}
	system += truncationSystemAddition
	messages[0].text = joinText(system, messages[0].text)
	if current := &messages[len(messages)-1]; current.role == "user" && !cfg.disabled && !suppressTags {
		if current.text == "" {
			current.text = "(empty placeholder)"
		}
		current.text = thinkingTagsPrefix(cfg) + current.text
		opts.fakeReasoning = true
	}
	var history []any
	total := 3
	for i, m := range messages {
		total += 4 + estimateText(m.role, false) + estimateText(m.text, false) + 100*len(m.images)
		for _, u := range m.uses {
			total += 4 + estimateText(jsonText(u), false)
		}
		for _, r := range m.results {
			total += estimateText(jsonText(r), false)
		}
		if i < len(messages)-1 {
			history = append(history, nativeMessage(m, model, nil))
		}
	}
	for _, t := range tools {
		total += 4 + estimateText(jsonText(t), false)
	}
	if strings.HasPrefix(model, "claude") {
		total = total * 115 / 100
	}
	opts.inputEstimate = total
	state := object{"chatTriggerType": "MANUAL", "conversationId": newID(), "currentMessage": nativeMessage(messages[len(messages)-1], model, tools)}
	if len(history) > 0 {
		state["history"] = history
	}
	payload := object{"conversationState": state}
	if profile != "" {
		payload["profileArn"] = profile
	}
	if len(fields) > 0 {
		payload["additionalModelRequestFields"] = fields
	}
	return payload, opts, nil
}

func joinText(a, b string) string {
	if a == "" {
		return b
	}
	if b == "" {
		return a
	}
	return a + "\n\n" + b
}

// injectTruncationNotices applies one-time recovery state to this request:
// a user tool_result whose call was truncated gets the [API Limitation]
// notice prepended, and an assistant message matching a truncated generation
// is followed by a synthetic [System Notice] user turn (routes_anthropic.py).
func injectTruncationNotices(messages []message) []message {
	out := make([]message, 0, len(messages)+1)
	for _, m := range messages {
		for _, r := range m.results {
			result := obj(r)
			if !popToolTruncation(str(result["toolUseId"])) {
				continue
			}
			for _, c := range list(result["content"]) {
				block := obj(c)
				if _, ok := block["text"]; ok {
					block["text"] = truncationToolNotice + "\n\n---\n\nOriginal tool result:\n" + str(block["text"])
				}
			}
		}
		out = append(out, m)
		if m.role == "assistant" && m.text != "" && popContentTruncation(m.text) {
			out = append(out, message{role: "user", text: truncationUserMessage})
		}
	}
	return out
}
func textOnly(v any) (string, error) {
	if v == nil {
		return "", nil
	}
	if s, ok := v.(string); ok {
		return s, nil
	}
	a, ok := v.([]any)
	if !ok {
		return "", fmt.Errorf("content must be text or blocks")
	}
	var out string
	for _, v := range a {
		b := obj(v)
		if str(b["type"]) != "text" {
			return "", fmt.Errorf("unsupported text content block %q", str(b["type"]))
		}
		out += str(b["text"])
	}
	return out, nil
}
func parseMessage(m object, protocol string) (message, error) {
	result := message{role: str(m["role"])}
	if result.role == "tool" {
		result.role = "user"
		text, images, err := resultContent(m["content"])
		if err != nil {
			return result, err
		}
		result.images = images
		id := str(m["tool_call_id"])
		if id == "" {
			return result, fmt.Errorf("tool_call_id is required")
		}
		result.results = append(result.results, toolResult(id, text))
		return result, nil
	}
	if s, ok := m["content"].(string); ok {
		result.text = s
	} else if m["content"] != nil {
		blocks, ok := m["content"].([]any)
		if !ok {
			return result, fmt.Errorf("content must be text or blocks")
		}
		for _, v := range blocks {
			b := obj(v)
			switch str(b["type"]) {
			case "text":
				result.text += str(b["text"])
			case "thinking", "redacted_thinking": // Native history has no reasoning channel.
			case "tool_reference": // Claude Code 延迟工具标记(models_anthropic.py:128),Kiro 无对应物,忽略。
			case "image", "image_url":
				if result.role != "user" {
					return result, fmt.Errorf("images are only supported in user turns")
				}
				image, err := parseImage(b)
				if err != nil {
					return result, err
				}
				if image != nil {
					result.images = append(result.images, image)
				}
			case "tool_use":
				if protocol != "anthropic" || result.role != "assistant" {
					return result, fmt.Errorf("tool_use must be in an assistant turn")
				}
				u, err := toolUse(str(b["id"]), str(b["name"]), b["input"])
				if err != nil {
					return result, err
				}
				result.uses = append(result.uses, u)
			case "tool_result":
				if protocol != "anthropic" || result.role != "user" {
					return result, fmt.Errorf("tool_result must be in a user turn")
				}
				text, images, err := resultContent(b["content"])
				if err != nil {
					return result, err
				}
				result.images = append(result.images, images...)
				id := str(b["tool_use_id"])
				if id == "" {
					return result, fmt.Errorf("tool_use_id is required")
				}
				result.results = append(result.results, toolResult(id, text))
			default:
				return result, fmt.Errorf("unsupported content block %q", str(b["type"]))
			}
		}
	}
	for _, v := range list(m["tool_calls"]) {
		if protocol != "openai" || result.role != "assistant" {
			return result, fmt.Errorf("tool_calls must be in an assistant turn")
		}
		tc := obj(v)
		if str(tc["type"]) != "function" {
			return result, fmt.Errorf("only function tool calls are supported")
		}
		f := obj(tc["function"])
		u, err := toolUse(str(tc["id"]), str(f["name"]), f["arguments"])
		if err != nil {
			return result, err
		}
		result.uses = append(result.uses, u)
	}
	return result, nil
}
func resultContent(v any) (string, []any, error) {
	if v == nil {
		return "", nil, nil
	}
	if text, ok := v.(string); ok {
		return text, nil, nil
	}
	blocks, ok := v.([]any)
	if !ok {
		return "", nil, fmt.Errorf("tool result content must be text or blocks")
	}
	var text strings.Builder
	var images []any
	for _, v := range blocks {
		b := obj(v)
		switch str(b["type"]) {
		case "text":
			text.WriteString(str(b["text"]))
		case "image", "image_url":
			image, err := parseImage(b)
			if err != nil {
				return "", nil, err
			}
			if image != nil {
				images = append(images, image)
			}
		case "tool_reference": // converters_core.py:269-270: 跳过。
			continue
		default:
			return "", nil, fmt.Errorf("unsupported tool result block %q", str(b["type"]))
		}
	}
	return text.String(), images, nil
}

func toolUse(id, name string, input any) (object, error) {
	if id == "" || name == "" {
		return nil, fmt.Errorf("tool call id/name is required")
	}
	if s, ok := input.(string); ok {
		parsed, err := decodeObject(s)
		if err == nil {
			input = parsed
		}
	}
	if obj(input) == nil {
		input = object{} // coerce_tool_input_to_dict: non-object inputs degrade to {}
	}
	return object{"toolUseId": id, "name": name, "input": input}, nil
}

// converters_core.py:819/845: status 恒 "success",is_error 不入上行负载,
// 失败信息只经 content 文本传达给模型。
func toolResult(id, text string) object {
	if text == "" {
		text = "(empty result)"
	}
	return object{"toolUseId": id, "status": "success", "content": []any{object{"text": text}}}
}

// parseImage 对齐 converters_core.py 的宽松策略:URL 图片与空 data 跳过
// (返回 nil),data URL 前缀解析失败保留原始 data;媒体类型与 base64
// 合法性不在边界拒绝,交由上游判定。
func parseImage(b object) (object, error) {
	var media, data string
	if str(b["type"]) == "image" {
		src := obj(b["source"])
		if str(src["type"]) != "base64" {
			return nil, nil
		}
		media, data = str(src["media_type"]), str(src["data"])
	} else {
		data = str(obj(b["image_url"])["url"])
		if !strings.HasPrefix(data, "data:") {
			return nil, nil
		}
	}
	if strings.HasPrefix(data, "data:") {
		if prefix, rest, ok := strings.Cut(data, ","); ok {
			media = strings.TrimSuffix(strings.TrimPrefix(prefix, "data:"), ";base64")
			data = rest
		}
	}
	if data == "" {
		return nil, nil
	}
	if media == "" {
		media = "image/jpeg"
	}
	return object{"format": strings.TrimPrefix(media, "image/"), "source": object{"bytes": data}}, nil
}
func nativeMessage(m message, model string, tools []any) object {
	text := m.text
	if text == "" {
		text = "(empty placeholder)"
	}
	if m.role == "assistant" {
		a := object{"content": text}
		if len(m.uses) > 0 {
			a["toolUses"] = m.uses
		}
		return object{"assistantResponseMessage": a}
	}
	u := object{"content": text, "modelId": model, "origin": "AI_EDITOR"}
	if len(m.images) > 0 {
		u["images"] = m.images
	}
	context := object{}
	if len(tools) > 0 {
		context["tools"] = tools
	}
	if len(m.results) > 0 {
		context["toolResults"] = m.results
	}
	if len(context) > 0 {
		u["userInputMessageContext"] = context
	}
	return object{"userInputMessage": u}
}
func sanitizeSchema(v any) any {
	switch x := v.(type) {
	case map[string]any:
		out := object{}
		for k, v := range x {
			required, isArray := v.([]any)
			if k == "additionalProperties" || (k == "required" && isArray && len(required) == 0) {
				continue
			}
			if k == "properties" || k == "$defs" || k == "definitions" || k == "patternProperties" {
				if members, ok := v.(map[string]any); ok {
					preserved := object{}
					for name, schema := range members {
						preserved[name] = sanitizeSchema(schema)
					}
					out[k] = preserved
					continue
				}
			}
			out[k] = sanitizeSchema(v)
		}
		return out
	case []any:
		out := make([]any, len(x))
		for i, v := range x {
			out[i] = sanitizeSchema(v)
		}
		return out
	default:
		return v
	}
}
func effortFields(cfg thinkingConfig, thinking object, model string) (object, error) {
	path := ""
	allowed := []string{"low", "medium", "high", "xhigh", "max"}
	switch model {
	case "claude-opus-5", "claude-sonnet-5", "claude-opus-4.8":
		path = "output_config"
	case "claude-sonnet-4.6":
		path = "output_config"
		allowed = []string{"low", "medium", "high", "max"}
	case "gpt-5.5", "gpt-5.6-sol", "gpt-5.6-terra", "gpt-5.6-luna":
		path = "reasoning"
		allowed = append([]string{"none"}, allowed...)
	}
	fields := object{}
	if path == "" {
		return fields, nil
	} // No verified native field for this model.
	effort := cfg.effort
	if cfg.disabled {
		effort = "none"
	}
	if effort != "" {
		found := false
		for _, tier := range allowed {
			if effort == tier {
				found = true
			}
		}
		if !found && effort != "none" {
			ranks := []string{"low", "medium", "high", "xhigh", "max"}
			rank := -1
			for i, t := range ranks {
				if t == effort {
					rank = i
				}
			}
			adopted := "medium"
			if rank >= 0 {
				adopted = allowed[0]
				for i, t := range ranks {
					if i <= rank {
						for _, a := range allowed {
							if t == a {
								adopted = t
							}
						}
					}
				}
			}
			effort = adopted
			found = true
		}
		if found {
			fields[path] = object{"effort": effort}
		}
	}
	if cfg.adaptive {
		fields["thinking"] = thinking
	}
	return fields, nil
}
