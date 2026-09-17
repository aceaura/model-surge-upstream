// Package upmodels 查询上游账号实际可用的模型清单。与 quota 同类：结果是
// 运行观测值而非配置，只缓存在进程内存里并带存活时长，不落 PostgreSQL 也不进 Redis。
//
// 它回答的是「这个账号在上游能用哪些模型」，与下发面 /v1/models（本服务已配置
// 哪些模型）互补：前者是上游事实，后者是本地配置。
package upmodels

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sort"
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
	bodyLimit      = 1 << 20
)

// Entry 是上游模型条目。除 ID 外各家给的字段参差不齐，只收敛能普遍拿到的两项。
type Entry struct {
	ID          string `json:"id"`
	DisplayName string `json:"display_name,omitempty"`
}

type Report struct {
	Account string `json:"account"`
	// Queryable 为假表示该 provider 未声明列举接口，这不是错误。
	Queryable bool      `json:"queryable"`
	Models    []Entry   `json:"models"`
	At        time.Time `json:"at"`
}

type Accounts interface {
	Get(ctx context.Context, name string) (account.Account, error)
}

type entry struct {
	report  Report
	expires time.Time
}

type Lister struct {
	accounts Accounts
	client   *http.Client
	ttl      time.Duration

	mu     sync.RWMutex
	cached map[string]entry
}

func New(accounts Accounts, ttl time.Duration) *Lister {
	return &Lister{
		accounts: accounts,
		client:   &http.Client{Timeout: requestTimeout},
		ttl:      ttl,
		cached:   map[string]entry{},
	}
}

// SetClient 供测试注入桩上游。
func (l *Lister) SetClient(c *http.Client) { l.client = c }

func (l *Lister) List(ctx context.Context, accountName string) (Report, error) {
	if r, ok := l.lookup(accountName); ok {
		return r, nil
	}

	acc, err := l.accounts.Get(ctx, accountName)
	if err != nil {
		return Report{}, err
	}
	spec, ok := acc.Spec()
	if !ok {
		return Report{}, apperr.New(apperr.InvalidProvider,
			fmt.Sprintf("account %q references unknown provider %q", acc.Name, acc.ProviderID))
	}
	if spec.Models == nil {
		// 不可查询是一种正常答案，不是错误。
		report := Report{Account: acc.Name, Queryable: false, Models: []Entry{}, At: time.Now().UTC()}
		l.store(accountName, report)
		return report, nil
	}

	report, err := l.fetch(ctx, spec, acc)
	if err != nil {
		return Report{}, err
	}
	l.store(accountName, report)
	return report, nil
}

func (l *Lister) fetch(ctx context.Context, spec provider.Spec, acc account.Account) (Report, error) {
	method := spec.Models.Method
	if method == "" {
		method = http.MethodGet
	}
	url := strings.TrimRight(acc.EffectiveBaseURL(spec), "/") + spec.Models.Path
	req, err := http.NewRequestWithContext(ctx, method, url, nil)
	if err != nil {
		return Report{}, apperr.Wrap(apperr.UpstreamUnavailable, "build models request", err)
	}
	for k, v := range resolve.AuthHeaders(spec, acc) {
		req.Header.Set(k, v)
	}

	resp, err := l.client.Do(req)
	if err != nil {
		return Report{}, apperr.Wrap(apperr.UpstreamUnavailable, "models request failed", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, bodyLimit))
	if err != nil {
		return Report{}, apperr.Wrap(apperr.UpstreamUnavailable, "read models response", err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return Report{}, apperr.New(apperr.UpstreamUnavailable,
			fmt.Sprintf("upstream model listing returned %d", resp.StatusCode))
	}

	return Report{
		Account:   acc.Name,
		Queryable: true,
		Models:    parseEntries(body),
		At:        time.Now().UTC(),
	}, nil
}

// parseEntries 从上游响应里提取模型条目。各家外层键与条目字段名不一，
// 认不出就回空列表而不编造，避免把猜测当事实报给运维者。
func parseEntries(body []byte) []Entry {
	var payload map[string]any
	if json.Unmarshal(body, &payload) != nil {
		return []Entry{}
	}
	// OpenAI/Anthropic/DeepSeek 用 data，Gemini 用 models。
	var raw []any
	for _, key := range []string{"data", "models"} {
		if list, ok := payload[key].([]any); ok {
			raw = list
			break
		}
	}

	out := make([]Entry, 0, len(raw))
	seen := map[string]bool{}
	for _, item := range raw {
		obj, ok := item.(map[string]any)
		if !ok {
			continue
		}
		id := firstString(obj, "id", "name", "model")
		if id == "" || seen[id] {
			continue
		}
		seen[id] = true
		out = append(out, Entry{ID: id, DisplayName: firstString(obj, "display_name", "displayName")})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

func firstString(obj map[string]any, keys ...string) string {
	for _, k := range keys {
		if s, ok := obj[k].(string); ok && s != "" {
			return s
		}
	}
	return ""
}

func (l *Lister) lookup(name string) (Report, bool) {
	l.mu.RLock()
	defer l.mu.RUnlock()
	e, ok := l.cached[name]
	if !ok || time.Now().After(e.expires) {
		return Report{}, false
	}
	return e.report, true
}

func (l *Lister) store(name string, r Report) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.cached[name] = entry{report: r, expires: time.Now().Add(l.ttl)}
}

// Forget 丢弃某账号的缓存，供账号更新后调用。
func (l *Lister) Forget(name string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	delete(l.cached, name)
}
