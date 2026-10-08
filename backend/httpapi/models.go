package httpapi

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"

	"github.com/aceaura/model-surge-upstream/backend/apperr"
	"github.com/aceaura/model-surge-upstream/backend/effort"
	"github.com/aceaura/model-surge-upstream/backend/model"
)

type modelRequest struct {
	ID            string          `json:"id"`
	Account       string          `json:"account"`
	NativeModel   string          `json:"native_model"`
	Protocol      string          `json:"protocol"`
	ContextWindow int             `json:"context_window"`
	Defaults      json.RawMessage `json:"defaults"`
	Overrides     json.RawMessage `json:"overrides"`
	Compact       json.RawMessage `json:"compact"`
	// Efforts 推理档支持列表:null=自动(跟随上游声明),数组=显式声明;缺省不改现状。
	Efforts json.RawMessage `json:"efforts"`
	// EffortFormat effort 写入格式:缺省(null)不改现状,空串=协议内置映射,
	// 非空=显式格式(effort.FormatXxx 枚举,保存期校验)。
	EffortFormat *string `json:"effort_format"`
	// Rectifier 整流器配置(kiro 拒答自动重试):缺省不改现状,{}=关闭。
	Rectifier    json.RawMessage `json:"rectifier"`
	Enabled      *bool           `json:"enabled"`
}

func (r modelRequest) input(id string) model.Input {
	in := model.Input{
		ID:            id,
		Account:       r.Account,
		NativeModel:   r.NativeModel,
		Protocol:      r.Protocol,
		ContextWindow: r.ContextWindow,
		Defaults:      r.Defaults,
		Overrides:     r.Overrides,
		Compact:       r.Compact,
		Efforts:       r.Efforts,
		EffortFormat:  r.EffortFormat,
		Rectifier:     r.Rectifier,
		Enabled:       true,
	}
	if r.Enabled != nil {
		in.Enabled = *r.Enabled
	}
	return in
}

// decorateEfforts 现算模型的有效档位填进响应:显式数组本地归一(写路径
// 已校验,存量脏数据退回空列表而不挡读路径);自动模式跟随上游声明,
// 声明源未装配或查询失败都按无声明(不支持)处置——与 resolve 侧同口径。
func (h handler) decorateEfforts(ctx context.Context, m *model.Model) {
	var declared []string
	if effort.Auto(m.Efforts) && h.UpstreamModels != nil {
		declared = h.UpstreamModels.DeclaredEfforts(ctx, m.Account, m.NativeModel)
	}
	eff, err := effort.Effective(m.Efforts, declared)
	if err != nil {
		eff = []effort.Entry{}
	}
	m.EffortsEffective = eff
}

func (h handler) listModels(w http.ResponseWriter, r *http.Request) {
	models, err := h.Models.List(r.Context(), r.URL.Query().Get("account"))
	if err != nil {
		writeError(w, err)
		return
	}
	for i := range models {
		h.decorateEfforts(r.Context(), &models[i])
	}
	writeJSON(w, http.StatusOK, map[string]any{"models": models})
}

func (h handler) getModel(w http.ResponseWriter, r *http.Request) {
	m, err := h.Models.Get(r.Context(), r.PathValue("id"))
	if err != nil {
		writeError(w, err)
		return
	}
	h.decorateEfforts(r.Context(), &m)
	writeJSON(w, http.StatusOK, map[string]any{"model": m})
}

func (h handler) createModel(w http.ResponseWriter, r *http.Request) {
	var req modelRequest
	if !decodeBody(w, r, &req) {
		return
	}
	m, err := h.Models.Create(r.Context(), req.input(req.ID))
	if err != nil {
		writeError(w, err)
		return
	}
	h.decorateEfforts(r.Context(), &m)
	writeJSON(w, http.StatusCreated, map[string]any{"model": m})
}

func (h handler) updateModel(w http.ResponseWriter, r *http.Request) {
	var req modelRequest
	if !decodeBody(w, r, &req) {
		return
	}
	in := req.input(r.PathValue("id"))
	// body 带 id 且与路径不同即改名;缺省/相同都按普通更新。
	in.NewID = req.ID
	m, err := h.Models.Update(r.Context(), in)
	if err != nil {
		writeError(w, err)
		return
	}
	h.decorateEfforts(r.Context(), &m)
	writeJSON(w, http.StatusOK, map[string]any{"model": m})
}

func (h handler) deleteModel(w http.ResponseWriter, r *http.Request) {
	if err := h.Models.Delete(r.Context(), r.PathValue("id")); err != nil {
		writeError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

type reorderModelsRequest struct {
	IDs []string `json:"ids"`
}

// reorderModels 承接模型页拖拽排序:body 给全量模型 id 的新顺序,
// 仓储按序重写 sort_order。id 缺失/重复都拒,避免半截顺序落库。
func (h handler) reorderModels(w http.ResponseWriter, r *http.Request) {
	var req reorderModelsRequest
	if !decodeBody(w, r, &req) {
		return
	}
	if len(req.IDs) == 0 {
		writeCode(w, apperr.InvalidRequest, "ids is required")
		return
	}
	seen := make(map[string]bool, len(req.IDs))
	for _, id := range req.IDs {
		if seen[id] {
			writeCode(w, apperr.InvalidRequest, fmt.Sprintf("duplicate model %q in ids", id))
			return
		}
		seen[id] = true
	}
	if err := h.Models.Reorder(r.Context(), req.IDs); err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}
