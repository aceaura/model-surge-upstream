package quota

// Kiro 配额:GetUsageLimits 走 CodeWhisperer 控制面(q.<region>.amazonaws.com),
// 按 nextToken 翻页。凭据是刷新型,查询前经 TokenSource 续期;region 从
// 凭据区域或 profile ARN 推导。请求头组与 Kiro IDE 实测流量逐字节对齐。

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"maps"
	"math"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/aceaura/model-surge-upstream/backend/account"
	"github.com/aceaura/model-surge-upstream/backend/apperr"
	"github.com/aceaura/model-surge-upstream/backend/kiro"
	"github.com/aceaura/model-surge-upstream/backend/provider"
)

// newScriptUUID 生成随机 UUID(v4):Amz-Sdk-Invocation-Id 要求每请求唯一。
func newScriptUUID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return fmt.Sprintf("%d", time.Now().UnixNano())
	}
	b[6] = b[6]&0x0f | 0x40
	b[8] = b[8]&0x3f | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}

func kiroMeters(ctx context.Context, q *Quota, spec provider.Spec, acc account.Account) ([]Meter, error) {
	if q.tokens == nil {
		return nil, apperr.New(apperr.QuotaUnavailable, "kiro quota requires a token source")
	}
	// 账号还没落 ARN 时先续期一次:续期过程回填 profileArn,重读账号拿新值。
	if strings.TrimSpace(acc.Credential.ProfileARN) == "" {
		if _, err := q.tokens.AccessToken(ctx, acc); err == nil {
			if fresh, err := q.accounts.Get(ctx, acc.Name); err == nil {
				acc = fresh
			}
		}
	}
	if strings.TrimSpace(acc.Credential.ProfileARN) == "" {
		return nil, apperr.New(apperr.InvalidRequest,
			"kiro quota requires the credential profileArn")
	}
	token, err := q.tokens.AccessToken(ctx, acc)
	if err != nil {
		return nil, apperr.Wrap(apperr.QuotaUnavailable, "refresh kiro access token", err)
	}
	region := acc.Credential.KiroAPIRegion()

	var pages []map[string]any
	nextToken := ""
	for range 100 {
		payload := map[string]any{
			"profileArn":   acc.Credential.ProfileARN,
			"origin":       "AI_EDITOR",
			"resourceType": "AGENTIC_REQUEST",
		}
		if nextToken != "" {
			payload["nextToken"] = nextToken
		}
		raw, err := json.Marshal(payload)
		if err != nil {
			return nil, err
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodPost,
			"https://q."+region+".amazonaws.com/", bytes.NewReader(raw))
		if err != nil {
			return nil, apperr.Wrap(apperr.QuotaUnavailable, "build quota request", err)
		}
		req.Header.Set("Authorization", "Bearer "+token)
		req.Header.Set("Accept", "application/json")
		req.Header.Set("Content-Type", "application/x-amz-json-1.0")
		req.Header.Set("x-amz-target", "com.amazon.aws.codewhisperer.runtime.AmazonCodeWhispererService.GetUsageLimits")
		req.Header.Set("User-Agent", kiro.ChatUserAgent())
		req.Header.Set("X-Amz-User-Agent", "aws-sdk-js/1.0.27 KiroIDE-0.7.45-"+kiro.Fingerprint())
		req.Header.Set("X-Amzn-Codewhisperer-Optout", "true")
		req.Header.Set("X-Amzn-Kiro-Agent-Mode", "vibe")
		req.Header.Set("Amz-Sdk-Invocation-Id", newScriptUUID())
		req.Header.Set("Amz-Sdk-Request", "attempt=1; max=3")

		body, _, err := q.do(req)
		if err != nil {
			return nil, err
		}
		var page map[string]any
		if err := json.Unmarshal(body, &page); err != nil {
			return nil, apperr.Wrap(apperr.QuotaUnavailable, "decode usage limits response", err)
		}
		pages = append(pages, page)
		// 重复 token 防护:上游回同样的 nextToken 说明翻页到头或卡死。
		nt, _ := page["nextToken"].(string)
		if nt == "" || nt == nextToken {
			break
		}
		nextToken = nt
	}
	return kiroPagesMeters(pages, time.Now())
}

// kiroPagesMeters 把翻页结果映射成计量项:每个 usageBreakdown 一条主计量,
// ACTIVE 且未过期的 freeTrialInfo/bonuses 各出一条赠额计量。
func kiroPagesMeters(pages []map[string]any, now time.Time) ([]Meter, error) {
	out := []Meter{}
	sawList := false
	for _, page := range pages {
		raw, ok := page["usageBreakdownList"].([]any)
		if !ok {
			return nil, apperr.New(apperr.QuotaUnavailable,
				"Kiro quota response has no usageBreakdownList array")
		}
		if len(raw) > 0 {
			sawList = true
		}
		pageReset, err := kiroTimeOf(page["nextDateReset"])
		if err != nil {
			return nil, err
		}
		// ccswitch-usage-script.js: 超额状态在页面级 overageConfiguration,
		// 上限在条目级 overageCap(WithPrecision 优先)。
		extraDoc := map[string]any{}
		for _, key := range []string{"subscriptionInfo", "userInfo"} {
			if value := page[key]; value != nil {
				extraDoc[key] = value
			}
		}
		if oc, ok := page["overageConfiguration"].(map[string]any); ok {
			if status, _ := oc["overageStatus"].(string); status != "" {
				extraDoc["overageStatus"] = status
			}
		}
		for _, item := range raw {
			u, ok := item.(map[string]any)
			if !ok {
				continue
			}
			// 上游 displayName 是英文资源名(Credit / Agentic requests),而这条
			// breakdown 恒按月重置,行内标签与其他供应商的月度窗统一叫「本月」。
			label := "本月"
			itemReset := pageReset
			if itemReset == nil {
				r, err := kiroTimeOf(u["resetDate"])
				if err != nil {
					return nil, err
				}
				itemReset = r
			}
			itemDoc := maps.Clone(extraDoc)
			if enabled, ok := u["overageEnabled"].(bool); ok {
				itemDoc["overageEnabled"] = enabled
			}
			if extraDoc["overageStatus"] == "ENABLED" {
				if v, ok := numberOf(u["overageCapWithPrecision"]); ok {
					itemDoc["overageCap"] = v
				} else if v, ok := numberOf(u["overageCap"]); ok {
					itemDoc["overageCap"] = v
				}
			}
			extra := ""
			if len(itemDoc) > 0 {
				if b, err := json.Marshal(itemDoc); err == nil {
					extra = string(b)
				}
			}
			if m, ok := kiroAllowanceMeter(u, label, itemReset, extra); ok {
				out = append(out, m)
			}
			grants, err := kiroGrants(u)
			if err != nil {
				return nil, err
			}
			for _, g := range grants {
				if g.status != "ACTIVE" {
					continue
				}
				end, err := kiroTimeOf(g.expiry)
				if err != nil {
					return nil, err
				}
				if end != nil && !end.After(now) {
					continue
				}
				endISO := ""
				if end != nil {
					endISO = end.Format(time.RFC3339)
				}
				metaDoc := map[string]any{
					"subscriptionInfo": page["subscriptionInfo"],
					"status":           g.status,
					"expiresAt":        endISO,
					"bonusCode":        g.bonusCode,
				}
				if userInfo := page["userInfo"]; userInfo != nil {
					metaDoc["userInfo"] = userInfo
				}
				meta, _ := json.Marshal(metaDoc)
				if m, ok := kiroAllowanceMeter(g.body, g.suffix, nil, string(meta)); ok {
					out = append(out, m)
				}
			}
		}
	}
	if sawList && len(out) == 0 {
		return nil, apperr.New(apperr.QuotaUnavailable,
			"Kiro usage breakdown contains no credit allowances")
	}
	return out, nil
}

// kiroAllowanceMeter 映射一条额度:usageLimitWithPrecision 优先、usageLimit
// 回退;total 与 used 齐备时 remaining=max(0,total-used)。
func kiroAllowanceMeter(a map[string]any, label string, resetAt *time.Time, extra string) (Meter, bool) {
	num := func(keys ...string) *float64 {
		for _, k := range keys {
			if v, ok := numberOf(a[k]); ok {
				return &v
			}
		}
		return nil
	}
	total := num("usageLimitWithPrecision", "usageLimit")
	used := num("currentUsageWithPrecision", "currentUsage")
	if total == nil && used == nil {
		return Meter{}, false
	}
	m := Meter{
		Kind:  provider.MeterUsage,
		Unit:  provider.UnitCredits,
		Label: label,
		Total: total,
		Used:  used,
		Reset: provider.ResetMonthly,
		Extra: extra,
	}
	if total != nil && used != nil {
		remaining := math.Max(0, *total-*used)
		m.Remaining = &remaining
	}
	m.ResetAt = resetAt
	return m, true
}

type kiroGrant struct {
	body      map[string]any
	status    string
	expiry    any
	suffix    string
	bonusCode string
}

// kiroGrants 收集一条 breakdown 上的赠额:freeTrialInfo 与 bonuses 同构处理。
func kiroGrants(u map[string]any) ([]kiroGrant, error) {
	var out []kiroGrant
	if ft, ok := u["freeTrialInfo"].(map[string]any); ok {
		status, _ := ft["freeTrialStatus"].(string)
		out = append(out, kiroGrant{body: ft, status: status, expiry: ft["freeTrialExpiry"], suffix: "试用"})
	}
	if bonuses, ok := u["bonuses"].([]any); ok {
		for _, raw := range bonuses {
			b, ok := raw.(map[string]any)
			if !ok {
				continue
			}
			status, _ := b["status"].(string)
			name, _ := b["displayName"].(string)
			code, _ := b["bonusCode"].(string)
			suffix := "赠额 " + name
			if name == "" {
				suffix = "赠额 " + code
			}
			out = append(out, kiroGrant{body: b, status: status, expiry: b["expiresAt"],
				suffix: strings.TrimSpace(suffix), bonusCode: code})
		}
	}
	return out, nil
}

// kiroTimeOf 归一 Kiro 的时刻写法:数字与纯数字字符串按 Unix 秒,RFC3339
// 原样解析,日期型(2006-01-02)补午夜 UTC;空值返回 nil,认不出报错。
func kiroTimeOf(v any) (*time.Time, error) {
	switch t := v.(type) {
	case nil:
		return nil, nil
	case float64:
		tm := time.UnixMilli(int64(math.Round(t * 1000))).UTC()
		return &tm, nil
	case string:
		if t == "" {
			return nil, nil
		}
		if secs, err := strconv.ParseInt(t, 10, 64); err == nil {
			tm := time.Unix(secs, 0).UTC()
			return &tm, nil
		}
		if tm, err := time.Parse(time.RFC3339, t); err == nil {
			tm = tm.UTC()
			return &tm, nil
		}
		if tm, err := time.Parse("2006-01-02", t); err == nil {
			tm = tm.UTC()
			return &tm, nil
		}
	}
	return nil, apperr.New(apperr.QuotaUnavailable,
		fmt.Sprintf("invalid Kiro timestamp: %v", v))
}
