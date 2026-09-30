package httpapi

import (
	"net/http"

	"github.com/aceaura/model-surge-upstream/backend/apperr"
)

func (h handler) listChatSessions(w http.ResponseWriter, r *http.Request) {
	sessions, err := h.Chat.ListSessions(r.Context())
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"sessions": sessions})
}

func (h handler) createChatSession(w http.ResponseWriter, r *http.Request) {
	s, err := h.Chat.CreateSession(r.Context())
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"session": s})
}

func (h handler) renameChatSession(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Title string `json:"title"`
	}
	if ok := decodeBody(w, r, &in); !ok {
		return
	}
	sess, err := h.Chat.RenameSession(r.Context(), r.PathValue("id"), in.Title)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"session": sess})
}

func (h handler) deleteChatSession(w http.ResponseWriter, r *http.Request) {
	if err := h.Chat.DeleteSession(r.Context(), r.PathValue("id")); err != nil {
		writeError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h handler) listChatMessages(w http.ResponseWriter, r *http.Request) {
	sess, msgs, err := h.Chat.Messages(r.Context(), r.PathValue("id"))
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"session": sess, "messages": msgs})
}

func (h handler) clearChatMessages(w http.ResponseWriter, r *http.Request) {
	if err := h.Chat.ClearMessages(r.Context(), r.PathValue("id")); err != nil {
		writeError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h handler) sendChatMessage(w http.ResponseWriter, r *http.Request) {
	var in struct {
		ModelID string `json:"model_id"`
		Content string `json:"content"`
	}
	if ok := decodeBody(w, r, &in); !ok {
		return
	}
	if in.ModelID == "" {
		writeCode(w, apperr.InvalidRequest, "model_id is required")
		return
	}
	msgs, err := h.Chat.Send(r.Context(), r.PathValue("id"), in.ModelID, in.Content)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"messages": msgs})
}
