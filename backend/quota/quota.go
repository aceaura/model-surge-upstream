// Package quota 查询账号额度。结果只缓存在进程内存里并带存活时长，
// 不落 PostgreSQL 也不进 Redis——额度是运行观测值，不是配置。
package quota

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
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

type Report struct {
	Account string `json:"account"`
	// Queryable 为假表示该 provider 未声明额度接口，这不是错误。
	Queryable bool               `json:"queryable"`
	Remaining *float64           `json:"remaining,omitempty"`
	Total     *float64           `json:"total,omitempty"`
	Currency  string             `json:"currency,omitempty"`
	Reset     provider.ResetRule `json:"reset,omitempty"`
	At        time.Time          `json:"at"`
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
		report := Report{Account: acc.Name, Queryable: false, At: time.Now().UTC()}
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
		Reset:     spec.Quota.Reset,
		At:        time.Now().UTC(),
	}
	remaining, total, currency := parseBalance(body)
	report.Remaining, report.Total, report.Currency = remaining, total, currency
	return report, nil
}

// parseBalance 从上游响应里尽力提取余量。各家字段名不一，认不出就只回
// Queryable=true 而不带数值，避免把猜测当事实报给运维者。
func parseBalance(body []byte) (remaining, total *float64, currency string) {
	var payload map[string]any
	if json.Unmarshal(body, &payload) != nil {
		return nil, nil, ""
	}
	// DeepSeek 形态：{"balance_infos":[{"currency":"CNY","total_balance":"12.34"}]}
	if infos, ok := payload["balance_infos"].([]any); ok && len(infos) > 0 {
		if first, ok := infos[0].(map[string]any); ok {
			if v, ok := numberOf(first["total_balance"]); ok {
				remaining = &v
			}
			if c, ok := first["currency"].(string); ok {
				currency = c
			}
			return remaining, nil, currency
		}
	}
	for _, key := range []string{"remaining", "remaining_credits", "balance", "credit_left"} {
		if v, ok := numberOf(payload[key]); ok {
			remaining = &v
			break
		}
	}
	for _, key := range []string{"total", "total_credits", "granted", "quota"} {
		if v, ok := numberOf(payload[key]); ok {
			total = &v
			break
		}
	}
	if c, ok := payload["currency"].(string); ok {
		currency = c
	}
	return remaining, total, currency
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
