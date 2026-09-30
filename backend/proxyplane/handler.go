package proxyplane

import (
	"bytes"
	"context"
	"crypto/subtle"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/aceaura/model-surge-upstream/backend/apperr"
	"github.com/aceaura/model-surge-upstream/backend/provider"
	"github.com/aceaura/model-surge-upstream/backend/resolve"
	"github.com/aceaura/model-surge-upstream/backend/usage"
)

// bodyLimit 是转发请求的体上限。聊天请求体（含长上下文）远小于此；
// 限制只为挡住误传的大文件，不为裁剪正常流量。
const bodyLimit = 32 << 20

// family 是路径前缀决定的协议族。不做协议转化：族只用来校验模型的
// 出站协议与客户端期望一致，以及决定错误响应与模型列举的原生形态。
type family string

const (
	familyAnthropic family = "anthropic"
	familyOpenAI    family = "openai"
	familyGemini    family = "gemini"
)

type Handler struct {
	key      []byte
	resolver Resolver
	client   *http.Client
	sink     UsageSink
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

func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	fam, suffix, ok := splitPrefix(r.URL.Path)
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
		switch {
		case fam == familyAnthropic && suffix == "/v1/models":
			h.listModels(w, r, fam, provider.ProtocolAnthropic)
		case fam == familyOpenAI && suffix == "/v1/models":
			h.listModels(w, r, fam, provider.ProtocolChatCompletions, provider.ProtocolResponses)
		case fam == familyGemini && suffix == "/v1beta/models":
			h.listModels(w, r, fam, provider.ProtocolGemini)
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
		h.forwardGemini(w, r, suffix)
		return
	}
	want := provider.ProtocolAnthropic
	if fam == familyOpenAI {
		switch suffix {
		case "/v1/chat/completions":
			want = provider.ProtocolChatCompletions
		case "/v1/responses":
			want = provider.ProtocolResponses
		default:
			http.NotFound(w, r)
			return
		}
	}
	h.forwardWithBodyModel(w, r, fam, suffix, want)
}

// splitPrefix 把路径拆成协议族与上游路径后缀。协议靠前缀区分，
// 无法也不试图从请求体嗅探。
func splitPrefix(path string) (family, string, bool) {
	for _, p := range []struct {
		fam    family
		prefix string
	}{
		{familyAnthropic, "/anthropic"},
		{familyOpenAI, "/openai"},
		{familyGemini, "/gemini"},
	} {
		if rest, ok := strings.CutPrefix(path, p.prefix); ok && strings.HasPrefix(rest, "/") {
			return p.fam, rest, true
		}
	}
	return "", "", false
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
	up, err := http.NewRequestWithContext(r.Context(), http.MethodPost, url, bytes.NewReader(encoded))
	if err != nil {
		writeFamilyError(w, fam, apperr.Wrap(apperr.UpstreamUnavailable, "build upstream request", err))
		return
	}
	copyHeaders(up.Header, r.Header, stripRequestHeaders)
	up.Header.Set("Content-Type", "application/json")
	// 认证头最后写：账号形态（含自定义头叠加）压过客户端带来的任何认证痕迹。
	for k, v := range target.Headers {
		up.Header.Set(k, v)
	}

	start := time.Now()
	resp, err := h.client.Do(up)
	if err != nil {
		writeFamilyError(w, fam, apperr.Wrap(apperr.UpstreamUnavailable, "upstream request failed", err))
		h.record(r.Context(), target, isStream, usage.Usage{}, http.StatusBadGateway, 0, time.Since(start))
		return
	}
	defer resp.Body.Close()

	sniffer := usage.NewSniffer(target.Protocol, resp.Header.Get("Content-Type"))
	out := &usageWriter{ResponseWriter: w, sniffer: sniffer, start: start}

	copyHeaders(w.Header(), resp.Header, stripResponseHeaders)
	w.WriteHeader(resp.StatusCode)
	// 每次写入后立即 flush：SSE 逐事件到达客户端，不等缓冲区填满。
	_, _ = io.Copy(out, resp.Body)

	u, _ := sniffer.Result()
	h.record(r.Context(), target, isStream, u, resp.StatusCode, out.latency, time.Since(start))
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
			})
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"object": "list", "data": data})
	}
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
