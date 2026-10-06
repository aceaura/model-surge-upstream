package quota

// 内置额度查询:按 provider ID 整合的 Go 实现,是唯一的查询路径(账号级
// JS 脚本通道已废除)。注册表键集合必须恰好等于声明了 QuotaQueryable 的
// provider 规格,builtin_test.go 有一致性测试兜底。

import (
	"context"
	"fmt"
	"io"
	"net/http"

	"github.com/aceaura/model-surge-upstream/backend/account"
	"github.com/aceaura/model-surge-upstream/backend/apperr"
	"github.com/aceaura/model-surge-upstream/backend/provider"
)

const bodyLimit = 64 * 1024

// builtinQuery 某 provider 的内置额度查询。端点通了但响应无可识别字段时
// 返回空列表(Queryable 仍为 true),认不出的字段不猜。
type builtinQuery func(ctx context.Context, q *Quota, spec provider.Spec, acc account.Account) ([]Meter, error)

var builtinQuotas = map[string]builtinQuery{
	"bailian-cn/token-plan": bailianMeters,
	"deepseek/api":          deepseekMeters,
	"kimi/coding":           kimiUsagesMeters,
	"kiro":                  kiroMeters,
	"openai/codex":          codexMeters,
}

// do 执行一次上游请求并读出响应体:传输错误与非 2xx 归一为 QuotaUnavailable。
func (q *Quota) do(req *http.Request) ([]byte, http.Header, error) {
	resp, err := q.client.Do(req)
	if err != nil {
		return nil, nil, apperr.Wrap(apperr.QuotaUnavailable, "quota request failed", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, bodyLimit))
	if err != nil {
		return nil, nil, apperr.Wrap(apperr.QuotaUnavailable, "read quota response", err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, nil, apperr.New(apperr.QuotaUnavailable,
			fmt.Sprintf("upstream quota query returned %d", resp.StatusCode))
	}
	return body, resp.Header, nil
}

// parseRateLimitMeters 读取滚动速率窗口。这些维度只出现在响应头里，
// requests 与 tokens 各自独立计数、各自重置，因此是两条计量项。
func parseRateLimitMeters(header http.Header) []Meter {
	if header == nil {
		return nil
	}
	dims := []struct {
		unit  provider.MeterUnit
		label string
		slug  string
	}{
		{provider.UnitRequests, "requests", "requests"},
		{provider.UnitTokens, "tokens", "tokens"},
	}
	var out []Meter
	for _, d := range dims {
		m := Meter{
			Kind:  provider.MeterRateLimit,
			Unit:  d.unit,
			Label: d.label,
			Reset: provider.ResetRolling,
		}
		if v, ok := numberOf(header.Get("x-ratelimit-remaining-" + d.slug)); ok {
			m.Remaining = &v
		}
		if v, ok := numberOf(header.Get("x-ratelimit-limit-" + d.slug)); ok {
			m.Total = &v
		}
		if m.Remaining == nil && m.Total == nil {
			continue
		}
		if t, ok := timeOf(header.Get("x-ratelimit-reset-" + d.slug)); ok {
			m.ResetAt = &t
		}
		out = append(out, m)
	}
	return out
}
