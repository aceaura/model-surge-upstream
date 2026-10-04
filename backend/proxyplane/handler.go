package proxyplane

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/aceaura/model-surge-upstream/backend/apperr"
	"github.com/aceaura/model-surge-upstream/backend/codex"
	"github.com/aceaura/model-surge-upstream/backend/compact"
	"github.com/aceaura/model-surge-upstream/backend/effort"
	"github.com/aceaura/model-surge-upstream/backend/provider"
	"github.com/aceaura/model-surge-upstream/backend/resolve"
	"github.com/aceaura/model-surge-upstream/backend/usage"
)

// bodyLimit 是转发请求的体上限。聊天请求体（含长上下文）远小于此；
// 限制只为挡住误传的大文件，不为裁剪正常流量。
const bodyLimit = 32 << 20

// family 是路径形状决定的协议族。不做协议转化：族只用来校验模型的
// 出站协议与客户端期望一致，以及决定错误响应与模型列举的原生形态。
type family string

const (
	familyAnthropic family = "anthropic"
	familyOpenAI    family = "openai"
	familyGemini    family = "gemini"
)

type Handler struct {
	key       []byte
	resolver  Resolver
	client    *http.Client
	sink      UsageSink
	compactor *compact.Runner
	// invalidator 作废 OAuth 账号当前 access_token;nil 表示未装配
	// OAuth(遇 oauth 账号 401 时只能透传,不能刷新重试)。
	invalidator Invalidator
}

// Invalidator 上游 401 时作废 OAuth 账号的 access_token(oauth.Manager
// 实现),下次解析强制续期。
type Invalidator interface {
	Invalidate(name, accessToken string)
}

// UsageRecord 转发面旁路统计产出的一次请求记录：不管成功失败都记，
// 失败请求用量为 0，但请求数与状态码要进统计（运维要看失败率）。
type UsageRecord struct {
	Protocol    string
	ModelID     string
	Account     string
	NativeModel string
	Usage       usage.Usage
	StatusCode  int
	IsStreaming bool
	LatencyMS   int64
	DurationMS  int64
	At          time.Time
}

// UsageSink 接收用量记录。在响应透传完成的路径上被调用，
// 实现方必须自己起 goroutine：不能拖慢客户端连接。nil 表示不统计。
type UsageSink func(ctx context.Context, rec UsageRecord)

func NewHandler(apiKey string, resolver Resolver, sink UsageSink) *Handler {
	// 不设整体超时：流式响应可能持续数分钟，生命周期由客户端断开控制。
	tr := http.DefaultTransport.(*http.Transport).Clone()
	return &Handler{
		key:      []byte(apiKey),
		resolver: resolver,
		client:   &http.Client{Transport: tr},
		sink:     sink,
	}
}

// WithCompactor 挂上上下文压缩器（nil 表示关闭，默认）。
// 压缩只在请求体定稿后作用：auto 模式改写 body，error 模式直接回 400，
// passive 模式无操作；响应透传路径不受影响。
func (h *Handler) WithCompactor(c *compact.Runner) *Handler {
	h.compactor = c
	return h
}

// WithInvalidator 挂上 OAuth token 作废器（nil 表示关闭，默认）:
// codex 等 OAuth 账号上游 401 时作废旧 token、重解析、原样重放一次。
func (h *Handler) WithInvalidator(v Invalidator) *Handler {
	h.invalidator = v
	return h
}

func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	fam, ok := familyOf(r.URL.Path)
	if !ok {
		http.NotFound(w, r)
		return
	}
	if !h.authorized(r) {
		writeFamilyError(w, fam, apperr.New(apperr.Unauthorized, "missing or invalid proxy api key"))
		return
	}

	// 模型列举：客户端启动时发现「我命名的模型」，按族给原生形态。
	if r.Method == http.MethodGet {
		switch r.URL.Path {
		case "/v1/models":
			// anthropic 与 openai 客户端都打 /v1/models:按放钥位置
			// 分族——x-api-key 是 Anthropic SDK 的原生位置,其余
			// (Bearer/?key=)按 openai 形态回。
			if r.Header.Get("x-api-key") != "" {
				h.listModels(w, r, familyAnthropic, provider.ProtocolAnthropic)
			} else {
				h.listModels(w, r, familyOpenAI, provider.ProtocolChatCompletions, provider.ProtocolResponses)
			}
		case "/v1beta/models":
			h.listModels(w, r, familyGemini, provider.ProtocolGemini)
		default:
			http.NotFound(w, r)
		}
		return
	}
	if r.Method != http.MethodPost {
		writeFamilyError(w, fam, apperr.New(apperr.InvalidRequest, "method not allowed"))
		return
	}

	if fam == familyGemini {
		h.forwardGemini(w, r, r.URL.Path)
		return
	}
	var want string
	switch {
	case r.URL.Path == "/v1/messages" || strings.HasPrefix(r.URL.Path, "/v1/messages/"):
		// messages 整棵子树(含 count_tokens)都归 anthropic。
		want = provider.ProtocolAnthropic
	case r.URL.Path == "/v1/chat/completions":
		want = provider.ProtocolChatCompletions
	case r.URL.Path == "/v1/responses":
		want = provider.ProtocolResponses
	default:
		http.NotFound(w, r)
		return
	}
	h.forwardWithBodyModel(w, r, fam, r.URL.Path, want)
}

// familyOf 按路径形状定协议族:三族端点天然不撞车(anthropic 的
// /v1/messages、openai 的 /v1/chat/completions 与 /v1/responses、
// gemini 的 /v1beta/...),三协议由此共用一个 base URL,客户端按各自
// 原生路径直配,不再要 /anthropic /openai /gemini 前缀。唯一共用的
// GET /v1/models 在列举分支里按认证头位置二次分族。
func familyOf(path string) (family, bool) {
	switch {
	case path == "/v1/messages" || strings.HasPrefix(path, "/v1/messages/"):
		return familyAnthropic, true
	case path == "/v1/chat/completions", path == "/v1/responses", path == "/v1/models":
		return familyOpenAI, true
	case strings.HasPrefix(path, "/v1beta/"):
		return familyGemini, true
	}
	return "", false
}

// authorized 接受各协议客户端放密钥的原生位置：Bearer（OpenAI）、
// x-api-key（Anthropic）、x-goog-api-key 或 ?key=（Gemini）。
func (h *Handler) authorized(r *http.Request) bool {
	presented := bearerToken(r)
	if presented == "" {
		presented = r.Header.Get("x-api-key")
	}
	if presented == "" {
		presented = r.Header.Get("x-goog-api-key")
	}
	if presented == "" {
		presented = r.URL.Query().Get("key")
	}
	return presented != "" && subtle.ConstantTimeCompare([]byte(presented), h.key) == 1
}

func bearerToken(r *http.Request) string {
	raw := r.Header.Get("Authorization")
	if len(raw) < 7 || !strings.EqualFold(raw[:7], "bearer ") {
		return ""
	}
	return strings.TrimSpace(raw[7:])
}

// forwardWithBodyModel 处理 anthropic/openai 两族：模型别名在请求体的
// model 字段里。解析 → 校验协议 → 改写 model → 合并参数 → 透传。
func (h *Handler) forwardWithBodyModel(w http.ResponseWriter, r *http.Request, fam family, suffix, wantProtocol string) {
	raw, err := io.ReadAll(io.LimitReader(r.Body, bodyLimit))
	if err != nil {
		writeFamilyError(w, fam, apperr.New(apperr.InvalidRequest, "read request body failed"))
		return
	}
	var obj map[string]any
	if err := json.Unmarshal(raw, &obj); err != nil {
		writeFamilyError(w, fam, apperr.New(apperr.InvalidJSON, "request body is not valid json"))
		return
	}
	alias, _ := obj["model"].(string)
	if alias == "" {
		writeFamilyError(w, fam, apperr.New(apperr.InvalidRequest, "model is required"))
		return
	}

	target, err := h.resolver.Resolve(r.Context(), alias)
	if err != nil {
		writeFamilyError(w, fam, err)
		return
	}
	if target.Protocol != wantProtocol {
		writeFamilyError(w, fam, apperr.New(apperr.InvalidProtocol,
			fmt.Sprintf("model %q speaks protocol %q, not %q; this gateway does not convert protocols",
				alias, target.Protocol, wantProtocol)))
		return
	}

	obj["model"] = target.NativeModel
	// reasoning_level 是网关自有的顶层数字档:命中模型声明的档位即消费
	// (不进上游)并给思考开关/档位参数赋值,先于 defaults/overrides 合并,
	// 之后 overrides 仍可压盖(强制值优先级最高)。
	if err := applyReasoningLevel(target, obj); err != nil {
		writeFamilyError(w, fam, err)
		return
	}
	merged := mergeParams(rawObject(target.Defaults), obj)
	merged = mergeParams(merged, rawObject(target.Overrides))
	// OpenAI 流式默认不回 usage，统计会全盲。仅当客户端要流式时注入
	// include_usage：非流式响应本就带 usage，不碰请求体。
	if wantProtocol == provider.ProtocolChatCompletions {
		if stream, _ := merged["stream"].(bool); stream {
			opts, _ := merged["stream_options"].(map[string]any)
			if opts == nil {
				opts = map[string]any{}
			}
			if _, ok := opts["include_usage"]; !ok {
				opts["include_usage"] = true
				merged["stream_options"] = opts
			}
		}
	}
	// 上下文压缩策略在请求体定稿后执行：估算超阈值时 auto 改写 body、
	// error 直接回 400 让客户端自行压缩、passive 无操作。
	if h.compactor != nil {
		out, estimated, reject := h.compactor.Run(r.Context(), target, merged,
			func(u usage.Usage, status int, latency, duration time.Duration) {
				h.record(r.Context(), target, false, u, status, latency, duration)
			})
		if reject {
			writeContextExceeded(w, fam, estimated, target.ContextWindow)
			h.record(r.Context(), target, false, usage.Usage{}, http.StatusBadRequest, 0, 0)
			return
		}
		merged = out
	}
	// codex 订阅端点的硬约束(store/stream/剥采样参数/instructions)与
	// 路径映射(/v1/responses→/responses)最后应用:压过 defaults/overrides。
	if target.ProviderID == codex.ProviderID {
		merged = codex.ShapeBody(merged, target.NativeModel)
		suffix = codex.MapSuffix(suffix)
	}
	h.forward(w, r, fam, target, suffix, merged)
}

// forwardGemini 处理 gemini 族：模型别名在路径里
// （/v1beta/models/{别名}:generateContent 等），体里没有 model 字段。
// 别名含 / 时无法从路径还原，直接拒绝——这类别名请走另外两族。
func (h *Handler) forwardGemini(w http.ResponseWriter, r *http.Request, suffix string) {
	rest, ok := strings.CutPrefix(suffix, "/v1beta/models/")
	if !ok {
		http.NotFound(w, r)
		return
	}
	alias, tail, _ := strings.Cut(rest, ":")
	if alias == "" || strings.Contains(alias, "/") {
		writeFamilyError(w, familyGemini, apperr.New(apperr.InvalidRequest,
			"gemini model alias must be a single path segment"))
		return
	}

	target, err := h.resolver.Resolve(r.Context(), alias)
	if err != nil {
		writeFamilyError(w, familyGemini, err)
		return
	}
	if target.Protocol != provider.ProtocolGemini {
		writeFamilyError(w, familyGemini, apperr.New(apperr.InvalidProtocol,
			fmt.Sprintf("model %q speaks protocol %q, not %q; this gateway does not convert protocols",
				alias, target.Protocol, provider.ProtocolGemini)))
		return
	}

	raw, err := io.ReadAll(io.LimitReader(r.Body, bodyLimit))
	if err != nil {
		writeFamilyError(w, familyGemini, apperr.New(apperr.InvalidRequest, "read request body failed"))
		return
	}
	var obj map[string]any
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &obj); err != nil {
			writeFamilyError(w, familyGemini, apperr.New(apperr.InvalidJSON, "request body is not valid json"))
			return
		}
	} else {
		obj = map[string]any{}
	}
	if err := applyReasoningLevel(target, obj); err != nil {
		writeFamilyError(w, familyGemini, err)
		return
	}
	merged := mergeParams(rawObject(target.Defaults), obj)
	merged = mergeParams(merged, rawObject(target.Overrides))

	newSuffix := "/v1beta/models/" + target.NativeModel
	if tail != "" {
		newSuffix += ":" + tail
	}
	h.forward(w, r, familyGemini, target, newSuffix, merged)
}

// forward 把改写后的请求发往解析出的上游，并把响应（含流式）原样回传。
// 回传的同时旁路嗅探用量：统计失败或无用量都不影响透传本身。
// codex 等 OAuth 账号遇 401 时作废旧 token、重解析、原样重放一次:
// 订阅登录态的 access_token 短命,401 多半是服务端提前作废。
func (h *Handler) forward(w http.ResponseWriter, r *http.Request, fam family, target resolve.ResolvedTarget, suffix string, body map[string]any) {
	encoded, err := json.Marshal(body)
	if err != nil {
		writeFamilyError(w, fam, apperr.New(apperr.InvalidJSON, "re-encode request body failed"))
		return
	}
	isStream, _ := body["stream"].(bool)

	url := strings.TrimRight(target.BaseURL, "/") + suffix
	if q := stripKeyQuery(r.URL.RawQuery); q != "" {
		url += "?" + q
	}
	// 请求体字节固定,重试时按新头集重建请求。
	build := func(headers map[string]string) (*http.Request, error) {
		up, err := http.NewRequestWithContext(r.Context(), http.MethodPost, url, bytes.NewReader(encoded))
		if err != nil {
			return nil, err
		}
		copyHeaders(up.Header, r.Header, stripRequestHeaders)
		up.Header.Set("Content-Type", "application/json")
		if target.ProviderID == codex.ProviderID {
			// 客户端自带的会话头一律作废(sub2api 同款):session 由服务端
			// 按账号+prompt_cache_key 派生,防串号遥测。
			for _, k := range []string{"session_id", "conversation_id", "x-client-request-id"} {
				up.Header.Del(k)
			}
			key, _ := body["prompt_cache_key"].(string)
			sid := codex.SessionID(target.Account, key)
			up.Header.Set("session_id", sid)
			up.Header.Set("conversation_id", sid)
			up.Header.Set("x-client-request-id", randomUUID())
		}
		// 认证头最后写：账号形态（含自定义头叠加）压过客户端带来的任何认证痕迹。
		for k, v := range headers {
			up.Header.Set(k, v)
		}
		return up, nil
	}

	start := time.Now()
	up, err := build(target.Headers)
	if err != nil {
		writeFamilyError(w, fam, apperr.Wrap(apperr.UpstreamUnavailable, "build upstream request", err))
		return
	}
	resp, err := h.client.Do(up)
	if err != nil {
		writeFamilyError(w, fam, apperr.Wrap(apperr.UpstreamUnavailable, "upstream request failed", err))
		h.record(r.Context(), target, isStream, usage.Usage{}, http.StatusBadGateway, 0, time.Since(start))
		return
	}

	// OAuth 账号 401:作废旧 token 重解析重放一次;仍 401 则原样透传给客户端。
	if resp.StatusCode == http.StatusUnauthorized && h.invalidator != nil && target.ProviderID == codex.ProviderID {
		_, _ = io.Copy(io.Discard, resp.Body)
		_ = resp.Body.Close()
		h.invalidator.Invalidate(target.Account, bearerTokenValue(target.Headers))
		fresh, rerr := h.resolver.Resolve(r.Context(), target.ModelID)
		if rerr != nil {
			writeFamilyError(w, fam, rerr)
			h.record(r.Context(), target, isStream, usage.Usage{}, http.StatusUnauthorized, 0, time.Since(start))
			return
		}
		if up, err = build(fresh.Headers); err == nil {
			target = fresh
			resp, err = h.client.Do(up)
		}
		if err != nil {
			writeFamilyError(w, fam, apperr.Wrap(apperr.UpstreamUnavailable, "upstream retry failed", err))
			h.record(r.Context(), target, isStream, usage.Usage{}, http.StatusBadGateway, 0, time.Since(start))
			return
		}
	}
	defer resp.Body.Close()

	contentType := resp.Header.Get("Content-Type")
	// Codex 强制 SSE，但上游可能标为 text/plain。
	if target.ProviderID == codex.ProviderID {
		contentType = "text/event-stream"
	}
	sniffer := usage.NewSniffer(target.Protocol, contentType)
	out := &usageWriter{ResponseWriter: w, sniffer: sniffer, start: start}

	copyHeaders(w.Header(), resp.Header, stripResponseHeaders)
	w.WriteHeader(resp.StatusCode)
	// 每次写入后立即 flush：SSE 逐事件到达客户端，不等缓冲区填满。
	_, _ = io.Copy(out, resp.Body)

	u, _ := sniffer.Result()
	h.record(r.Context(), target, isStream, u, resp.StatusCode, out.latency, time.Since(start))
}

// bearerTokenValue 从头集里取出 Bearer 载荷;取不到返回空串。
func bearerTokenValue(headers map[string]string) string {
	auth := headers["Authorization"]
	if len(auth) < 7 || !strings.EqualFold(auth[:7], "bearer ") {
		return ""
	}
	return strings.TrimSpace(auth[7:])
}

// randomUUID 生成 v4 形态 UUID,供 x-client-request-id 使用。
func randomUUID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return ""
	}
	b[6] = b[6]&0x0f | 0x40
	b[8] = b[8]&0x3f | 0x80
	return fmt.Sprintf("%s-%s-%s-%s-%s",
		hex.EncodeToString(b[0:4]), hex.EncodeToString(b[4:6]),
		hex.EncodeToString(b[6:8]), hex.EncodeToString(b[8:10]),
		hex.EncodeToString(b[10:16]))
}

// record 把一次转发的用量交给 sink。sink 为 nil（统计未装配）即无操作；
// 上下文取 WithoutCancel：handler 返回后请求上下文即取消，落库不能跟着取消。
func (h *Handler) record(ctx context.Context, target resolve.ResolvedTarget, isStream bool, u usage.Usage, status int, latency, duration time.Duration) {
	if h.sink == nil {
		return
	}
	rec := UsageRecord{
		Protocol:    target.Protocol,
		ModelID:     target.ModelID,
		Account:     target.Account,
		NativeModel: target.NativeModel,
		Usage:       u,
		StatusCode:  status,
		IsStreaming: isStream,
		LatencyMS:   latency.Milliseconds(),
		DurationMS:  duration.Milliseconds(),
		At:          time.Now(),
	}
	h.sink(context.WithoutCancel(ctx), rec)
}

// usageWriter 透传响应的同时把字节喂给嗅探器，并记首字节时延。
type usageWriter struct {
	http.ResponseWriter
	sniffer *usage.Sniffer
	start   time.Time
	latency time.Duration
	primed  bool
}

func (u *usageWriter) Write(p []byte) (int, error) {
	if !u.primed {
		u.latency = time.Since(u.start)
		u.primed = true
	}
	u.sniffer.Write(p)
	n, err := u.ResponseWriter.Write(p)
	if f, ok := u.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
	return n, err
}

// listModels 按族输出原生形态的命名模型清单。只列启用中且协议匹配的：
// 不做转化，客户端用不了其它协议的模型，列出来只会误导。
func (h *Handler) listModels(w http.ResponseWriter, r *http.Request, fam family, protocols ...string) {
	listing, err := h.resolver.List(r.Context())
	if err != nil {
		writeFamilyError(w, fam, err)
		return
	}
	match := map[string]bool{}
	for _, p := range protocols {
		match[p] = true
	}

	w.Header().Set("Content-Type", "application/json")
	switch fam {
	case familyAnthropic:
		data := []any{}
		for _, m := range listing {
			if !m.Enabled || !match[m.Protocol] {
				continue
			}
			data = append(data, map[string]any{
				"id": m.ID, "type": "model", "display_name": m.ID,
				"created_at": time.Now().UTC().Format(time.RFC3339),
				"efforts": m.Efforts,
			})
		}
		body := map[string]any{"data": data, "has_more": false}
		if len(data) > 0 {
			body["first_id"] = data[0].(map[string]any)["id"]
			body["last_id"] = data[len(data)-1].(map[string]any)["id"]
		}
		_ = json.NewEncoder(w).Encode(body)
	case familyGemini:
		models := []any{}
		for _, m := range listing {
			if !m.Enabled || !match[m.Protocol] || strings.Contains(m.ID, "/") {
				continue
			}
			models = append(models, map[string]any{
				"name": "models/" + m.ID, "displayName": m.ID,
				"efforts": m.Efforts,
			})
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"models": models})
	default: // openai
		data := []any{}
		for _, m := range listing {
			if !m.Enabled || !match[m.Protocol] {
				continue
			}
			data = append(data, map[string]any{
				"id": m.ID, "object": "model", "created": 0, "owned_by": m.ProviderID,
				"efforts": m.Efforts,
			})
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"object": "list", "data": data})
	}
}

// applyReasoningLevel 消费请求顶层的 reasoning_level 数字档:命中模型
// 声明的档位(名=档号)即从体里删除(网关自有字段不进上游),并按映射出
// 的档位值给思考开关/档位参数赋值;未提供的、形态不对的、值不在声明
// 列表里的都原样透传。赋值落点两级:模型配了映射脚本(effort_script)
// 即由脚本接管写入位置,否则走协议内置映射(effort.Apply,0 档映射
// none=关闭思考)。赋值落在客户端参数层:优先级 defaults < 映射 <
// overrides,JSON 覆盖参数恒可压盖映射结果。
func applyReasoningLevel(target resolve.ResolvedTarget, body map[string]any) error {
	level, ok := reasoningLevel(body["reasoning_level"])
	if !ok {
		return nil
	}
	mapped, hit := effort.LevelOf(target.Efforts, level)
	if !hit {
		return nil
	}
	delete(body, "reasoning_level")
	if target.EffortScript == "" {
		effort.Apply(target.Protocol, body, mapped)
		return nil
	}
	out, err := effort.RunScript(target.EffortScript, effort.ScriptContext{
		Level:    level,
		Value:    mapped,
		Protocol: target.Protocol,
		Efforts:  target.Efforts,
		Request:  body,
	})
	if err != nil {
		return err
	}
	// 脚本返回完整新体,替换原 map 内容(调用方持有的是同一引用)。
	for k := range body {
		delete(body, k)
	}
	for k, v := range out {
		body[k] = v
	}
	return nil
}

// reasoningLevel 归一 reasoning_level 的取值:字符串("2")与整数
// 字面量(2)都认,空串/负数/小数/其他类型视为未提供。
func reasoningLevel(v any) (string, bool) {
	switch t := v.(type) {
	case string:
		s := strings.TrimSpace(t)
		return s, s != ""
	case float64:
		if t >= 0 && t == math.Trunc(t) {
			return strconv.FormatInt(int64(t), 10), true
		}
	}
	return "", false
}

// rawObject 把 defaults/overrides 的 RawMessage 读成对象；空或 null 视为 {}。
func rawObject(raw json.RawMessage) map[string]any {
	out := map[string]any{}
	if len(raw) == 0 {
		return out
	}
	_ = json.Unmarshal(raw, &out)
	if out == nil {
		return map[string]any{}
	}
	return out
}

// mergeParams 递归合并：overlay 压 base，对象深合并，数组与标量整体替换。
// 与 api.md 3.5 约定给调用方的合并语义一致，转发面把它挪到服务端执行。
func mergeParams(base, overlay map[string]any) map[string]any {
	out := make(map[string]any, len(base)+len(overlay))
	for k, v := range base {
		out[k] = v
	}
	for k, v := range overlay {
		if ov, ok := v.(map[string]any); ok {
			if bv, ok := out[k].(map[string]any); ok {
				out[k] = mergeParams(bv, ov)
				continue
			}
		}
		out[k] = v
	}
	return out
}

// 认证与逐跳头不进上游。Content-Length/Host 由 Go 按新体与目标重建。
var stripRequestHeaders = map[string]bool{
	"authorization":     true,
	"x-api-key":         true,
	"x-goog-api-key":    true,
	"content-length":    true,
	"connection":        true,
	"keep-alive":        true,
	"proxy-connection":  true,
	"te":                true,
	"trailer":           true,
	"transfer-encoding": true,
	"upgrade":           true,
}

var stripResponseHeaders = map[string]bool{
	"connection":        true,
	"keep-alive":        true,
	"te":                true,
	"trailer":           true,
	"transfer-encoding": true,
	"upgrade":           true,
}

func copyHeaders(dst, src http.Header, skip map[string]bool) {
	for k, vs := range src {
		if skip[strings.ToLower(k)] {
			continue
		}
		for _, v := range vs {
			dst.Add(k, v)
		}
	}
}

// stripKeyQuery 摘掉 gemini 风格的 ?key= 凭据参数，其余查询串原样保留。
func stripKeyQuery(raw string) string {
	parts := strings.Split(raw, "&")
	kept := parts[:0]
	for _, p := range parts {
		if strings.HasPrefix(strings.ToLower(p), "key=") {
			continue
		}
		kept = append(kept, p)
	}
	return strings.Join(kept, "&")
}
