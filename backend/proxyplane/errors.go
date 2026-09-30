package proxyplane

import (
	"encoding/json"
	"errors"
	"net/http"

	"github.com/aceaura/model-surge-upstream/backend/apperr"
)

// 转发面的错误不走 httpapi 的统一信封：调用方是各协议的原生 SDK，
// 只认自家错误外壳。码到 HTTP 状态的映射与 httpapi 保持一致。
var statusByCode = map[apperr.Code]int{
	apperr.Unauthorized:        http.StatusUnauthorized,
	apperr.NotFound:            http.StatusNotFound,
	apperr.AlreadyExists:       http.StatusConflict,
	apperr.AccountDisabled:     http.StatusConflict,
	apperr.ModelDisabled:       http.StatusConflict,
	apperr.InvalidProvider:     http.StatusBadRequest,
	apperr.InvalidCredential:   http.StatusBadRequest,
	apperr.InvalidProtocol:     http.StatusBadRequest,
	apperr.InvalidJSON:         http.StatusBadRequest,
	apperr.InvalidRequest:      http.StatusBadRequest,
	apperr.QuotaUnavailable:    http.StatusBadGateway,
	apperr.UpstreamUnavailable: http.StatusBadGateway,
	apperr.StorageError:        http.StatusInternalServerError,
}

func statusOf(code apperr.Code) int {
	if s, ok := statusByCode[code]; ok {
		return s
	}
	return http.StatusInternalServerError
}

// writeFamilyError 按协议族输出原生错误形态。非领域错误只回通用文案，
// 不把底层细节（连接串、内部地址）泄露给数据面调用方。
func writeFamilyError(w http.ResponseWriter, fam family, err error) {
	code := apperr.CodeOf(err)
	status := statusOf(code)

	message := "internal error"
	var domain *apperr.Error
	if errors.As(err, &domain) {
		message = domain.Message
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	switch fam {
	case familyAnthropic:
		_ = json.NewEncoder(w).Encode(map[string]any{
			"type": "error",
			"error": map[string]any{
				"type":    anthropicErrorType(status),
				"message": message,
			},
		})
	case familyGemini:
		_ = json.NewEncoder(w).Encode(map[string]any{
			"error": map[string]any{
				"code":    status,
				"message": message,
				"status":  geminiErrorStatus(status),
			},
		})
	default: // openai
		_ = json.NewEncoder(w).Encode(map[string]any{
			"error": map[string]any{
				"message": message,
				"type":    openaiErrorType(status),
				"code":    string(code),
			},
		})
	}
}

func anthropicErrorType(status int) string {
	switch status {
	case http.StatusUnauthorized:
		return "authentication_error"
	case http.StatusNotFound:
		return "not_found_error"
	case http.StatusTooManyRequests:
		return "rate_limit_error"
	case http.StatusBadGateway, http.StatusInternalServerError:
		return "api_error"
	default:
		return "invalid_request_error"
	}
}

func openaiErrorType(status int) string {
	switch status {
	case http.StatusUnauthorized:
		return "authentication_error"
	case http.StatusBadGateway, http.StatusInternalServerError:
		return "server_error"
	default:
		return "invalid_request_error"
	}
}

func geminiErrorStatus(status int) string {
	switch status {
	case http.StatusUnauthorized:
		return "UNAUTHENTICATED"
	case http.StatusNotFound:
		return "NOT_FOUND"
	case http.StatusConflict:
		return "ABORTED"
	case http.StatusBadGateway:
		return "UNAVAILABLE"
	case http.StatusInternalServerError:
		return "INTERNAL"
	default:
		return "INVALID_ARGUMENT"
	}
}
