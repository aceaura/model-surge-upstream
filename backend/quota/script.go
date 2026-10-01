// 账号级额度查询脚本的执行器,CC Switch usage_script 同款机制:
// 脚本是一个 JS 对象字面量 ({ request: {...}, extractor: function(response) {...} }),
// request 描述上游查询请求,extractor 把响应 JSON 映射成计量项。
// 代码里的 {{apiKey}} / {{baseUrl}} 在执行前替换为账号内置凭据与生效地址,
// 脚本本身不保存密钥。每个渠道的端点、字段与格式编排都不一样,因此代码随账号存储。
package quota

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/dop251/goja"

	"github.com/aceaura/model-surge-upstream/backend/account"
	"github.com/aceaura/model-surge-upstream/backend/apperr"
	"github.com/aceaura/model-surge-upstream/backend/provider"
)

const (
	defaultScriptTimeout = 10 * time.Second
	maxScriptTimeout     = 120 * time.Second
	scriptBodyLimit      = 256 * 1024
)

// scriptTimeout 归一化脚本超时:0 走默认,负值按默认,封顶 maxScriptTimeout。
func scriptTimeout(seconds int) time.Duration {
	if seconds <= 0 {
		return defaultScriptTimeout
	}
	d := time.Duration(seconds) * time.Second
	if d > maxScriptTimeout {
		return maxScriptTimeout
	}
	return d
}

// RunScript 对账号执行一段额度脚本并返回报告。测试入口与正式查询共用:
// 测试传未落库的代码,正式查询传账号已存代码。
func RunScript(ctx context.Context, spec provider.Spec, acc account.Account, code string, timeoutSeconds int) (Report, error) {
	code = strings.TrimSpace(code)
	if code == "" {
		return Report{}, apperr.New(apperr.InvalidRequest, "quota script code is empty")
	}
	timeout := scriptTimeout(timeoutSeconds)

	// 变量替换在求值前做纯文本替换,与 CC Switch 行为一致:
	// {{apiKey}} 取账号内置凭据,{{baseUrl}} 取生效地址(去尾斜杠)。
	code = strings.NewReplacer(
		"{{apiKey}}", acc.Credential.APIKey,
		"{{baseUrl}}", strings.TrimRight(acc.EffectiveBaseURL(spec), "/"),
	).Replace(code)

	vm := goja.New()
	// 超时中断覆盖求值与 extractor 调用两段 JS;HTTP 阶段走客户端自身超时。
	timer := time.AfterFunc(timeout, func() { vm.Interrupt("quota script timeout") })
	defer timer.Stop()

	v, err := vm.RunString(code)
	if err != nil {
		return Report{}, apperr.Wrap(apperr.QuotaUnavailable, "quota script eval", err)
	}
	if goja.IsUndefined(v) || goja.IsNull(v) {
		return Report{}, apperr.New(apperr.QuotaUnavailable, "quota script must evaluate to an object")
	}
	obj := v.ToObject(vm)
	if obj == nil {
		return Report{}, apperr.New(apperr.QuotaUnavailable, "quota script must evaluate to an object")
	}

	extractor, ok := goja.AssertFunction(obj.Get("extractor"))
	if !ok {
		return Report{}, apperr.New(apperr.QuotaUnavailable, "quota script missing extractor function")
	}

	req, err := buildScriptRequest(ctx, vm, obj.Get("request"))
	if err != nil {
		return Report{}, err
	}

	client := &http.Client{Timeout: timeout}
	resp, err := client.Do(req)
	if err != nil {
		return Report{}, apperr.Wrap(apperr.QuotaUnavailable, "quota script request failed", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, scriptBodyLimit))
	if err != nil {
		return Report{}, apperr.Wrap(apperr.QuotaUnavailable, "read quota script response", err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return Report{}, apperr.New(apperr.QuotaUnavailable,
			fmt.Sprintf("upstream quota query returned %d", resp.StatusCode))
	}

	// 响应按 JSON 喂给 extractor;非 JSON 原样给字符串,由脚本自行处理。
	var payload any
	if err := json.Unmarshal(body, &payload); err != nil {
		payload = string(body)
	}

	res, err := extractor(goja.Undefined(), vm.ToValue(payload))
	if err != nil {
		return Report{}, apperr.Wrap(apperr.QuotaUnavailable, "quota script extractor", err)
	}

	meters, err := scriptMeters(res.Export())
	if err != nil {
		return Report{}, err
	}
	return Report{
		Account:   acc.Name,
		Queryable: true,
		Meters:    meters,
		At:        time.Now().UTC(),
	}, nil
}

// buildScriptRequest 从脚本的 request 块构造 HTTP 请求。url 必填,
// method 默认 GET,headers 逐对设置,body 可选字符串。认证头不自动叠加:
// 脚本用 {{apiKey}} 显式声明,渠道放哪个头由渠道脚本说了算。
func buildScriptRequest(ctx context.Context, vm *goja.Runtime, v goja.Value) (*http.Request, error) {
	if goja.IsUndefined(v) || goja.IsNull(v) {
		return nil, apperr.New(apperr.QuotaUnavailable, "quota script missing request block")
	}
	obj := v.ToObject(vm)
	if obj == nil {
		return nil, apperr.New(apperr.QuotaUnavailable, "quota script missing request block")
	}
	url := strings.TrimSpace(jsString(obj.Get("url")))
	if url == "" {
		return nil, apperr.New(apperr.QuotaUnavailable, "quota script request.url is required")
	}
	method := strings.ToUpper(strings.TrimSpace(jsString(obj.Get("method"))))
	if method == "" {
		method = http.MethodGet
	}
	var body io.Reader
	if b := obj.Get("body"); b != nil && !goja.IsUndefined(b) && !goja.IsNull(b) {
		body = strings.NewReader(b.String())
	}
	req, err := http.NewRequestWithContext(ctx, method, url, body)
	if err != nil {
		return nil, apperr.Wrap(apperr.QuotaUnavailable, "build quota script request", err)
	}
	if h := obj.Get("headers"); h != nil && !goja.IsUndefined(h) && !goja.IsNull(h) {
		if hobj := h.ToObject(vm); hobj != nil {
			for _, k := range hobj.Keys() {
				req.Header.Set(k, hobj.Get(k).String())
			}
		}
	}
	return req, nil
}

// jsString 安全取字符串属性:goja 的 Object.Get 对缺失键返回 Go nil,
// 直接 .String() 会空指针;nil/undefined/null 一律归空串。
func jsString(v goja.Value) string {
	if v == nil || goja.IsUndefined(v) || goja.IsNull(v) {
		return ""
	}
	return v.String()
}

// scriptResult 是 extractor 返回值的落点,字段名与 CC Switch 的 UsageData 对齐,
// 使用户的既有渠道脚本可以原样搬过来。
type scriptResult struct {
	isValid        *bool
	invalidMessage string
	planName       string
	remaining      *float64
	used           *float64
	total          *float64
	unit           string
	currency       string
	extra          string
	resetsAt       string
}

// scriptMeters 把 extractor 的返回值(单对象或对象数组)映射成计量项。
// isValid 为 false 是脚本明确报告的失效(密钥过期、套餐停用等),整体判失败
// 并带上脚本给的说明。
func scriptMeters(exported any) ([]Meter, error) {
	if exported == nil {
		return nil, apperr.New(apperr.QuotaUnavailable, "quota script extractor returned nothing")
	}
	var items []any
	switch v := exported.(type) {
	case map[string]any:
		items = []any{v}
	case []any:
		items = v
	default:
		return nil, apperr.New(apperr.QuotaUnavailable,
			fmt.Sprintf("quota script extractor must return an object or array, got %T", exported))
	}

	out := []Meter{}
	for _, raw := range items {
		m, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		r := toScriptResult(m)
		if r.isValid != nil && !*r.isValid {
			msg := r.invalidMessage
			if msg == "" {
				msg = "script reported the plan as invalid"
			}
			return nil, apperr.New(apperr.QuotaUnavailable, msg)
		}
		out = append(out, r.meter())
	}
	return out, nil
}

func toScriptResult(m map[string]any) scriptResult {
	var r scriptResult
	if b, ok := m["isValid"].(bool); ok {
		r.isValid = &b
	}
	r.invalidMessage, _ = m["invalidMessage"].(string)
	r.planName, _ = m["planName"].(string)
	if v, ok := numberOf(m["remaining"]); ok {
		r.remaining = &v
	}
	if v, ok := numberOf(m["used"]); ok {
		r.used = &v
	}
	if v, ok := numberOf(m["total"]); ok {
		r.total = &v
	}
	r.unit, _ = m["unit"].(string)
	r.currency, _ = m["currency"].(string)
	r.extra, _ = m["extra"].(string)
	// 重置时刻三种写法都认:CC Switch 的 resetsAt、snake 的 reset_at、resetAt。
	for _, key := range []string{"resetsAt", "reset_at", "resetAt"} {
		if s, ok := m[key].(string); ok && s != "" {
			r.resetsAt = s
			break
		}
	}
	return r
}

func (r scriptResult) meter() Meter {
	unit, currency := scriptUnit(r.unit)
	if r.currency != "" {
		currency = r.currency
	}
	kind := provider.MeterUsage
	if r.remaining != nil && r.used == nil {
		kind = provider.MeterBalance
	}
	m := Meter{
		Kind:      kind,
		Unit:      unit,
		Currency:  currency,
		Label:     r.planName,
		Remaining: r.remaining,
		Used:      r.used,
		Total:     r.total,
		Extra:     r.extra,
		Reset:     provider.ResetNone,
	}
	if t, ok := timeOf(r.resetsAt); ok {
		m.ResetAt = &t
	}
	return m
}

// scriptUnit 归一脚本的自由文本单位:币种三字母码与 $/¥ 归 currency,
// % 归 percent,请求/token 各归其位,认不出的按点数处理。
func scriptUnit(u string) (provider.MeterUnit, string) {
	switch strings.ToLower(strings.TrimSpace(u)) {
	case "%", "percent", "percentage":
		return provider.UnitPercent, ""
	case "requests", "request", "req":
		return provider.UnitRequests, ""
	case "tokens", "token":
		return provider.UnitTokens, ""
	case "$":
		return provider.UnitCurrency, "USD"
	case "¥", "￥":
		return provider.UnitCurrency, "CNY"
	case "usd", "cny", "eur", "gbp", "jpy", "hkd":
		return provider.UnitCurrency, strings.ToUpper(strings.TrimSpace(u))
	default:
		return provider.UnitCredits, ""
	}
}
