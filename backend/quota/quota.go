// Package quota 查询账号额度。结果只缓存在进程内存里并带存活时长，
// 不落 PostgreSQL 也不进 Redis——额度是运行观测值，不是配置。
package quota

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/aceaura/model-surge-upstream/backend/account"
	"github.com/aceaura/model-surge-upstream/backend/apperr"
	"github.com/aceaura/model-surge-upstream/backend/provider"
	"github.com/aceaura/model-surge-upstream/backend/resolve"
)

const (
	requestTimeout = 10 * time.Second
	bodyLimit      = 64 * 1024
)

// Meter 一条计量项。上游额度不是单一数值：预付费只有余额，后付费只有
// 已用量，速率窗口则是 requests 与 tokens 两条独立计数各自重置。
// 因此报告承载一组计量项，单值形态退化成只有一项。
type Meter struct {
	Kind  provider.MeterKind `json:"kind"`
	Unit  provider.MeterUnit `json:"unit"`
	Label string             `json:"label,omitempty"`
	// Currency 仅在 Unit 为 currency 时有意义。
	Currency  string             `json:"currency,omitempty"`
	Remaining *float64           `json:"remaining,omitempty"`
	Total     *float64           `json:"total,omitempty"`
	Used      *float64           `json:"used,omitempty"`
	Reset     provider.ResetRule `json:"reset,omitempty"`
	// ResetAt 下次重置的绝对时刻，上游给了才有。周期配额与速率窗口下
	// 这比静态的 Reset 规律更有用。
	ResetAt *time.Time `json:"reset_at,omitempty"`
}

type Report struct {
	Account string `json:"account"`
	// Queryable 为假表示该 provider 未声明额度接口，这不是错误。
	Queryable bool      `json:"queryable"`
	Meters    []Meter   `json:"meters"`
	At        time.Time `json:"at"`
}

type Accounts interface {
	Get(ctx context.Context, name string) (account.Account, error)
}

type entry struct {
	report  Report
	expires time.Time
}

type Quota struct {
	accounts Accounts
	client   *http.Client
	ttl      time.Duration

	mu     sync.RWMutex
	cached map[string]entry
}

func New(accounts Accounts, ttl time.Duration) *Quota {
	return &Quota{
		accounts: accounts,
		client:   &http.Client{Timeout: requestTimeout},
		ttl:      ttl,
		cached:   map[string]entry{},
	}
}

// SetClient 供测试注入桩上游。
func (q *Quota) SetClient(c *http.Client) { q.client = c }

func (q *Quota) Query(ctx context.Context, accountName string) (Report, error) {
	if r, ok := q.lookup(accountName); ok {
		return r, nil
	}

	acc, err := q.accounts.Get(ctx, accountName)
	if err != nil {
		return Report{}, err
	}
	spec, ok := acc.Spec()
	if !ok {
		return Report{}, apperr.New(apperr.InvalidProvider,
			fmt.Sprintf("account %q references unknown provider %q", acc.Name, acc.ProviderID))
	}
	if spec.Quota == nil {
		// 不可查询是一种正常答案，不是错误。
		report := Report{Account: acc.Name, Queryable: false, Meters: []Meter{}, At: time.Now().UTC()}
		q.store(accountName, report)
		return report, nil
	}

	report, err := q.fetch(ctx, spec, acc)
	if err != nil {
		return Report{}, err
	}
	q.store(accountName, report)
	return report, nil
}

func (q *Quota) fetch(ctx context.Context, spec provider.Spec, acc account.Account) (Report, error) {
	method := spec.Quota.Method
	if method == "" {
		method = http.MethodGet
	}
	url := strings.TrimRight(acc.EffectiveBaseURL(spec), "/") + spec.Quota.Path
	req, err := http.NewRequestWithContext(ctx, method, url, nil)
	if err != nil {
		return Report{}, apperr.Wrap(apperr.QuotaUnavailable, "build quota request", err)
	}
	for k, v := range resolve.AuthHeaders(spec, acc) {
		req.Header.Set(k, v)
	}

	resp, err := q.client.Do(req)
	if err != nil {
		return Report{}, apperr.Wrap(apperr.QuotaUnavailable, "quota request failed", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, bodyLimit))
	if err != nil {
		return Report{}, apperr.Wrap(apperr.QuotaUnavailable, "read quota response", err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return Report{}, apperr.New(apperr.QuotaUnavailable,
			fmt.Sprintf("upstream quota query returned %d", resp.StatusCode))
	}

	report := Report{
		Account:   acc.Name,
		Queryable: true,
		Meters:    parseMeters(body, resp.Header, *spec.Quota),
		At:        time.Now().UTC(),
	}
	return report, nil
}

// parseMeters 从响应体与响应头里尽力提取计量项。各家字段名不一，认不出就
// 回空列表而不是猜一个数出来——只报 Queryable=true 表示端点通了。
// 声明的 kind/unit/reset 作为体内计量项的兜底语义。
func parseMeters(body []byte, header http.Header, decl provider.QuotaAPI) []Meter {
	out := parseBodyMeters(body, decl)
	out = append(out, parseRateLimitMeters(header)...)
	return out
}

func parseBodyMeters(body []byte, decl provider.QuotaAPI) []Meter {
	var payload map[string]any
	if json.Unmarshal(body, &payload) != nil {
		return []Meter{}
	}

	base := Meter{Kind: decl.Kind, Unit: decl.Unit, Reset: decl.Reset}

	// DeepSeek 形态：{"balance_infos":[{"currency":"CNY","total_balance":"12.34"}]}
	// 多币种时每种是一条独立计量项。
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
	for _, key := range []string{"total", "total_credits", "granted", "quota", "hard_limit_usd"} {
		if v, ok := numberOf(payload[key]); ok {
			m.Total = &v
			break
		}
	}
	// 后付费形态只报已用量，没有余量可言。
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

// timeOf 接受 RFC3339 时刻与 Unix 秒两种写法：各家响应头两种都有。
func timeOf(v any) (time.Time, bool) {
	s, ok := v.(string)
	if !ok || s == "" {
		return time.Time{}, false
	}
	if t, err := time.Parse(time.RFC3339, s); err == nil {
		return t.UTC(), true
	}
	if secs, err := strconv.ParseInt(s, 10, 64); err == nil {
		return time.Unix(secs, 0).UTC(), true
	}
	return time.Time{}, false
}

func numberOf(v any) (float64, bool) {
	switch t := v.(type) {
	case float64:
		return t, true
	case json.Number:
		f, err := t.Float64()
		return f, err == nil
	case string:
		var f float64
		if _, err := fmt.Sscanf(t, "%g", &f); err == nil {
			return f, true
		}
	}
	return 0, false
}

func (q *Quota) lookup(name string) (Report, bool) {
	q.mu.RLock()
	defer q.mu.RUnlock()
	e, ok := q.cached[name]
	if !ok || time.Now().After(e.expires) {
		return Report{}, false
	}
	return e.report, true
}

func (q *Quota) store(name string, r Report) {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.cached[name] = entry{report: r, expires: time.Now().Add(q.ttl)}
}

// Forget 丢弃某账号的缓存，供账号更新后调用。
func (q *Quota) Forget(name string) {
	q.mu.Lock()
	defer q.mu.Unlock()
	delete(q.cached, name)
}
