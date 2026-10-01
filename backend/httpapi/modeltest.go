package httpapi

import (
	"net/http"

	"github.com/aceaura/model-surge-upstream/backend/apperr"
	"github.com/aceaura/model-surge-upstream/backend/modelcheck"
	"github.com/aceaura/model-surge-upstream/backend/resolve"
)

// testModel 检测命名模型到真实上游的连通性。直接从 model+account 构造
// 目标而不走 Resolver：Resolver 会拒绝未启用的模型/账号，但检测的意义
// 恰恰是启用前先验证链路、凭据与上游模型名（CC Switch 同款语义：
// 检测不触碰启用状态）。结果恒为 200 + Result JSON——失败是检测结果，
// 不是请求错误。
func (h handler) testModel(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	m, err := h.Models.Get(r.Context(), id)
	if err != nil {
		writeError(w, err)
		return
	}
	acc, err := h.Accounts.Get(r.Context(), m.Account)
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

	target := resolve.ResolvedTarget{
		ModelID:     m.ID,
		Account:     acc.Name,
		ProviderID:  spec.ID,
		Protocol:    m.Protocol,
		BaseURL:     acc.EffectiveBaseURL(spec),
		NativeModel: m.NativeModel,
		Headers:     resolve.AuthHeaders(spec, acc),
	}
	writeJSON(w, http.StatusOK, modelcheck.Check(r.Context(), target))
}
