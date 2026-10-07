package upmodels

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/aceaura/model-surge-upstream/backend/account"
	"github.com/aceaura/model-surge-upstream/backend/apperr"
	"github.com/aceaura/model-surge-upstream/backend/kiro"
	"github.com/aceaura/model-surge-upstream/backend/provider"
)

type kiroHeaderFunc func(context.Context, provider.Spec, account.Account) (map[string]string, error)

func (f kiroHeaderFunc) HeadersFor(ctx context.Context, spec provider.Spec, acc account.Account) (map[string]string, error) {
	return f(ctx, spec, acc)
}

func kiroAccount(base string) account.Account {
	a := acct("kiro-1", kiro.ProviderID, base)
	a.Credential.Kind = provider.CredKiroRefresh
	a.Credential.RefreshToken = "refresh-token"
	a.Credential.ProfileARN = "arn:aws:codewhisperer:eu-west-1:123:profile/test"
	return a
}

func kiroTestHeaders(_ context.Context, _ provider.Spec, acc account.Account) (map[string]string, error) {
	return kiro.Headers("live-token", acc.Credential.ProfileARN), nil
}

type kiroInvalidateFunc func(string, string)

func (f kiroInvalidateFunc) Invalidate(name, token string) { f(name, token) }

func TestKiroModelsForbiddenRefresh(t *testing.T) {
	for _, mode := range []string{"success", "persistent", "refresh failure", "metadata", "later page", "retry budget"} {
		t.Run(mode, func(t *testing.T) {
			calls, invalidations := 0, 0
			var a account.Account
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls++
				if mode == "retry budget" && calls == 1 {
					w.WriteHeader(http.StatusInternalServerError)
					return
				}
				if mode == "later page" && calls == 1 {
					fmt.Fprint(w, `{"models":[{"modelId":"first"}],"nextToken":"page2"}`)
					return
				}
				if r.Header.Get("Authorization") == "Bearer old-token" || mode == "persistent" {
					w.WriteHeader(http.StatusForbidden)
					return
				}
				if r.Header.Get("Authorization") != "Bearer new-token" {
					t.Error("refreshed authorization was not used")
				}
				if mode == "metadata" && (r.URL.Query().Has("profileArn") || r.URL.Path != "/refreshed/ListAvailableModels") {
					t.Error("refreshed endpoint or OIDC identity retained old metadata")
				}
				if mode == "later page" && r.URL.Query().Get("nextToken") != "page2" {
					t.Error("refresh lost pagination cursor")
				}
				fmt.Fprint(w, `{"models":[{"modelId":"fresh"}]}`)
			}))
			defer srv.Close()
			a = kiroAccount(srv.URL)
			accounts := fakeAccounts{a.Name: a}
			l := New(accounts, time.Minute).WithHeaderSource(kiroHeaderFunc(func(_ context.Context, _ provider.Spec, _ account.Account) (map[string]string, error) {
				if invalidations > 0 {
					if mode == "refresh failure" {
						return nil, errors.New("refresh failed")
					}
					if mode == "metadata" {
						a.Credential.ClientID, a.Credential.ClientSecret = "client", "secret"
						a.BaseURL = srv.URL + "/refreshed"
						accounts[a.Name] = a
					}
					return kiro.Headers("new-token", a.Credential.ProfileARN), nil
				}
				return kiro.Headers("old-token", a.Credential.ProfileARN), nil
			})).WithTokenInvalidator(kiroInvalidateFunc(func(name, token string) {
				invalidations++
				if name != a.Name || token != "old-token" {
					t.Error("incorrect token invalidation")
				}
			}))
			got, err := l.List(context.Background(), a.Name)
			if err != nil || invalidations != 1 {
				t.Fatalf("invalidations=%d err=%v", invalidations, err)
			}
			wantCalls := 2
			if mode == "refresh failure" {
				wantCalls = 1
			} else if mode == "later page" || mode == "retry budget" {
				wantCalls = 3
			}
			if calls != wantCalls {
				t.Fatalf("calls=%d want=%d", calls, wantCalls)
			}
			if mode == "persistent" || mode == "refresh failure" {
				if len(got.Models) != len(kiroFallbackModels) {
					t.Fatalf("fallback=%v", got.Models)
				}
			} else {
				want := []Entry{{ID: "auto-kiro"}, {ID: "fresh"}}
				if mode == "later page" {
					want = []Entry{{ID: "auto-kiro"}, {ID: "first"}, {ID: "fresh"}}
				}
				if !reflect.DeepEqual(got.Models, want) {
					t.Fatalf("models=%v want=%v", got.Models, want)
				}
			}
		})
	}
}

func TestRound29KiroModelsWrappedSecurityErrors(t *testing.T) {
	for _, tc := range []struct {
		name  string
		cause error
	}{
		{"TLS", errors.New("remote error: TLS: handshake failure")},
		{"SSL", errors.New("SSL handshake failed")},
		{"certificate", errors.New("x509: certificate signed by unknown authority")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			wrapped := fmt.Errorf("upstream: %w", &net.OpError{Op: "dial", Net: "tcp", Err: tc.cause})
			if kiroRetryable(wrapped, 0) {
				t.Error("wrapped security error classified as retryable")
			}
			a := kiroAccount("https://kiro.invalid")
			l := New(fakeAccounts{a.Name: a}, time.Minute).WithHeaderSource(kiroHeaderFunc(kiroTestHeaders))
			calls := 0
			l.SetClient(&http.Client{Transport: kiroRoundTripFunc(func(r *http.Request) (*http.Response, error) {
				calls++
				if r.URL.Path != "/ListAvailableModels" || r.Header.Get("Authorization") != "Bearer live-token" {
					t.Error("model request lost path/auth")
				}
				return nil, wrapped
			})})
			got, err := l.List(context.Background(), a.Name)
			if err != nil || calls != 1 || !got.Queryable || len(got.Models) != len(kiroFallbackModels) {
				t.Fatalf("calls=%d models=%d queryable=%v err=%v", calls, len(got.Models), got.Queryable, err)
			}
		})
	}
}

func TestKiroListRetryClassification(t *testing.T) {
	for _, cause := range []error{
		&tls.CertificateVerificationError{Err: x509.UnknownAuthorityError{}},
		x509.UnknownAuthorityError{},
		x509.HostnameError{Certificate: &x509.Certificate{}, Host: "invalid"},
		x509.CertificateInvalidError{Cert: &x509.Certificate{}},
		tls.RecordHeaderError{},
	} {
		wrapped := &net.OpError{Op: "dial", Net: "tcp", Err: cause}
		if kiroRetryable(wrapped, 0) {
			t.Errorf("TLS error retried: %T", cause)
		}
	}
	for _, err := range []error{io.EOF, io.ErrUnexpectedEOF, &net.DNSError{Err: "temporary", IsTemporary: true}} {
		if !kiroRetryable(err, 0) {
			t.Errorf("network error not retried: %v", err)
		}
	}
	for status, want := range map[int]bool{200: false, 401: false, 429: true, 500: true, 503: true} {
		if kiroRetryable(nil, status) != want {
			t.Errorf("status=%d retry=%v", status, !want)
		}
	}
}

func TestKiroListAuthenticationPaginationAndRefresh(t *testing.T) {
	calls := 0
	freshProfile := "arn:aws:codewhisperer:eu-central-1:123:profile/fresh"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.Method != "GET" || r.URL.Path != "/native/ListAvailableModels" {
			t.Errorf("method/path = %s %s", r.Method, r.URL.Path)
		}
		if r.URL.Query().Get("origin") != "AI_EDITOR" || r.URL.Query().Get("profileArn") != freshProfile {
			t.Errorf("query = %s", r.URL.RawQuery)
		}
		if r.Header.Get("Authorization") != "Bearer live-token" || r.Header.Get("X-Amz-User-Agent") == "" || r.Header.Get("Accept") != "application/json" {
			t.Errorf("headers = %v", r.Header)
		}
		if r.Header.Get(kiro.HeaderProvider) != "" || r.Header.Get(kiro.HeaderProfileARN) != "" {
			t.Error("internal routing headers leaked")
		}
		switch calls {
		case 1:
			if r.URL.Query().Get("nextToken") != "" {
				t.Error("unexpected initial pagination token")
			}
			fmt.Fprint(w, `{"models":[{"modelId":"z","modelName":"Zed"}],"nextToken":"page +/2"}`)
		case 2:
			if r.URL.Query().Get("nextToken") != "page +/2" {
				t.Errorf("pagination token = %q", r.URL.Query().Get("nextToken"))
			}
			// auto 属 HIDDEN_FROM_LIST:自身不入列表,以别名 auto-kiro 出现。
			fmt.Fprint(w, `{"models":[{"modelId":"a","modelName":"Alpha"},{"modelId":"z"},{"modelId":"auto"}]}`)
		default:
			t.Error("unexpected extra page")
		}
	}))
	defer srv.Close()
	accounts := fakeAccounts{"kiro-1": kiroAccount("http://127.0.0.1:1")}
	l := New(accounts, time.Minute).WithHeaderSource(kiroHeaderFunc(func(ctx context.Context, spec provider.Spec, a account.Account) (map[string]string, error) {
		a.Credential.ProfileARN = freshProfile
		a.BaseURL = srv.URL + "/native"
		accounts[a.Name] = a
		return kiroTestHeaders(ctx, spec, a)
	}))
	// Production clients may carry the generation transport: flags must be
	// removed before GET reaches that transport.
	l.SetClient(&http.Client{Transport: kiro.NewTransport(srv.Client().Transport)})
	got, err := l.List(context.Background(), "kiro-1")
	if err != nil {
		t.Fatal(err)
	}
	want := []Entry{{ID: "a", DisplayName: "Alpha"}, {ID: "auto-kiro"}, {ID: "z", DisplayName: "Zed"}}
	if !got.Queryable || !reflect.DeepEqual(got.Models, want) || calls != 2 {
		t.Fatalf("report = %+v, calls = %d", got, calls)
	}
	if _, err := l.List(context.Background(), "kiro-1"); err != nil || calls != 2 {
		t.Fatalf("cache failed: %v, calls=%d", err, calls)
	}
}

func TestKiroModelsControlEndpoint(t *testing.T) {
	spec := provider.Spec{BaseURL: kiro.DefaultBaseURL}
	for _, tc := range []struct {
		name, base, apiRegion, profile, ssoRegion, want string
	}{
		{"default does not use SSO", "", "", "", "eu-west-1", "https://q.us-east-1.amazonaws.com"},
		{"profile region", "", "", "arn:aws:codewhisperer:eu-west-1:123:profile/x", "us-east-2", "https://q.eu-west-1.amazonaws.com"},
		{"explicit API region", "", "ap-northeast-1", "arn:aws:codewhisperer:eu-west-1:123:profile/x", "", "https://q.ap-northeast-1.amazonaws.com"},
		{"default override", kiro.DefaultBaseURL + "/", "eu-west-2", "", "", "https://q.eu-west-2.amazonaws.com"},
		{"official runtime", "https://runtime.eu-central-1.kiro.dev/", "ap-northeast-1", "", "", "https://q.eu-central-1.amazonaws.com"},
		{"Q unchanged", "https://q.us-west-2.amazonaws.com/", "eu-west-2", "", "", "https://q.us-west-2.amazonaws.com"},
		{"custom unchanged", "http://127.0.0.1:9999/native/", "eu-west-2", "", "", "http://127.0.0.1:9999/native"},
		{"lookalike unchanged", "https://runtime.us-east-1.kiro.dev.example.com", "", "", "", "https://runtime.us-east-1.kiro.dev.example.com"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a := kiroAccount(tc.base)
			a.Credential.APIRegion, a.Credential.ProfileARN, a.Credential.Region = tc.apiRegion, tc.profile, tc.ssoRegion
			if got := kiroControlBase(spec, a); got != tc.want {
				t.Errorf("base = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestKiroModelsFailures(t *testing.T) {
	// account_manager.py:534-538: 拉取失败一律回退静态已知模型表。
	for _, tc := range []struct {
		name, body string
		status     int
	}{
		{"HTTP failure", `{"models":[]}`, 401},
		{"invalid JSON", `nope`, 200},
		{"missing list", `{}`, 200},
		{"null list", `{"models":null}`, 200},
		{"wrong list type", `{"models":{}}`, 200},
		{"bad next token", `{"models":[],"nextToken":123}`, 200},
		{"oversized body", strings.Repeat(" ", bodyLimit+1), 200},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(tc.status)
				fmt.Fprint(w, tc.body)
			}))
			defer srv.Close()
			l := New(fakeAccounts{"kiro-1": kiroAccount(srv.URL)}, time.Minute).WithHeaderSource(kiroHeaderFunc(kiroTestHeaders))
			got, err := l.List(context.Background(), "kiro-1")
			if err != nil {
				t.Fatalf("expected fallback, error = %v", err)
			}
			if len(got.Models) != len(kiroFallbackModels) || got.Models[0].ID != "auto-kiro" {
				t.Fatalf("fallback = %v", got.Models)
			}
			if _, cached := l.lookup("kiro-1"); !cached {
				t.Error("fallback report should be cached like a normal listing")
			}
		})
	}
	// 重复 token:已拿到合法(空)列表,翻页终止而非回退。
	t.Run("repeated token", func(t *testing.T) {
		calls := 0
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			calls++
			fmt.Fprint(w, `{"models":[],"nextToken":"same"}`)
		}))
		defer srv.Close()
		l := New(fakeAccounts{"kiro-1": kiroAccount(srv.URL)}, time.Minute).WithHeaderSource(kiroHeaderFunc(kiroTestHeaders))
		got, err := l.List(context.Background(), "kiro-1")
		if err != nil || !reflect.DeepEqual(got.Models, []Entry{{ID: "auto-kiro"}}) || calls != 2 {
			t.Fatalf("report = %+v, err = %v, calls = %d", got, err, calls)
		}
	})
}

func TestKiroModelsPaginationLimit(t *testing.T) {
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls++
		fmt.Fprintf(w, `{"models":[],"nextToken":"%d"}`, calls)
	}))
	defer srv.Close()
	l := New(fakeAccounts{"kiro-1": kiroAccount(srv.URL)}, time.Minute).WithHeaderSource(kiroHeaderFunc(kiroTestHeaders))
	got, err := l.List(context.Background(), "kiro-1")
	if err != nil || !reflect.DeepEqual(got.Models, []Entry{{ID: "auto-kiro"}}) || calls != kiroMaxPages {
		t.Fatalf("report = %+v, err = %v, calls = %d", got, err, calls)
	}
}

func TestKiroModelsTokenWiring(t *testing.T) {
	accounts := fakeAccounts{"kiro-1": kiroAccount("http://127.0.0.1:1")}
	l := New(accounts, time.Minute)
	if _, err := l.List(context.Background(), "kiro-1"); !apperr.Is(err, apperr.UpstreamUnavailable) || !strings.Contains(err.Error(), "header source") {
		t.Fatalf("missing wiring: %v", err)
	}
	l.WithHeaderSource(kiroHeaderFunc(func(context.Context, provider.Spec, account.Account) (map[string]string, error) {
		return kiro.Headers("", ""), nil
	}))
	if _, err := l.List(context.Background(), "kiro-1"); !apperr.Is(err, apperr.UpstreamUnavailable) || !strings.Contains(err.Error(), "access token") {
		t.Fatalf("empty token: %v", err)
	}
	failure := errors.New("refresh failed")
	l.WithHeaderSource(kiroHeaderFunc(func(context.Context, provider.Spec, account.Account) (map[string]string, error) { return nil, failure }))
	if _, err := l.List(context.Background(), "kiro-1"); !errors.Is(err, failure) {
		t.Fatalf("refresh failure: %v", err)
	}
	l.WithHeaderSource(kiroHeaderFunc(kiroTestHeaders))
	got, err := l.List(context.Background(), "kiro-1")
	if err != nil || len(got.Models) != len(kiroFallbackModels) {
		t.Fatalf("network failure should fall back: %v", err)
	}
}

func TestKiroModelsEmptyListNoProfile(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Has("profileArn") {
			t.Error("empty profile must not be sent")
		}
		fmt.Fprint(w, `{"models":[]}`)
	}))
	defer srv.Close()
	a := kiroAccount(srv.URL)
	a.Credential.ProfileARN = ""
	l := New(fakeAccounts{a.Name: a}, time.Minute).WithHeaderSource(kiroHeaderFunc(kiroTestHeaders))
	got, err := l.List(context.Background(), a.Name)
	if err != nil || !reflect.DeepEqual(got.Models, []Entry{{ID: "auto-kiro"}}) {
		t.Fatalf("empty list = %+v, error = %v", got, err)
	}
}

type kiroRoundTripFunc func(*http.Request) (*http.Response, error)

func (f kiroRoundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestKiroModelsOfficialRequestURL(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/ListAvailableModels" {
			t.Errorf("method/path = %s %s", r.Method, r.URL.Path)
		}
		fmt.Fprint(w, `{"models":[{"modelId":"actual-upstream-model"}]}`)
	}))
	defer srv.Close()
	a := kiroAccount("")
	a.Credential.APIRegion = "ap-northeast-1"
	a.Credential.Region = "us-east-2"
	l := New(fakeAccounts{a.Name: a}, time.Minute).WithHeaderSource(kiroHeaderFunc(kiroTestHeaders))
	l.SetClient(&http.Client{Transport: kiroRoundTripFunc(func(r *http.Request) (*http.Response, error) {
		if r.URL.Scheme != "https" || r.URL.Host != "q.ap-northeast-1.amazonaws.com" || r.URL.Query().Get("profileArn") != a.Credential.ProfileARN {
			t.Errorf("official URL = %s", r.URL)
		}
		// Redirect only inside the test transport; production selection remains
		// observable without making any request to the public service.
		local, err := http.NewRequestWithContext(r.Context(), r.Method, srv.URL+r.URL.RequestURI(), nil)
		if err != nil {
			return nil, err
		}
		local.Header = r.Header.Clone()
		return srv.Client().Transport.RoundTrip(local)
	})})
	got, err := l.List(context.Background(), a.Name)
	if err != nil || !reflect.DeepEqual(got.Models, []Entry{{ID: "actual-upstream-model"}, {ID: "auto-kiro"}}) {
		t.Fatalf("report = %+v, err = %v", got, err)
	}
}

func TestKiroStaleModelsOnRefreshFailure(t *testing.T) {
	for _, failure := range []string{"http", "json", "missing modelId", "null entry"} {
		t.Run(failure, func(t *testing.T) {
			calls := 0
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				calls++
				if calls == 1 {
					fmt.Fprint(w, `{"models":[{"modelId":"new-model"}]}`)
					return
				}
				if failure == "http" {
					w.WriteHeader(http.StatusUnauthorized)
				}
				switch failure {
				case "missing modelId":
					fmt.Fprint(w, `{"models":[{}]}`)
				case "null entry":
					fmt.Fprint(w, `{"models":[null]}`)
				default:
					fmt.Fprint(w, `invalid-json`)
				}
			}))
			defer srv.Close()
			a := kiroAccount(srv.URL)
			l := New(fakeAccounts{a.Name: a}, time.Minute).WithHeaderSource(kiroHeaderFunc(kiroTestHeaders))
			first, err := l.List(context.Background(), a.Name)
			if err != nil {
				t.Fatal(err)
			}
			l.mu.Lock()
			cached := l.cached[a.Name]
			cached.expires = time.Now().Add(-time.Second)
			l.cached[a.Name] = cached
			l.mu.Unlock()
			got, err := l.List(context.Background(), a.Name)
			if err != nil || !reflect.DeepEqual(got, first) || calls != 2 {
				t.Fatalf("first=%+v got=%+v calls=%d err=%v", first, got, calls, err)
			}
			l.Forget(a.Name)
			got, err = l.List(context.Background(), a.Name)
			if err != nil || len(got.Models) != len(kiroFallbackModels) || calls != 3 {
				t.Fatalf("forgotten report=%+v calls=%d err=%v", got, calls, err)
			}
		})
	}
}

func TestKiroOIDCModelsOmitsProfile(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Has("profileArn") || r.URL.Query().Get("origin") != "AI_EDITOR" {
			t.Errorf("OIDC query=%s", r.URL.RawQuery)
		}
		fmt.Fprint(w, `{"models":[]}`)
	}))
	defer srv.Close()
	a := kiroAccount(srv.URL)
	a.Credential.ClientID, a.Credential.ClientSecret = "client", "secret"
	l := New(fakeAccounts{a.Name: a}, time.Minute).WithHeaderSource(kiroHeaderFunc(kiroTestHeaders))
	got, err := l.List(context.Background(), a.Name)
	if err != nil || !reflect.DeepEqual(got.Models, []Entry{{ID: "auto-kiro"}}) {
		t.Fatalf("report=%+v err=%v", got, err)
	}
}
