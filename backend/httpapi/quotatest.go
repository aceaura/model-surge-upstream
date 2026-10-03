package httpapi

import (
	"net/http"

	"github.com/aceaura/model-surge-upstream/backend/apperr"
	"github.com/aceaura/model-surge-upstream/backend/quota"
)

// testQuotaScriptRequest 试跑额度脚本的入参。code 必填,timeout_seconds
// 为 0 走后端默认。凭据与生效地址取自既有账号,不在请求体里传。
// variables 是表单里未落库的自定义变量,先试跑先替换,与落库后的
// 正式查询同一条 ReplaceScriptVars 路径。
type testQuotaScriptRequest struct {
	Code           string            `json:"code"`
	TimeoutSeconds int               `json:"timeout_seconds"`
	Variables      map[string]string `json:"variables"`
}

// testQuotaScript 用账号内置凭据试跑一段未落库的额度脚本(CC Switch
// 脚本弹窗的「测试」语义:保存前先验证代码与渠道端点)。脚本或上游
// 失败是试跑结果(200 + ok:false),只有账号本身不存在才是请求错误。
func (h handler) testQuotaScript(w http.ResponseWriter, r *http.Request) {
	var req testQuotaScriptRequest
	if !decodeBody(w, r, &req) {
		return
	}
	report, err := h.Quota.TestScript(r.Context(), r.PathValue("name"),
		quota.ReplaceScriptVars(req.Code, req.Variables), req.TimeoutSeconds)
	if err != nil {
		if apperr.Is(err, apperr.NotFound) || apperr.Is(err, apperr.InvalidProvider) {
			writeError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "report": report})
}
