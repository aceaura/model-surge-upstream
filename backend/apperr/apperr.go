// Package apperr 定义领域错误码。仓储与业务层只产出这些码，
// HTTP 层负责把码映射成状态码——两者不互相知道对方的表示形式。
package apperr

import "errors"

type Code string

const (
	Unauthorized      Code = "unauthorized"
	NotFound          Code = "not_found"
	AlreadyExists     Code = "already_exists"
	AccountDisabled   Code = "account_disabled"
	ModelDisabled     Code = "model_disabled"
	InvalidProvider   Code = "invalid_provider"
	InvalidCredential Code = "invalid_credential"
	InvalidProtocol   Code = "invalid_protocol"
	InvalidJSON       Code = "invalid_json"
	InvalidRequest    Code = "invalid_request"
	QuotaUnavailable  Code = "quota_unavailable"
	StorageError      Code = "storage_error"
)

type Error struct {
	Code    Code
	Message string
	cause   error
}

func (e *Error) Error() string {
	if e.cause != nil {
		return string(e.Code) + ": " + e.Message + ": " + e.cause.Error()
	}
	return string(e.Code) + ": " + e.Message
}

func (e *Error) Unwrap() error { return e.cause }

func New(code Code, message string) *Error { return &Error{Code: code, Message: message} }

func Wrap(code Code, message string, cause error) *Error {
	return &Error{Code: code, Message: message, cause: cause}
}

// CodeOf 提取错误码，非领域错误一律视为 storage_error。
func CodeOf(err error) Code {
	var e *Error
	if errors.As(err, &e) {
		return e.Code
	}
	return StorageError
}

func Is(err error, code Code) bool { return CodeOf(err) == code }
