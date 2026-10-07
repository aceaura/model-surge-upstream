// Package kiro implements the Kiro generation wire protocol without a gateway.
package kiro

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/user"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/aceaura/model-surge-upstream/backend/apperr"
)

const (
	ProviderID       = "kiro.global.subscribe.standard"
	HeaderProfileARN = "X-Msu-Kiro-Profile-Arn"
	HeaderProvider   = "X-Msu-Upstream-Provider"
	DefaultBaseURL   = "https://runtime.us-east-1.kiro.dev"
	maxRequestBytes  = 8 << 20
	maxPayloadBytes  = 600000
	maxRetryAttempts = 3
	// firstTokenMaxAttempts 对齐 config.py FIRST_TOKEN_MAX_RETRIES=3。
	firstTokenMaxAttempts = 3
)

// machineFingerprint 复刻 utils.py get_machine_fingerprint:
// sha256("{hostname}-{username}-kiro-gateway"),与 Kiro IDE 流量画像一致。
var machineFingerprint = sync.OnceValue(func() string {
	hostname, _ := os.Hostname()
	username := ""
	if u, err := user.Current(); err == nil {
		username = u.Username
	}
	sum := sha256.Sum256([]byte(hostname + "-" + username + "-kiro-gateway"))
	return hex.EncodeToString(sum[:])
})

// Fingerprint 暴露机器指纹,供配额/续期等旁路请求拼 UA。
func Fingerprint() string { return machineFingerprint() }

// ChatUserAgent 对齐 utils.py get_kiro_headers 的完整 SDK 串。
func ChatUserAgent() string {
	return "aws-sdk-js/1.0.27 ua/2.1 os/win32#10.0.19044 lang/js md/nodejs#22.21.1 api/codewhispererstreaming#1.0.27 m/E KiroIDE-0.7.45-" + machineFingerprint()
}

// IDEUserAgent 是 refreshToken 端点用的短 UA(auth.py:705)。
func IDEUserAgent() string {
	return "KiroIDE-0.7.45-" + machineFingerprint()
}

// retryBackoff follows config.py BASE_RETRY_DELAY with exponential doubling.
// Tests replace it to keep retry coverage fast.
var retryBackoff = func(attempt int) time.Duration {
	return time.Duration(1<<attempt) * time.Second
}

// Headers supplies native authentication and identity. The two routing headers
// are consumed locally and never sent to AWS.
func Headers(token, profileARN string) map[string]string {
	return map[string]string{
		"Authorization":               "Bearer " + token,
		"Content-Type":                "application/x-amz-json-1.0",
		"X-Amz-Target":                "AmazonCodeWhispererStreamingService.GenerateAssistantResponse",
		"User-Agent":                  ChatUserAgent(),
		"X-Amz-User-Agent":            "aws-sdk-js/1.0.27 KiroIDE-0.7.45-" + machineFingerprint(),
		"X-Amzn-Codewhisperer-Optout": "true",
		"X-Amzn-Kiro-Agent-Mode":      "vibe",
		"Amz-Sdk-Invocation-Id":       newID(),
		"Amz-Sdk-Request":             "attempt=1; max=3",
		HeaderProvider:                ProviderID,
		HeaderProfileARN:              profileARN,
	}
}

var regionPattern = regexp.MustCompile(`^[a-z]{2}(?:-[a-z]+)+-\d+$`)

// Region prefers the profile's region, then the credential's region.
func Region(credentialRegion, profileARN string) string {
	parts := strings.Split(profileARN, ":")
	if len(parts) >= 6 && parts[0] == "arn" && regionPattern.MatchString(parts[3]) {
		return parts[3]
	}
	if regionPattern.MatchString(credentialRegion) {
		return credentialRegion
	}
	return "us-east-1"
}

// Endpoints returns chat and control-plane base URLs. Builder ID accounts with
// no profile use Q for chat; runtime requires a profile. RoundTrip itself never
// changes the URL host: callers select the base URL with this helper if needed.
func Endpoints(credentialRegion, profileARN string) (chat, control string) {
	region := Region(credentialRegion, profileARN)
	control = "https://q." + region + ".amazonaws.com"
	if profileARN == "" {
		return control, control
	}
	return "https://runtime." + region + ".kiro.dev", control
}

func newID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic(err)
	}
	b[6] = (b[6] & 15) | 64
	b[8] = (b[8] & 63) | 128
	s := hex.EncodeToString(b[:])
	return s[:8] + "-" + s[8:12] + "-" + s[12:16] + "-" + s[16:20] + "-" + s[20:]
}

type transport struct {
	base, kiroBase http.RoundTripper
}

// NewTransport adapts only requests explicitly marked HeaderProvider=kiro.
func NewTransport(base http.RoundTripper) http.RoundTripper {
	if base == nil {
		base = http.DefaultTransport
	}
	native := base
	if tr, ok := base.(*http.Transport); ok {
		clone := tr.Clone()
		if clone.ResponseHeaderTimeout == 0 || clone.ResponseHeaderTimeout > streamStallTimeout {
			clone.ResponseHeaderTimeout = streamStallTimeout
		}
		native = clone
	}
	return &transport{base: base, kiroBase: native}
}

func (t *transport) RoundTrip(req *http.Request) (*http.Response, error) {
	if req.Header.Get(HeaderProvider) != ProviderID {
		return t.base.RoundTrip(req)
	}
	if req.Method != http.MethodPost {
		return nil, apperr.New(apperr.InvalidRequest, "kiro: unsupported method "+req.Method)
	}
	protocol := ""
	switch req.URL.Path {
	case "/v1/messages":
		protocol = "anthropic"
	case "/v1/chat/completions":
		protocol = "openai"
	case "/v1/messages/count_tokens":
		protocol = "count_tokens"
	default:
		return nil, apperr.New(apperr.InvalidRequest, fmt.Sprintf("kiro: unsupported API path %q", req.URL.Path))
	}
	if req.Body == nil {
		return nil, apperr.New(apperr.InvalidRequest, "kiro: missing request body")
	}
	// GetBody preserves replayable caller bodies. As with any RoundTripper, a
	// non-replayable body is consumed and closed, but no request fields are changed.
	source := req.Body
	if req.GetBody != nil {
		var err error
		source, err = req.GetBody()
		if err != nil {
			return nil, apperr.Wrap(apperr.InvalidRequest, "kiro: read request", err)
		}
	}
	raw, err := io.ReadAll(io.LimitReader(source, maxRequestBytes+1))
	source.Close()
	if err != nil {
		return nil, apperr.Wrap(apperr.InvalidRequest, "kiro: read request", err)
	}
	if len(raw) > maxRequestBytes {
		return nil, apperr.New(apperr.InvalidRequest, fmt.Sprintf("kiro: request exceeds %d bytes", maxRequestBytes))
	}
	if protocol == "count_tokens" {
		// routes_anthropic.py count_tokens_endpoint: 纯本地估算,不打上游;
		// Claude Code 靠它决定何时触发会话压缩。基于原始请求
		// (messages/tools/system),三部分分别乘 1.15 校正
		// (tokenizer.py:208-209/251-252/291-292),返回 {"input_tokens": n}。
		_, options, err := convertRequest(raw, "count_tokens", req.Header.Get(HeaderProfileARN))
		if err != nil {
			return nil, apperr.Wrap(apperr.InvalidRequest, "kiro", err)
		}
		p := options.estimateParts
		data, err := json.Marshal(object{"input_tokens": p[0]*115/100 + p[1]*115/100 + p[2]*115/100})
		if err != nil {
			return nil, err
		}
		header := http.Header{"Content-Type": {"application/json"}}
		return &http.Response{
			StatusCode: 200, Status: "200 OK", Header: header,
			Body: io.NopCloser(bytes.NewReader(data)), ContentLength: int64(len(data)),
			Request: req,
		}, nil
	}
	// models_anthropic.py:375: Anthropic 端点 max_tokens 必填(值本身不上行),
	// 且 pydantic int 只接受整数/整值浮点/数值字符串/布尔;小数、非数值
	// 字符串、数组对象与 null 一律 422。与 FastAPI 一样在 HTTP 边界拒绝。
	if protocol == "anthropic" {
		if probe, err := decodeObject(string(raw)); err == nil {
			if probe["max_tokens"] == nil {
				return nil, apperr.New(apperr.InvalidRequest, "kiro: invalid request: max_tokens is required")
			}
			if _, ok := pydanticInteger(probe["max_tokens"]); !ok {
				return nil, apperr.New(apperr.InvalidRequest, "kiro: invalid request: max_tokens must be an integer")
			}
		}
	}
	payload, options, err := convertRequest(raw, protocol, req.Header.Get(HeaderProfileARN))
	if err != nil {
		return nil, apperr.Wrap(apperr.InvalidRequest, "kiro", err)
	}
	encoded, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}
	if len(encoded) > maxPayloadBytes {
		return nil, apperr.New(apperr.InvalidRequest, fmt.Sprintf("kiro: native payload exceeds %d bytes (history is not silently trimmed)", maxPayloadBytes))
	}
	ctx, cancel := context.WithCancel(req.Context())
	upstream := req.Clone(ctx)
	u := *req.URL
	u.Path, u.RawPath, u.RawQuery, u.Fragment, u.User = "/generateAssistantResponse", "", "", "", nil
	upstream.URL = &u
	upstream.Host = ""
	upstream.Header = make(http.Header)
	// Rebuild, rather than forwarding client headers (API keys, cookies, AWS
	// targets, identity overrides, compression and routing hints included).
	token := strings.TrimPrefix(req.Header.Get("Authorization"), "Bearer ")
	if token == "" || !strings.HasPrefix(req.Header.Get("Authorization"), "Bearer ") {
		cancel()
		return nil, fmt.Errorf("kiro: native Bearer authorization required")
	}
	for k, v := range Headers(token, req.Header.Get(HeaderProfileARN)) {
		upstream.Header.Set(k, v)
	}
	upstream.Header.Del(HeaderProvider)
	upstream.Header.Del(HeaderProfileARN)
	upstream.Header.Set("Accept", "application/vnd.amazon.eventstream")
	upstream.Header.Set("Accept-Encoding", "identity")
	upstream.TransferEncoding = nil
	upstream.Trailer = nil
	// http_client.py: 429/5xx 与瞬时网络错误(超时/DNS/连接重置,SSL 除外)在
	// 首字节前重试,指数退避(1s, 2s; 三次),每次换新 invocation id。
	// streaming_core.py: 拿到 200 后再等首 token(FIRST_TOKEN_TIMEOUT=15s,按
	// effort 倍率放大、封顶 120s),超时取消请求整体重发,最多 3 次。
	// sendPayload 发一次完整负载;恢复重试改的是 encoded,Body 每次重建。
	sendPayload := func() (*http.Response, io.ReadCloser, error) {
		var resp *http.Response
		var firstChunk []byte
	send:
		for ft := 0; ; ft++ {
			for attempt := 0; ; attempt++ {
				upstream.Header.Set("Amz-Sdk-Invocation-Id", newID())
				upstream.Body = io.NopCloser(bytes.NewReader(encoded))
				upstream.GetBody = func() (io.ReadCloser, error) { return io.NopCloser(bytes.NewReader(encoded)), nil }
				upstream.ContentLength = int64(len(encoded))
				resp, err = t.kiroBase.RoundTrip(upstream)
				if err != nil {
					if !retryableNetErr(err) || attempt >= maxRetryAttempts-1 {
						return nil, nil, err
					}
					if !sleepBeforeRetry(ctx, retryBackoff(attempt)) {
						return nil, nil, ctx.Err()
					}
					continue
				}
				if resp == nil {
					return nil, nil, fmt.Errorf("kiro: upstream transport returned no response")
				}
				retryable := resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode >= 500
				if !retryable || attempt >= maxRetryAttempts-1 {
					break
				}
				_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<20))
				resp.Body.Close()
				if !sleepBeforeRetry(ctx, retryBackoff(attempt)) {
					return nil, nil, ctx.Err()
				}
			}
			if resp.StatusCode < 200 || resp.StatusCode >= 300 {
				break send // 错误响应原样透传,不参与首 token 重试
			}
			firstChunk, err = peekFirstChunk(ctx, resp.Body, firstTokenTimeout(options.effort))
			switch {
			case err == nil, errors.Is(err, io.EOF):
				// 空响应在 Python 里是正常结束,交给 finalize 判定。
				break send
			case errors.Is(err, errFirstTokenTimeout):
				resp.Body.Close()
				if ft >= firstTokenMaxAttempts-1 {
					return nil, nil, fmt.Errorf("kiro: model did not respond within %s after %d attempts",
						firstTokenTimeout(options.effort), firstTokenMaxAttempts)
				}
			default:
				return nil, nil, err
			}
		}
		if resp.Body == nil {
			return nil, nil, fmt.Errorf("kiro: upstream returned no body")
		}
		rawBody := io.ReadCloser(&stallBody{body: resp.Body})
		if len(firstChunk) > 0 {
			rawBody = &prependBody{Reader: io.MultiReader(bytes.NewReader(firstChunk), rawBody), Closer: rawBody}
		}
		return resp, rawBody, nil
	}
	// streaming_core.py collect_with_tool_choice_retry: 严格 tool_choice 下整体
	// 缓冲响应做语义校验,违规注入 [Tool Policy Recovery] 指令重发一次。
	for recovery := 0; ; recovery++ {
		resp, rawBody, err := sendPayload()
		if err != nil {
			cancel()
			return nil, err
		}
		out := *resp
		out.Header = resp.Header.Clone()
		out.Request = req
		if resp.StatusCode < 200 || resp.StatusCode >= 300 {
			// routes_anthropic.py:622-670 / routes_openai.py:584: 上游错误体
			// 重写为客户端协议形状,已知 reason 换友好文案;读不出明文时原样透传。
			if ce := resp.Header.Get("Content-Encoding"); ce == "" || ce == "identity" || resp.Uncompressed {
				data, _ := io.ReadAll(io.LimitReader(rawBody, 1<<20))
				rawBody.Close()
				cancel()
				// The buffered/rebuilt body no longer has the upstream wire metadata.
				out.Header.Del("Content-Length")
				out.Header.Del("Content-Encoding")
				out.Header.Del("ETag")
				out.TransferEncoding = nil
				out.Trailer = nil
				out.Uncompressed = false
				msg := enhanceKiroError(data)
				var shape object
				if options.protocol == "openai" {
					shape = object{"error": object{"message": msg, "type": "kiro_api_error", "code": resp.StatusCode}}
				} else {
					shape = object{"type": "error", "error": object{"type": "api_error", "message": msg}}
				}
				encoded, merr := json.Marshal(shape)
				if merr == nil {
					out.Body = io.NopCloser(bytes.NewReader(encoded))
					out.ContentLength = int64(len(encoded))
					out.Header.Set("Content-Type", "application/json")
					return &out, nil
				}
				out.Body = io.NopCloser(bytes.NewReader(data))
				out.ContentLength = int64(len(data))
				return &out, nil
			}
			out.Body = ownedBody(rawBody, ctx, cancel)
			return &out, nil // Preserve real upstream HTTP errors, never wrap as completions.
		}
		if ce := resp.Header.Get("Content-Encoding"); ce != "" && ce != "identity" && !resp.Uncompressed {
			rawBody.Close()
			cancel()
			return nil, fmt.Errorf("kiro: unexpected upstream content encoding %q", ce)
		}
		out.Header.Del("Content-Length")
		out.Header.Del("Content-Encoding")
		out.Header.Del("ETag")
		out.ContentLength = -1
		out.TransferEncoding = nil
		out.Trailer = nil
		state := newResponseState(options)
		if options.policyMode != "" && options.protocol == "anthropic" {
			// 严格 Anthropic 先按非流语义收集:合并 thinking 置首,
			// 保留全局末签名及 fallback,校验通过后再回放 SSE。
			state.options.stream = false
		}
		if options.policyMode == "" {
			body := ownedBody(rawBody, ctx, cancel)
			if options.stream {
				// routes_anthropic.py:532-535: 流式响应显式 keep-alive。
				out.Header.Set("Content-Type", "text/event-stream")
				out.Header.Set("Cache-Control", "no-cache")
				out.Header.Set("Connection", "keep-alive")
				out.Body = newStreamBody(body, state, req.Context())
			} else {
				defer body.Close()
				if err := collectResponse(body, state, req.Context()); err != nil {
					return nil, err
				}
				data, err := json.Marshal(state.response())
				if err != nil {
					return nil, err
				}
				out.Header.Set("Content-Type", "application/json")
				out.Body = io.NopCloser(bytes.NewReader(data))
			}
			return &out, nil
		}
		// 严格模式:校验通过才回放给客户端;OpenAI SSE 先落缓冲,
		// Anthropic 收集最终块。body 用子 ctx,关闭它不会取消共享 ctx,
		// 恢复重试仍能复用 upstream。
		var captured bytes.Buffer
		emit := func(name string, v object) {
			data, _ := json.Marshal(v)
			if name != "" {
				captured.WriteString("event: " + name + "\n")
			}
			captured.WriteString("data: ")
			captured.Write(data)
			captured.WriteString("\n\n")
		}
		if options.stream && state.options.stream {
			state.emit = emit
			state.start()
		}
		bctx, bcancel := context.WithCancel(ctx)
		sbody := ownedBody(rawBody, bctx, bcancel)
		err = collectResponse(sbody, state, req.Context())
		sbody.Close()
		var violation *toolViolation
		switch {
		case err == nil:
			violation = strictViolation(state)
		case errors.As(err, &violation):
		default:
			cancel()
			return nil, err
		}
		if violation != nil {
			if recovery > 0 {
				cancel()
				// routes_openai.py:414-423 / routes_anthropic.py:479-488:
				// 恢复重试仍违规,502 + 协议错误体;消息带
				// "tool_choice_not_satisfied: " 前缀(streaming_core.py:126-130)。
				msg := "tool_choice_not_satisfied: " + violation.msg
				var shape object
				if options.protocol == "openai" {
					shape = object{"error": object{"message": msg, "type": "tool_choice_not_satisfied", "code": "tool_choice_not_satisfied"}}
				} else {
					shape = object{"type": "error", "error": object{"type": "tool_choice_not_satisfied", "message": msg}}
				}
				encoded, merr := json.Marshal(shape)
				if merr != nil {
					return nil, merr
				}
				out.StatusCode = 502
				out.Status = "502 Bad Gateway"
				out.Header = http.Header{"Content-Type": {"application/json"}}
				out.Body = io.NopCloser(bytes.NewReader(encoded))
				out.ContentLength = int64(len(encoded))
				return &out, nil
			}
			encoded, err = recoveryDirective(payload, options, violation)
			if err != nil {
				cancel()
				return nil, err
			}
			continue
		}
		if options.stream {
			if options.protocol == "anthropic" {
				state.options.stream = true
				state.blockIndex = -1
				state.emit = emit
				state.start()
				toolIndex := 0
				for _, value := range state.blocks {
					block := obj(value)
					kind := str(block["type"])
					var start, delta object
					switch kind {
					case "text":
						start = object{"type": "text", "text": ""}
						delta = object{"type": "text_delta", "text": block["text"]}
					case "thinking":
						start = object{"type": "thinking", "thinking": ""}
						delta = object{"type": "thinking_delta", "thinking": block["thinking"]}
					case "tool_use":
						// finalize 按 finalTools 顺序追加工具块;参数从保序
						// 文本转 UTF-8,不能重新 marshal input map 排序键。
						partial, _ := normalizeOrderedJSON(state.finalTools[toolIndex].args, false)
						toolIndex++
						start = object{"type": "tool_use", "id": block["id"], "name": block["name"], "input": object{}}
						delta = object{"type": "input_json_delta", "partial_json": partial}
					}
					// 只回放已 finalize 的块,不再次累积文本或计数。
					state.openBlock(kind, start)
					state.send("content_block_delta", object{"type": "content_block_delta", "index": state.blockIndex, "delta": delta})
					if kind == "thinking" {
						state.send("content_block_delta", object{"type": "content_block_delta", "index": state.blockIndex, "delta": object{"type": "signature_delta", "signature": block["signature"]}})
					}
					state.closeBlock()
				}
			}
			state.finishStream()
			if options.protocol == "openai" {
				captured.WriteString("data: [DONE]\n\n")
			}
			out.Header.Set("Content-Type", "text/event-stream")
			out.Header.Set("Cache-Control", "no-cache")
			out.Header.Set("Connection", "keep-alive")
			out.Body = io.NopCloser(bytes.NewReader(captured.Bytes()))
		} else {
			data, err := json.Marshal(state.response())
			if err != nil {
				cancel()
				return nil, err
			}
			out.Header.Set("Content-Type", "application/json")
			out.Body = io.NopCloser(bytes.NewReader(data))
		}
		// 此路 Body 是已捕获字节的 Reader,上游流已在 collectResponse 后
		// 关闭,ctx 不再被引用;显式 cancel 收口(其余返回路径由
		// ownedBody/managedBody 的 Close 代为调 cancel)。
		cancel()
		return &out, nil
	}
}

type managedBody struct {
	io.ReadCloser
	cancel    context.CancelFunc
	stop      func() bool
	onceClose func() error
}

// prependBody 把首 token 探测读到的字节拼回上游流。
type prependBody struct {
	io.Reader
	io.Closer
}

var errFirstTokenTimeout = errors.New("kiro: first token timeout")

// enhanceKiroError 对齐 kiro_errors.py enhance_kiro_error:已知 reason 换
// 友好文案,未知错误保留原始 message 并附 reason;非 JSON 体用原文。
func enhanceKiroError(data []byte) string {
	var payload struct {
		Message *string `json:"message"`
		Reason  *string `json:"reason"`
	}
	if err := json.Unmarshal(data, &payload); err != nil {
		if text := strings.TrimSpace(string(data)); text != "" {
			return text
		}
		return "Unknown error"
	}
	original := "Unknown error"
	if payload.Message != nil {
		original = *payload.Message
	}
	reason := "UNKNOWN"
	if payload.Reason != nil {
		reason = *payload.Reason
	}
	switch reason {
	case "CONTENT_LENGTH_EXCEEDS_THRESHOLD":
		return "Model context limit reached. Conversation size exceeds model capacity."
	case "MONTHLY_REQUEST_COUNT":
		return "Monthly request limit exceeded. Account has reached its monthly quota."
	case "INVALID_MODEL_ID":
		return "Invalid model ID or insufficient subscription level to use it."
	}
	if original == "Improperly formed request." && (reason == "UNKNOWN" || reason == "null") {
		return "Kiro API rejected the request. If problem persists, open issue with info and attached debug logs at:" +
			"https://github.com/jwadow/kiro-gateway/issues"
	}
	if payload.Reason != nil && reason != "UNKNOWN" {
		return original + " (reason: " + reason + ")"
	}
	return original
}

// streamStallTimeout 对齐 http_client.py STREAMING_READ_TIMEOUT=300:流中段
// 单次读停滞超时即关闭 body 报错,防挂死连接占住会话。测试替换以缩短等待。
var streamStallTimeout = 300 * time.Second

// stallBody 给流式响应中段每次 Read 套停滞超时;超时关闭底层 body 并以
// 停滞错误代替底层的关闭错误返回。
type stallBody struct{ body io.ReadCloser }

func (b *stallBody) Read(p []byte) (int, error) {
	tctx, stop := context.WithTimeout(context.Background(), streamStallTimeout)
	defer stop()
	done := context.AfterFunc(tctx, func() { _ = b.body.Close() })
	defer done()
	n, err := b.body.Read(p)
	if n == 0 && tctx.Err() == context.DeadlineExceeded {
		return 0, fmt.Errorf("kiro: upstream stream stalled for %s", streamStallTimeout)
	}
	return n, err
}

func (b *stallBody) Close() error { return b.body.Close() }

// firstTokenTimeout 对齐 config.py: 基准 15s,按 effort 倍率放大
// (low 1.5/medium 2/high 4/xhigh 6/max 8),封顶 120s。
// 测试替换它以缩短等待。
var firstTokenTimeout = func(effort string) time.Duration {
	mult := 1.0
	switch effort {
	case "low":
		mult = 1.5
	case "medium":
		mult = 2.0
	case "high":
		mult = 4.0
	case "xhigh":
		mult = 6.0
	case "max":
		mult = 8.0
	}
	d := time.Duration(float64(15*time.Second) * mult)
	if cap := 120 * time.Second; d > cap {
		d = cap
	}
	return d
}

// peekFirstChunk 在 wait 内等到上游第一块字节;超时关闭 body 并返回
// errFirstTokenTimeout。读到的字节由调用方拼回流。
func peekFirstChunk(ctx context.Context, body io.ReadCloser, wait time.Duration) ([]byte, error) {
	tctx, stop := context.WithTimeout(ctx, wait)
	defer stop()
	done := context.AfterFunc(tctx, func() { _ = body.Close() })
	defer done()
	buf := make([]byte, 8192)
	n, err := body.Read(buf)
	switch {
	case n > 0:
		return buf[:n], nil
	case tctx.Err() == context.DeadlineExceeded:
		return nil, errFirstTokenTimeout
	case err != nil:
		return nil, err
	default:
		return nil, io.EOF
	}
}

// retryableNetErr 不重试 TLS/证书错误，即使外层包装成 net.Error。
func retryableNetErr(err error) bool {
	var verifyErr *tls.CertificateVerificationError
	var authorityErr x509.UnknownAuthorityError
	var hostnameErr x509.HostnameError
	var invalidErr x509.CertificateInvalidError
	var recordErr tls.RecordHeaderError
	if errors.As(err, &verifyErr) || errors.As(err, &authorityErr) || errors.As(err, &hostnameErr) ||
		errors.As(err, &invalidErr) || errors.As(err, &recordErr) {
		return false
	}
	// TLS alerts can be plain errors wrapped by net.OpError, not typed TLS errors.
	message := strings.ToLower(err.Error())
	if strings.Contains(message, "ssl") || strings.Contains(message, "tls") || strings.Contains(message, "certificate") {
		return false
	}
	var netErr net.Error
	if errors.As(err, &netErr) {
		return true
	}
	return errors.Is(err, io.ErrUnexpectedEOF) || errors.Is(err, io.EOF)
}

func sleepBeforeRetry(ctx context.Context, d time.Duration) bool {
	timer := time.NewTimer(d)
	select {
	case <-ctx.Done():
		timer.Stop()
		return false
	case <-timer.C:
		return true
	}
}

func ownedBody(body io.ReadCloser, ctx context.Context, cancel context.CancelFunc) *managedBody {
	// AfterFunc uses no waiting goroutine and closes even a custom blocking body
	// on cancellation. once ensures concurrent Close/cancellation is safe.
	closer := onceCloser(body)
	b := &managedBody{ReadCloser: body, cancel: cancel, onceClose: closer}
	b.stop = context.AfterFunc(ctx, func() { _ = closer() })
	return b
}
func (b *managedBody) Close() error { b.stop(); b.cancel(); return b.onceClose() }
