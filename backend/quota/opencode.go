package quota

import (
	"context"
	"encoding/json"
	"math"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/aceaura/model-surge-upstream/backend/account"
	"github.com/aceaura/model-surge-upstream/backend/apperr"
	"github.com/aceaura/model-surge-upstream/backend/provider"
	"github.com/aceaura/model-surge-upstream/backend/resolve"
)

func opencodeGoMeters(ctx context.Context, q *Quota, spec provider.Spec, acc account.Account) ([]Meter, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet,
		strings.TrimRight(acc.EffectiveBaseURL(spec), "/")+"/v1/usage", nil)
	if err != nil {
		return nil, apperr.Wrap(apperr.QuotaUnavailable, "build quota request", err)
	}
	for key, value := range resolve.AuthHeaders(spec, acc) {
		req.Header.Set(key, value)
	}
	provider.ApplyRequestHeaders(spec.ID, "", acc.Name, "", req.Header)
	req.Header.Set("Accept", "application/json")
	body, header, err := q.do(req)
	if err != nil {
		return nil, err
	}
	meters, err := opencodeGoUsageMeters(body)
	if err != nil {
		return nil, err
	}
	return append(meters, parseRateLimitMeters(header)...), nil
}

func opencodeGoUsageMeters(body []byte) ([]Meter, error) {
	var payload map[string]any
	if err := json.Unmarshal(body, &payload); err != nil {
		return nil, apperr.Wrap(apperr.QuotaUnavailable, "decode opencode go usage response", err)
	}
	usage, _ := mapOf(payload, "usage")
	out := []Meter{}
	for _, window := range []struct {
		key   string
		label string
		reset provider.ResetRule
	}{
		{"rolling", "5小时", provider.ResetRolling},
		{"weekly", "7天", provider.ResetRolling},
		{"monthly", "本月", provider.ResetMonthly},
	} {
		win, ok := mapOf(usage, window.key)
		if !ok {
			continue
		}
		var used float64
		switch value := win["percent"].(type) {
		case float64:
			used = value
		case string:
			var err error
			used, err = strconv.ParseFloat(value, 64)
			if err != nil {
				continue
			}
		default:
			continue
		}
		if math.IsNaN(used) || math.IsInf(used, 0) || used < 0 || used > 100 {
			continue
		}
		total := 100.0
		meter := Meter{
			Kind:  provider.MeterUsage,
			Unit:  provider.UnitPercent,
			Label: window.label,
			Used:  &used,
			Total: &total,
			Reset: window.reset,
		}
		if raw, ok := win["resetsAt"].(string); ok {
			if resetAt, err := time.Parse(time.RFC3339, raw); err == nil {
				resetAt = resetAt.UTC()
				meter.ResetAt = &resetAt
			}
		}
		out = append(out, meter)
	}
	if len(out) == 0 {
		return nil, apperr.New(apperr.QuotaUnavailable, "opencode go usage response carried no valid quota windows")
	}
	return out, nil
}
