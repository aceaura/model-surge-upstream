package httpapi

import (
	"encoding/json"
	"net/http"

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
	Enabled       *bool           `json:"enabled"`
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
		Enabled:       true,
	}
	if r.Enabled != nil {
		in.Enabled = *r.Enabled
	}
	return in
}

func (h handler) listModels(w http.ResponseWriter, r *http.Request) {
	models, err := h.Models.List(r.Context(), r.URL.Query().Get("account"))
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"models": models})
}

func (h handler) getModel(w http.ResponseWriter, r *http.Request) {
	m, err := h.Models.Get(r.Context(), r.PathValue("id"))
	if err != nil {
		writeError(w, err)
		return
	}
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
	writeJSON(w, http.StatusCreated, map[string]any{"model": m})
}

func (h handler) updateModel(w http.ResponseWriter, r *http.Request) {
	var req modelRequest
	if !decodeBody(w, r, &req) {
		return
	}
	m, err := h.Models.Update(r.Context(), req.input(r.PathValue("id")))
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"model": m})
}

func (h handler) deleteModel(w http.ResponseWriter, r *http.Request) {
	if err := h.Models.Delete(r.Context(), r.PathValue("id")); err != nil {
		writeError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
