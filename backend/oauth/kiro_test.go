package oauth

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/aceaura/model-surge-upstream/backend/account"
	"github.com/aceaura/model-surge-upstream/backend/credential"
	"github.com/aceaura/model-surge-upstream/backend/provider"
)

type kiroRefreshTransport struct {
	base http.RoundTripper
	url  string
	host string
}

func (tr kiroRefreshTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	tr.host = r.URL.Host
	clone := r.Clone(r.Context())
	endpoint, _ := http.NewRequest(r.Method, tr.url+r.URL.Path, nil)
	clone.Header.Set("X-Test-Original-Host", r.URL.Host)
	clone.URL = endpoint.URL
	return tr.base.RoundTrip(clone)
}

func kiroAccount() account.Account {
	return account.Account{Name: "kiro-1", ProviderID: "kiro.global.subscribe.standard", Credential: credential.Credential{
		Kind: provider.CredKiroRefresh, RefreshToken: "rt-secret", Region: "us-east-1",
	}}
}

func TestKiroRefreshDesktopAndSSO(t *testing.T) {
	for _, sso := range []bool{false, true} {
		t.Run(map[bool]string{false: "desktop", true: "sso"}[sso], func(t *testing.T) {
			acc := kiroAccount()
			if sso {
				acc.Credential.Region = "eu-west-1"
				acc.Credential.ClientID, acc.Credential.ClientSecret = "client-test", "secret-test"
			}
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != http.MethodPost || r.Header.Get("Content-Type") != "application/json" {
					t.Errorf("bad request method/content-type")
				}
				var body map[string]string
				if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
					t.Error(err)
				}
				if body["refreshToken"] != "rt-secret" || body["refresh_token"] != "" {
					t.Errorf("bad refresh payload")
				}
				if sso {
					if r.URL.Path != "/token" || r.Header.Get("X-Test-Original-Host") != "oidc.eu-west-1.amazonaws.com" ||
						body["clientId"] != "client-test" || body["clientSecret"] != "secret-test" || body["grantType"] != "refresh_token" {
						t.Error("bad SSO endpoint/payload")
					}
				} else if r.URL.Path != "/refreshToken" || r.Header.Get("X-Test-Original-Host") != "prod.us-east-1.auth.desktop.kiro.dev" || len(body) != 1 {
					t.Error("bad Desktop endpoint/payload")
				}
				_, _ = io.WriteString(w, `{"accessToken":"at-new","refreshToken":"rt-rotated","expiresIn":3600,"profileArn":"arn:aws:codewhisperer:us-east-1:123:profile/test"}`)
			}))
			defer srv.Close()
			store := &fakeStore{}
			client := &http.Client{Transport: kiroRefreshTransport{base: srv.Client().Transport, url: srv.URL}}
			m := newManager(store, client, TokenURL, ClientID)
			token, err := m.AccessToken(context.Background(), acc)
			if err != nil || token != "at-new" {
				t.Fatalf("token=%q err=%v", token, err)
			}
			got := store.last()
			if got.RefreshToken != "rt-rotated" || got.ProfileARN == "" || got.Kind != provider.CredKiroRefresh || time.Until(got.Expiry) < 50*time.Minute {
				t.Fatalf("bad persisted refreshed credential")
			}
			if got.ClientID != acc.Credential.ClientID || got.ClientSecret != acc.Credential.ClientSecret {
				t.Fatal("SSO registration lost on refresh")
			}
		})
	}
}

func TestKiroRefreshErrors(t *testing.T) {
	for _, tc := range []struct {
		name   string
		status int
		body   string
		reauth bool
	}{
		{"expired", 400, `{"error":"invalid_grant"}`, true},
		{"unauthorized", 401, `{}`, true},
		{"server", 500, `{}`, false},
		{"bad-registration", 400, `{"error":"invalid_client"}`, false},
		{"json", 200, `not-json-secret`, false},
		{"missing-token", 200, `{}`, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(tc.status)
				_, _ = io.WriteString(w, tc.body)
			}))
			defer srv.Close()
			store := &fakeStore{}
			m := newManager(store, &http.Client{Transport: kiroRefreshTransport{base: srv.Client().Transport, url: srv.URL}}, TokenURL, ClientID)
			_, err := m.AccessToken(context.Background(), kiroAccount())
			if err == nil || errors.Is(err, ErrNeedsReauth) != tc.reauth || m.NeedsReauth("kiro-1") != tc.reauth {
				t.Fatalf("err=%v needsReauth=%v", err, m.NeedsReauth("kiro-1"))
			}
			if strings.Contains(err.Error(), "secret") || len(store.saved) != 0 {
				t.Fatal("error leaked upstream body or persisted failed refresh")
			}
		})
	}
}

func TestKiroRefreshSingleFlightAndInvalidation(t *testing.T) {
	var hits atomic.Int64
	entered, release := make(chan struct{}), make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if hits.Add(1) == 1 {
			close(entered)
			<-release
		}
		_, _ = io.WriteString(w, `{"accessToken":"at-new","expiresIn":3600}`)
	}))
	defer srv.Close()
	store := &fakeStore{}
	m := newManager(store, &http.Client{Transport: kiroRefreshTransport{base: srv.Client().Transport, url: srv.URL}}, TokenURL, ClientID)
	acc := kiroAccount()
	var wg sync.WaitGroup
	wg.Add(1)
	go func() { defer wg.Done(); _, _ = m.AccessToken(context.Background(), acc) }()
	<-entered
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := m.AccessToken(ctx, acc); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled wait = %v", err)
	}
	close(release)
	wg.Wait()
	if hits.Load() != 1 {
		t.Fatal("refresh was not single flight")
	}
	acc.Credential = store.last()
	if token, err := m.AccessToken(context.Background(), acc); err != nil || token != "at-new" || hits.Load() != 1 {
		t.Fatal("fresh token not reused")
	}
	m.Invalidate(acc.Name, "at-new")
	if _, err := m.AccessToken(context.Background(), acc); err != nil || hits.Load() != 2 {
		t.Fatalf("invalidated token not refreshed: %v", err)
	}
}
