package httpapi

import (
	"encoding/json"
	"errors"
	"net/http"

	"github.com/aceaura/model-surge-upstream/backend/apperr"
)

type errorBody struct {
	Code    apperr.Code `json:"code"`
	Message string      `json:"message"`
	Status  int         `json:"status"`
}

type errorEnvelope struct {
	Error errorBody `json:"error"`
}

var statusByCode = map[apperr.Code]int{
	apperr.Unauthorized:      http.StatusUnauthorized,
	apperr.NotFound:          http.StatusNotFound,
	apperr.AlreadyExists:     http.StatusConflict,
	apperr.AccountDisabled:   http.StatusConflict,
	apperr.ModelDisabled:     http.StatusConflict,
	apperr.InvalidProvider:   http.StatusBadRequest,
	apperr.InvalidCredential: http.StatusBadRequest,
	apperr.InvalidProtocol:   http.StatusBadRequest,
	apperr.InvalidJSON:       http.StatusBadRequest,
	apperr.InvalidRequest:    http.StatusBadRequest,
	apperr.QuotaUnavailable:  http.StatusBadGateway,
	apperr.StorageError:      http.StatusInternalServerError,
}

func statusOf(code apperr.Code) int {
	if s, ok := statusByCode[code]; ok {
		return s
	}
	return http.StatusInternalServerError
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

// writeError 把领域错误映射成信封。非领域错误只回通用文案，
// 避免把底层错误细节（可能含连接串）泄露给调用方。
func writeError(w http.ResponseWriter, err error) {
	code := apperr.CodeOf(err)
	status := statusOf(code)

	message := "internal error"
	var domain *apperr.Error
	if errors.As(err, &domain) {
		message = domain.Message
	}
	writeJSON(w, status, errorEnvelope{Error: errorBody{Code: code, Message: message, Status: status}})
}

func writeCode(w http.ResponseWriter, code apperr.Code, message string) {
	writeError(w, apperr.New(code, message))
}
