package httpapi

import (
	"encoding/json"
	"net/http"

	"github.com/aceaura/model-surge-upstream/backend/account"
	"github.com/aceaura/model-surge-upstream/backend/apperr"
	"github.com/aceaura/model-surge-upstream/backend/credential"
)

type accountRequest struct {
	Name       string            `json:"name"`
	ProviderID string            `json:"provider_id"`
	APIKey     string            `json:"api_key"`
	Credential json.RawMessage   `json:"credential"`
	BaseURL    string            `json:"base_url"`
	Headers    map[string]string `json:"headers"`
	Enabled    *bool             `json:"enabled"`
}

// input 把请求体转成仓储入参。凭据支持两种写法：完整 credential 对象，
// 或只给 api_key（客户端常用的简写）。两者都不给表示保留原凭据。
func (r accountRequest) input(name string) (account.Input, error) {
	in := account.Input{
		Name:       name,
		ProviderID: r.ProviderID,
		BaseURL:    r.BaseURL,
		Headers:    r.Headers,
		Enabled:    true,
	}
	if r.Enabled != nil {
		in.Enabled = *r.Enabled
	}
	switch {
	case len(r.Credential) > 0:
		cred, err := credential.Decode(r.Credential)
		if err != nil {
			return account.Input{}, apperr.New(apperr.InvalidCredential, err.Error())
		}
		in.Credential = cred
	case r.APIKey != "":
		in.Credential = credential.Credential{Kind: "api_key", APIKey: r.APIKey}
	}
	return in, nil
}

func (h handler) listAccounts(w http.ResponseWriter, r *http.Request) {
	accounts, err := h.Accounts.List(r.Context())
	if err != nil {
		writeError(w, err)
		return
	}
	views := make([]account.View, 0, len(accounts))
	for _, a := range accounts {
		views = append(views, a.View())
	}
	writeJSON(w, http.StatusOK, map[string]any{"accounts": views})
}

func (h handler) getAccount(w http.ResponseWriter, r *http.Request) {
	acc, err := h.Accounts.Get(r.Context(), r.PathValue("name"))
	if err != nil {
		writeError(w, err)
		return
	}
	models, err := h.Accounts.CountModels(r.Context(), acc.Name)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"account": acc.View(), "model_count": models})
}

func (h handler) createAccount(w http.ResponseWriter, r *http.Request) {
	var req accountRequest
	if !decodeBody(w, r, &req) {
		return
	}
	in, err := req.input(req.Name)
	if err != nil {
		writeError(w, err)
		return
	}
	acc, err := h.Accounts.Create(r.Context(), in)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"account": acc.View()})
}

func (h handler) updateAccount(w http.ResponseWriter, r *http.Request) {
	var req accountRequest
	if !decodeBody(w, r, &req) {
		return
	}
	name := r.PathValue("name")
	in, err := req.input(name)
	if err != nil {
		writeError(w, err)
		return
	}
	acc, err := h.Accounts.Update(r.Context(), in)
	if err != nil {
		writeError(w, err)
		return
	}
	// 凭据或端点可能已变，丢弃该账号的额度与上游模型清单缓存。
	h.Quota.Forget(name)
	h.UpstreamModels.Forget(name)
	writeJSON(w, http.StatusOK, map[string]any{"account": acc.View()})
}

func (h handler) deleteAccount(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	deleted, err := h.Accounts.Delete(r.Context(), name)
	if err != nil {
		writeError(w, err)
		return
	}
	h.Quota.Forget(name)
	h.UpstreamModels.Forget(name)
	writeJSON(w, http.StatusOK, map[string]any{"deleted_models": deleted})
}

func decodeBody(w http.ResponseWriter, r *http.Request, v any) bool {
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		writeCode(w, apperr.InvalidJSON, "request body is not valid json: "+err.Error())
		return false
	}
	return true
}
