package upmodels

import (
	"context"
	"net/http"
	"net/http/httptest"
	"slices"
	"sync/atomic"
	"testing"
	"time"

	"github.com/aceaura/model-surge-upstream/backend/account"
	"github.com/aceaura/model-surge-upstream/backend/apperr"
	"github.com/aceaura/model-surge-upstream/backend/credential"
	"github.com/aceaura/model-surge-upstream/backend/provider"
)

type fakeAccounts map[string]account.Account

func (f fakeAccounts) Get(_ context.Context, name string) (account.Account, error) {
	acc, ok := f[name]
	if !ok {
		return account.Account{}, apperr.New(apperr.NotFound, "account "+name+" not found")
	}
	return acc, nil
}

func acct(name, providerID, baseURL string) account.Account {
	return account.Account{
		Name:       name,
		ProviderID: providerID,
		Credential: credential.Credential{Kind: provider.CredAPIKey, APIKey: "sk-abcdefghijkl"},
		BaseURL:    baseURL,
		Headers:    map[string]string{},
		Enabled:    true,
	}
}

func TestListNotQueryable(t *testing.T) {
	// ark 未声明列举接口。
	l := New(fakeAccounts{"ark-1": acct("ark-1", "ark/api", "")}, time.Minute)
	got, err := l.List(context.Background(), "ark-1")
	if err != nil {
		t.Fatalf("missing listing api must not be an error: %v", err)
	}
	if got.Queryable {
		t.Error("Queryable should be false")
	}
	if got.Models == nil {
		t.Error("Models should be an empty slice, not null")
	}
}

func TestListAccountNotFound(t *testing.T) {
	l := New(fakeAccounts{}, time.Minute)
	if _, err := l.List(context.Background(), "ghost"); !apperr.Is(err, apperr.NotFound) {
		t.Errorf("code = %q, want not_found", apperr.CodeOf(err))
	}
}

func TestListSuccessOpenAIShape(t *testing.T) {
	var gotPath, gotAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath, gotAuth = r.URL.Path, r.Header.Get("Authorization")
		_, _ = w.Write([]byte(`{"data":[{"id":"deepseek-chat"},{"id":"deepseek-reasoner"}]}`))
	}))
	defer srv.Close()

	l := New(fakeAccounts{"ds-1": acct("ds-1", "deepseek/api", srv.URL)}, time.Minute)
	got, err := l.List(context.Background(), "ds-1")
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if !got.Queryable {
		t.Error("Queryable should be true")
	}
	if len(got.Models) != 2 {
		t.Fatalf("models = %v, want 2 entries", got.Models)
	}
	if got.Models[0].ID != "deepseek-chat" || got.Models[1].ID != "deepseek-reasoner" {
		t.Errorf("models should be sorted by id, got %v", got.Models)
	}
	if gotPath != "/models" {
		t.Errorf("path = %q, should come from the provider spec", gotPath)
	}
	if gotAuth != "Bearer sk-abcdefghijkl" {
		t.Errorf("auth header = %q", gotAuth)
	}
}

func TestListAnthropicKeyAuth(t *testing.T) {
	var gotKey, gotVersion string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotKey = r.Header.Get("x-api-key")
		gotVersion = r.Header.Get("anthropic-version")
		_, _ = w.Write([]byte(`{"data":[{"id":"kimi-k2-turbo"}]}`))
	}))
	defer srv.Close()

	l := New(fakeAccounts{"kimi-1": acct("kimi-1", "kimi/coding", srv.URL)}, time.Minute)
	if _, err := l.List(context.Background(), "kimi-1"); err != nil {
		t.Fatalf("list: %v", err)
	}
	if gotKey != "sk-abcdefghijkl" {
		t.Errorf("x-api-key = %q, anthropic_key providers must not use bearer", gotKey)
	}
	if gotVersion == "" {
		t.Error("anthropic-version header should be set")
	}
}

func TestListUpstreamFailure(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer srv.Close()

	l := New(fakeAccounts{"ds-1": acct("ds-1", "deepseek/api", srv.URL)}, time.Minute)
	_, err := l.List(context.Background(), "ds-1")
	if !apperr.Is(err, apperr.UpstreamUnavailable) {
		t.Errorf("code = %q, want upstream_unavailable", apperr.CodeOf(err))
	}
}

func TestListUnreachableUpstream(t *testing.T) {
	l := New(fakeAccounts{"ds-1": acct("ds-1", "deepseek/api", "http://127.0.0.1:1")}, time.Minute)
	if _, err := l.List(context.Background(), "ds-1"); !apperr.Is(err, apperr.UpstreamUnavailable) {
		t.Errorf("code = %q, want upstream_unavailable", apperr.CodeOf(err))
	}
}

func TestListCachesWithinTTL(t *testing.T) {
	var hits int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		atomic.AddInt64(&hits, 1)
		_, _ = w.Write([]byte(`{"data":[{"id":"m"}]}`))
	}))
	defer srv.Close()

	l := New(fakeAccounts{"ds-1": acct("ds-1", "deepseek/api", srv.URL)}, time.Minute)
	for range 3 {
		if _, err := l.List(context.Background(), "ds-1"); err != nil {
			t.Fatal(err)
		}
	}
	if got := atomic.LoadInt64(&hits); got != 1 {
		t.Errorf("upstream hits = %d, want 1 within TTL", got)
	}
}

func TestForget(t *testing.T) {
	var hits int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		atomic.AddInt64(&hits, 1)
		_, _ = w.Write([]byte(`{"data":[{"id":"m"}]}`))
	}))
	defer srv.Close()

	l := New(fakeAccounts{"ds-1": acct("ds-1", "deepseek/api", srv.URL)}, time.Minute)
	if _, err := l.List(context.Background(), "ds-1"); err != nil {
		t.Fatal(err)
	}
	l.Forget("ds-1")
	if _, err := l.List(context.Background(), "ds-1"); err != nil {
		t.Fatal(err)
	}
	if got := atomic.LoadInt64(&hits); got != 2 {
		t.Errorf("upstream hits = %d, Forget should drop the cache", got)
	}
}

func TestParseEntriesShapes(t *testing.T) {
	cases := map[string]struct {
		body string
		want []Entry
	}{
		"openai data": {
			`{"data":[{"id":"gpt-4"},{"id":"gpt-3"}]}`,
			[]Entry{{ID: "gpt-3"}, {ID: "gpt-4"}},
		},
		"gemini models with display name": {
			`{"models":[{"name":"models/gemini-pro","displayName":"Gemini Pro"}]}`,
			[]Entry{{ID: "models/gemini-pro", DisplayName: "Gemini Pro"}},
		},
		"duplicates collapse": {
			`{"data":[{"id":"a"},{"id":"a"}]}`,
			[]Entry{{ID: "a"}},
		},
		"entries without id are skipped": {
			`{"data":[{"object":"model"},{"id":"ok"}]}`,
			[]Entry{{ID: "ok"}},
		},
		"unknown envelope": {`{"whatever":[{"id":"a"}]}`, []Entry{}},
		"not json":         {`nope`, []Entry{}},
		"not a list":       {`{"data":"oops"}`, []Entry{}},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			got := parseEntries([]byte(tc.body))
			if len(got) != len(tc.want) {
				t.Fatalf("entries = %v, want %v", got, tc.want)
			}
			for i := range got {
				if got[i].ID != tc.want[i].ID || got[i].DisplayName != tc.want[i].DisplayName {
					t.Errorf("entry %d = %+v, want %+v", i, got[i], tc.want[i])
				}
			}
		})
	}
}

// TestParseDeclaredEfforts 锁定 supported_reasoning_levels 双形态解析:
// codex 订阅端点是 [{effort,description}] 对象数组,OpenAI 兼容网关是
// 字符串数组;值按上游原样保留(声明序,词表外私有档不丢),未声明回 nil。
func TestParseDeclaredEfforts(t *testing.T) {
	cases := map[string]struct {
		body string
		want []string
	}{
		"对象数组形态(codex)": {
			`{"data":[{"slug":"gpt-6.1-sol","supported_reasoning_levels":[{"effort":"high","description":"x"},{"effort":"low"}]}]}`,
			[]string{"high", "low"},
		},
		"字符串数组形态(网关)": {
			`{"data":[{"id":"o4-mini","supported_reasoning_levels":["medium","low","high"]}]}`,
			[]string{"medium", "low", "high"},
		},
		"原值保留(词表外不丢,去空白)": {
			`{"data":[{"id":"m","supported_reasoning_levels":["ultra","extra-high"," High "]}]}`,
			[]string{"ultra", "extra-high", "High"},
		},
		"未声明回 nil": {
			`{"data":[{"id":"m"}]}`,
			nil,
		},
		"空数组回 nil": {
			`{"data":[{"id":"m","supported_reasoning_levels":[]}]}`,
			nil,
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			got := parseEntries([]byte(tc.body))
			if len(got) != 1 {
				t.Fatalf("entries = %v, want 1 entry", got)
			}
			if !slices.Equal(got[0].Efforts, tc.want) {
				t.Errorf("efforts = %v, want %v", got[0].Efforts, tc.want)
			}
		})
	}
}

// TestDeclaredEfforts 锁定按原生模型名查声明:命中返回声明;未声明/模型不在
// 清单/上游失败都回 nil(无声明即不支持,不是错误)。
func TestDeclaredEfforts(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"data":[` +
			`{"id":"gpt-6.1-sol","supported_reasoning_levels":[{"effort":"low"},{"effort":"high"}]},` +
			`{"id":"plain-model"}]}`))
	}))
	defer srv.Close()

	l := New(fakeAccounts{"oa-1": acct("oa-1", "openai/api", srv.URL)}, time.Minute)
	ctx := context.Background()
	if got := l.DeclaredEfforts(ctx, "oa-1", "gpt-6.1-sol"); !slices.Equal(got, []string{"low", "high"}) {
		t.Errorf("declared = %v, want [low high]", got)
	}
	if got := l.DeclaredEfforts(ctx, "oa-1", "plain-model"); got != nil {
		t.Errorf("模型未声明 = %v, want nil", got)
	}
	if got := l.DeclaredEfforts(ctx, "oa-1", "ghost-model"); got != nil {
		t.Errorf("模型不在清单 = %v, want nil", got)
	}
	if got := l.DeclaredEfforts(ctx, "ghost-account", "gpt-6.1-sol"); got != nil {
		t.Errorf("账号不存在 = %v, want nil", got)
	}
}

// TestDeclaredEffortsCachesWithList 声明查询与清单共用同一份 TTL 缓存:
// 先 List 后 DeclaredEfforts 不再打上游。
func TestDeclaredEffortsCachesWithList(t *testing.T) {
	var hits int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		atomic.AddInt64(&hits, 1)
		_, _ = w.Write([]byte(`{"data":[{"id":"m","supported_reasoning_levels":["low"]}]}`))
	}))
	defer srv.Close()

	l := New(fakeAccounts{"oa-1": acct("oa-1", "openai/api", srv.URL)}, time.Minute)
	ctx := context.Background()
	if _, err := l.List(ctx, "oa-1"); err != nil {
		t.Fatal(err)
	}
	if got := l.DeclaredEfforts(ctx, "oa-1", "m"); !slices.Equal(got, []string{"low"}) {
		t.Errorf("declared = %v, want [low]", got)
	}
	if got := atomic.LoadInt64(&hits); got != 1 {
		t.Errorf("upstream hits = %d, want 1(共用清单缓存)", got)
	}
}

// TestHeaderSourceOverridesStaticAuth 装配头来源后用其构造上游头
// (oauth 账号的活体 token 路径),而不是静态空 Bearer。
func TestHeaderSourceOverridesStaticAuth(t *testing.T) {
	var gotAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		_, _ = w.Write([]byte(`{"data":[{"id":"m"}]}`))
	}))
	defer srv.Close()

	l := New(fakeAccounts{"cx-1": acct("cx-1", "openai/codex", srv.URL)}, time.Minute).
		WithHeaderSource(headerSourceFunc(func(context.Context, provider.Spec, account.Account) (map[string]string, error) {
			return map[string]string{"Authorization": "Bearer live-token"}, nil
		}))
	if _, err := l.List(context.Background(), "cx-1"); err != nil {
		t.Fatal(err)
	}
	if gotAuth != "Bearer live-token" {
		t.Errorf("auth = %q, want 头来源的活体 token", gotAuth)
	}
}

type headerSourceFunc func(context.Context, provider.Spec, account.Account) (map[string]string, error)

func (f headerSourceFunc) HeadersFor(ctx context.Context, spec provider.Spec, acc account.Account) (map[string]string, error) {
	return f(ctx, spec, acc)
}

// TestCodexModelsURLCarriesClientVersion codex 清单端点必须带 client_version
// 协商参数,缺了上游恒 400(sub2api 实测);Accept 恒为 JSON(凭据形态头里
// 的 SSE Accept 不适用清单 GET)。
func TestCodexModelsURLCarriesClientVersion(t *testing.T) {
	var gotURL, gotAccept string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotURL = r.URL.String()
		gotAccept = r.Header.Get("Accept")
		_, _ = w.Write([]byte(`{"models":[{"slug":"gpt-6.1-sol","supported_reasoning_levels":[{"effort":"low"}]}]}`))
	}))
	defer srv.Close()

	l := New(fakeAccounts{"cx-1": acct("cx-1", "openai/codex", srv.URL)}, time.Minute)
	got, err := l.List(context.Background(), "cx-1")
	if err != nil {
		t.Fatal(err)
	}
	if gotURL != "/models?client_version=0.160.0" {
		t.Errorf("url = %q, want client_version 协商参数", gotURL)
	}
	if gotAccept != "application/json" {
		t.Errorf("Accept = %q, want application/json", gotAccept)
	}
	if len(got.Models) != 1 || got.Models[0].ID != "gpt-6.1-sol" {
		t.Fatalf("models = %+v, want slug 作为条目 ID", got.Models)
	}
	if !slices.Equal(got.Models[0].Efforts, []string{"low"}) {
		t.Errorf("efforts = %v, want [low]", got.Models[0].Efforts)
	}
}

func TestConcurrentLists(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"data":[{"id":"m"}]}`))
	}))
	defer srv.Close()

	l := New(fakeAccounts{"ds-1": acct("ds-1", "deepseek/api", srv.URL)}, time.Minute)
	done := make(chan struct{})
	for range 8 {
		go func() {
			defer func() { done <- struct{}{} }()
			if _, err := l.List(context.Background(), "ds-1"); err != nil {
				t.Errorf("list: %v", err)
			}
		}()
	}
	for range 8 {
		<-done
	}
}
