package quota

// 百炼 Token Plan(中国个人版):控制台 CLI 网关三个逻辑接口顺序聚合——
// usage 拿各窗口用量,subscription 拿套餐 specCode,quota-config 拿该套餐
// 各窗口总额。凭据是控制台 access_token,与推理 API Key 严格分离。
// 表单体是固定的网关参数,与已验证的调用逐字节一致。

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/aceaura/model-surge-upstream/backend/account"
	"github.com/aceaura/model-surge-upstream/backend/apperr"
	"github.com/aceaura/model-surge-upstream/backend/provider"
)

const (
	bailianConsoleURL = "https://bailian-cs.console.aliyun.com/cli/api.json"
	bailianAPIPrefix  = "zeldaHttp.apikeyMgr.%2Ftokenplan%2Fpersonal%2Fapi%2Fv2%2F"
	bailianLoginHint  = "百炼控制台登录已失效，请执行 bl auth generate-access-token 续签并检查自动续期任务；AccessKey 失效时请在额度查询设置中重新执行「安装 bl 并验证」"
)

// bailianMeters 聚合三接口出窗口计量。任一接口失败整份报告不可用:
// 窗口总额依赖 subscription/quota-config,缺一就会报错数字。
func bailianMeters(ctx context.Context, q *Quota, spec provider.Spec, acc account.Account) ([]Meter, error) {
	token := bailianConsoleToken(acc)
	if token == "" {
		return nil, apperr.New(apperr.InvalidRequest,
			"未读取到百炼 CLI 控制台登录态，请在额度查询设置中执行「安装 bl 并验证」，并确认后端已配置 MSU_BAILIAN_CLI_CONFIG（推理 API Key 不能用于额度查询）")
	}
	payloads := make([]any, 3)
	for i, api := range []string{"usage", "subscription", "quota-config"} {
		p, err := q.bailianCall(ctx, token, api)
		if err != nil {
			return nil, err
		}
		payloads[i] = p
	}
	return bailianMetersOf(payloads[0], payloads[1], payloads[2])
}

// bailianConsoleToken 取控制台 access_token:MSU_BAILIAN_CLI_CONFIG 指向
// bl CLI 的 config.json 时优先读它——bl 撞到过期会用已存 AK/SK 自动续期
// 并写回该文件,直读它就永远拿到新鲜 token;读不到回落账号落库值。
func bailianConsoleToken(acc account.Account) string {
	if p := strings.TrimSpace(os.Getenv("MSU_BAILIAN_CLI_CONFIG")); p != "" {
		if raw, err := os.ReadFile(p); err == nil {
			var cfg struct {
				AccessToken string `json:"access_token"`
			}
			if json.Unmarshal(raw, &cfg) == nil {
				if t := strings.TrimSpace(cfg.AccessToken); t != "" {
					return t
				}
			}
		}
	}
	return strings.TrimSpace(acc.Credential.ConsoleAccessToken)
}

// bailianCall 调一个逻辑接口并拆双层信封:外层 data.success 与内层
// DataV2.data.success/code 任一不成都算失败。响应任何层级出现
// NotLogined 说明控制台登录态失效,返回重新登录指引。
func (q *Quota) bailianCall(ctx context.Context, token, api string) (any, error) {
	u := bailianConsoleURL + "?action=BroadScopeAspnGateway&api=" + bailianAPIPrefix + api + "&product=sfm_bailian"
	body := "params=%7B%22Api%22%3A%22" + bailianAPIPrefix + api + "%22%2C%22V%22%3A%221.0%22%2C%22Data%22%3A%7B%22cornerstoneParam%22%3A%7B%22protocol%22%3A%22V2%22%2C%22console%22%3A%22ONE_CONSOLE%22%2C%22productCode%22%3A%22p_efm%22%2C%22switchUserType%22%3A3%2C%22consoleSite%22%3A%22BAILIAN_ALIYUN%22%7D%7D%7D&region=cn-beijing"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, u, strings.NewReader(body))
	if err != nil {
		return nil, apperr.Wrap(apperr.QuotaUnavailable, "build quota request", err)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	raw, _, err := q.do(req)
	if err != nil {
		return nil, err
	}
	var payload any
	if err := json.Unmarshal(raw, &payload); err != nil {
		return nil, apperr.Wrap(apperr.QuotaUnavailable, "decode console response", err)
	}
	if bailianNotLogined(payload) {
		return nil, apperr.New(apperr.QuotaUnavailable, bailianLoginHint)
	}
	outer, _ := payload.(map[string]any)
	data, _ := outer["data"].(map[string]any)
	if data["success"] != true {
		return nil, apperr.New(apperr.QuotaUnavailable, bailianError(payload))
	}
	v2, _ := data["DataV2"].(map[string]any)
	inner, _ := v2["data"].(map[string]any)
	if inner["success"] != true || inner["code"] != "SUCCESS" {
		return nil, apperr.New(apperr.QuotaUnavailable, bailianError(payload))
	}
	return inner["data"], nil
}

// bailianNotLogined 递归识别响应里的未登录标记:网关把它埋在任意层级。
func bailianNotLogined(v any) bool {
	switch t := v.(type) {
	case string:
		s := strings.ToLower(t)
		return s == "notlogined" || s == "bailiangateway.login.notlogined"
	case map[string]any:
		for _, val := range t {
			if bailianNotLogined(val) {
				return true
			}
		}
	case []any:
		for _, val := range t {
			if bailianNotLogined(val) {
				return true
			}
		}
	}
	return false
}

func bailianError(payload any) string {
	if m, ok := payload.(map[string]any); ok {
		for _, key := range []string{"message", "errorMsg", "errMsg"} {
			if s, ok := m[key].(string); ok && s != "" {
				return "百炼控制台额度接口未返回成功结果：" + s
			}
		}
	}
	return "百炼控制台额度接口未返回成功结果，请确认控制台登录状态后重试"
}

// bailianMetersOf 把三接口的 data 合成窗口计量:usage 给各窗口已用比例,
// subscription 给套餐 specCode,quota-config 按 specCode 给各窗口总额。
// 缺席窗口不出计量而不是按零报——没报就是没数据。
func bailianMetersOf(usage, subscription, config any) ([]Meter, error) {
	u, _ := usage.(map[string]any)
	sub, _ := subscription.(map[string]any)
	cfg, _ := config.(map[string]any)
	specCode, _ := sub["specCode"].(string)
	if specCode == "" {
		return nil, apperr.New(apperr.QuotaUnavailable, "未识别百炼订阅套餐")
	}
	plan, _ := cfg[specCode].(map[string]any)
	name := strings.ToUpper(specCode[:1]) + specCode[1:]

	windows := []struct {
		prefix string
		quota  string
		label  string
		reset  provider.ResetRule
	}{
		{"per5Hour", "five_hour", "5小时", provider.ResetRolling},
		{"per1Week", "weekly", "7天", provider.ResetRolling},
		{"per1Month", "monthly", "本月", provider.ResetMonthly},
	}
	out := []Meter{}
	for _, w := range windows {
		raw, ok := u[w.prefix+"Percentage"]
		if !ok || raw == nil {
			continue
		}
		ratio, ok := numberOf(raw)
		if !ok || ratio < 0 || ratio > 1 {
			return nil, apperr.New(apperr.QuotaUnavailable,
				"百炼额度使用比例无效，必须在 0 到 1 之间")
		}
		used := ratio * 100
		m := Meter{
			Kind:  provider.MeterUsage,
			Unit:  provider.UnitPercent,
			Label: w.label,
			Used:  &used,
			Reset: w.reset,
			Extra: name,
		}
		if totalRaw, ok := plan[w.quota]; ok && totalRaw != nil {
			total, ok := numberOf(totalRaw)
			if !ok || total < 0 {
				return nil, apperr.New(apperr.QuotaUnavailable, "百炼套餐总额度无效")
			}
			usedCredits := ratio * total
			remaining := total - usedCredits
			m.Unit = provider.UnitCredits
			m.Total = &total
			m.Used = &usedCredits
			m.Remaining = &remaining
		}
		if resetRaw, ok := u[w.prefix+"ResetTime"]; ok && resetRaw != nil {
			ms, ok := numberOf(resetRaw)
			if !ok || ms < 0 {
				return nil, apperr.New(apperr.QuotaUnavailable, "百炼额度重置时间无效")
			}
			t := time.UnixMilli(int64(ms)).UTC()
			m.ResetAt = &t
		}
		out = append(out, m)
	}
	if len(out) == 0 {
		return nil, apperr.New(apperr.QuotaUnavailable,
			"未识别百炼额度用量，请确认个人 Token Plan 订阅后重试")
	}
	return out, nil
}
