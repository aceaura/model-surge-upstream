package httpapi

import (
	"crypto/subtle"
	"net/http"
	"strings"

	"github.com/aceaura/model-surge-upstream/backend/apperr"
)

// requireKey 校验 Bearer 密钥。管理面与下发面各持一个中间件实例，
// 两者密钥独立，互不通用。
func requireKey(key string, next http.Handler) http.Handler {
	expected := []byte(key)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		presented := bearer(r)
		if presented == "" || subtle.ConstantTimeCompare([]byte(presented), expected) != 1 {
			writeCode(w, apperr.Unauthorized, "missing or invalid service key")
			return
		}
		next.ServeHTTP(w, r)
	})
}

func bearer(r *http.Request) string {
	raw := r.Header.Get("Authorization")
	if raw == "" {
		return ""
	}
	if after, ok := cutPrefixFold(raw, "bearer "); ok {
		return strings.TrimSpace(after)
	}
	return ""
}

func cutPrefixFold(s, prefix string) (string, bool) {
	if len(s) < len(prefix) || !strings.EqualFold(s[:len(prefix)], prefix) {
		return "", false
	}
	return s[len(prefix):], true
}
