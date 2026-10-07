package httpapi

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"hash/crc32"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/aceaura/model-surge-upstream/backend/account"
	"github.com/aceaura/model-surge-upstream/backend/credential"
	"github.com/aceaura/model-surge-upstream/backend/kiro"
	"github.com/aceaura/model-surge-upstream/backend/model"
	"github.com/aceaura/model-surge-upstream/backend/modelcheck"
	"github.com/aceaura/model-surge-upstream/backend/provider"
)

// 给夹具添一个指向本地 httptest 的账号与模型，返回模型 id。
// 模型故意停用：锁「未启用也能先测通」的语义（Resolver 会拒绝，检测不走它）。
func (f *fixture) addTestableModel(t *testing.T, upstreamURL string) string {
	t.Helper()
	f.accounts.data["probe-1"] = account.Account{
		Name: "probe-1", ProviderID: "kimi.global.subscribe.coding", BaseURL: upstreamURL,
		Credential: credential.Credential{Kind: provider.CredAPIKey, APIKey: secret},
		Headers:    map[string]string{}, Enabled: true,
	}
	f.models.data["probe-1/m"] = model.Model{
		ID: "probe-1/m", Account: "probe-1", NativeModel: "native-x",
		Protocol: provider.ProtocolAnthropic, Enabled: false,
		Defaults: json.RawMessage(`{}`), Overrides: json.RawMessage(`{}`),
	}
	return "probe-1/m"
}

func TestModelTestDisabledModelStillCheckable(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/messages" {
			t.Errorf("probe path = %q, want /v1/messages", r.URL.Path)
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{}`))
	}))
	defer upstream.Close()

	f := newFixture(t)
	id := f.addTestableModel(t, upstream.URL)

	rec := f.do(t, "POST", "/admin/model-test/"+id, adminKey, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", rec.Code, rec.Body.String())
	}
	var res modelcheck.Result
	if err := json.Unmarshal(rec.Body.Bytes(), &res); err != nil {
		t.Fatalf("decode result: %v", err)
	}
	if !res.OK || res.StatusCode != http.StatusOK {
		t.Errorf("result = %+v, want ok 200", res)
	}
}

// 上游拒绝是检测结果而非请求错误：管理面仍回 200，结果里带状态码。
func TestModelTestUpstreamRejects(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"error":"bad key"}`))
	}))
	defer upstream.Close()

	f := newFixture(t)
	id := f.addTestableModel(t, upstream.URL)

	rec := f.do(t, "POST", "/admin/model-test/"+id, adminKey, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", rec.Code, rec.Body.String())
	}
	var res modelcheck.Result
	if err := json.Unmarshal(rec.Body.Bytes(), &res); err != nil {
		t.Fatalf("decode result: %v", err)
	}
	if res.OK || res.StatusCode != http.StatusUnauthorized || res.Error == "" {
		t.Errorf("result = %+v, want not-ok 401 with error", res)
	}
}

func TestModelTestUnknownModel(t *testing.T) {
	f := newFixture(t)
	rec := f.do(t, "POST", "/admin/model-test/nope/x", adminKey, "")
	if rec.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404", rec.Code)
	}
}

type probeTokenState struct {
	Resolver
	accounts      *stubAccounts
	headers       int
	invalidations [][2]string
	failAt        int
	latestURL     string
}

func (s *probeTokenState) NeedsReauth(string) bool { return false }
func (s *probeTokenState) Reset(string)            {}
func (s *probeTokenState) Invalidate(name, token string) {
	s.invalidations = append(s.invalidations, [2]string{name, token})
}
func (s *probeTokenState) HeadersFor(_ context.Context, spec provider.Spec, acc account.Account) (map[string]string, error) {
	s.headers++
	if s.headers == s.failAt {
		return nil, errors.New("private-token header failure")
	}
	token := "private-token-old"
	if len(s.invalidations) > 0 {
		token = "private-token-new"
		acc.BaseURL = s.latestURL
		acc.Credential.ProfileARN = "arn:aws:codewhisperer:us-east-1:123:profile/new"
		s.accounts.data[acc.Name] = acc
	}
	if spec.ID == kiro.ProviderID {
		return kiro.Headers(token, acc.Credential.ProfileARN), nil
	}
	return map[string]string{"Authorization": "Bearer " + token}, nil
}

func probeEvent(kind, payload string) []byte {
	var headers []byte
	for _, h := range [][2]string{{":message-type", "event"}, {":event-type", kind}} {
		headers = append(headers, byte(len(h[0])))
		headers = append(headers, h[0]...)
		headers = append(headers, 7, byte(len(h[1])>>8), byte(len(h[1])))
		headers = append(headers, h[1]...)
	}
	b := make([]byte, 12)
	binary.BigEndian.PutUint32(b, uint32(16+len(headers)+len(payload)))
	binary.BigEndian.PutUint32(b[4:], uint32(len(headers)))
	binary.BigEndian.PutUint32(b[8:], crc32.ChecksumIEEE(b[:8]))
	b = append(b, headers...)
	b = append(b, payload...)
	return binary.BigEndian.AppendUint32(b, crc32.ChecksumIEEE(b))
}

func TestRound27KiroProbeRefresh(t *testing.T) {
	for _, protocol := range []string{provider.ProtocolAnthropic, provider.ProtocolChatCompletions} {
		for _, endpoint := range []string{"model", "account"} {
			for _, tc := range []struct {
				name                                                  string
				provider                                              string
				first, second, failAt                                 int
				nilOAuth                                              bool
				wantCalls, wantHeaders, wantInvalidations, wantStatus int
				wantOK                                                bool
			}{
				{"refresh", kiro.ProviderID, 403, 200, 0, false, 2, 2, 1, 200, true},
				{"still_forbidden", kiro.ProviderID, 403, 403, 0, false, 2, 2, 1, 403, false},
				{"refresh_headers_fail", kiro.ProviderID, 403, 200, 2, false, 1, 2, 1, 0, false},
				{"initial_headers_fail", kiro.ProviderID, 403, 200, 1, false, 0, 1, 0, 0, false},
				{"nil_oauth", kiro.ProviderID, 403, 200, 0, true, 1, 1, 0, 403, false},
				{"unauthorized", kiro.ProviderID, 401, 200, 0, false, 1, 1, 0, 401, false},
				{"bad_request", kiro.ProviderID, 400, 200, 0, false, 1, 1, 0, 400, false},
				{"success", kiro.ProviderID, 200, 200, 0, false, 1, 1, 0, 200, true},
				{"other_provider", "kimi.global.subscribe.coding", 403, 200, 0, false, 1, 1, 0, 403, false},
			} {
				t.Run(fmt.Sprintf("%s/%s/%s", protocol, endpoint, tc.name), func(t *testing.T) {
					var calls atomic.Int32
					serve := func(w http.ResponseWriter, r *http.Request) {
						n := calls.Add(1)
						wantToken := "Bearer private-token-old"
						status := tc.first
						if n > 1 {
							wantToken, status = "Bearer private-token-new", tc.second
						}
						if r.Header.Get("Authorization") != wantToken {
							t.Error("probe did not use the expected token")
						}
						if tc.provider == kiro.ProviderID && r.URL.Path != "/generateAssistantResponse" {
							t.Errorf("native probe path = %q", r.URL.Path)
						}
						if n > 1 {
							var body map[string]any
							if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
								t.Error(err)
							}
							if body["profileArn"] != "arn:aws:codewhisperer:us-east-1:123:profile/new" {
								t.Errorf("refreshed profile = %v", body["profileArn"])
							}
						}
						if status != 200 {
							w.WriteHeader(status)
							_, _ = w.Write([]byte(`{"message":"private-token upstream rejection"}`))
							return
						}
						if tc.provider == kiro.ProviderID {
							w.Header().Set("Content-Type", "application/vnd.amazon.eventstream")
							_, _ = w.Write(probeEvent("assistantResponseEvent", `{"content":"pong"}`))
							_, _ = w.Write(probeEvent("metadataEvent", `{"contextUsagePercentage":1}`))
						} else {
							_, _ = w.Write([]byte(`{}`))
						}
					}
					fresh := httptest.NewServer(http.HandlerFunc(serve))
					defer fresh.Close()
					initial := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
						if calls.Load() > 0 {
							t.Error("probe did not reload account base URL after refresh")
						}
						serve(w, r)
					}))
					defer initial.Close()
					f := newFixture(t)
					id := f.addTestableModel(t, initial.URL)
					acc := f.accounts.data["probe-1"]
					acc.ProviderID, acc.Enabled = tc.provider, false
					if tc.provider == kiro.ProviderID {
						acc.Credential = credential.Credential{Kind: provider.CredKiroRefresh, RefreshToken: "refresh-token"}
					}
					f.accounts.data[acc.Name] = acc
					m := f.models.data[id]
					m.Protocol, m.NativeModel = protocol, "claude-sonnet-4.6"
					f.models.data[id] = m
					source := &probeTokenState{accounts: f.accounts, failAt: tc.failAt, latestURL: fresh.URL}
					var state OAuthState = source
					if tc.nilOAuth {
						state = nil
					}
					f.server = NewServer(Deps{Accounts: f.accounts, Models: f.models, Resolver: source, OAuth: state, AdminKey: adminKey})
					path := "/admin/model-test/" + id
					if endpoint == "account" {
						path = "/admin/account-test/probe-1"
					}
					rec := f.do(t, "POST", path, adminKey, "")
					if rec.Code != 200 {
						t.Fatalf("management status = %d: %s", rec.Code, rec.Body.String())
					}
					var result modelcheck.Result
					if err := json.Unmarshal(rec.Body.Bytes(), &result); err != nil {
						t.Fatal(err)
					}
					if result.OK != tc.wantOK || result.StatusCode != tc.wantStatus {
						t.Errorf("result = %+v, want ok=%v status=%d", result, tc.wantOK, tc.wantStatus)
					}
					if int(calls.Load()) != tc.wantCalls || source.headers != tc.wantHeaders || len(source.invalidations) != tc.wantInvalidations {
						t.Errorf("calls=%d headers=%d invalidations=%d", calls.Load(), source.headers, len(source.invalidations))
					}
					if len(source.invalidations) > 0 && source.invalidations[0] != [2]string{"probe-1", "private-token-old"} {
						t.Error("wrong invalidated account/token")
					}
					if tc.provider == kiro.ProviderID && strings.Contains(rec.Body.String(), "private-token") {
						t.Error("probe error exposed credentials")
					}
				})
			}
		}
	}
}
