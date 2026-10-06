package quota

// ChatGPT 订阅额度:GET backend-api/wham/usage 回 rate_limit 的两个滚动窗
// (primary=5 小时、secondary=周),used_percent 已是百分数。端点不在
// /backend-api/codex 子路径下,必须写绝对地址,拼 provider BaseURL 恒 404。
// 头组与 2026-10-03 实测通过 Cloudflare 的那一组逐字节一致。

import (
	"context"
	"encoding/json"
	"math"
	"net/http"
	"strconv"
	"time"

	"github.com/aceaura/model-surge-upstream/backend/account"
	"github.com/aceaura/model-surge-upstream/backend/apperr"
	"github.com/aceaura/model-surge-upstream/backend/provider"
)

// codexUsageURL 是包级变量而非常量:测试桩上游时替换为 httptest 地址。
var codexUsageURL = "https://chatgpt.com/backend-api/wham/usage"

func codexMeters(ctx context.Context, q *Quota, spec provider.Spec, acc account.Account) ([]Meter, error) {
	if q.tokens == nil {
		return nil, apperr.New(apperr.QuotaUnavailable, "codex quota requires a token source")
	}
	token, err := q.tokens.AccessToken(ctx, acc)
	if err != nil {
		return nil, apperr.Wrap(apperr.QuotaUnavailable, "refresh codex access token", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, codexUsageURL, nil)
	if err != nil {
		return nil, apperr.Wrap(apperr.QuotaUnavailable, "build quota request", err)
	}
	for k, v := range map[string]string{
		"Accept":             "application/json",
		"Authorization":      "Bearer " + token,
		"ChatGPT-Account-Id": acc.Credential.AccountID,
		"openai-beta":        "codex-1",
		"oai-language":       "zh-CN",
		"originator":         "Codex Desktop",
		"sec-fetch-site":     "none",
		"sec-fetch-mode":     "no-cors",
		"sec-fetch-dest":     "empty",
		"priority":           "u=4, i",
		"User-Agent":         codexUserAgent,
	} {
		req.Header.Set(k, v)
	}
	body, _, err := q.do(req)
	if err != nil {
		return nil, err
	}
	return codexUsageMeters(body)
}

const codexUserAgent = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/131.0.0.0 Safari/537.36"

// codexUsageMeters 取 rate_limit 的两个窗口。窗口缺失不算错(订阅档位不同
// 可能只有一个窗);两个都没有才报不可用,避免把空报告当成「额度为零」。
func codexUsageMeters(body []byte) ([]Meter, error) {
	var payload map[string]any
	if err := json.Unmarshal(body, &payload); err != nil {
		return nil, apperr.Wrap(apperr.QuotaUnavailable, "decode codex usage response", err)
	}
	rl, _ := mapOf(payload, "rate_limit")
	out := []Meter{}
	for _, w := range []struct {
		key      string
		fallback string
	}{
		{"primary_window", "5小时"},
		{"secondary_window", "7天"},
	} {
		win, ok := mapOf(rl, w.key)
		if !ok {
			continue
		}
		if m, ok := codexWindowMeter(win, w.fallback); ok {
			out = append(out, m)
		}
	}
	if len(out) == 0 {
		return nil, apperr.New(apperr.QuotaUnavailable,
			"codex usage response carried no rate limit windows")
	}
	return out, nil
}

func codexWindowMeter(win map[string]any, fallback string) (Meter, bool) {
	used, ok := numberOf(win["used_percent"])
	if !ok {
		return Meter{}, false
	}
	total := 100.0
	m := Meter{
		Kind:  provider.MeterUsage,
		Unit:  provider.UnitPercent,
		Label: codexWindowLabel(win["limit_window_seconds"], fallback),
		Total: &total,
		Used:  &used,
		Reset: provider.ResetRolling,
	}
	// reset_at 是 Unix 秒的 JSON 数字,timeOf 只认字符串形态。
	if secs, ok := numberOf(win["reset_at"]); ok && secs > 0 {
		t := time.Unix(int64(secs), 0).UTC()
		m.ResetAt = &t
	} else if t, ok := timeOf(win["reset_at"]); ok {
		m.ResetAt = &t
	}
	return m, true
}

// codexWindowLabel 由窗口秒数生成标签:5 小时与 7 天是已知档位,其余按
// 天/小时整除给名,拿不到秒数时用调用方的窗口默认名。
func codexWindowLabel(v any, fallback string) string {
	secs, ok := numberOf(v)
	if !ok || secs <= 0 {
		return fallback
	}
	switch {
	case secs == 18000:
		return "5小时"
	case secs == 604800:
		return "7天"
	case secs >= 86400:
		return strconv.Itoa(int(math.Round(secs/86400))) + "天"
	default:
		return strconv.Itoa(int(math.Round(secs/3600))) + "小时"
	}
}
