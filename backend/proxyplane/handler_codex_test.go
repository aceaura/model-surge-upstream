package proxyplane

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/aceaura/model-surge-upstream/backend/apperr"
	"github.com/aceaura/model-surge-upstream/backend/codex"
	"github.com/aceaura/model-surge-upstream/backend/resolve"
)

// scriptedResolver 按调用次序返回不同目标:401 重试时第二次解析给新 token。
type scriptedResolver struct {
	mu      sync.Mutex
	targets []resolve.ResolvedTarget
	calls   int
}

func (s *scriptedResolver) Resolve(_ context.Context, _ string) (resolve.ResolvedTarget, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.calls >= len(s.targets) {
		return resolve.ResolvedTarget{}, apperr.New(apperr.NotFound, "no scripted target")
	}
	t := s.targets[s.calls]
	s.calls++
	return t, nil
}

func (s *scriptedResolver) List(context.Context) ([]resolve.Listing, error) { return nil, nil }

type fakeInvalidator struct {
	mu      sync.Mutex
	account string
	token   string
	calls   int
}

func (f *fakeInvalidator) Invalidate(name, accessToken string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.account, f.token = name, accessToken
	f.calls++
}

func codexTarget(baseURL, token string) resolve.ResolvedTarget {
	return resolve.ResolvedTarget{
		ModelID: "my-codex", Account: "gpt-1", ProviderID: codex.ProviderID,
		Protocol: "responses", BaseURL: baseURL, NativeModel: "gpt-5-codex",
		Headers: codex.Headers(token, "acc-id-1"),
	}
}

// 订阅端点端到端:体整形、路径映射、session 头隔离、身份头齐备。
func TestCodexForwardShapesBodyAndHeaders(t *testing.T) {
	cap := &captured{}
	up := httptest.NewServer(cap.handler(http.StatusOK, `{"ok":true}`))
	defer up.Close()

	resolver := fakeResolver{targets: map[string]resolve.ResolvedTarget{
		"my-codex": codexTarget(up.URL+"/backend-api/codex", "at-live"),
	}}
	h := NewHandler(testKey, resolver, nil)

	rec := doRequest(t, h, http.MethodPost, "/v1/responses",
		map[string]string{
			"Authorization": "Bearer " + testKey,
			"session_id":    "client-supplied-session",
		},
		`{"model":"my-codex","store":true,"stream":false,"temperature":0.7,`+
			`"reasoning":{"effort":"high"},"prompt_cache_key":"conv-1","input":"hi"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body %s", rec.Code, rec.Body.String())
	}

	body, headers, path := cap.snapshot()
	if path != "/backend-api/codex/responses" {
		t.Errorf("path = %q, want suffix mapped to /responses", path)
	}
	var got map[string]any
	if err := json.Unmarshal(body, &got); err != nil {
		t.Fatal(err)
	}
	if got["store"] != false || got["stream"] != true {
		t.Errorf("store/stream = %v/%v, want forced false/true", got["store"], got["stream"])
	}
	if _, ok := got["temperature"]; ok {
		t.Error("temperature should be stripped")
	}
	if got["model"] != "gpt-5-codex" {
		t.Errorf("model = %v", got["model"])
	}
	inc, _ := got["include"].([]any)
	if len(inc) != 1 || inc[0] != "reasoning.encrypted_content" {
		t.Errorf("include = %v", got["include"])
	}
	if s, _ := got["instructions"].(string); s == "" {
		t.Error("instructions should be injected")
	}

	if headers.Get("Authorization") != "Bearer at-live" {
		t.Errorf("Authorization = %q", headers.Get("Authorization"))
	}
	if headers.Get("chatgpt-account-id") != "acc-id-1" {
		t.Errorf("chatgpt-account-id = %q", headers.Get("chatgpt-account-id"))
	}
	if headers.Get("originator") == "" || headers.Get("version") == "" || headers.Get("OpenAI-Beta") == "" {
		t.Error("codex identity headers missing")
	}
	sid := headers.Get("session_id")
	if sid == "" || sid == "client-supplied-session" {
		t.Errorf("session_id = %q, want server-derived, not client-supplied", sid)
	}
	if want := codex.SessionID("gpt-1", "conv-1"); sid != want {
		t.Errorf("session_id = %q, want derived %q", sid, want)
	}
	if headers.Get("conversation_id") != sid {
		t.Errorf("conversation_id = %q, want mirror of session_id", headers.Get("conversation_id"))
	}
	if headers.Get("x-client-request-id") == "" {
		t.Error("x-client-request-id should be set")
	}
}

// 401 触发作废+重解析+原样重放;第二次用新 token 成功。
func TestCodexForwardRetriesOnceOn401(t *testing.T) {
	var hits atomic.Int64
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		if hits.Add(1) == 1 {
			if got := r.Header.Get("Authorization"); got != "Bearer at-stale" {
				t.Errorf("first attempt Authorization = %q", got)
			}
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = io.WriteString(w, `{"error":"token expired"}`)
			return
		}
		if got := r.Header.Get("Authorization"); got != "Bearer at-fresh" {
			t.Errorf("retry Authorization = %q", got)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"ok":true}`)
	}))
	defer up.Close()

	resolver := &scriptedResolver{targets: []resolve.ResolvedTarget{
		codexTarget(up.URL, "at-stale"),
		codexTarget(up.URL, "at-fresh"),
	}}
	inv := &fakeInvalidator{}
	h := NewHandler(testKey, resolver, nil).WithInvalidator(inv)

	rec := doRequest(t, h, http.MethodPost, "/v1/responses",
		map[string]string{"Authorization": "Bearer " + testKey},
		`{"model":"my-codex","input":"hi"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body %s", rec.Code, rec.Body.String())
	}
	if hits.Load() != 2 {
		t.Errorf("upstream hits = %d, want 2", hits.Load())
	}
	inv.mu.Lock()
	defer inv.mu.Unlock()
	if inv.calls != 1 || inv.account != "gpt-1" || inv.token != "at-stale" {
		t.Errorf("invalidate = (%d, %q, %q)", inv.calls, inv.account, inv.token)
	}
}

// 重试仍 401:原样透传,不死循环。
func TestCodexForwardRetryStill401PassesThrough(t *testing.T) {
	var hits atomic.Int64
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		hits.Add(1)
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = io.WriteString(w, `{"error":"unauthorized"}`)
	}))
	defer up.Close()

	resolver := &scriptedResolver{targets: []resolve.ResolvedTarget{
		codexTarget(up.URL, "at-1"), codexTarget(up.URL, "at-2"),
	}}
	h := NewHandler(testKey, resolver, nil).WithInvalidator(&fakeInvalidator{})

	rec := doRequest(t, h, http.MethodPost, "/v1/responses",
		map[string]string{"Authorization": "Bearer " + testKey},
		`{"model":"my-codex","input":"hi"}`)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401 passthrough", rec.Code)
	}
	if hits.Load() != 2 {
		t.Errorf("upstream hits = %d, want exactly 2 (one retry)", hits.Load())
	}
	if !strings.Contains(rec.Body.String(), "unauthorized") {
		t.Errorf("body = %s", rec.Body.String())
	}
}

// 非 codex 目标 401 不重试。
func TestNonCodex401NotRetried(t *testing.T) {
	var hits atomic.Int64
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		hits.Add(1)
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer up.Close()

	resolver := fakeResolver{targets: map[string]resolve.ResolvedTarget{
		"my-gpt": {
			ModelID: "my-gpt", Account: "openai-1", ProviderID: "openai",
			Protocol: "responses", BaseURL: up.URL, NativeModel: "gpt-5",
			Headers: map[string]string{"Authorization": "Bearer real-key"},
		},
	}}
	h := NewHandler(testKey, resolver, nil).WithInvalidator(&fakeInvalidator{})

	rec := doRequest(t, h, http.MethodPost, "/v1/responses",
		map[string]string{"Authorization": "Bearer " + testKey},
		`{"model":"my-gpt","input":"hi"}`)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d", rec.Code)
	}
	if hits.Load() != 1 {
		t.Errorf("upstream hits = %d, want 1 (no retry for non-oauth)", hits.Load())
	}
}
