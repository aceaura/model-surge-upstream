// Package modelcheck 检测命名模型到真实上游的连通性：按出站协议构造一个
// 最小原生请求发往上游，量出时延并判定结果。语义参照 CC Switch 的连通性
// 检测——网络级错误（DNS/拒连/TLS/超时）才算链路不通；拿到 HTTP 响应即
// 说明链路可达，但非 2xx 仍判失败并带上状态码与上游说明，因为模型级检测
// 的价值正在于验证凭据与模型名。检测只读：不合并 defaults/overrides、
// 不进用量统计、不看模型与账号的启用状态（未启用也该能先测通）。
//
// 本包含两级刻意不同的判据，勿统一：
//   - Check（模型级）：非 2xx 判失败——回答「能不能用」（凭据+模型名）。
//   - Reachability（账号级）：拿到任意 HTTP 响应（含 401/403/404/5xx）即
//     可达——回答「能不能到」。对齐 CC Switch stream_check 的「可达 ≠
//     配置正确」：账号级不验鉴权，凭据对错归模型级管。
package modelcheck

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/aceaura/model-surge-upstream/backend/apperr"
	"github.com/aceaura/model-surge-upstream/backend/provider"
	"github.com/aceaura/model-surge-upstream/backend/resolve"
)

// timeout 是单次探测的上游超时。CC Switch 默认 8s；探测体极小，取 10s
// 足够覆盖冷启动 TLS 握手，又不会让界面久等。
const timeout = 10 * time.Second

// bodyLimit 是探测响应的读取上限。正常回包只有几百字节，限制只为防异常上游。
const bodyLimit = 1 << 20

// snippetLen 是错误信息里附带的上游响应截断长度。
const snippetLen = 300

// Result 是一次连通性检测的结果。OK 的判定口径由检测函数决定：
// Check 仅在拿到 2xx 时为真；Reachability 拿到任意 HTTP 响应即为真
// （StatusCode 仍带回供展示）。网络级失败时 StatusCode 为 0，Error 说明原因。
type Result struct {
	OK         bool   `json:"ok"`
	StatusCode int    `json:"status_code"`
	LatencyMS  int64  `json:"latency_ms"`
	Error      string `json:"error,omitempty"`
}

// Check 对解析好的目标发一次最小探测请求。target 由调用方组装
// （httpapi 直接从 model+account 构造，跳过启用态校验）。
func Check(ctx context.Context, target resolve.ResolvedTarget) Result {
	suffix, body, err := probeRequest(target)
	if err != nil {
		return Result{Error: err.Error()}
	}
	encoded, err := json.Marshal(body)
	if err != nil {
		return Result{Error: apperr.Wrap(apperr.InvalidJSON, "encode probe request", err).Error()}
	}

	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	url := strings.TrimRight(target.BaseURL, "/") + suffix
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(encoded))
	if err != nil {
		return Result{Error: apperr.Wrap(apperr.UpstreamUnavailable, "build probe request", err).Error()}
	}
	req.Header.Set("Content-Type", "application/json")
	for k, v := range target.Headers {
		req.Header.Set(k, v)
	}

	start := time.Now()
	resp, err := (&http.Client{}).Do(req)
	latency := time.Since(start)
	if err != nil {
		// 网络级失败：DNS、拒连、TLS、超时都落到这里（CC Switch 同款判据）。
		return Result{LatencyMS: latency.Milliseconds(),
			Error: "上游不可达：" + err.Error()}
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, bodyLimit))
	latency = time.Since(start)

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return Result{
			StatusCode: resp.StatusCode,
			LatencyMS:  latency.Milliseconds(),
			Error:      fmt.Sprintf("上游返回 HTTP %d：%s", resp.StatusCode, snippet(raw)),
		}
	}
	return Result{OK: true, StatusCode: resp.StatusCode, LatencyMS: latency.Milliseconds()}
}

// Reachability 检测账号生效 base_url 的可达性（CC Switch stream_check 同款
// 语义）：GET base_url，不带鉴权头也不带账号自定义头、不看启用状态。收到
// 任意 HTTP 响应（含 401/403/404/5xx）即「可达」（OK=true）；仅 DNS/拒连/
// TLS/超时等网络级错误判失败。可达 ≠ 配置正确——凭据与模型名的验证是
// 模型级 Check 的职责。
//
// 时延是 TTFB：Do 返回（响应头到达）即停表，不读 body——与 Check 读完
// body 再停表不同，那里要读 snippet 做错误说明，这里不需要。
func Reachability(ctx context.Context, baseURL string) Result {
	url := strings.TrimSpace(baseURL)
	if url == "" {
		return Result{Error: "base_url 为空"}
	}

	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		// 非法 URL 视为检测失败，不是请求错误（与 Check 的构造失败口径一致）。
		return Result{Error: apperr.Wrap(apperr.UpstreamUnavailable, "build reachability request", err).Error()}
	}

	start := time.Now()
	resp, err := (&http.Client{}).Do(req)
	latency := time.Since(start)
	if err != nil {
		// 网络级失败：DNS、拒连、TLS、超时都落到这里（CC Switch 同款判据）。
		return Result{LatencyMS: latency.Milliseconds(),
			Error: "上游不可达：" + err.Error()}
	}
	defer resp.Body.Close()
	return Result{OK: true, StatusCode: resp.StatusCode, LatencyMS: latency.Milliseconds()}
}

// probeRequest 按协议生成探测路径与最小请求体。max_tokens 压到最低，
// 让上游尽快返回：测的是「链路+凭据+模型名」，不是补全质量。
func probeRequest(target resolve.ResolvedTarget) (string, map[string]any, error) {
	switch target.Protocol {
	case provider.ProtocolAnthropic:
		return "/v1/messages", map[string]any{
			"model":      target.NativeModel,
			"max_tokens": 1,
			"messages":   []map[string]any{{"role": "user", "content": "ping"}},
		}, nil
	case provider.ProtocolChatCompletions:
		return "/v1/chat/completions", map[string]any{
			"model":      target.NativeModel,
			"max_tokens": 1,
			"messages":   []map[string]any{{"role": "user", "content": "ping"}},
		}, nil
	case provider.ProtocolResponses:
		return "/v1/responses", map[string]any{
			"model":             target.NativeModel,
			"max_output_tokens": 16, // Responses 协议多数实现要求 ≥16
			"input":             "ping",
		}, nil
	case provider.ProtocolGemini:
		return "/v1beta/models/" + target.NativeModel + ":generateContent", map[string]any{
			"contents":         []map[string]any{{"role": "user", "parts": []map[string]any{{"text": "ping"}}}},
			"generationConfig": map[string]any{"maxOutputTokens": 1},
		}, nil
	default:
		return "", nil, apperr.New(apperr.InvalidProtocol,
			fmt.Sprintf("model %q speaks unknown protocol %q", target.ModelID, target.Protocol))
	}
}

func snippet(raw []byte) string {
	s := strings.TrimSpace(string(raw))
	if r := []rune(s); len(r) > snippetLen {
		s = string(r[:snippetLen]) + "…"
	}
	return s
}
