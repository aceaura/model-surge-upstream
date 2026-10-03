package oauth

import (
	"context"
	"fmt"
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

type fakeStore struct {
	mu    sync.Mutex
	saved []credential.Credential
	err   error
}

func (s *fakeStore) UpdateCredential(_ context.Context, _ string, cred credential.Credential) error {
	if s.err != nil {
		return s.err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.saved = append(s.saved, cred)
	return nil
}

func (s *fakeStore) last() credential.Credential {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.saved[len(s.saved)-1]
}

func oauthCred(accessToken string, expiry time.Time) credential.Credential {
	return credential.Credential{
		Kind:         provider.CredOAuthRefresh,
		RefreshToken: "rt-old",
		AccessToken:  accessToken,
		Expiry:       expiry,
		AccountID:    "acc-1",
	}
}

func accWith(cred credential.Credential) account.Account {
	return account.Account{Name: "a1", ProviderID: "openai-codex", Credential: cred}
}

// tokenServer 记录请求数并按脚本应答。
type tokenServer struct {
	hits  atomic.Int64
	serve func(w http.ResponseWriter, body string)
}

func (ts *tokenServer) handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ts.hits.Add(1)
		if ct := r.Header.Get("Content-Type"); ct != "application/x-www-form-urlencoded" {
			http.Error(w, "want form encoding, got "+ct, http.StatusBadRequest)
			return
		}
		if err := r.ParseForm(); err != nil {
			http.Error(w, "bad form", http.StatusBadRequest)
			return
		}
		body := map[string]string{}
		for k := range r.PostForm {
			body[k] = r.PostForm.Get(k)
		}
		if body["grant_type"] != "refresh_token" || body["client_id"] == "" || body["refresh_token"] == "" {
			http.Error(w, "bad grant", http.StatusBadRequest)
			return
		}
		if body["scope"] != Scope {
			http.Error(w, "bad scope "+body["scope"], http.StatusBadRequest)
			return
		}
		if r.Header.Get("originator") != Originator {
			http.Error(w, "missing originator", http.StatusBadRequest)
			return
		}
		ts.serve(w, body["refresh_token"])
	})
}

func newFixture(t *testing.T, serve func(w http.ResponseWriter, body string)) (*Manager, *fakeStore, *tokenServer) {
	t.Helper()
	ts := &tokenServer{serve: serve}
	srv := httptest.NewServer(ts.handler())
	t.Cleanup(srv.Close)
	store := &fakeStore{}
	return newManager(store, srv.Client(), srv.URL, "client-test"), store, ts
}

func okToken(w http.ResponseWriter, access, refresh string, expiresIn int64) {
	w.Header().Set("Content-Type", "application/json")
	fmt.Fprintf(w, `{"access_token":%q,"refresh_token":%q,"expires_in":%d}`, access, refresh, expiresIn)
}

func TestFreshTokenReturnedWithoutRefresh(t *testing.T) {
	m, _, ts := newFixture(t, func(w http.ResponseWriter, _ string) { okToken(w, "at-new", "", 3600) })
	acc := accWith(oauthCred("at-good", time.Now().Add(time.Hour)))

	tok, err := m.AccessToken(context.Background(), acc)
	if err != nil || tok != "at-good" {
		t.Fatalf("token=%q err=%v", tok, err)
	}
	if ts.hits.Load() != 0 {
		t.Errorf("fresh token should not hit token endpoint, hits=%d", ts.hits.Load())
	}
}

func TestNearExpiryRefreshesAndPersists(t *testing.T) {
	m, store, _ := newFixture(t, func(w http.ResponseWriter, rt string) {
		if rt != "rt-old" {
			t.Errorf("refresh_token sent = %q", rt)
		}
		okToken(w, "at-new", "rt-rotated", 3600)
	})
	acc := accWith(oauthCred("at-old", time.Now().Add(time.Minute)))

	tok, err := m.AccessToken(context.Background(), acc)
	if err != nil || tok != "at-new" {
		t.Fatalf("token=%q err=%v", tok, err)
	}
	saved := store.last()
	if saved.AccessToken != "at-new" || saved.RefreshToken != "rt-rotated" {
		t.Errorf("persisted = %+v", saved)
	}
	if saved.AccountID != "acc-1" || saved.Kind != provider.CredOAuthRefresh {
		t.Errorf("identity fields must be preserved: %+v", saved)
	}
	if time.Until(saved.Expiry) < 50*time.Minute {
		t.Errorf("expiry should be ~1h out: %v", saved.Expiry)
	}
}

func TestMinimalPasteRefreshesImmediately(t *testing.T) {
	m, _, ts := newFixture(t, func(w http.ResponseWriter, _ string) { okToken(w, "at-new", "", 3600) })
	acc := accWith(oauthCred("", time.Time{}))

	tok, err := m.AccessToken(context.Background(), acc)
	if err != nil || tok != "at-new" {
		t.Fatalf("token=%q err=%v", tok, err)
	}
	if ts.hits.Load() != 1 {
		t.Errorf("hits=%d, want 1", ts.hits.Load())
	}
}

func TestRefreshKeepsOldRefreshTokenWhenNotRotated(t *testing.T) {
	m, store, _ := newFixture(t, func(w http.ResponseWriter, _ string) { okToken(w, "at-new", "", 3600) })
	acc := accWith(oauthCred("", time.Time{}))

	if _, err := m.AccessToken(context.Background(), acc); err != nil {
		t.Fatal(err)
	}
	if got := store.last().RefreshToken; got != "rt-old" {
		t.Errorf("refresh_token = %q, want rt-old preserved", got)
	}
}

func TestInvalidGrantMarksNeedsReauthAndShortCircuits(t *testing.T) {
	m, _, ts := newFixture(t, func(w http.ResponseWriter, _ string) {
		w.WriteHeader(http.StatusBadRequest)
		fmt.Fprint(w, `{"error":{"code":"invalid_grant"}}`)
	})
	acc := accWith(oauthCred("", time.Time{}))

	_, err := m.AccessToken(context.Background(), acc)
	if err == nil || !strings.Contains(err.Error(), "re-authorization") {
		t.Fatalf("err=%v, want ErrNeedsReauth", err)
	}
	if !m.NeedsReauth("a1") {
		t.Error("account should be marked needs-reauth")
	}

	if _, err := m.AccessToken(context.Background(), acc); err == nil {
		t.Fatal("second call should also fail")
	}
	if ts.hits.Load() != 1 {
		t.Errorf("终态失败后应短路不再打签发方, hits=%d", ts.hits.Load())
	}
}

func TestTransientFailureDoesNotMarkReauth(t *testing.T) {
	m, _, ts := newFixture(t, func(w http.ResponseWriter, _ string) {
		w.WriteHeader(http.StatusInternalServerError)
	})
	acc := accWith(oauthCred("", time.Time{}))

	if _, err := m.AccessToken(context.Background(), acc); err == nil {
		t.Fatal("expected error")
	}
	if m.NeedsReauth("a1") {
		t.Error("transient failure must not mark needs-reauth")
	}
	if _, err := m.AccessToken(context.Background(), acc); err == nil {
		t.Fatal("expected error again")
	}
	if ts.hits.Load() != 2 {
		t.Errorf("暂态失败应可重试, hits=%d", ts.hits.Load())
	}
}

func TestConcurrentRefreshSingleFlight(t *testing.T) {
	m, _, ts := newFixture(t, func(w http.ResponseWriter, _ string) {
		time.Sleep(20 * time.Millisecond)
		okToken(w, "at-new", "", 3600)
	})
	acc := accWith(oauthCred("", time.Time{}))

	var wg sync.WaitGroup
	errs := make(chan error, 20)
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			tok, err := m.AccessToken(context.Background(), acc)
			if err != nil {
				errs <- err
				return
			}
			if tok != "at-new" {
				errs <- fmt.Errorf("token=%q", tok)
			}
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Error(err)
	}
	if hits := ts.hits.Load(); hits != 1 {
		t.Errorf("并发刷新应单飞, hits=%d, want 1", hits)
	}
}

func TestInvalidateForcesRefreshDespiteFreshExpiry(t *testing.T) {
	m, _, ts := newFixture(t, func(w http.ResponseWriter, _ string) { okToken(w, "at-new", "", 3600) })
	acc := accWith(oauthCred("at-stale", time.Now().Add(time.Hour)))

	// 未作废前:expiry 新鲜,直接复用
	if tok, _ := m.AccessToken(context.Background(), acc); tok != "at-stale" {
		t.Fatalf("token=%q, want cached at-stale", tok)
	}
	// 上游 401 作废后:即便 expiry 新鲜也要刷新
	m.Invalidate("a1", "at-stale")
	tok, err := m.AccessToken(context.Background(), acc)
	if err != nil || tok != "at-new" {
		t.Fatalf("token=%q err=%v", tok, err)
	}
	if ts.hits.Load() != 1 {
		t.Errorf("hits=%d, want 1", ts.hits.Load())
	}
}

func TestWrongKindRejected(t *testing.T) {
	m, _, _ := newFixture(t, func(w http.ResponseWriter, _ string) { okToken(w, "at-new", "", 3600) })
	acc := accWith(credential.Credential{Kind: provider.CredAPIKey, APIKey: "sk-x"})
	if _, err := m.AccessToken(context.Background(), acc); err == nil {
		t.Fatal("api_key kind should be rejected by oauth manager")
	}
}
