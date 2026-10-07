package httpapi

import (
	"context"
	"fmt"
	"net/http"
	"strings"

	"github.com/aceaura/model-surge-upstream/backend/account"
	"github.com/aceaura/model-surge-upstream/backend/apperr"
	"github.com/aceaura/model-surge-upstream/backend/kiro"
	"github.com/aceaura/model-surge-upstream/backend/model"
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
	res, err := h.probeModel(r.Context(), m, acc)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, res)
}

// probeModel 用模型级判据探一次上游:非 2xx 判失败,回答「能不能用」。
// 返回的 error 只表示请求本身没法成立(提供商未知、账号读取失败),探测
// 失败本身是 Result 的内容。
func (h handler) probeModel(ctx context.Context, m model.Model, acc account.Account) (modelcheck.Result, error) {
	spec, ok := acc.Spec()
	if !ok {
		return modelcheck.Result{}, apperr.New(apperr.InvalidProvider,
			"account "+acc.Name+" references unknown provider "+acc.ProviderID)
	}

	isKiro := spec.ID == kiro.ProviderID
	for attempt := 0; ; attempt++ {
		headers, err := h.Resolver.HeadersFor(ctx, spec, acc)
		if err != nil {
			if isKiro {
				return modelcheck.Result{Error: "Kiro probe authentication failed"}, nil
			}
			return modelcheck.Result{Error: err.Error()}, nil
		}
		if isKiro {
			acc, err = h.Accounts.Get(ctx, acc.Name)
			if err != nil {
				return modelcheck.Result{}, err
			}
		}
		result := modelcheck.Check(ctx, resolve.ResolvedTarget{
			ModelID:     m.ID,
			Account:     acc.Name,
			ProviderID:  spec.ID,
			Protocol:    m.Protocol,
			BaseURL:     acc.EffectiveBaseURL(spec),
			NativeModel: m.NativeModel,
			Headers:     headers,
		})
		if isKiro && result.StatusCode == http.StatusForbidden && attempt == 0 && h.OAuth != nil {
			token := ""
			for name, value := range headers {
				if strings.EqualFold(name, "Authorization") && len(value) >= 7 && strings.EqualFold(value[:7], "Bearer ") {
					token = strings.TrimSpace(value[7:])
					break
				}
			}
			h.OAuth.Invalidate(acc.Name, token)
			continue
		}
		if isKiro && result.Error != "" {
			result.Error = fmt.Sprintf("Kiro upstream probe failed (HTTP %d)", result.StatusCode)
		}
		return result, nil
	}
}
