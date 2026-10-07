package kiro

import (
	"encoding/json"
	"fmt"
	"io"
	"math/big"
	"regexp"
	"sort"
	"strconv"
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
	// estimateParts 是 inputEstimate 的 messages/tools/system 分解,
	// count_tokens 端点对各部分分别乘 1.15 校正(tokenizer.py:208-209)。
	estimateParts [3]int
	allowedTools  map[string]bool
	forbidTools   bool
	fakeReasoning bool
	effort        string
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
	contextSuffix = regexp.MustCompile(`(?i)\[\d+[mk]\]$`)
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

// pyDumps 是 json.dumps(ensure_ascii=False) 的估算用近似:带空格分隔符、
// 非 ASCII 原文。键序不影响长度。
func pyDumps(v any) string {
	s, _ := normalizeOrderedJSON(jsonText(v), false)
	return s
}

// estimateOriginalTokens 复刻 tokenizer.py estimate_request_tokens:基于原
// 始请求(messages/tools/system 未转换),返回三部分分解。openai 兜底不
// 含 system(streaming_openai.py:322-326)。
func estimateOriginalTokens(root object, protocol string) [3]int {
	parts := [3]int{estimateOriginalMessages(list(root["messages"])), estimateOriginalTools(list(root["tools"])), 0}
	if protocol != "openai" {
		parts[2] = estimateOriginalSystem(root["system"])
	}
	return parts
}

// tokenizer.py:110-210 count_message_tokens:每消息 +4、末尾 +3,块级分流
// (text/image=100/tool_use/tool_result/未知块 json.dumps/裸项 str())。
func estimateOriginalMessages(msgs []any) int {
	total := 0
	for _, v := range msgs {
		m := obj(v)
		total += 4
		total += estimateText(str(m["role"]), false)
		switch c := m["content"].(type) {
		case string:
			total += estimateText(c, false)
		case []any:
			for _, item := range c {
				b := obj(item)
				if b == nil {
					total += estimateText(pythonicString(item), false)
					continue
				}
				switch str(b["type"]) {
				case "text":
					total += estimateText(str(b["text"]), false)
				case "image", "image_url":
					total += 100
				case "tool_use":
					total += estimateText(str(b["id"]), false)
					total += estimateText(str(b["name"]), false)
					total += estimateText(pyDumps(b["input"]), false)
				case "tool_result":
					total += estimateText(str(b["tool_use_id"]), false)
					if ie := b["is_error"]; ie != nil {
						total += estimateText(pythonicString(ie), false)
					}
					switch rc := b["content"].(type) {
					case string:
						total += estimateText(rc, false)
					case []any:
						for _, rb := range rc {
							rbd := obj(rb)
							if rbd == nil {
								total += estimateText(pythonicString(rb), false)
								continue
							}
							// tokenizer.py:170-179: 内层只计 text 与图片,
							// 其余 dict 块忽略。
							switch str(rbd["type"]) {
							case "text":
								total += estimateText(str(rbd["text"]), false)
							case "image", "image_url":
								total += 100
							}
						}
					case nil:
					default:
						total += estimateText(pythonicString(rc), false)
					}
				default:
					total += estimateText(pyDumps(b), false)
				}
			}
		}
		for _, tv := range list(m["tool_calls"]) {
			tc := obj(tv)
			total += 4
			f := obj(tc["function"])
			total += estimateText(str(f["name"]), false)
			// tokenizer.py:198: arguments 按原文计;非字符串(无 pydantic
			// 校验)按 json.dumps 近似。
			switch a := f["arguments"].(type) {
			case string:
				total += estimateText(a, false)
			case nil:
			default:
				total += estimateText(pyDumps(a), false)
			}
		}
		total += estimateText(str(m["tool_call_id"]), false)
	}
	return total + 3
}

// tokenizer.py:213-253 count_tools_tokens:每工具 +4,兼容 openai 包裹与
// anthropic 扁平两种形态,input_schema/parameters 取 json.dumps。
func estimateOriginalTools(tools []any) int {
	total := 0
	for _, tv := range tools {
		t := obj(tv)
		total += 4
		payload := t
		if str(t["type"]) == "function" {
			if f := obj(t["function"]); f != nil {
				payload = f
			}
		}
		total += estimateText(str(payload["name"]), false)
		total += estimateText(str(payload["description"]), false)
		params := payload["input_schema"]
		if params == nil {
			params = payload["parameters"]
		}
		if params != nil {
			total += estimateText(pyDumps(params), false)
		}
	}
	return total
}

// tokenizer.py:256-293 count_system_tokens:字符串 / 块列表(含
// cache_control)/ 其他标量 str()。
func estimateOriginalSystem(sys any) int {
	total := 0
	switch s := sys.(type) {
	case string:
		total += estimateText(s, false)
	case []any:
		for _, item := range s {
			b := obj(item)
			if b == nil {
				total += estimateText(pythonicString(item), false)
				continue
			}
			total += estimateText(str(b["text"]), false)
			if cc := b["cache_control"]; cc != nil {
				total += estimateText(pyDumps(cc), false)
			}
		}
	case nil:
	default:
		total += estimateText(pythonicString(s), false)
	}
	return total
}

func convertRequest(raw []byte, protocol, profile string) (object, requestOptions, error) {
	// models_anthropic.py:404-425: count_tokens 的 pydantic 模型只声明
	// model/messages/system/tools(extra=allow),生成参数(stream/采样/
	// thinking/tool_choice/stop_sequences/metadata 等)不参与校验;
	// 消息与工具的结构校验(AnthropicMessage/AnthropicTool)依然生效。
	countTokens := protocol == "count_tokens"
	if countTokens {
		protocol = "anthropic"
	}
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
	// pydantic bool lax 强迫:true/false/1/0/yes/no/on/off/t/f/y/n(大小写
	// 不敏感)接受,其余 422。count_tokens 模型无 stream 字段(extra 忽略)。
	if !countTokens {
		if v, has := root["stream"]; has {
			var ok bool
			opts.stream, ok = pydanticBool(v)
			if !ok {
				return fail(fmt.Errorf("stream must be boolean"))
			}
		}
	}
	// models_anthropic.py:393-395: temperature/top_p ∈ [0,1]、top_k ≥ 0,
	// 越界、非数值与小数 top_k 在参考实现里 422(pydantic Field 校验)。
	if protocol == "anthropic" && !countTokens {
		for _, key := range []string{"temperature", "top_p"} {
			if v := root[key]; v != nil {
				if f, ok := pydanticFloat(v); !ok || !(f >= 0 && f <= 1) {
					return fail(fmt.Errorf("%s must be a number in [0, 1]", key))
				}
			}
		}
		if v := root["top_k"]; v != nil {
			if n, ok := pydanticInteger(v); !ok || n.Sign() < 0 {
				return fail(fmt.Errorf("top_k must be a non-negative integer"))
			}
		}
		// models_anthropic.py:382-386: thinking/output_config 须为对象,
		// reasoning_effort 须为字符串,类型错误 422。
		for _, key := range []string{"thinking", "output_config"} {
			if v := root[key]; v != nil && obj(v) == nil {
				return fail(fmt.Errorf("%s must be an object", key))
			}
		}
		// models_anthropic.py:347-349: AnthropicOutputConfig.effort 为
		// Optional[str],pydantic v2 lax 不强迫数字/布尔为 str,类型错误 422。
		if oc := obj(root["output_config"]); oc != nil {
			if e, has := oc["effort"]; has && e != nil {
				if _, ok := e.(string); !ok {
					return fail(fmt.Errorf("output_config.effort must be a string"))
				}
			}
		}
		if v := root["reasoning_effort"]; v != nil {
			if _, ok := v.(string); !ok {
				return fail(fmt.Errorf("reasoning_effort must be a string"))
			}
		}
		// models_anthropic.py:398-399: stop_sequences 为 List[str]、
		// metadata 为 Dict——字段本不上行,仅状态码对齐(422)。
		if v := root["stop_sequences"]; v != nil {
			items, ok := v.([]any)
			if !ok {
				return fail(fmt.Errorf("stop_sequences must be a list of strings"))
			}
			for _, item := range items {
				if _, ok := item.(string); !ok {
					return fail(fmt.Errorf("stop_sequences must be a list of strings"))
				}
			}
		}
		if v := root["metadata"]; v != nil && obj(v) == nil {
			return fail(fmt.Errorf("metadata must be an object"))
		}
	}
	// models_openai.py:158-165: 数值字段只强迫类型不约束范围,非数值 422;
	// n/max_tokens/max_completion_tokens 须为整数。
	if protocol == "openai" {
		for _, key := range []string{"temperature", "top_p", "presence_penalty", "frequency_penalty"} {
			if v := root[key]; v != nil {
				if _, ok := pydanticFloat(v); !ok {
					return fail(fmt.Errorf("%s must be a number", key))
				}
			}
		}
		for _, key := range []string{"n", "max_tokens", "max_completion_tokens", "top_logprobs", "seed"} {
			if v := root[key]; v != nil {
				if _, ok := pydanticInteger(v); !ok {
					return fail(fmt.Errorf("%s must be an integer", key))
				}
			}
		}
		for _, key := range []string{"logprobs", "parallel_tool_calls"} {
			if v := root[key]; v != nil {
				if _, ok := pydanticBool(v); !ok {
					return fail(fmt.Errorf("%s must be boolean", key))
				}
			}
		}
		if v := root["stream_options"]; v != nil && obj(v) == nil {
			return fail(fmt.Errorf("stream_options must be an object"))
		}
		if v := root["user"]; v != nil {
			if _, ok := v.(string); !ok {
				return fail(fmt.Errorf("user must be a string"))
			}
		}
		// models_openai.py:163/177: stop 为 Union[str, List[str]]、
		// logit_bias 为 Dict[str, float]——字段本不上行,仅状态码对齐(422)。
		if v := root["stop"]; v != nil {
			if _, ok := v.(string); !ok {
				items, ok := v.([]any)
				if !ok {
					return fail(fmt.Errorf("stop must be a string or a list of strings"))
				}
				for _, item := range items {
					if _, ok := item.(string); !ok {
						return fail(fmt.Errorf("stop must be a string or a list of strings"))
					}
				}
			}
		}
		if v := root["logit_bias"]; v != nil {
			m := obj(v)
			if m == nil {
				return fail(fmt.Errorf("logit_bias must be an object"))
			}
			for _, val := range m {
				if _, ok := pydanticFloat(val); !ok {
					return fail(fmt.Errorf("logit_bias values must be numbers"))
				}
			}
		}
	}
	// streaming_openai.py: 收尾 usage 块恒发,stream_options.include_usage 忽略。
	// models_openai.py:160: n 字段被接受但从不读取(n≠1 静默返回单条),
	// 不在边界拒绝。
	// converters_anthropic.py:106-116: 顶层 system 的多个文本块 "\n" 连接。
	// 该字段只属 anthropic 协议;openai 模型无此字段,参考实现直接忽略。
	system := ""
	if protocol == "anthropic" {
		var err error
		system, err = systemPromptText(root["system"])
		if err != nil {
			return fail(err)
		}
		// extensions/billing_header_strip.py:42-62(app_entry 恒装,默认开):
		// 剥掉 Claude Code 注入的系统提示首行 billing 归属(cch 每请求随机,
		// 会毒化上游缓存前缀)。
		system = stripBillingHeader(system)
	}
	var tools []any
	var toolDocs []string
	// extensions/tool_name_alias.py:68-78(app_entry 恒装):先登记本轮全部
	// 工具名——合法名进保留集防止被长名别名抢占,非法/超长名取
	// t_<sha256[:12]>_<suffix> 别名上行,响应侧恢复原名。
	var toolNames []string
	for _, v := range list(root["tools"]) {
		t := obj(v)
		if t == nil {
			return fail(fmt.Errorf("tool definitions must be objects"))
		}
		for _, key := range []string{"description", "type"} {
			if value := t[key]; value != nil {
				if _, ok := value.(string); !ok {
					return fail(fmt.Errorf("tool %s must be a string", key))
				}
			}
		}
		if value := t["input_schema"]; value != nil && obj(value) == nil {
			return fail(fmt.Errorf("tool input_schema must be an object"))
		}
		if protocol == "anthropic" {
			if value := t["max_uses"]; value != nil {
				if _, ok := pydanticInteger(value); !ok {
					return fail(fmt.Errorf("tool max_uses must be an integer"))
				}
			}
			for _, key := range []string{"allowed_domains", "blocked_domains"} {
				if value := t[key]; value != nil {
					items, ok := value.([]any)
					if !ok {
						return fail(fmt.Errorf("tool %s must be a list of strings", key))
					}
					for _, item := range items {
						if _, ok := item.(string); !ok {
							return fail(fmt.Errorf("tool %s must be a list of strings", key))
						}
					}
				}
			}
			if value := t["user_location"]; value != nil && obj(value) == nil {
				return fail(fmt.Errorf("tool user_location must be an object"))
			}
		}
		if protocol == "openai" {
			if value := t["name"]; value != nil {
				if _, ok := value.(string); !ok {
					return fail(fmt.Errorf("tool name must be a string"))
				}
			}
			if value := t["function"]; value != nil {
				fn := obj(value)
				if fn == nil {
					return fail(fmt.Errorf("tool function must be an object"))
				}
				if _, ok := fn["name"].(string); !ok {
					return fail(fmt.Errorf("tool function name is required"))
				}
				if desc := fn["description"]; desc != nil {
					if _, ok := desc.(string); !ok {
						return fail(fmt.Errorf("tool function description must be a string"))
					}
				}
				if params := fn["parameters"]; params != nil && obj(params) == nil {
					return fail(fmt.Errorf("tool function parameters must be an object"))
				}
			}
			// models_openai.py:117: type 为 str(缺省 "function"),pydantic
			// v2 lax 不强迫非字符串与 null,整请求 422;字符串非 function
			// 的条目由 converters_openai.py:280 跳过。
			if tv, has := t["type"]; has {
				s, ok := tv.(string)
				if !ok {
					return fail(fmt.Errorf("tool type must be a string"))
				}
				if s != "function" {
					continue
				}
			}
			if fn := obj(t["function"]); fn != nil {
				t = fn
			} else if t["name"] == nil {
				continue
			}
		}
		if n, ok := t["name"].(string); ok {
			toolNames = append(toolNames, n)
		}
	}
	registerToolNames(toolNames)
	for _, v := range list(root["tools"]) {
		t := obj(v)
		flat := false
		if protocol == "openai" {
			if typ, has := t["type"]; has && str(typ) != "function" {
				continue
			}
			if fn := obj(t["function"]); fn != nil {
				t = fn
			} else if t["name"] != nil {
				flat = true
			} else {
				continue
			}
		}
		name, nameOK := t["name"].(string)
		if !nameOK {
			// models_anthropic.py / models_openai.py: 工具 name 必填字符串。
			return fail(fmt.Errorf("tool name is required"))
		}
		// allowedTools 键保持客户端原名(resolve_anthropic_tool_choice 的
		// "client-visible allowed names":tool_choice 解析与响应校验都在
		// 别名前用原名);Go 仍拒重名(B2 边界更严,保留)。
		if opts.allowedTools[name] {
			return fail(fmt.Errorf("duplicate tool %q", name))
		}
		opts.allowedTools[name] = true
		// tool_name_alias.py:126-143: 宽化部署不再拒绝超长/非法名,统一
		// 换别名上行;描述占位与长描述迁移段也用别名(参考实现在
		// build_kiro_payload 的 convert_tools_to_kiro_format 里先别名后
		// 转换)。
		name = aliasToolName(name)
		schema := t["input_schema"]
		if protocol == "openai" && !flat {
			schema = t["parameters"]
		}
		if schema == nil {
			// models_anthropic.py:275-287: anthropic 自定义工具(无 type 字段)
			// 缺 input_schema 拒绝;其余缺省走 sanitize_schema 的空表兜底
			// (converters_core.py:536-537: not schema → {})。
			if protocol == "anthropic" && t["type"] == nil {
				return fail(fmt.Errorf("input_schema is required for user-defined tool %q", name))
			}
			schema = object{}
		}
		if obj(schema) == nil {
			return fail(fmt.Errorf("tool %q schema must be an object", name))
		}
		description := str(t["description"])
		if strings.TrimSpace(description) == "" {
			// converters_core.py:701-704: 纯空白描述同样换占位。
			description = "Tool: " + name
		}
		if utf8.RuneCountInString(description) > 10000 {
			// converters_core.py:616-636: 超长描述迁入系统提示的
			// "# Tool Documentation" 段,工具上只留指引占位。
			toolDocs = append(toolDocs, "## Tool: "+name+"\n\n"+description)
			description = "[Full documentation in system prompt under '## Tool: " + name + "']"
		}
		tools = append(tools, object{"toolSpecification": object{"name": name, "description": description, "inputSchema": object{"json": sanitizeSchema(schema)}}})
	}
	// Kiro has no tool-choice field: auto/none map to tool presence, while
	// required/named are expressed as a [Tool Policy] system directive with
	// tool filtering (converters_core.py:ToolChoicePolicy).
	directive := ""
	if tc := root["tool_choice"]; tc != nil && !countTokens {
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
			// resolve_anthropic_tool_choice(converters_anthropic.py:377-385):
			// 策略解析与过滤都在别名前用客户端原名,被选中工具在
			// build_kiro_payload 才别名——长名 named 同样合法。
			if !opts.allowedTools[named] {
				return fail(fmt.Errorf("tool_choice references unknown tool %q", named))
			}
			namedAlias := aliasToolName(named)
			filtered := []any{}
			for _, t := range tools {
				if str(obj(obj(t)["toolSpecification"])["name"]) == namedAlias {
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
	var systemMsgs []string
	lastWasTool := false
	for _, v := range list(root["messages"]) {
		m := obj(v)
		role := str(m["role"])
		// models_openai.py: role 必填且须为字符串,缺省/null/非字符串 422。
		if protocol == "openai" {
			if _, ok := m["role"].(string); !ok {
				return fail(fmt.Errorf("role is required"))
			}
		}
		// models_anthropic.py:229 + extensions/role_widening.py:44-46(app_entry
		// 部署恒装):role 宽化为 Literal["user","assistant","system"],新版
		// Claude Code 的内联 system 消息(system-reminder 等)被接受,经
		// normalize_message_roles 折为 user;其余角色仍被 422 拒绝。
		if protocol == "anthropic" && role != "user" && role != "assistant" && role != "system" {
			return fail(fmt.Errorf("role must be user, assistant or system"))
		}
		// converters_openai.py:169-175: 只有 openai 的 system 进系统提示,
		// 多条 "\n" 连接并整体 strip;developer 等未知角色按 user 解析,但
		// 归一化在合并之后(converters_core.py:1728-1740: merge → 首条
		// user → normalize → alternating)。anthropic 内联 system 不进系统
		// 提示(系统提示只读 root.system),作为普通消息走流水线。
		if protocol == "openai" && role == "system" {
			s, e := textOnly(m["content"])
			if e != nil {
				return fail(e)
			}
			systemMsgs = append(systemMsgs, s)
			continue
		}
		parseable := role == "user" || role == "assistant" || (protocol == "openai" && role == "tool")
		if !parseable {
			// anthropic 内联 system 保留原角色:提取门控只认 user/assistant
			// (converters_anthropic.py:297-319),system 消息只贡献文本,
			// 且合并分组按原始角色(system 不与相邻 user 合并)。
			if protocol != "anthropic" || role != "system" {
				m["role"] = "user"
			}
		}
		msg, e := parseMessage(m, protocol)
		if e != nil {
			return fail(e)
		}
		if protocol == "openai" && role == "tool" && lastWasTool {
			// converters_openai.py:186-212: 连续 tool 消息聚成一条 user,
			// 不经过同角色合并(避免空文本 "\n" 连接 artifact)。
			last := &messages[len(messages)-1]
			last.results = append(last.results, msg.results...)
			last.images = append(last.images, msg.images...)
			continue
		}
		if !parseable {
			// 原始角色保留给 merge 分组:归一化在 merge 之后
			// (converters_core.py:1728-1740),此前不与 user 合并。
			msg.role = role
		}
		messages = append(messages, msg)
		lastWasTool = protocol == "openai" && role == "tool"
	}
	if len(systemMsgs) > 0 {
		system = strings.TrimSpace(strings.Join(systemMsgs, "\n"))
	}
	if len(messages) == 0 {
		return fail(fmt.Errorf("messages must contain a user or assistant turn"))
	}
	// Truncation recovery (truncation_state.py): a previous response that was
	// cut mid-stream earns a one-time synthetic notice in this request.
	// routes_anthropic.py:230-254 在原始消息上注入,随后走整条流水线
	// (strip/孤儿判定 → merge → 首条 user → normalize → alternating →
	// repair),与参考实现同序。
	messages = injectTruncationNotices(messages)
	// converters_core.py:1712-1725: 工具内容剥离与孤儿 results 文本化在
	// merge(1728)之前逐消息判定——带 results 的 user 前驱是无 results 的
	// user 时按孤儿文本化,不会被 merge 后的相邻关系救回。
	// "已声明"按过滤后实际上行的工具集判定(tool_choice=named 只留被选
	// 工具,未选工具的历史调用同样降级为文本)。
	declared := map[string]bool{}
	for _, t := range tools {
		declared[str(obj(obj(t)["toolSpecification"])["name"])] = true
	}
	historyAsText := len(tools) == 0
	for _, m := range messages {
		for _, u := range m.uses {
			if !declared[str(obj(u)["name"])] {
				historyAsText = true
			}
		}
	}
	if historyAsText {
		// strip_all_tool_content(converters_core.py:1032-1113): 全部工具
		// 内容转文本,原正文 → tool_calls → tool_results 顺序 "\n\n" 连接。
		for i := range messages {
			m := &messages[i]
			m.text = joinText(m.text, toolCallsToText(m.uses))
			m.text = joinText(m.text, toolResultsToText(m.results))
			m.uses = nil
			m.results = nil
		}
	} else {
		// ensure_assistant_before_tool_results(converters_core.py:1116-1189):
		// 前驱(原始顺序)不是带 toolUses 的 assistant 时,孤儿 results
		// 文本化——无法合成合法 assistant(工具名未知)。
		for i := range messages {
			m := &messages[i]
			if len(m.results) == 0 || (i > 0 && messages[i-1].role == "assistant" && len(messages[i-1].uses) > 0) {
				continue
			}
			m.text = joinText(m.text, toolResultsToText(m.results))
			m.results = nil
		}
	}
	// merge_adjacent_messages(converters_core.py:1192-1274): 同角色相邻
	// 合并,文本用单个 "\n" 无条件连接;未归一角色按原始角色分组。
	merged := make([]message, 0, len(messages))
	for _, m := range messages {
		if len(merged) > 0 && merged[len(merged)-1].role == m.role {
			last := &merged[len(merged)-1]
			last.text = last.text + "\n" + m.text
			// converters_core.py:1220-1249: merge 只并 content/tool_calls/
			// tool_results,不并 images——被并消息的图片静默丢弃。
			last.uses = append(last.uses, m.uses...)
			last.results = append(last.results, m.results...)
		} else {
			merged = append(merged, m)
		}
	}
	messages = merged
	if messages[0].role != "user" {
		messages = append([]message{{role: "user", text: "(empty placeholder)"}}, messages...)
	}
	// normalize_message_roles + ensure_alternating_roles: 未知角色归一为 user
	// 后,相邻 user 之间插合成 assistant 占位(Kiro 要求角色交错)。
	for i := range messages {
		if messages[i].role != "user" && messages[i].role != "assistant" {
			messages[i].role = "user"
		}
	}
	alternating := make([]message, 0, len(messages)+2)
	for i, m := range messages {
		if i > 0 && m.role == "user" && alternating[len(alternating)-1].role == "user" {
			alternating = append(alternating, message{role: "assistant", text: "(empty placeholder)"})
		}
		alternating = append(alternating, m)
	}
	messages = alternating
	if !historyAsText {
		// Kiro 400s the whole request on unpaired tool context, so the
		// reference repairs instead of rejecting: every assistant toolUse
		// without a result gets a synthetic placeholder
		// (repair_unpaired_tool_uses, converters_core.py:1745). Results with
		// unknown/duplicate ids pass through for the upstream.
		for i := range messages {
			m := &messages[i]
			if len(m.uses) == 0 {
				continue
			}
			if i+1 >= len(messages) {
				// converters_core.py:1406-1409: assistant 后无 user 消息时
				// 插入合成 user 承载占位 toolResults——末条 assistant 的
				// toolUses 因此保留在 history,而不是被 trailing 处理丢弃。
				synthetic := message{role: "user"}
				for _, u := range m.uses {
					// converters_core.py:1413-1420: 真值 id 原样参与配对,
					// 非字符串 id 同样合成占位。
					if id := obj(u)["toolUseId"]; truthy(id) {
						synthetic.results = append(synthetic.results, toolResult(id,
							"[gateway: tool result was not delivered by the client; "+
								"the tool likely produced an image or other media that was "+
								"moved into an adjacent user message.]"))
					}
				}
				if len(synthetic.results) > 0 {
					messages = append(messages, synthetic)
				}
				break
			}
			next := &messages[i+1]
			seen := map[string]bool{}
			for _, r := range next.results {
				// 配对按原始 id 值(repair_unpaired_tool_uses 用 dict.get
				// 原值集合),JSON 文本作规范化键。
				if id := obj(r)["toolUseId"]; truthy(id) {
					seen[jsonText(id)] = true
				}
			}
			for _, u := range m.uses {
				id := obj(u)["toolUseId"]
				if truthy(id) && !seen[jsonText(id)] {
					next.results = append(next.results, toolResult(id,
						"[gateway: tool result was not delivered by the client; "+
							"the tool likely produced an image or other media that was "+
							"moved into an adjacent user message.]"))
				}
			}
		}
	}
	trailingAssistant := false
	if messages[len(messages)-1].role == "assistant" {
		// converters_core.py:1772-1778: 末条 assistant 进 history 时 toolUses
		// 静默丢弃,current 换成占位 user。带 toolUses 的末条 assistant
		// 已被上面的 repair 用合成 user 接住,走不到这里。
		messages[len(messages)-1].uses = nil
		messages = append(messages, message{role: "user", text: "(empty placeholder)"})
		trailingAssistant = true
	}
	cfg := extractThinking(root, protocol)
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
	// 参考实现的系统段拼接模式:已有内容直接追加,空系统则 strip 该段
	// (converters_anthropic.py:510、converters_core.py:1702-1712)。
	appendSystem := func(add string) {
		if system == "" {
			system = strings.TrimSpace(add)
		} else {
			system += add
		}
	}
	appendSystem(directive)
	if len(toolDocs) > 0 {
		appendSystem("\n\n---\n# Tool Documentation\nThe following tools have detailed documentation that couldn't fit in the tool definition.\n\n" + strings.Join(toolDocs, "\n\n---\n\n"))
	}
	if !suppressTags {
		appendSystem(thinkingSystemAddition)
	}
	appendSystem(truncationSystemAddition)
	// converters_core.py:1754-1758: 系统提示无条件 "\n\n" 前缀进首条
	// user(空正文也会留下尾部 "\n\n")。
	if system != "" {
		messages[0].text = system + "\n\n" + messages[0].text
	}
	// converters_core.py:1815-1819: tags 只注入原本的末条 user;末条是
	// assistant 时占位 user 不注入。
	if current := &messages[len(messages)-1]; current.role == "user" && !trailingAssistant && !cfg.disabled && !suppressTags {
		if current.text == "" {
			current.text = "(empty placeholder)"
		}
		current.text = thinkingTagsPrefix(cfg) + current.text
		opts.fakeReasoning = true
	}
	var history []any
	for i, m := range messages {
		if i < len(messages)-1 {
			history = append(history, nativeMessage(m, model, nil))
		}
	}
	// tokenizer.py estimate_request_tokens: 输入估算基于原始请求
	// (messages/tools/system 未转换,不含注入段与合成占位),usage 兜底
	// 不乘校正系数(streaming_anthropic.py:178-183、
	// streaming_openai.py:322-326);count_tokens 端点在 transport 侧对
	// 三部分分别乘 1.15(routes_anthropic.py:1124、tokenizer.py:208-209)。
	opts.estimateParts = estimateOriginalTokens(root, protocol)
	opts.inputEstimate = opts.estimateParts[0] + opts.estimateParts[1] + opts.estimateParts[2]
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

// converters_core.py:977-979/1021-1023 的逐字标记格式:工具上下文转文本时
// id 为空省略括号段,空结果占位 "(empty result)",多个之间 "\n\n" 分隔。
func toolCallsToText(uses []any) string {
	parts := []string{}
	for _, u := range uses {
		o := obj(u)
		name := str(o["name"])
		// converters_core.py:972: 文本化用 unified arguments 原文(见
		// toolUse 调用方);缺键时退回 coerce 后 input 的 JSON。
		args, has := o["argsText"].(string)
		if !has {
			args = jsonText(o["input"])
		}
		if id := o["toolUseId"]; truthy(id) {
			parts = append(parts, "[Tool: "+name+" ("+pythonicString(id)+")]\n"+args)
		} else {
			parts = append(parts, "[Tool: "+name+"]\n"+args)
		}
	}
	return strings.Join(parts, "\n\n")
}
func toolResultsToText(results []any) string {
	parts := []string{}
	for _, r := range results {
		o := obj(r)
		content := ""
		for _, c := range list(o["content"]) {
			content += str(obj(c)["text"])
		}
		if content == "" {
			content = "(empty result)"
		}
		if id := o["toolUseId"]; truthy(id) {
			parts = append(parts, "[Tool Result ("+pythonicString(id)+")]\n"+content)
		} else {
			parts = append(parts, "[Tool Result]\n"+content)
		}
	}
	return strings.Join(parts, "\n\n")
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

// textOnly 是 openai system 角色消息的内容提取,走 extract_text_content 的
// 宽松语义(converters_core.py:265-282):图片与 tool_reference 跳过,未知
// 字典块收割 text 键,裸字符串拼接,标量按 Python str() 收场。
func textOnly(v any) (string, error) {
	if v == nil {
		return "", nil
	}
	if s, ok := v.(string); ok {
		return s, nil
	}
	a, ok := v.([]any)
	if !ok {
		return pythonicString(v), nil
	}
	var out string
	for _, v := range a {
		if s, ok := v.(string); ok {
			out += s
			continue
		}
		b := obj(v)
		if b == nil {
			continue
		}
		switch str(b["type"]) {
		case "text":
			out += str(b["text"])
		case "image", "image_url", "tool_reference":
			continue
		default:
			out += str(b["text"])
		}
	}
	return out, nil
}

// systemPromptText 是顶层 system 字段的提取:字符串原样,块列表只收
// type=text 的块"\n"连接,其余块静默跳过(converters_anthropic.py:105-117);
// 标量与非对象块项由 pydantic 联合类型在边界拒绝(models_anthropic.py:343)。
// billingHeaderLine 对齐 billing_header_strip.py:42-44:只匹配系统提示首行
// 的 billing 归属行及其换行,大小写不敏感。
var billingHeaderLine = regexp.MustCompile(`(?i)^x-anthropic-billing-header:[^\n]*\n?`)

// stripBillingHeader 剥掉首行 billing 归属;有剥离时再去掉顶部残留空行
// (billing_header_strip.py:54-62)。
func stripBillingHeader(s string) string {
	if s == "" {
		return s
	}
	stripped := billingHeaderLine.ReplaceAllString(s, "")
	if stripped != s {
		return strings.TrimLeft(stripped, "\n")
	}
	return s
}

func systemPromptText(v any) (string, error) {
	if v == nil {
		return "", nil
	}
	if s, ok := v.(string); ok {
		return s, nil
	}
	a, ok := v.([]any)
	if !ok {
		// models_anthropic.py:343: SystemPrompt=Union[str,
		// List[SystemContentBlock],List[Dict]]——标量系统提示 422。
		return "", fmt.Errorf("system must be a string or a list of content blocks")
	}
	parts := []string{}
	typed := true
	for _, v := range a {
		b := obj(v)
		if b == nil {
			return "", fmt.Errorf("system blocks must be objects")
		}
		if _, ok := b["text"].(string); !ok {
			typed = false
		}
		if typ, has := b["type"]; has && typ != "text" {
			typed = false
		}
		if cc := b["cache_control"]; cc != nil && obj(cc) == nil {
			typed = false
		}
	}
	for _, v := range a {
		b := obj(v)
		if typed || str(b["type"]) == "text" {
			parts = append(parts, str(b["text"]))
		}
	}
	return strings.Join(parts, "\n"), nil
}
func anthropicImageSource(src object) (object, int, bool) {
	for _, kind := range []string{"base64", "url"} {
		if typ, has := src["type"]; has && typ != kind {
			continue
		}
		keys := []string{"media_type", "data"}
		if kind == "url" {
			keys = []string{"url"}
		}
		out, score, valid := object{"type": kind}, 0, true
		if _, has := src["type"]; has {
			score += 2
		}
		for _, key := range keys {
			if _, ok := src[key].(string); !ok {
				valid = false
				break
			}
			out[key] = src[key]
			score += 2
		}
		if valid {
			return out, score, true
		}
	}
	return nil, 0, false
}

func anthropicBlock(b object, nested bool) (object, int, bool) {
	var best object
	bestScore := -1
	for _, kind := range []string{"text", "thinking", "image", "tool_use", "tool_result", "tool_reference", "server_tool_use", "web_search_tool_result", "unknown"} {
		if nested && kind != "text" && kind != "image" && kind != "tool_reference" {
			continue
		}
		if typ, has := b["type"]; has {
			if _, ok := typ.(string); !ok || (kind != "unknown" && typ != kind) {
				continue
			}
		} else if kind == "unknown" {
			continue
		}
		required, optional := []string{}, []string{}
		extra := false
		switch kind {
		case "text":
			required = []string{"text"}
		case "thinking":
			required, optional = []string{"thinking"}, []string{"signature"}
		case "image":
			required = []string{"source"}
		case "tool_use":
			required = []string{"id", "name", "input"}
		case "tool_result":
			required, optional, extra = []string{"tool_use_id"}, []string{"content", "is_error"}, true
		case "tool_reference":
			required, extra = []string{"tool_name"}, true
		case "server_tool_use":
			required, optional, extra = []string{"id", "name"}, []string{"input"}, true
		case "web_search_tool_result":
			required, optional, extra = []string{"tool_use_id"}, []string{"content"}, true
		case "unknown":
			extra = true
		}
		out, known := object{"type": kind}, map[string]bool{"type": true}
		score, valid := 0, true
		if typ, has := b["type"]; has {
			out["type"] = typ
			score += 2
		}
		for _, key := range required {
			if _, has := b[key]; !has {
				valid = false
			}
		}
		for _, key := range append(required, optional...) {
			known[key] = true
			value, has := b[key]
			if !has {
				continue
			}
			score += 2
			switch key {
			case "source":
				var sourceScore int
				var ok bool
				value, sourceScore, ok = anthropicImageSource(obj(value))
				valid = valid && ok
				score += sourceScore
			case "input":
				if kind == "tool_use" {
					if text, ok := value.(string); ok {
						value, _ = decodeObject(text)
						if obj(value) == nil {
							value = object{}
						}
					}
				}
				valid = valid && obj(value) != nil
			case "content":
				if value == nil {
					break
				}
				if _, ok := value.(string); ok {
					break
				}
				items, ok := value.([]any)
				if kind == "web_search_tool_result" {
					if obj(value) != nil {
						break
					}
					for _, item := range items {
						ok = ok && obj(item) != nil
					}
					valid = valid && ok
					break
				}
				values := make([]any, 0, len(items))
				for _, item := range items {
					block, blockScore, blockOK := anthropicBlock(obj(item), true)
					ok = ok && blockOK
					score += blockScore
					values = append(values, block)
				}
				valid = valid && ok
				value = values
			case "is_error":
				if value != nil {
					var ok bool
					value, ok = pydanticBool(value)
					valid = valid && ok
				}
			default:
				_, ok := value.(string)
				valid = valid && ok
			}
			out[key] = value
		}
		if !valid {
			continue
		}
		if extra {
			// Smart-union scoring weights declared fields above preserved extras.
			for key, value := range b {
				if !known[key] {
					out[key] = value
					score++
				}
			}
		}
		switch kind {
		case "thinking":
			if _, has := out["signature"]; !has {
				out["signature"] = ""
			}
		case "tool_result":
			for _, key := range []string{"content", "is_error"} {
				if _, has := out[key]; !has {
					out[key] = nil
				}
			}
		case "server_tool_use":
			if _, has := out["input"]; !has {
				out["input"] = object{}
			}
		case "web_search_tool_result":
			if _, has := out["content"]; !has {
				out["content"] = nil
			}
		}
		if score > bestScore {
			best, bestScore = out, score
		}
	}
	return best, bestScore, best != nil
}

func parseMessage(m object, protocol string) (message, error) {
	result := message{role: str(m["role"])}
	// models_anthropic.py:86: content 必填,null 与缺省同样 422;
	// openai 侧 content 可缺省。
	if protocol == "anthropic" && m["content"] == nil {
		return result, fmt.Errorf("content is required")
	}
	if protocol == "openai" {
		if v := m["tool_calls"]; v != nil {
			if _, ok := v.([]any); !ok {
				return result, fmt.Errorf("tool_calls must be an array")
			}
		}
		// models_openai.py:80-82: tool_call_id/name 为 Optional[str],
		// pydantic v2 lax 不强迫非字符串,整请求 422;缺省与 null 合法。
		for _, key := range []string{"tool_call_id", "name"} {
			if v, has := m[key]; has && v != nil {
				if _, ok := v.(string); !ok {
					return result, fmt.Errorf("%s must be a string", key)
				}
			}
		}
	}
	if result.role == "tool" {
		result.role = "user"
		// converters_openai.py:64-85: tool 消息只存在于 openai 协议,内容
		// 走 extract_text_content 宽松提取。
		text, images, err := resultContent(m["content"], "openai")
		if err != nil {
			return result, err
		}
		result.images = images
		// converters_openai.py:190: tool_call_id 缺省按空串上行,不拒绝。
		result.results = append(result.results, toolResult(str(m["tool_call_id"]), text))
		return result, nil
	}
	if s, ok := m["content"].(string); ok {
		result.text = s
	} else if m["content"] != nil {
		blocks, ok := m["content"].([]any)
		if !ok {
			// converters_core.py:267: openai 标量内容按 Python str() 收为
			// 文本;anthropic 由 pydantic 联合类型在边界拒绝。
			if protocol != "openai" {
				return result, fmt.Errorf("content must be text or blocks")
			}
			result.text = pythonicString(m["content"])
			blocks = nil
		}
		for i, v := range blocks {
			if s, ok := v.(string); ok {
				// extract_text_content: openai 列表里的裸字符串直接拼接;
				// anthropic 的 ContentBlock 联合类型不含字符串项,422。
				if protocol != "openai" {
					return result, fmt.Errorf("content blocks must be objects")
				}
				result.text += s
				continue
			}
			b := obj(v)
			if b == nil {
				// openai 非标量非字典项跳过;anthropic 422。
				if protocol != "openai" {
					return result, fmt.Errorf("content blocks must be objects")
				}
				continue
			}
			if protocol == "anthropic" {
				var ok bool
				b, _, ok = anthropicBlock(b, false)
				if !ok {
					return result, fmt.Errorf("content block does not match a supported type")
				}
				blocks[i] = b
			}
			switch str(b["type"]) {
			case "text":
				// 缺 text 的畸形块落 UnknownContentBlock 兜底被接受,但
				// 转换器 converters_anthropic.py:76 block.text 抛
				// AttributeError,参考实现整请求 500——不复制崩溃,Go 保持
				// 400(刻意分歧)。openai 转换器 get("text","") 宽容。
				if protocol == "anthropic" {
					if _, ok := b["text"].(string); !ok {
						return result, fmt.Errorf("text block requires a string text field")
					}
				}
				result.text += str(b["text"])
			case "thinking": // Native history has no reasoning channel.
				// 缺 thinking 字段的畸形块经 pydantic smart union 落入
				// UnknownContentBlock 兜底被接受,转换层不读 thinking 块,
				// 静默忽略(server_tool_blocks.py:22-27)。
			// extensions/server_tool_blocks.py:121-131: 宽化联合以
			// UnknownContentBlock 兜底,redacted_thinking 等未列名块型
			// 被接受并静默忽略——落入 default 分支。
			case "tool_reference": // Claude Code 延迟工具标记(models_anthropic.py:128),Kiro 无对应物,忽略。
			case "image", "image_url":
				// pydantic smart union: source 畸形的 image 与 image_url 都
				// 落入 UnknownContentBlock 兜底被接受。extract_images_from_
				// content(converters_core.py:322-353)对两种块型统一处理:
				// data-URL 提取、http(s) 跳过、source 缺失静默跳过——
				// anthropic 消息级不再有图片块的边界 422(tool_result 内层
				// 联合未宽化,仍严格)。
				// converters_anthropic.py:297-319: 图片只为 user 角色提取,
				// assistant 等角色的图片块静默忽略。
				if result.role != "user" {
					continue
				}
				image, err := parseImage(b)
				if err != nil {
					return result, err
				}
				if image != nil {
					result.images = append(result.images, image)
				}
			case "tool_use":
				// tool_use 块只在 anthropic assistant 回合提取;user 回合与
				// openai 内容里的同类块参考实现静默丢弃。
				if protocol != "anthropic" || result.role != "assistant" {
					continue
				}
				// pydantic smart union: 字段畸形的 tool_use 落 UnknownContentBlock
				// 兜底,提取器(converters_anthropic.py:228-245)按 dict 语义
				// 处理——input 缺省 {},id/name 只验真值,非字符串真值(如
				// int)原样上行;别名包装仅处理字符串名(tool_name_alias.py:83)。
				id, name := b["id"], b["name"]
				if !truthy(id) || !truthy(name) {
					continue
				}
				alias := name
				if s, ok := name.(string); ok {
					alias = aliasToolName(s)
				}
				u := toolUse(id, alias, b["input"])
				// converters_anthropic.py:254: unified arguments 恒为 coerce 后
				// 的 dict,文本化渲染取其 Python repr。
				u["argsText"] = pyRepr(u["input"])
				result.uses = append(result.uses, u)
			case "tool_result":
				// user 回合提取(anthropic 与 openai 皆支持,
				// converters_openai.py:64-85),其余角色静默丢弃。
				if result.role != "user" {
					continue
				}
				id := b["tool_use_id"]
				if protocol == "anthropic" {
					// pydantic smart union: 缺 tool_use_id 落 UnknownContentBlock
					// 兜底,提取器只验真值(converters_anthropic.py:151)——
					// 空串/0/None 丢弃,非字符串真值(如 int)原样上行。
					if !truthy(id) {
						continue
					}
				} else if id == nil {
					// openai 侧缺省按 "" 保留上行
					// (converters_openai.py:79-82 .get("tool_use_id", ""))。
					id = ""
				}
				text, images, err := resultContent(b["content"], protocol)
				if err != nil {
					return result, err
				}
				result.images = append(result.images, images...)
				result.results = append(result.results, toolResult(id, text))
			default:
				if protocol == "openai" {
					result.text += str(b["text"])
				}
			}
		}
	}
	for _, v := range list(m["tool_calls"]) {
		// 参考实现只从 openai assistant 消息提取 tool_calls,其余位置忽略。
		if protocol != "openai" || result.role != "assistant" {
			continue
		}
		tc := obj(v)
		if tc == nil {
			continue
		}
		// converters_openai.py:137-145: 只读 id 与 function,不校验
		// type 字段;id/name/arguments 缺省分别按 ""/""/"{}" 保留,
		// 非字符串值原样上行;别名包装仅处理字符串名
		// (tool_name_alias.py:92-95)。
		f := obj(tc["function"])
		id := tc["id"]
		if id == nil {
			id = ""
		}
		name := f["name"]
		if name == nil {
			name = ""
		}
		alias := name
		if s, ok := name.(string); ok {
			alias = aliasToolName(s)
		}
		u := toolUse(id, alias, f["arguments"])
		// 文本化渲染用 unified arguments 原文:字符串 verbatim,缺省
		// "{}",非标量(dict 等,models_openai.py:81 List[Any] 无校验)
		// 取 Python repr。
		switch a := f["arguments"].(type) {
		case string:
			u["argsText"] = a
		case nil:
			u["argsText"] = "{}"
		default:
			u["argsText"] = pyRepr(a)
		}
		result.uses = append(result.uses, u)
	}
	return result, nil
}

// resultContent 提取 tool_result 内容。pydantic smart union: anthropic 侧
// content 校验失败(标量/字典/含裸字符串或联合外块的列表)时整块落
// UnknownContentBlock 被接受,转换层按 dict 语义走 extract_text_content
// 宽容路径(converters_anthropic.py:151-156 → converters_core.py:260-282);
// openai 侧本就是同一函数(converters_openai.py:79-82)。两协议唯一差异:
// 非标量非列表内容 anthropic 先验真值(假值收空串,str(v) if v else ""),
// openai 恒 str()(extract_text_content 末尾 return str(content))。
func resultContent(v any, protocol string) (string, []any, error) {
	if v == nil {
		return "", nil, nil
	}
	if text, ok := v.(string); ok {
		return text, nil, nil
	}
	blocks, ok := v.([]any)
	if !ok {
		if protocol == "anthropic" && !truthy(v) {
			return "", nil, nil
		}
		if obj(v) != nil {
			// Python str(dict) 即 repr:{'foo': 1}。
			return pyRepr(v), nil, nil
		}
		return pythonicString(v), nil, nil
	}
	var text strings.Builder
	var images []any
	for _, item := range blocks {
		if s, ok := item.(string); ok {
			// extract_text_content: 列表里的裸字符串直接拼接。
			text.WriteString(s)
			continue
		}
		b := obj(item)
		if b == nil {
			// extract_text_content: 无 text 属性的非标量项静默跳过。
			continue
		}
		switch str(b["type"]) {
		case "image", "image_url":
			// extract_text_content 跳过图片块;图片由
			// extract_images_from_tool_results 经 dict 语义另行提取
			// (converters_anthropic.py:190-200),坏 source 宽容跳过。
			image, err := parseImage(b)
			if err != nil {
				return "", nil, err
			}
			if image != nil {
				images = append(images, image)
			}
		case "tool_reference":
			// extract_text_content 同样跳过(converters_core.py:269-270)。
			continue
		default:
			// extract_text_content: text 块取 item.get("text",""),未知
			// 块带 text 键收割,否则跳过——两者同为 str(b["text"])。
			text.WriteString(str(b["text"]))
		}
	}
	return text.String(), images, nil
}

// pythonicString 复刻 Python str():布尔 True/False,数字保留 JSON 原文
// (json.loads 的 int/float 区分与 UseNumber 的原文保留一致)。
func pythonicString(v any) string {
	switch x := v.(type) {
	case bool:
		if x {
			return "True"
		}
		return "False"
	case json.Number:
		return x.String()
	case string:
		return x
	}
	return fmt.Sprintf("%v", v)
}

var integerString = regexp.MustCompile(`^-?[0-9]+(?:_[0-9]+)*$`)

func pydanticInteger(v any) (*big.Int, bool) {
	s := ""
	switch x := v.(type) {
	case bool:
		if x {
			return big.NewInt(1), true
		}
		return big.NewInt(0), true
	case json.Number:
		s = x.String()
		if strings.ContainsAny(s, ".eE") {
			f, err := x.Float64()
			if err != nil || f < -9223372036854775808.0 || f >= 9223372036854775808.0 || f != float64(int64(f)) {
				return nil, false
			}
			return big.NewInt(int64(f)), true
		}
	case string:
		s = strings.TrimSpace(x)
		negative := strings.HasPrefix(s, "-")
		if strings.HasPrefix(s, "+") || negative {
			s = s[1:]
			if strings.HasPrefix(s, "+") || strings.HasPrefix(s, "-") {
				return nil, false
			}
		}
		if whole, fraction, has := strings.Cut(s, "."); has {
			if fraction == "" || strings.Trim(fraction, "0") != "" {
				return nil, false
			}
			s = whole
		}
		if strings.HasPrefix(s, "0") {
			if strings.HasSuffix(s, "_") {
				return nil, false
			}
			s = strings.TrimLeft(s, "0_")
			if s == "" {
				s = "0"
			}
		}
		if negative {
			s = "-" + s
		}
	default:
		return nil, false
	}
	if !integerString.MatchString(s) {
		return nil, false
	}
	s = strings.ReplaceAll(s, "_", "")
	if len(strings.TrimPrefix(s, "-")) > 4300 {
		return nil, false
	}
	n, ok := new(big.Int).SetString(s, 10)
	return n, ok
}

func pydanticBool(v any) (bool, bool) {
	switch x := v.(type) {
	case bool:
		return x, true
	case json.Number:
		f, err := x.Float64()
		if err == nil && (f == 0 || f == 1) {
			return f == 1, true
		}
	case string:
		switch strings.ToLower(x) {
		case "true", "1", "yes", "on", "t", "y":
			return true, true
		case "false", "0", "no", "off", "f", "n":
			return false, true
		}
	}
	return false, false
}

// pydanticFloat 复刻 pydantic v2 lax 模式的 float 强迫:数字、数值字符串、
// 布尔接受,其余拒绝。
func pydanticFloat(v any) (float64, bool) {
	switch x := v.(type) {
	case json.Number:
		f, err := x.Float64()
		return f, err == nil
	case string:
		f, err := strconv.ParseFloat(strings.TrimSpace(x), 64)
		return f, err == nil
	case bool:
		if x {
			return 1, true
		}
		return 0, true
	}
	return 0, false
}

// toolUse 构建 Kiro 工具调用。空 id/name 不拒绝(converters_openai.py:
// 138-145 原样保留,空 name 由 extract_tool_uses_from_message 缺省空串
// 上行);非字符串真值 id/name 原样上行(converters_anthropic.py:245 只验
// 真值)。input 按 coerce_tool_input_to_dict 等价处理:dict 原样,字符串
// 解析为 JSON 对象,其余(含解析失败、非对象 JSON、标量)退化 {}。
// argsText 由调用方事后赋值(空串亦须落键以区分缺省),仅供无工具声明
// 时的文本化渲染,上行前由 stripArgsText 剔除(converters_core.py:965-981)。
func toolUse(id, name, input any) object {
	if s, ok := input.(string); ok {
		parsed, err := decodeObject(s)
		if err == nil {
			input = parsed
		}
	}
	if obj(input) == nil {
		input = object{}
	}
	return object{"toolUseId": id, "name": name, "input": input}
}

// stripArgsText 剔除内部键 argsText,避免泄漏进 Kiro 上行负载与 token
// 估算。
func stripArgsText(u any) object {
	o := obj(u)
	if _, has := o["argsText"]; !has {
		return o
	}
	c := make(object, len(o)-1)
	for k, v := range o {
		if k != "argsText" {
			c[k] = v
		}
	}
	return c
}

// pyRepr 复刻 Python repr():字符串单引号(含单引号且无双引号时换双引号,
// 否则反斜杠转义)、True/False/None、数字原文、容器 ", " 连接、dict 键
// 排序(Go map 无插入序,取确定性近似)。用于 anthropic 侧 dict 参数的文
// 本化渲染(tool_calls_to_text 对 unified dict arguments 的 str())。
func pyRepr(v any) string {
	switch x := v.(type) {
	case nil:
		return "None"
	case bool:
		if x {
			return "True"
		}
		return "False"
	case string:
		return pyStrRepr(x)
	case json.Number:
		return x.String()
	case map[string]any:
		keys := make([]string, 0, len(x))
		for k := range x {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		parts := make([]string, 0, len(keys))
		for _, k := range keys {
			parts = append(parts, pyStrRepr(k)+": "+pyRepr(x[k]))
		}
		return "{" + strings.Join(parts, ", ") + "}"
	case []any:
		parts := make([]string, 0, len(x))
		for _, e := range x {
			parts = append(parts, pyRepr(e))
		}
		return "[" + strings.Join(parts, ", ") + "]"
	}
	return fmt.Sprintf("%v", v)
}

func pyStrRepr(s string) string {
	quote := byte('\'')
	if strings.Contains(s, "'") && !strings.Contains(s, "\"") {
		quote = '"'
	}
	var b strings.Builder
	b.WriteByte(quote)
	for _, r := range s {
		switch r {
		case '\\':
			b.WriteString("\\\\")
		case '\n':
			b.WriteString("\\n")
		case '\r':
			b.WriteString("\\r")
		case '\t':
			b.WriteString("\\t")
		case rune(quote):
			b.WriteByte('\\')
			b.WriteRune(r)
		default:
			b.WriteRune(r)
		}
	}
	b.WriteByte(quote)
	return b.String()
}

// converters_core.py:819/845: status 恒 "success",is_error 不入上行负载,
// 失败信息只经 content 文本传达给模型。id 原样上行(非字符串真值保留,
// converters_anthropic.py:151 只验真值)。
func toolResult(id any, text string) object {
	if text == "" {
		text = "(empty result)"
	}
	return object{"toolUseId": id, "status": "success", "content": []any{object{"text": text}}}
}

// parseImage 对齐 converters_core.py 的宽松策略:URL 图片与空 data 跳过
// (返回 nil)。两阶段语义:extract_images_from_content(:322-397)对
// image_url 块解析 data-URL,无逗号整图跳过;image 块原样进 unified,
// convert_images_to_kiro_format(:750-785)再剥 data: 前缀,无逗号仅
// warning、原始 data 保留上行。media 取 header ";" 首段,空值不覆盖原
// media_type;format 取 media_type "/" 末段,无 "/" 用全串。
func parseImage(b object) (object, error) {
	var media, data string
	if str(b["type"]) == "image" {
		src := obj(b["source"])
		if str(src["type"]) != "base64" {
			return nil, nil
		}
		if m, has := src["media_type"]; has {
			media = str(m)
		} else {
			media = "image/jpeg"
		}
		data = str(src["data"])
	} else {
		data = str(obj(b["image_url"])["url"])
		if !strings.HasPrefix(data, "data:") {
			return nil, nil
		}
		// image_url 在提取阶段解析:无逗号 ValueError,整图跳过。
		prefix, rest, ok := strings.Cut(data, ",")
		if !ok {
			return nil, nil
		}
		media = strings.TrimPrefix(strings.SplitN(prefix, ";", 2)[0], "data:")
		data = rest
	}
	if data == "" {
		return nil, nil
	}
	if strings.HasPrefix(data, "data:") {
		if prefix, rest, ok := strings.Cut(data, ","); ok {
			if extracted := strings.TrimPrefix(strings.SplitN(prefix, ";", 2)[0], "data:"); extracted != "" {
				media = extracted
			}
			data = rest
		}
		// 无逗号:convert 阶段 ValueError 仅 warning,原始 data(含
		// "data:..." 前缀)原样上行(converters_core.py:767-770)。
	}
	format := media
	if i := strings.LastIndex(media, "/"); i >= 0 {
		format = media[i+1:]
	}
	return object{"format": format, "source": object{"bytes": data}}, nil
}
func nativeMessage(m message, model string, tools []any) object {
	text := m.text
	if text == "" {
		text = "(empty placeholder)"
	}
	if m.role == "assistant" {
		a := object{"content": text}
		if len(m.uses) > 0 {
			uses := make([]any, len(m.uses))
			for i, u := range m.uses {
				uses[i] = stripArgsText(u)
			}
			a["toolUses"] = uses
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
