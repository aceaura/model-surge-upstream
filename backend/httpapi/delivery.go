package httpapi

import (
	"net/http"

	"github.com/aceaura/model-surge-upstream/backend/apperr"
)

// resolveRequest 只接收模型标识。刻意不接收上游请求体或调用方参数：
// 本服务不转发数据面流量，只回答目标长什么样；参数怎么叠加由调用方决定。
type resolveRequest struct {
	ModelID string `json:"model_id"`
}

func (h handler) resolve(w http.ResponseWriter, r *http.Request) {
	var req resolveRequest
	if !decodeBody(w, r, &req) {
		return
	}
	if req.ModelID == "" {
		writeCode(w, apperr.InvalidRequest, "model_id is required")
		return
	}
	target, err := h.Resolver.Resolve(r.Context(), req.ModelID)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, target)
}

func (h handler) deliveryModels(w http.ResponseWriter, r *http.Request) {
	listing, err := h.Resolver.List(r.Context())
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"models": listing})
}

func (h handler) quota(w http.ResponseWriter, r *http.Request) {
	report, err := h.Quota.Query(r.Context(), r.PathValue("name"))
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, report)
}

func (h handler) health(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	dbErr := h.Health.PingDB(ctx)
	body := map[string]any{
		"ready":    dbErr == nil,
		"database": dbErr == nil,
		"cache":    h.Health.CacheReady(ctx),
	}
	status := http.StatusOK
	if dbErr != nil {
		status = http.StatusServiceUnavailable
	}
	writeJSON(w, status, body)
}
