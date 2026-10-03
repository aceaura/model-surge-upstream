// kimi 会员月总额度查询链。月度配额(订阅 Balance)只在网页网关暴露:
// API key 可调 /coding/v1/usages 拿 5 小时/7 天窗,但 GetSubscriptionStats
// 要网页会话 JWT。链路:refresh_token(用户粘贴或取自 kimi-desktop 本地
// 存储,上游无硬轮换)→ auth.kimi.com 换短效 access_token(内存缓存到
// exp 前 5 分钟)→ www.kimi.com GetSubscriptionStats 取 subscriptionBalance。
package quota

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/aceaura/model-surge-upstream/backend/account"
	"github.com/aceaura/model-surge-upstream/backend/apperr"
	"github.com/aceaura/model-surge-upstream/backend/provider"
	"github.com/aceaura/model-surge-upstream/backend/ringlog"
)

const (
	// access_token 的保守兜底寿命:JWT exp 解析失败时按此时长缓存。
	kimiAccessFallbackTTL = 30 * time.Minute
	kimiAccessSkew        = 5 * time.Minute
)

// 端点是包级变量而非常量:测试桩上游时替换为 httptest 地址。
var (
	kimiRefreshURL = "https://auth.kimi.com/api/account.gateway.v1.AuthService/RefreshToken"
	kimiStatsURL   = "https://www.kimi.com/apiv2/kimi.gateway.membership.v2.MembershipService/GetSubscriptionStats"
)

// kimiAccessCache 按账号缓存换出的网页 access_token。短命令牌每次查询都
// 换会把刷新端点打成热点,也拖慢额度接口。
type kimiAccessCache struct {
	mu     sync.Mutex
	tokens map[string]kimiAccess
}

type kimiAccess struct {
	token   string
	expires time.Time
}

func (c *kimiAccessCache) get(accountName string) (string, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	a, ok := c.tokens[accountName]
	if !ok || time.Now().After(a.expires) {
		return "", false
	}
	return a.token, true
}

func (c *kimiAccessCache) put(accountName, token string, expires time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.tokens == nil {
		c.tokens = map[string]kimiAccess{}
	}
	c.tokens[accountName] = kimiAccess{token: token, expires: expires}
}

func (c *kimiAccessCache) drop(accountName string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	delete(c.tokens, accountName)
}

// appendKimiMonthly 对配了网页会话 refresh_token 的 kimi 账号追加「本月」
// 计量(会员月总额度)。查询失败只记日志不拖垮主报告——5 小时/7 天窗来自
// API key 链路,月度是增强项;令牌失效时主链路不应跟着黑屏。
func (q *Quota) appendKimiMonthly(ctx context.Context, spec provider.Spec, acc account.Account, report *Report) {
	if spec.ID != "kimi" || acc.Credential.WebRefreshToken == "" {
		return
	}
	m, err := q.kimiMonthlyMeter(ctx, acc)
	if err != nil {
		ringlog.Push("warn", "quota", fmt.Sprintf("kimi monthly quota for %s: %v", acc.Name, err))
		return
	}
	if m == nil {
		return
	}
	report.Queryable = true
	report.Meters = append(report.Meters, *m)
}

func (q *Quota) kimiMonthlyMeter(ctx context.Context, acc account.Account) (*Meter, error) {
	token, err := q.kimiAccessToken(ctx, acc)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, kimiStatsURL, strings.NewReader("{}"))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")

	resp, err := q.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("subscription stats request: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusUnauthorized {
		// 缓存的 access_token 提前失效:丢掉缓存,下次查询重新换。
		q.kimiTokens.drop(acc.Name)
		return nil, fmt.Errorf("subscription stats returned 401")
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("subscription stats returned %d", resp.StatusCode)
	}
	var payload map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		return nil, fmt.Errorf("decode subscription stats: %w", err)
	}
	return kimiMonthlyMeterOf(payload), nil
}

// kimiMonthlyMeterOf 把 GetSubscriptionStats 响应映射成「本月」计量。
// subscriptionBalance 是会员月度总额度(类型 SUBSCRIPTION 的 Balance),
// amountUsedRatio 是本月已用比例,expireTime 即月度重置时刻;免费用户
// 没有订阅 Balance,不出计量。字段名驼峰/蛇形都认(connect 两种 JSON 都见过)。
func kimiMonthlyMeterOf(payload map[string]any) *Meter {
	balance, ok := mapOf(payload, "subscriptionBalance", "subscription_balance")
	if !ok {
		return nil
	}
	ratio, ok := numberOf(anyOf(balance, "amountUsedRatio", "amount_used_ratio"))
	if !ok {
		return nil
	}
	used := math.Round(ratio*1000) / 10
	m := &Meter{
		Kind:  provider.MeterUsage,
		Unit:  provider.UnitPercent,
		Label: "本月",
		Used:  &used,
	}
	if s, ok := anyOf(balance, "expireTime", "expire_time").(string); ok {
		if t, ok := timeOf(s); ok {
			m.ResetAt = &t
		}
	}
	return m
}

func (q *Quota) kimiAccessToken(ctx context.Context, acc account.Account) (string, error) {
	if t, ok := q.kimiTokens.get(acc.Name); ok {
		return t, nil
	}
	body, err := json.Marshal(map[string]string{"refresh_token": acc.Credential.WebRefreshToken})
	if err != nil {
		return "", err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, kimiRefreshURL, bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := q.client.Do(req)
	if err != nil {
		return "", fmt.Errorf("refresh web token: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusUnauthorized {
		return "", apperr.New(apperr.QuotaUnavailable, "kimi web refresh token rejected (401), re-paste the session token")
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", fmt.Errorf("refresh web token returned %d", resp.StatusCode)
	}
	var payload map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		return "", fmt.Errorf("decode refresh response: %w", err)
	}
	token, _ := anyOf(payload, "accessToken", "access_token").(string)
	if token == "" {
		return "", fmt.Errorf("refresh response carried no access token")
	}
	q.kimiTokens.put(acc.Name, token, kimiAccessExpiry(token))
	return token, nil
}

// kimiAccessExpiry 从 JWT 载荷取 exp 减安全余量;解析失败走兜底寿命。
func kimiAccessExpiry(token string) time.Time {
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return time.Now().Add(kimiAccessFallbackTTL)
	}
	raw, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return time.Now().Add(kimiAccessFallbackTTL)
	}
	var claims struct {
		Exp int64 `json:"exp"`
	}
	if json.Unmarshal(raw, &claims) != nil || claims.Exp == 0 {
		return time.Now().Add(kimiAccessFallbackTTL)
	}
	return time.Unix(claims.Exp, 0).Add(-kimiAccessSkew)
}

// anyOf 按候选键名取第一个存在的值(驼峰/蛇形兼容)。
func anyOf(m map[string]any, keys ...string) any {
	for _, k := range keys {
		if v, ok := m[k]; ok {
			return v
		}
	}
	return nil
}

func mapOf(m map[string]any, keys ...string) (map[string]any, bool) {
	if v, ok := anyOf(m, keys...).(map[string]any); ok {
		return v, true
	}
	return nil, false
}
