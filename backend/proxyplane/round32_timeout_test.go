package proxyplane

import (
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/aceaura/model-surge-upstream/backend/kiro"
	"github.com/aceaura/model-surge-upstream/backend/provider"
	"github.com/aceaura/model-surge-upstream/backend/resolve"
)

// 三十二轮:network_errors.py 的超时分类——超时类上游失败回 504,其余
// 网络错误回 502;错误类型串与 502 同族(anthropic api_error、openai
// server_error)。
func TestRound32KiroUpstreamTimeoutHTTP(t *testing.T) {
	for _, tc := range []struct {
		name       string
		path       string
		protocol   string
		cause      error
		wantStatus int
		wantType   string
	}{
		{"timeout anthropic", "/v1/messages", provider.ProtocolAnthropic, fmt.Errorf("dial: %w", kiro.ErrUpstreamTimeout), http.StatusGatewayTimeout, "api_error"},
		{"timeout openai", "/v1/chat/completions", provider.ProtocolChatCompletions, fmt.Errorf("dial: %w", kiro.ErrUpstreamTimeout), http.StatusGatewayTimeout, "server_error"},
		{"refused anthropic", "/v1/messages", provider.ProtocolAnthropic, errors.New("connection refused"), http.StatusBadGateway, "api_error"},
		{"refused openai", "/v1/chat/completions", provider.ProtocolChatCompletions, errors.New("connection refused"), http.StatusBadGateway, "server_error"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			target := resolve.ResolvedTarget{ModelID: "kiro-r32-timeout", ProviderID: kiro.ProviderID, Protocol: tc.protocol,
				BaseURL: "http://unused", NativeModel: "claude-sonnet-4.5", Headers: kiro.Headers("test-access", "")}
			h := NewHandler(testKey, fakeResolver{targets: map[string]resolve.ResolvedTarget{target.ModelID: target}}, nil)
			h.client.Transport = round29RoundTripFunc(func(*http.Request) (*http.Response, error) { return nil, tc.cause })
			r := httptest.NewRequest("POST", tc.path, strings.NewReader(`{"model":"kiro-r32-timeout","max_tokens":1,"messages":[{"role":"user","content":"q"}]}`))
			r.Header.Set("Authorization", "Bearer "+testKey)
			w := httptest.NewRecorder()
			h.ServeHTTP(w, r)
			if w.Code != tc.wantStatus || !strings.Contains(w.Body.String(), `"`+tc.wantType+`"`) {
				t.Fatalf("status=%d want=%d body=%s", w.Code, tc.wantStatus, w.Body)
			}
		})
	}
}
