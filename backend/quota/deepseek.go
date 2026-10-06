package quota

// DeepSeek 余额:GET /user/balance 的 balance_infos 每币种一条计量;
// 响应头的速率窗口另出 requests/tokens 两条。预付费形态,充值才涨。

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"

	"github.com/aceaura/model-surge-upstream/backend/account"
	"github.com/aceaura/model-surge-upstream/backend/apperr"
	"github.com/aceaura/model-surge-upstream/backend/provider"
	"github.com/aceaura/model-surge-upstream/backend/resolve"
)

func deepseekMeters(ctx context.Context, q *Quota, spec provider.Spec, acc account.Account) ([]Meter, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet,
		strings.TrimRight(acc.EffectiveBaseURL(spec), "/")+"/user/balance", nil)
	if err != nil {
		return nil, apperr.Wrap(apperr.QuotaUnavailable, "build quota request", err)
	}
	for k, v := range resolve.AuthHeaders(spec, acc) {
		req.Header.Set(k, v)
	}
	body, header, err := q.do(req)
	if err != nil {
		return nil, err
	}
	return append(deepseekBodyMeters(body), parseRateLimitMeters(header)...), nil
}

// deepseekBodyMeters 解析余额响应:{"balance_infos":[{"currency":"CNY",
// "total_balance":"12.34"}]} 每币种一条;认不出的字段不猜,回退通用键名
// 仍无命中则空列表(只报 Queryable=true 表示端点通了)。
func deepseekBodyMeters(body []byte) []Meter {
	var payload map[string]any
	if json.Unmarshal(body, &payload) != nil {
		return []Meter{}
	}

	base := Meter{Kind: provider.MeterBalance, Unit: provider.UnitCurrency, Reset: provider.ResetPrepaid}

	if infos, ok := payload["balance_infos"].([]any); ok {
		out := []Meter{}
		for _, raw := range infos {
			info, ok := raw.(map[string]any)
			if !ok {
				continue
			}
			m := base
			if v, ok := numberOf(info["total_balance"]); ok {
				m.Remaining = &v
			}
			if c, ok := info["currency"].(string); ok {
				m.Currency, m.Label = c, c
			}
			if m.Remaining != nil {
				out = append(out, m)
			}
		}
		if len(out) > 0 {
			return out
		}
	}

	m := base
	for _, key := range []string{"remaining", "remaining_credits", "balance", "credit_left"} {
		if v, ok := numberOf(payload[key]); ok {
			m.Remaining = &v
			break
		}
	}
	for _, key := range []string{"total", "total_credits", "granted", "quota"} {
		if v, ok := numberOf(payload[key]); ok {
			m.Total = &v
			break
		}
	}
	for _, key := range []string{"used", "usage", "total_usage", "spent"} {
		if v, ok := numberOf(payload[key]); ok {
			m.Used = &v
			break
		}
	}
	if c, ok := payload["currency"].(string); ok {
		m.Currency = c
	}
	if t, ok := timeOf(payload["reset_at"]); ok {
		m.ResetAt = &t
	}
	if m.Remaining == nil && m.Total == nil && m.Used == nil {
		return []Meter{}
	}
	return []Meter{m}
}
