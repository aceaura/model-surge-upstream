package httpapi

import (
	"net/http"

	"github.com/aceaura/model-surge-upstream/backend/modelcheck"
)

// testAccount 用账号下任一模型的模型级探针检测连通性：带凭据、带原生
// 模型名发最小补全请求，非 2xx 判失败。这与 /admin/model-test 同一判据
// ——回答「能不能用」而不是「能不能到」：根地址多数不接 GET，裸可达探测
// 恒回 404/403，看着像故障。不走 Resolver、不看 Enabled（未启用也该能
// 先测通）。账号下没有模型时退回根地址可达性探测，因为此时没有可用的
// 原生模型名可发。结果恒为 200 + Result JSON——失败是检测结果，不是
// 请求错误。
func (h handler) testAccount(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	acc, err := h.Accounts.Get(r.Context(), name)
	if err != nil {
		writeError(w, err)
		return
	}
	models, err := h.Models.List(r.Context(), acc.Name)
	if err != nil {
		writeError(w, err)
		return
	}
	if len(models) == 0 {
		spec, ok := acc.Spec()
		if !ok {
			writeJSON(w, http.StatusOK, modelcheck.Result{
				Error: "账号引用了未知提供商 " + acc.ProviderID})
			return
		}
		writeJSON(w, http.StatusOK,
			modelcheck.Reachability(r.Context(), acc.EffectiveBaseURL(spec)))
		return
	}
	res, err := h.probeModel(r.Context(), models[0], acc)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, res)
}
