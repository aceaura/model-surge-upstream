// Package oauth 维护 OAuth 刷新型凭据的 access_token 生命周期:
// 距过期不足刷新窗(或 401 被作废旧 token)时,用 refresh_token 向签发方
// 续期,产物经 Store 落库。单账号并发刷新单飞去重;终态失败
// (invalid_grant/401)记为需重新授权,之后短路不再打签发方。
//
// 常量口径取三家生产实现(sub2api/new-api/cc-switch)与 codex CLI 官方
// 实现(codex-rs)的交集:公开 client_id、form 编码的 refresh_token grant
// 带 scope、5 分钟刷新窗;授权侧请求带 originator 与 UA,与 codex-rs
// default_client.rs 一致。
package oauth

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/aceaura/model-surge-upstream/backend/account"
	"github.com/aceaura/model-surge-upstream/backend/credential"
	"github.com/aceaura/model-surge-upstream/backend/provider"
)

const (
	// TokenURL 是 OpenAI 授权服务的 token 端点。
	TokenURL = "https://auth.openai.com/oauth/token"
	// ClientID 是 codex CLI 的公开 OAuth client(订阅登录态共用一个 client)。
	ClientID = "app_EMoamEEZ73f0CkXaXp7hrann"
	// Scope 续期携带的 scope;不带 offline_access(codex-rs 同款)。
	Scope = "openid profile email"
	// Originator 标识 codex CLI 来源,授权侧与 backend-api 请求都要带。
	Originator = "codex_cli_rs"
	// ClientVersion 伪装的 codex CLI 版本;backend-api 按 originator+version
	// 路由模型队列,过低新版本模型直接 404(sub2api 实测下限 0.144.0;
	// 2026-10-03 实测 0.160.0 起才下发 gpt-6.1-sol,钉 0.160.0)。
	ClientVersion = "0.160.0"

	refreshWindow = 5 * time.Minute
	// kiroRefreshWindow 对齐 KiroaaS TOKEN_REFRESH_THRESHOLD=600s。
	kiroRefreshWindow = 10 * time.Minute
	defaultTTL        = time.Hour
)

// ErrNeedsReauth 是终态授权失败:refresh_token 过期/被复用/被吊销,
// 续期无意义,需用户重新粘贴登录态。
var ErrNeedsReauth = errors.New("oauth: refresh token rejected, re-authorization required")

// Store 持久化续期产物。由 account.Repo 实现;凭据之外字段不动。
type Store interface {
	UpdateCredential(ctx context.Context, name string, cred credential.Credential) error
}

type flight struct {
	done  chan struct{}
	token string
	err   error
}

type Manager struct {
	store    Store
	client   *http.Client
	tokenURL string
	clientID string

	mu      sync.Mutex
	flights map[string]*flight
	// invalid 记录被 401 作废的 access_token:凭据里的 expiry 还没到时,
	// 靠它强制下次取 token 走刷新而不是复用坏 token。
	invalid map[string]string
	// reauth 记录终态失败的账号,短路后续续期尝试。
	reauth map[string]bool
}

func NewManager(store Store) *Manager {
	return newManager(store, http.DefaultClient, TokenURL, ClientID)
}

func newManager(store Store, client *http.Client, tokenURL, clientID string) *Manager {
	return &Manager{
		store:    store,
		client:   client,
		tokenURL: tokenURL,
		clientID: clientID,
		flights:  map[string]*flight{},
		invalid:  map[string]string{},
		reauth:   map[string]bool{},
	}
}

// AccessToken 返回 acc 当前可用的 access_token。凭据不是 oauth_refresh
// 形态属于接线错误,直接报错。
func (m *Manager) AccessToken(ctx context.Context, acc account.Account) (string, error) {
	cred := acc.Credential
	if cred.Kind != provider.CredOAuthRefresh && cred.Kind != provider.CredKiroRefresh {
		return "", fmt.Errorf("oauth: account %q credential kind %q is not refreshable", acc.Name, cred.Kind)
	}

	m.mu.Lock()
	if m.reauth[acc.Name] {
		m.mu.Unlock()
		return "", ErrNeedsReauth
	}
	window := refreshWindow
	if cred.Kind == provider.CredKiroRefresh {
		window = kiroRefreshWindow
	}
	fresh := cred.AccessToken != "" &&
		cred.AccessToken != m.invalid[acc.Name] &&
		time.Until(cred.Expiry) > window
	if fresh {
		m.mu.Unlock()
		return cred.AccessToken, nil
	}
	if f, ok := m.flights[acc.Name]; ok {
		m.mu.Unlock()
		select {
		case <-f.done:
			return f.token, f.err
		case <-ctx.Done():
			return "", ctx.Err()
		}
	}
	f := &flight{done: make(chan struct{})}
	m.flights[acc.Name] = f
	m.mu.Unlock()

	f.token, f.err = m.refresh(ctx, acc)

	m.mu.Lock()
	delete(m.flights, acc.Name)
	m.mu.Unlock()
	close(f.done)
	return f.token, f.err
}

// Invalidate 上游 401 时调用:作废该凭据当前的 access_token,
// 下次 AccessToken 强制续期。
func (m *Manager) Invalidate(name string, accessToken string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if accessToken != "" {
		m.invalid[name] = accessToken
	}
}

// NeedsReauth 供管理面展示:账号是否处于终态授权失败、需重新粘贴登录态。
func (m *Manager) NeedsReauth(name string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.reauth[name]
}

// Reset 用户重新粘贴登录态后调用:清掉终态标记与作废记录,
// 下次取 token 用新凭据正常续期。
func (m *Manager) Reset(name string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.reauth, name)
	delete(m.invalid, name)
}

type tokenResponse struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	ExpiresIn    int64  `json:"expires_in"`
}

func (m *Manager) refresh(ctx context.Context, acc account.Account) (string, error) {
	if acc.Credential.Kind == provider.CredKiroRefresh {
		return m.refreshKiro(ctx, acc)
	}
	// form 编码:sub2api/new-api/cc-switch 三家生产实现一致,JSON 编码未见实证。
	form := url.Values{
		"grant_type":    {"refresh_token"},
		"refresh_token": {acc.Credential.RefreshToken},
		"client_id":     {m.clientID},
		"scope":         {Scope},
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, m.tokenURL, strings.NewReader(form.Encode()))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	// 授权侧身份头与 codex-rs default_client.rs 一致:只带 originator 与 UA。
	req.Header.Set("originator", Originator)
	req.Header.Set("User-Agent", Originator+"/"+ClientVersion)

	resp, err := m.client.Do(req)
	if err != nil {
		return "", fmt.Errorf("oauth: refresh request failed: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode == http.StatusBadRequest || resp.StatusCode == http.StatusUnauthorized {
		m.mu.Lock()
		m.reauth[acc.Name] = true
		m.mu.Unlock()
		return "", ErrNeedsReauth
	}
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("oauth: refresh returned %s", resp.Status)
	}

	var tr tokenResponse
	if err := json.NewDecoder(resp.Body).Decode(&tr); err != nil {
		return "", fmt.Errorf("oauth: refresh response is not valid json: %w", err)
	}
	if strings.TrimSpace(tr.AccessToken) == "" {
		return "", fmt.Errorf("oauth: refresh response missing access_token")
	}

	cred := acc.Credential
	cred.AccessToken = tr.AccessToken
	// 签发方轮换 refresh_token 时跟随轮换;没下发就保留旧的。
	if strings.TrimSpace(tr.RefreshToken) != "" {
		cred.RefreshToken = tr.RefreshToken
	}
	ttl := defaultTTL
	if tr.ExpiresIn > 0 {
		ttl = time.Duration(tr.ExpiresIn) * time.Second
	}
	cred.Expiry = time.Now().UTC().Add(ttl)

	if err := m.store.UpdateCredential(ctx, acc.Name, cred); err != nil {
		return "", fmt.Errorf("oauth: persist refreshed token: %w", err)
	}

	m.mu.Lock()
	delete(m.invalid, acc.Name)
	m.mu.Unlock()
	return cred.AccessToken, nil
}
