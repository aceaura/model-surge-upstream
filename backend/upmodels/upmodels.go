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
	"github.com/aceaura/model-surge-upstream/backend/codex"
	"github.com/aceaura/model-surge-upstream/backend/kiro"
	"github.com/aceaura/model-surge-upstream/backend/provider"
	"github.com/aceaura/model-surge-upstream/backend/resolve"
)

const (
	requestTimeout = 10 * time.Second
	bodyLimit      = 1 << 20
)

// Entry 是上游模型条目。除 ID 外各家给的字段参差不齐,只收敛能普遍拿到的
// 两项,外加推理档声明(动态适配数据源,见 effort 包)。
type Entry struct {
	ID          string `json:"id"`
	DisplayName string `json:"display_name,omitempty"`
	// Efforts 是上游为该模型声明的推理档(supported_reasoning_levels,
	// 原值,声明序)。nil 表示上游未声明——不等于不支持由本地判定,
	// 调用方按「无声明即不支持」处置。
	Efforts []string `json:"efforts,omitempty"`
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

// HeaderSource 按账号凭据形态构造上游头(resolve.Resolver 实现):
// oauth 账号的静态头只有空 Bearer,活体 token 与 codex 身份头离不开
// token 来源。未装配时回落 resolve.AuthHeaders 的静态形态。
type HeaderSource interface {
	HeadersFor(ctx context.Context, spec provider.Spec, acc account.Account) (map[string]string, error)
}

type entry struct {
	report  Report
	expires time.Time
}

type Lister struct {
	accounts     Accounts
	headerSource HeaderSource
	client       *http.Client
	ttl          time.Duration

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

// WithHeaderSource 挂上凭据形态感知的头来源;不挂则用静态认证头。
func (l *Lister) WithHeaderSource(h HeaderSource) *Lister {
	l.headerSource = h
	return l
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
	if spec.ID == kiro.ProviderID {
		return l.fetchKiro(ctx, spec, acc)
	}
	method := spec.Models.Method
	if method == "" {
		method = http.MethodGet
	}
	url := strings.TrimRight(acc.EffectiveBaseURL(spec), "/") + spec.Models.Path
	if spec.ID == codex.ProviderID {
		// 订阅清单端点的 client_version 协商是硬条件,缺了恒 400。
		url = codex.ModelsURL(acc.EffectiveBaseURL(spec))
	}
	req, err := http.NewRequestWithContext(ctx, method, url, nil)
	if err != nil {
		return Report{}, apperr.Wrap(apperr.UpstreamUnavailable, "build models request", err)
	}
	headers := resolve.AuthHeaders(spec, acc)
	if l.headerSource != nil {
		if headers, err = l.headerSource.HeadersFor(ctx, spec, acc); err != nil {
			return Report{}, err
		}
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	// 清单是 JSON GET:凭据形态头里给 SSE 转发准备的 Accept 在此不适用。
	req.Header.Set("Accept", "application/json")

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
		id := firstString(obj, "id", "modelId", "name", "model", "slug")
		if id == "" || seen[id] {
			continue
		}
		seen[id] = true
		out = append(out, Entry{
			ID:          id,
			DisplayName: firstString(obj, "display_name", "displayName", "modelName"),
			Efforts:     parseDeclaredEfforts(obj),
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

// parseDeclaredEfforts 提取条目的 supported_reasoning_levels。两种形态都认:
// codex 订阅端点是 [{effort, description}] 对象数组,OpenAI 兼容网关是
// 字符串数组(sub2api 同款双形态解析)。未声明回 nil;值按上游原样保留
// (去空白去空),去重与显示名推导在 effort.Effective 统一收口。
func parseDeclaredEfforts(obj map[string]any) []string {
	raw, ok := obj["supported_reasoning_levels"].([]any)
	if !ok || len(raw) == 0 {
		return nil
	}
	levels := make([]string, 0, len(raw))
	for _, item := range raw {
		switch v := item.(type) {
		case string:
			if s := strings.TrimSpace(v); s != "" {
				levels = append(levels, s)
			}
		case map[string]any:
			if s, ok := v["effort"].(string); ok {
				if s = strings.TrimSpace(s); s != "" {
					levels = append(levels, s)
				}
			}
		}
	}
	return levels
}

// DeclaredEfforts 返回账号某原生模型在上游声明的推理档(原值,声明序)。
// 上游不可查、查询失败或该模型未声明都回 nil——无声明即不支持,
// 这是正常答案而非错误,调用方按此隐藏档位入口。
func (l *Lister) DeclaredEfforts(ctx context.Context, accountName, nativeModel string) []string {
	r, err := l.List(ctx, accountName)
	if err != nil {
		return nil
	}
	for _, e := range r.Models {
		if e.ID == nativeModel {
			return e.Efforts
		}
	}
	return nil
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
