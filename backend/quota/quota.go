// Package quota 查询账号额度。结果只缓存在进程内存里并带存活时长，
// 不落 PostgreSQL 也不进 Redis——额度是运行观测值，不是配置。
package quota

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"sync"
	"time"

	"github.com/aceaura/model-surge-upstream/backend/account"
	"github.com/aceaura/model-surge-upstream/backend/apperr"
	"github.com/aceaura/model-surge-upstream/backend/provider"
)

const requestTimeout = 10 * time.Second

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
	// Extra 附带自由文本(套餐说明、到期日、订阅信息等),原样透传。
	Extra string `json:"extra,omitempty"`
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

// TokenSource 为刷新型凭据的账号在内置查询前取出当前可用的
// access_token(必要时续期)。由 oauth.Manager 实现,kiro 的内置查询依赖它。
type TokenSource interface {
	AccessToken(ctx context.Context, acc account.Account) (string, error)
}

type entry struct {
	report  Report
	expires time.Time
}

type Quota struct {
	accounts Accounts
	client   *http.Client
	ttl      time.Duration
	tokens   TokenSource

	kimiTokens kimiAccessCache

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

// SetTokenSource 接线刷新型凭据的 access_token 来源(oauth.Manager),
// kiro 的内置查询在执行前经它续期。
func (q *Quota) SetTokenSource(t TokenSource) { q.tokens = t }

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

	// 账号关掉了实时查询:不打上游,按不可查回答(与 provider 未声明
	// 同形),缓存照常生效。
	if !acc.QuotaSettings.QuotaEnabled() {
		report := Report{Account: acc.Name, Queryable: false, Meters: []Meter{}, At: time.Now().UTC()}
		q.store(accountName, report)
		return report, nil
	}

	var report Report
	if fn, ok := builtinQuotas[acc.ProviderID]; ok {
		// 内置实现按供应商整合在代码内(见 builtin.go);kimi 的会员月度
		// 额度由 appendKimiMonthly 在内置结果上另补。
		meters, err := fn(ctx, q, spec, acc)
		if err != nil {
			return Report{}, err
		}
		if meters == nil {
			meters = []Meter{}
		}
		report = Report{Account: acc.Name, Queryable: true, Meters: meters, At: time.Now().UTC()}
	} else {
		// 不可查询是一种正常答案，不是错误。
		report = Report{Account: acc.Name, Queryable: false, Meters: []Meter{}, At: time.Now().UTC()}
	}
	q.appendKimiMonthly(ctx, spec, acc, &report)
	q.store(accountName, report)
	return report, nil
}

// timeOf 接受 RFC3339 时刻与 Unix 秒两种写法：脚本提取器两种都会给。
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
	case int64:
		return float64(t), true
	case int:
		return float64(t), true
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

// Cached 返回缓存报告(无视存活期):额度定时轮询对空闲账号只回过缓存,
// 不触发上游查询,报告自带的查询时刻自然变老以表达"已停刷"。
// 无缓存返回 ok=false。
func (q *Quota) Cached(name string) (Report, bool) {
	q.mu.RLock()
	defer q.mu.RUnlock()
	e, ok := q.cached[name]
	return e.report, ok
}

// Forget 丢弃某账号的缓存，供账号更新后调用。
func (q *Quota) Forget(name string) {
	q.mu.Lock()
	defer q.mu.Unlock()
	delete(q.cached, name)
	q.kimiTokens.drop(name)
}
