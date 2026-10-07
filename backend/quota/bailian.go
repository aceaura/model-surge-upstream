package quota

// 百炼 Token Plan(中国个人版):控制台 CLI 网关三个逻辑接口顺序聚合——
// usage 拿各窗口用量,subscription 拿套餐 specCode,quota-config 拿该套餐
// 各窗口总额。凭据是控制台 access_token,与推理 API Key 严格分离。
// 表单体是固定的网关参数,与已验证的调用逐字节一致。

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"math"
	"net/http"
	"strings"
	"time"

	"github.com/aceaura/model-surge-upstream/backend/account"
	"github.com/aceaura/model-surge-upstream/backend/apperr"
	"github.com/aceaura/model-surge-upstream/backend/credential"
	"github.com/aceaura/model-surge-upstream/backend/provider"
)

const (
	bailianConsoleURL = "https://bailian-cs.console.aliyun.com/cli/api.json"
	bailianAPIPrefix  = "zeldaHttp.apikeyMgr.%2Ftokenplan%2Fpersonal%2Fapi%2Fv2%2F"
	bailianLoginHint  = "百炼控制台认证已失效，请检查账号 AccessKey ID/Secret 并重新「验证并保存」（推理 API Key 不能用于额度查询）"
	bailianMintURL    = "https://modelstudio.cn-beijing.aliyuncs.com/modelstudio/cli/generateAccessToken"
)

var errBailianNotLogined = apperr.New(apperr.QuotaUnavailable, bailianLoginHint)

// 认证失败不能写入账号，成功必须包含真实额度验证。
func (q *Quota) VerifyBailian(ctx context.Context, cred credential.Credential) (credential.Credential, error) {
	if cred.Kind != provider.CredAPIKey || cred.Validate() != nil {
		return credential.Credential{}, apperr.New(apperr.InvalidCredential, "百炼验证需要推理 API Key 和完整 AccessKey ID/Secret")
	}
	token, err := q.mintBailian(ctx, cred)
	if err != nil {
		return credential.Credential{}, err
	}
	if _, err := q.bailianQuota(ctx, token); err != nil {
		return credential.Credential{}, err
	}
	cred.ConsoleAccessToken = token
	cred.ConsoleVerifiedAt = time.Now().UTC()
	return cred, nil
}

// 锁内重读凭据，避免重复续期已被其他查询轮换的 Token。
func bailianMeters(ctx context.Context, q *Quota, _ provider.Spec, acc account.Account) ([]Meter, error) {
	q.bailianMu.Lock()
	defer q.bailianMu.Unlock()
	var err error
	acc, err = q.accounts.Get(ctx, acc.Name)
	if err != nil {
		return nil, err
	}
	if acc.ProviderID != "bailian.cn.subscribe.token-plan" {
		return nil, apperr.New(apperr.InvalidProvider, "百炼账号供应商已更改，请重试")
	}
	cred := acc.Credential
	if token := strings.TrimSpace(cred.ConsoleAccessToken); token != "" {
		meters, err := q.bailianQuota(ctx, token)
		if !errors.Is(err, errBailianNotLogined) {
			return meters, err
		}
	}
	// Missing or rejected token: mint once, then retry the whole triple once.
	token, err := q.mintBailian(ctx, cred)
	var meters []Meter
	if err == nil {
		meters, err = q.bailianQuota(ctx, token)
	}
	if err != nil {
		cred.ConsoleAccessToken = ""
		cred.ConsoleVerifiedAt = time.Time{}
		// Even cancellation must not leave a rejected token marked verified.
		clearCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), requestTimeout)
		defer cancel()
		if err := q.accounts.UpdateCredential(clearCtx, acc.Name, cred); err != nil {
			return nil, apperr.New(apperr.StorageError, "无法保存百炼认证失效状态，请重新验证")
		}
		return nil, apperr.New(apperr.QuotaUnavailable, bailianLoginHint)
	}
	cred.ConsoleAccessToken = token
	cred.ConsoleVerifiedAt = time.Now().UTC()
	if err := q.accounts.UpdateCredential(ctx, acc.Name, cred); err != nil {
		return nil, apperr.New(apperr.StorageError, "无法保存百炼续期认证，请重试")
	}
	return meters, nil
}

func (q *Quota) bailianQuota(ctx context.Context, token string) ([]Meter, error) {
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

// ACS3 必须包含全部 x-acs-* 头及 canonical headers 的尾部换行。
func signBailian(req *http.Request, cred credential.Credential, date, nonce string) {
	const signed = "content-type;host;x-acs-action;x-acs-content-sha256;x-acs-date;x-acs-signature-nonce;x-acs-version"
	emptyHash := bailianSHA256("")
	req.Host = "modelstudio.cn-beijing.aliyuncs.com"
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Acs-Action", "GenerateCLIAccessToken")
	req.Header.Set("X-Acs-Version", "2026-02-10")
	req.Header.Set("X-Acs-Content-Sha256", emptyHash)
	req.Header.Set("X-Acs-Date", date)
	req.Header.Set("X-Acs-Signature-Nonce", nonce)
	canonicalHeaders := "content-type:application/json\nhost:" + req.Host +
		"\nx-acs-action:GenerateCLIAccessToken\nx-acs-content-sha256:" + emptyHash +
		"\nx-acs-date:" + date + "\nx-acs-signature-nonce:" + nonce + "\nx-acs-version:2026-02-10\n"
	canonical := strings.Join([]string{"POST", "/modelstudio/cli/generateAccessToken", "", canonicalHeaders, signed, emptyHash}, "\n")
	mac := hmac.New(sha256.New, []byte(cred.BailianAccessKeySecret))
	_, _ = mac.Write([]byte("ACS3-HMAC-SHA256\n" + bailianSHA256(canonical)))
	req.Header.Set("Authorization", "ACS3-HMAC-SHA256 Credential="+cred.BailianAccessKeyID+
		",SignedHeaders="+signed+",Signature="+hex.EncodeToString(mac.Sum(nil)))
}

func bailianSHA256(s string) string {
	hash := sha256.Sum256([]byte(s))
	return hex.EncodeToString(hash[:])
}

func (q *Quota) mintBailian(ctx context.Context, cred credential.Credential) (string, error) {
	cred.BailianAccessKeyID = strings.TrimSpace(cred.BailianAccessKeyID)
	cred.BailianAccessKeySecret = strings.TrimSpace(cred.BailianAccessKeySecret)
	if cred.BailianAccessKeyID == "" || cred.BailianAccessKeySecret == "" {
		return "", apperr.New(apperr.InvalidCredential, bailianLoginHint)
	}
	var random [16]byte
	if _, err := rand.Read(random[:]); err != nil {
		return "", apperr.New(apperr.QuotaUnavailable, "百炼签名生成失败，请重试")
	}
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost, bailianMintURL, nil)
	signBailian(req, cred, time.Now().UTC().Format("2006-01-02T15:04:05Z"), hex.EncodeToString(random[:]))
	raw, err := q.bailianDo(req)
	if err != nil {
		return "", err
	}
	var reply struct {
		Success *bool  `json:"Success"`
		Code    string `json:"Code"`
		Token   string `json:"cliAccessToken"`
	}
	if json.Unmarshal(raw, &reply) != nil || (reply.Success != nil && !*reply.Success) ||
		(reply.Code != "" && reply.Code != "Success" && reply.Code != "200") || strings.TrimSpace(reply.Token) == "" || strings.ContainsAny(reply.Token, "\r\n") {
		return "", apperr.New(apperr.QuotaUnavailable, "百炼认证签发失败，请检查 AccessKey 权限并重新验证")
	}
	return strings.TrimSpace(reply.Token), nil
}

// 禁止重定向并隐藏上游错误，避免泄露签名头或凭据。
func (q *Quota) bailianDo(req *http.Request) ([]byte, error) {
	client := *q.client
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	if client.Timeout == 0 || client.Timeout > requestTimeout {
		client.Timeout = requestTimeout
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, apperr.New(apperr.QuotaUnavailable, "百炼认证或额度请求失败，请重试")
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
		return nil, errBailianNotLogined
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, apperr.New(apperr.QuotaUnavailable, "百炼认证或额度接口未返回成功结果")
	}
	raw, err := io.ReadAll(io.LimitReader(resp.Body, bodyLimit+1))
	if err != nil || len(raw) > bodyLimit {
		return nil, apperr.New(apperr.QuotaUnavailable, "百炼响应读取失败或超出大小限制")
	}
	return raw, nil
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

	raw, err := q.bailianDo(req)
	if err != nil {
		return nil, err
	}
	var payload any
	if err := json.Unmarshal(raw, &payload); err != nil {
		return nil, apperr.New(apperr.QuotaUnavailable, "百炼额度响应无效")
	}
	if bailianNotLogined(payload) {
		return nil, errBailianNotLogined
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
		return strings.Contains(strings.ToLower(t), "notlogined")
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

func bailianError(_ any) string {
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
		if !ok || math.IsNaN(ratio) || math.IsInf(ratio, 0) || ratio < 0 || ratio > 1 {
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
			if !ok || math.IsNaN(total) || math.IsInf(total, 0) || total < 0 {
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
			if !ok || math.IsNaN(ms) || math.IsInf(ms, 0) || ms < 0 || ms >= math.MaxInt64 {
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
