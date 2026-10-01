package httpapi

import (
	"net/http"

	"github.com/aceaura/model-surge-upstream/backend/apperr"
	"github.com/aceaura/model-surge-upstream/backend/modelcheck"
)

// testAccount 检测账号生效 base_url 的可达性（CC Switch stream_check 同款
// 语义）：不带鉴权头，拿到任意 HTTP 响应即「可达」，仅网络级错误判失败——
// 可达 ≠ 配置正确，凭据验证是模型级 /admin/model-test 的职责。不走
// Resolver、不看 Enabled（未启用也该能先测通）。结果恒为 200 + Result
// JSON——失败是检测结果，不是请求错误。
func (h handler) testAccount(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	acc, err := h.Accounts.Get(r.Context(), name)
	if err != nil {
		writeError(w, err)
		return
	}
	spec, ok := acc.Spec()
	if !ok {
		writeError(w, apperr.New(apperr.InvalidProvider,
			"account "+acc.Name+" references unknown provider "+acc.ProviderID))
		return
	}
	writeJSON(w, http.StatusOK, modelcheck.Reachability(r.Context(), acc.EffectiveBaseURL(spec)))
}
