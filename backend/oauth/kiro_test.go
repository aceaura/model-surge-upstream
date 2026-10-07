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

func TestKiroRefreshExpiryBuffer(t *testing.T) {
	for _, sso := range []bool{false, true} {
		for _, tc := range []struct {
			name, field string
			want        time.Duration
		}{
			{"default", "", 59 * time.Minute},
			{"normal", `,"expiresIn":3600`, 59 * time.Minute},
			{"short", `,"expiresIn":30`, -30 * time.Second},
			{"zero", `,"expiresIn":0`, -time.Minute},
		} {
			t.Run(tc.name+map[bool]string{false: "/desktop", true: "/sso"}[sso], func(t *testing.T) {
				acc := kiroAccount()
				if sso {
					acc.Credential.ClientID, acc.Credential.ClientSecret = "client", "secret"
				}
				srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
					io.WriteString(w, `{"accessToken":"new-token"`+tc.field+`}`)
				}))
				defer srv.Close()
				store := &fakeStore{}
				m := newManager(store, &http.Client{Transport: kiroRefreshTransport{base: srv.Client().Transport, url: srv.URL}}, TokenURL, ClientID)
				before := time.Now().Add(tc.want)
				if _, err := m.AccessToken(context.Background(), acc); err != nil {
					t.Fatal(err)
				}
				after := time.Now().Add(tc.want)
				expiry := store.last().Expiry
				if expiry.Before(before) || expiry.After(after) {
					t.Fatalf("expiry=%v range=%v..%v", expiry, before, after)
				}
			})
		}
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

// Round27 regressions use only in-memory transports, including rotation failures.
type round27KiroTransport func(*http.Request) (*http.Response, error)

func (tr round27KiroTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	return tr(r)
}

func round27KiroResponse(status int, body string) *http.Response {
	return &http.Response{
		StatusCode: status,
		Status:     http.StatusText(status),
		Header:     make(http.Header),
		Body:       io.NopCloser(strings.NewReader(body)),
	}
}

type round27KiroStep struct {
	refresh string
	body    string
}

func round27KiroFixture(t *testing.T, store *fakeStore, steps ...round27KiroStep) (*Manager, *atomic.Int64) {
	t.Helper()
	hits := &atomic.Int64{}
	client := &http.Client{Transport: round27KiroTransport(func(r *http.Request) (*http.Response, error) {
		hit := hits.Add(1)
		var body map[string]string
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error("refresh payload is not valid JSON")
			return round27KiroResponse(http.StatusBadRequest, `{"error":"invalid_request"}`), nil
		}
		if hit > int64(len(steps)) {
			t.Error("unexpected extra refresh request")
			return round27KiroResponse(http.StatusBadRequest, `{"error":"invalid_grant"}`), nil
		}
		step := steps[hit-1]
		if body["refreshToken"] != step.refresh {
			t.Error("refresh request did not use the expected credential")
			return round27KiroResponse(http.StatusBadRequest, `{"error":"invalid_grant"}`), nil
		}
		return round27KiroResponse(http.StatusOK, step.body), nil
	})}
	return newManager(store, client, TokenURL, ClientID), hits
}

func round27KiroToken(t *testing.T, m *Manager, acc account.Account, want string) {
	t.Helper()
	token, err := m.AccessToken(context.Background(), acc)
	if err != nil || token != want {
		t.Fatalf("AccessToken returned an unexpected token or error: %v", err)
	}
}

func TestRound27KiroOldSnapshotReusesSuccessfulCredential(t *testing.T) {
	for _, invalidated := range []bool{false, true} {
		t.Run(map[bool]string{false: "missing-access", true: "invalidated-fresh-access"}[invalidated], func(t *testing.T) {
			store := &fakeStore{}
			m, hits := round27KiroFixture(t, store, round27KiroStep{
				"rt-secret", `{"accessToken":"at-new","refreshToken":"rt-rotated","expiresIn":3600}`,
			})
			acc := kiroAccount()
			if invalidated {
				acc.Credential.AccessToken = "at-old"
				acc.Credential.Expiry = time.Now().Add(time.Hour)
				m.Invalidate(acc.Name, "at-old")
			}
			round27KiroToken(t, m, acc, "at-new")
			// The flight has been deleted, but the caller still holds the original snapshot.
			round27KiroToken(t, m, acc, "at-new")
			if hits.Load() != 1 || len(store.saved) != 1 || m.NeedsReauth(acc.Name) {
				t.Fatal("successful credential was not reused after flight completion")
			}
		})
	}
}

func TestRound27KiroCacheDoesNotOverrideCodexCredential(t *testing.T) {
	m, hits := round27KiroFixture(t, &fakeStore{}, round27KiroStep{
		"rt-secret", `{"accessToken":"at-new","refreshToken":"rt-rotated","expiresIn":3600}`,
	})
	acc := kiroAccount()
	round27KiroToken(t, m, acc, "at-new")
	acc.ProviderID = "openai.global.subscribe.codex"
	acc.Credential = oauthCred("at-codex", time.Now().Add(time.Hour))
	round27KiroToken(t, m, acc, "at-codex")
	if hits.Load() != 1 {
		t.Fatal("Kiro cache changed the Codex fresh-token path")
	}
}

func TestRound27KiroInvalidationUsesRotatedRefreshToken(t *testing.T) {
	store := &fakeStore{}
	m, hits := round27KiroFixture(t, store,
		round27KiroStep{"rt-secret", `{"accessToken":"at-new","refreshToken":"rt-rotated","expiresIn":3600}`},
		round27KiroStep{"rt-rotated", `{"accessToken":"at-next","refreshToken":"rt-next","expiresIn":3600}`},
	)
	acc := kiroAccount()
	round27KiroToken(t, m, acc, "at-new")
	m.Invalidate(acc.Name, "at-new")
	round27KiroToken(t, m, acc, "at-next")
	round27KiroToken(t, m, acc, "at-next")
	m.mu.Lock()
	_, invalid := m.invalid[acc.Name]
	m.mu.Unlock()
	if hits.Load() != 2 || len(store.saved) != 2 || invalid || m.NeedsReauth(acc.Name) {
		t.Fatal("rotation or invalidation state was not updated after refresh")
	}
}

func TestRound27KiroResetAllowsReimportedCredential(t *testing.T) {
	for _, fresh := range []bool{false, true} {
		t.Run(map[bool]string{false: "refresh", true: "fresh"}[fresh], func(t *testing.T) {
			store := &fakeStore{}
			m, hits := round27KiroFixture(t, store,
				round27KiroStep{"rt-secret", `{"accessToken":"at-new","refreshToken":"rt-rotated","expiresIn":3600}`},
				round27KiroStep{"rt-imported", `{"accessToken":"at-imported-next","refreshToken":"rt-imported-next","expiresIn":3600}`},
			)
			acc := kiroAccount()
			round27KiroToken(t, m, acc, "at-new")
			m.Invalidate(acc.Name, "at-new")
			m.Reset(acc.Name)
			acc.Credential.RefreshToken = "rt-imported"
			want, wantHits := "at-imported-next", int64(2)
			if fresh {
				acc.Credential.AccessToken = "at-imported"
				acc.Credential.Expiry = time.Now().Add(time.Hour)
				want, wantHits = "at-imported", 1
			}
			round27KiroToken(t, m, acc, want)
			if hits.Load() != wantHits {
				t.Fatal("Reset did not allow the reimported credential to take effect")
			}
		})
	}
}

func TestRound27KiroPersistenceFailureDoesNotCache(t *testing.T) {
	persistErr := errors.New("test persistence failure")
	store := &fakeStore{err: persistErr}
	m, hits := round27KiroFixture(t, store,
		round27KiroStep{"rt-secret", `{"accessToken":"at-unsaved","refreshToken":"rt-unsaved","expiresIn":3600}`},
		round27KiroStep{"rt-secret", `{"accessToken":"at-saved","refreshToken":"rt-saved","expiresIn":3600}`},
	)
	acc := kiroAccount()
	m.Invalidate(acc.Name, "at-old")
	if token, err := m.AccessToken(context.Background(), acc); token != "" || !errors.Is(err, persistErr) {
		t.Fatal("persistence failure should not return a usable token")
	}
	m.mu.Lock()
	invalid := m.invalid[acc.Name] == "at-old"
	m.mu.Unlock()
	if len(store.saved) != 0 || !invalid {
		t.Fatal("failed persistence must not save credentials or clear invalidation")
	}
	store.err = nil
	round27KiroToken(t, m, acc, "at-saved")
	if hits.Load() != 2 || len(store.saved) != 1 {
		t.Fatal("failed persistence incorrectly cached the rotated credential")
	}
}

func TestRound27KiroShortTTLUsesLatestRotatedRefreshToken(t *testing.T) {
	store := &fakeStore{}
	m, hits := round27KiroFixture(t, store,
		round27KiroStep{"rt-secret", `{"accessToken":"at-short","refreshToken":"rt-rotated","expiresIn":30}`},
		round27KiroStep{"rt-rotated", `{"accessToken":"at-short-next","refreshToken":"rt-next","expiresIn":0}`},
		round27KiroStep{"rt-next", `{"accessToken":"at-long","refreshToken":"rt-long","expiresIn":3600}`},
	)
	acc := kiroAccount()
	round27KiroToken(t, m, acc, "at-short")
	round27KiroToken(t, m, acc, "at-short-next")
	round27KiroToken(t, m, acc, "at-long")
	round27KiroToken(t, m, acc, "at-long")
	if hits.Load() != 3 || len(store.saved) != 3 {
		t.Fatal("short TTL refresh did not retain the latest rotation")
	}
}

// Done signals that AccessToken reached the existing-flight wait select.
type round27KiroWaitContext struct {
	context.Context
	waiting chan struct{}
	once    sync.Once
}

func (ctx *round27KiroWaitContext) Done() <-chan struct{} {
	ctx.once.Do(func() { close(ctx.waiting) })
	return ctx.Context.Done()
}

func TestRound27KiroFlightWaitResult(t *testing.T) {
	for _, tc := range []struct {
		name   string
		status int
		body   string
	}{
		{"success", http.StatusOK, `{"accessToken":"at-new","refreshToken":"rt-rotated","expiresIn":3600}`},
		{"transient", http.StatusInternalServerError, `{}`},
		{"reauth", http.StatusBadRequest, `{"error":"invalid_grant"}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var hits atomic.Int64
			entered, release := make(chan struct{}), make(chan struct{})
			var releaseOnce sync.Once
			unblock := func() { releaseOnce.Do(func() { close(release) }) }
			t.Cleanup(unblock)
			client := &http.Client{Transport: round27KiroTransport(func(r *http.Request) (*http.Response, error) {
				if hits.Add(1) == 1 {
					close(entered)
					select {
					case <-release:
					case <-r.Context().Done():
						return nil, r.Context().Err()
					}
				}
				return round27KiroResponse(tc.status, tc.body), nil
			})}
			store := &fakeStore{}
			m := newManager(store, client, TokenURL, ClientID)
			acc := kiroAccount()
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			type result struct {
				token string
				err   error
			}
			leader, waiter := make(chan result, 1), make(chan result, 1)
			go func() {
				token, err := m.AccessToken(ctx, acc)
				leader <- result{token, err}
			}()
			select {
			case <-entered:
			case <-ctx.Done():
				t.Fatal("refresh did not enter the transport")
			}
			waitCtx := &round27KiroWaitContext{Context: ctx, waiting: make(chan struct{})}
			go func() {
				token, err := m.AccessToken(waitCtx, acc)
				waiter <- result{token, err}
			}()
			select {
			case <-waitCtx.waiting:
			case <-ctx.Done():
				t.Fatal("second caller did not wait on the existing flight")
			}
			unblock()
			first, second := <-leader, <-waiter
			if first.token != second.token || first.err != second.err || hits.Load() != 1 {
				t.Fatal("flight waiter did not receive the leader's result")
			}
			token, err := m.AccessToken(ctx, acc)
			switch tc.name {
			case "success":
				if first.err != nil || token != "at-new" || err != nil || hits.Load() != 1 || len(store.saved) != 1 {
					t.Fatal("successful flight was not reused by the later stale snapshot")
				}
			case "transient":
				if first.err == nil || err == nil || m.NeedsReauth(acc.Name) || hits.Load() != 2 || len(store.saved) != 0 {
					t.Fatal("transient flight failure should allow retry without caching")
				}
			case "reauth":
				if !errors.Is(first.err, ErrNeedsReauth) || !errors.Is(err, ErrNeedsReauth) || !m.NeedsReauth(acc.Name) || hits.Load() != 1 || len(store.saved) != 0 {
					t.Fatal("terminal flight failure should short circuit without caching")
				}
			}
		})
	}
}

type kiroLifecycleResult struct {
	token string
	err   error
}

func kiroLifecycleStart(ctx context.Context, m *Manager, acc account.Account) <-chan kiroLifecycleResult {
	result := make(chan kiroLifecycleResult, 1)
	go func() {
		token, err := m.AccessToken(ctx, acc)
		result <- kiroLifecycleResult{token, err}
	}()
	return result
}

func kiroLifecycleAwait(t *testing.T, ctx context.Context, signal <-chan struct{}) {
	t.Helper()
	select {
	case <-signal:
	case <-ctx.Done():
		t.Fatal("timed out waiting for lifecycle barrier")
	}
}

func kiroLifecycleReceive(t *testing.T, ctx context.Context, result <-chan kiroLifecycleResult) kiroLifecycleResult {
	t.Helper()
	select {
	case got := <-result:
		return got
	case <-ctx.Done():
		t.Fatal("timed out waiting for AccessToken result")
		return kiroLifecycleResult{}
	}
}

func kiroLifecycleRelease(t *testing.T) (<-chan struct{}, func()) {
	t.Helper()
	release := make(chan struct{})
	var once sync.Once
	unblock := func() { once.Do(func() { close(release) }) }
	t.Cleanup(unblock)
	return release, unblock
}

func TestKiroResetRejectsOldFlightAfterFreshImport(t *testing.T) {
	for _, terminal := range []bool{false, true} {
		t.Run(map[bool]string{false: "success", true: "invalid_grant"}[terminal], func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			entered := make(chan struct{})
			release, unblock := kiroLifecycleRelease(t)
			var hits atomic.Int64
			client := &http.Client{Transport: round27KiroTransport(func(r *http.Request) (*http.Response, error) {
				if hits.Add(1) != 1 {
					t.Error("fresh import unexpectedly refreshed")
					return round27KiroResponse(http.StatusInternalServerError, `{}`), nil
				}
				close(entered)
				select {
				case <-release:
				case <-r.Context().Done():
					return nil, r.Context().Err()
				}
				if terminal {
					return round27KiroResponse(http.StatusBadRequest, `{"error":"invalid_grant"}`), nil
				}
				return round27KiroResponse(http.StatusOK, `{"accessToken":"at-old-result","refreshToken":"rt-old-result","expiresIn":3600}`), nil
			})}
			store := &fakeStore{}
			m := newManager(store, client, TokenURL, ClientID)
			acc := kiroAccount()
			old := kiroLifecycleStart(ctx, m, acc)
			kiroLifecycleAwait(t, ctx, entered)
			waitCtx := &round27KiroWaitContext{Context: ctx, waiting: make(chan struct{})}
			oldWaiter := kiroLifecycleStart(waitCtx, m, acc)
			kiroLifecycleAwait(t, ctx, waitCtx.waiting)

			m.Reset(acc.Name)
			imported := acc
			imported.Credential.RefreshToken = "rt-imported"
			imported.Credential.AccessToken = "at-imported"
			imported.Credential.Expiry = time.Now().Add(time.Hour)
			round27KiroToken(t, m, imported, "at-imported")
			// A retired flight must not clear invalidations created after Reset either.
			m.Invalidate(acc.Name, "at-other")
			unblock()
			for _, result := range []<-chan kiroLifecycleResult{old, oldWaiter} {
				got := kiroLifecycleReceive(t, ctx, result)
				if got.token != "" || !errors.Is(got.err, context.Canceled) {
					t.Errorf("retired flight returned token=%q err=%v, want cancellation", got.token, got.err)
				}
			}
			m.mu.Lock()
			_, cached := m.kiroCredentials[acc.Name]
			_, active := m.flights[acc.Name]
			invalid := m.invalid[acc.Name]
			m.mu.Unlock()
			if len(store.saved) != 0 || cached || active || invalid != "at-other" || m.NeedsReauth(acc.Name) {
				t.Error("retired flight persisted or polluted post-Reset state")
			}
			round27KiroToken(t, m, imported, "at-imported")
			if hits.Load() != 1 {
				t.Fatal("fresh import was not immediately reusable")
			}
		})
	}
}

func TestKiroResetOldCompletionPreservesNewFlight(t *testing.T) {
	for _, terminal := range []bool{false, true} {
		t.Run(map[bool]string{false: "success", true: "invalid_grant"}[terminal], func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			oldEntered, newEntered := make(chan struct{}), make(chan struct{})
			oldRelease, unblockOld := kiroLifecycleRelease(t)
			newRelease, unblockNew := kiroLifecycleRelease(t)
			var hits atomic.Int64
			client := &http.Client{Transport: round27KiroTransport(func(r *http.Request) (*http.Response, error) {
				hit := hits.Add(1)
				var body map[string]string
				if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
					t.Error(err)
					return nil, err
				}
				var release <-chan struct{}
				switch hit {
				case 1:
					if body["refreshToken"] != "rt-secret" {
						t.Error("old flight used unexpected refresh credential")
					}
					close(oldEntered)
					release = oldRelease
				case 2:
					if body["refreshToken"] != "rt-imported" {
						t.Error("new flight did not use imported refresh credential")
					}
					close(newEntered)
					release = newRelease
				default:
					t.Error("new-flight waiters issued an extra refresh")
					return round27KiroResponse(http.StatusInternalServerError, `{}`), nil
				}
				select {
				case <-release:
				case <-r.Context().Done():
					return nil, r.Context().Err()
				}
				if hit == 1 {
					if terminal {
						return round27KiroResponse(http.StatusBadRequest, `{"error":"invalid_grant"}`), nil
					}
					return round27KiroResponse(http.StatusOK, `{"accessToken":"at-old-result","refreshToken":"rt-old-result","expiresIn":3600}`), nil
				}
				return round27KiroResponse(http.StatusOK, `{"accessToken":"at-new-result","refreshToken":"rt-new-result","expiresIn":3600}`), nil
			})}
			store := &fakeStore{}
			m := newManager(store, client, TokenURL, ClientID)
			acc := kiroAccount()
			old := kiroLifecycleStart(ctx, m, acc)
			kiroLifecycleAwait(t, ctx, oldEntered)
			m.mu.Lock()
			oldFlight := m.flights[acc.Name]
			m.mu.Unlock()
			m.Reset(acc.Name)
			m.mu.Lock()
			retired := m.flights[acc.Name] != oldFlight
			m.mu.Unlock()
			if !retired {
				t.Fatal("Reset did not retire the old Kiro flight")
			}
			imported := acc
			imported.Credential.RefreshToken = "rt-imported"
			leader := kiroLifecycleStart(ctx, m, imported)
			kiroLifecycleAwait(t, ctx, newEntered)
			m.mu.Lock()
			newFlight := m.flights[acc.Name]
			m.mu.Unlock()
			waitCtx := &round27KiroWaitContext{Context: ctx, waiting: make(chan struct{})}
			waiter := kiroLifecycleStart(waitCtx, m, imported)
			kiroLifecycleAwait(t, ctx, waitCtx.waiting)

			unblockOld()
			got := kiroLifecycleReceive(t, ctx, old)
			if got.token != "" || !errors.Is(got.err, context.Canceled) {
				t.Errorf("retired flight returned token=%q err=%v", got.token, got.err)
			}
			m.mu.Lock()
			preserved := m.flights[acc.Name] == newFlight
			_, cached := m.kiroCredentials[acc.Name]
			m.mu.Unlock()
			if !preserved || cached || len(store.saved) != 0 || m.NeedsReauth(acc.Name) {
				t.Fatal("old completion deleted the new flight or polluted its state")
			}
			// This waiter arrives after old completion, while the new leader is blocked.
			lateCtx := &round27KiroWaitContext{Context: ctx, waiting: make(chan struct{})}
			lateWaiter := kiroLifecycleStart(lateCtx, m, imported)
			kiroLifecycleAwait(t, ctx, lateCtx.waiting)
			if hits.Load() != 2 {
				t.Fatal("late waiter did not join the new flight")
			}
			unblockNew()
			for _, result := range []<-chan kiroLifecycleResult{leader, waiter, lateWaiter} {
				got := kiroLifecycleReceive(t, ctx, result)
				if got.token != "at-new-result" || got.err != nil {
					t.Errorf("new flight returned token=%q err=%v", got.token, got.err)
				}
			}
			round27KiroToken(t, m, imported, "at-new-result")
			m.mu.Lock()
			_, active := m.flights[acc.Name]
			cachedCred := m.kiroCredentials[acc.Name]
			m.mu.Unlock()
			if active || hits.Load() != 2 || len(store.saved) != 1 || cachedCred.RefreshToken != "rt-new-result" || store.last().RefreshToken != "rt-new-result" {
				t.Fatal("new flight did not persist/cache exactly once and complete normally")
			}
		})
	}
}

func TestKiroResetLeavesCodexFlightActive(t *testing.T) {
	for _, terminal := range []bool{false, true} {
		t.Run(map[bool]string{false: "success", true: "reauth"}[terminal], func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			entered := make(chan struct{})
			release, unblock := kiroLifecycleRelease(t)
			var hits atomic.Int64
			client := &http.Client{Transport: round27KiroTransport(func(r *http.Request) (*http.Response, error) {
				if hits.Add(1) != 1 {
					t.Error("Reset broke Codex single-flight behavior")
					return round27KiroResponse(http.StatusInternalServerError, `{}`), nil
				}
				close(entered)
				select {
				case <-release:
				case <-r.Context().Done():
					return nil, r.Context().Err()
				}
				if terminal {
					return round27KiroResponse(http.StatusBadRequest, `{"error":"invalid_grant"}`), nil
				}
				return round27KiroResponse(http.StatusOK, `{"access_token":"at-codex","refresh_token":"rt-codex","expires_in":3600}`), nil
			})}
			store := &fakeStore{}
			m := newManager(store, client, TokenURL, ClientID)
			acc := accWith(oauthCred("", time.Time{}))
			leader := kiroLifecycleStart(ctx, m, acc)
			kiroLifecycleAwait(t, ctx, entered)
			m.mu.Lock()
			original := m.flights[acc.Name]
			m.kiroCredentials[acc.Name] = kiroAccount().Credential
			m.reauth[acc.Name] = true
			m.invalid[acc.Name] = "at-old"
			m.mu.Unlock()
			m.Reset(acc.Name)
			m.mu.Lock()
			preserved := m.flights[acc.Name] == original
			_, cached := m.kiroCredentials[acc.Name]
			_, invalid := m.invalid[acc.Name]
			m.mu.Unlock()
			if !preserved || cached || invalid || m.NeedsReauth(acc.Name) {
				t.Fatal("Reset changed the Codex flight or failed to clear reset state")
			}
			waitCtx := &round27KiroWaitContext{Context: ctx, waiting: make(chan struct{})}
			waiter := kiroLifecycleStart(waitCtx, m, acc)
			kiroLifecycleAwait(t, ctx, waitCtx.waiting)
			unblock()
			first := kiroLifecycleReceive(t, ctx, leader)
			second := kiroLifecycleReceive(t, ctx, waiter)
			if first != second || hits.Load() != 1 || m.NeedsReauth(acc.Name) != terminal {
				t.Fatal("Codex refresh/waiter/reauth behavior changed after Reset")
			}
			if terminal {
				if first.token != "" || !errors.Is(first.err, ErrNeedsReauth) || len(store.saved) != 0 {
					t.Fatal("Codex terminal refresh behavior changed")
				}
			} else if first.token != "at-codex" || first.err != nil || len(store.saved) != 1 {
				t.Fatal("Codex successful refresh was not persisted")
			}
			m.mu.Lock()
			_, active := m.flights[acc.Name]
			_, cached = m.kiroCredentials[acc.Name]
			m.mu.Unlock()
			if active || cached {
				t.Fatal("Codex completion left an active flight or populated the Kiro cache")
			}
		})
	}
}

type kiroLifecycleBlockingStore struct {
	fakeStore
	entered chan struct{}
	release <-chan struct{}
}

func (s *kiroLifecycleBlockingStore) UpdateCredential(ctx context.Context, name string, cred credential.Credential) error {
	close(s.entered)
	select {
	case <-s.release:
		return s.fakeStore.UpdateCredential(ctx, name, cred)
	case <-ctx.Done():
		return ctx.Err()
	}
}

func TestKiroResetSerializesWithPersistence(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	release, unblock := kiroLifecycleRelease(t)
	store := &kiroLifecycleBlockingStore{entered: make(chan struct{}), release: release}
	client := &http.Client{Transport: round27KiroTransport(func(_ *http.Request) (*http.Response, error) {
		return round27KiroResponse(http.StatusOK, `{"accessToken":"at-saved","refreshToken":"rt-saved","expiresIn":3600}`), nil
	})}
	m := newManager(store, client, TokenURL, ClientID)
	acc := kiroAccount()
	leader := kiroLifecycleStart(ctx, m, acc)
	kiroLifecycleAwait(t, ctx, store.entered)
	// Store entry synchronizes the assertion that persistence holds the Reset barrier.
	if m.mu.TryLock() {
		m.mu.Unlock()
		t.Fatal("Kiro persistence is not protected by the Reset barrier")
	}
	resetStarted, resetDone := make(chan struct{}), make(chan struct{})
	go func() {
		close(resetStarted)
		m.Reset(acc.Name)
		close(resetDone)
	}()
	kiroLifecycleAwait(t, ctx, resetStarted)
	unblock()
	kiroLifecycleAwait(t, ctx, resetDone)
	got := kiroLifecycleReceive(t, ctx, leader)
	if got.token != "at-saved" || got.err != nil || len(store.saved) != 1 {
		t.Fatal("refresh preceding the Reset barrier did not persist successfully")
	}
	m.mu.Lock()
	_, cached := m.kiroCredentials[acc.Name]
	_, active := m.flights[acc.Name]
	m.mu.Unlock()
	if cached || active || m.NeedsReauth(acc.Name) {
		t.Fatal("Reset was crossed by post-persistence cache backfill")
	}
	acc.Credential.AccessToken = "at-imported"
	acc.Credential.RefreshToken = "rt-imported"
	acc.Credential.Expiry = time.Now().Add(time.Hour)
	round27KiroToken(t, m, acc, "at-imported")
}
