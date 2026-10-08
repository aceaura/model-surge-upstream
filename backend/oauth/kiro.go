package oauth

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/aceaura/model-surge-upstream/backend/account"
	"github.com/aceaura/model-surge-upstream/backend/kiro"
)

func (m *Manager) refreshKiro(ctx context.Context, acc account.Account, f *flight) (string, error) {
	cred := acc.Credential
	if err := cred.Validate(); err != nil {
		return "", err
	}
	region := cred.KiroAuthRegion()
	endpoint := "https://prod." + region + ".auth.desktop.kiro.dev/refreshToken"
	payload := map[string]string{"refreshToken": cred.RefreshToken}
	if cred.ClientID != "" {
		endpoint = "https://oidc." + region + ".amazonaws.com/token"
		payload["grantType"] = "refresh_token"
		payload["clientId"], payload["clientSecret"] = cred.ClientID, cred.ClientSecret
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		return "", err
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(raw))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", kiro.IDEUserAgentFor(acc.Name))
	resp, err := m.client.Do(req)
	if err != nil {
		return "", fmt.Errorf("kiro: refresh request failed: %w", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	if err != nil {
		return "", fmt.Errorf("kiro: read refresh response: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		var failure struct {
			Error string `json:"error"`
		}
		_ = json.Unmarshal(body, &failure)
		if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden ||
			(resp.StatusCode == http.StatusBadRequest && (failure.Error == "invalid_grant" || failure.Error == "invalid_token" || failure.Error == "invalid_request")) {
			m.mu.Lock()
			if m.flights[acc.Name] != f {
				m.mu.Unlock()
				return "", context.Canceled
			}
			m.reauth[acc.Name] = true
			m.mu.Unlock()
			return "", ErrNeedsReauth
		}
		return "", fmt.Errorf("kiro: refresh returned HTTP %d", resp.StatusCode)
	}
	var data struct {
		AccessToken  string `json:"accessToken"`
		RefreshToken string `json:"refreshToken"`
		ProfileARN   string `json:"profileArn"`
		ExpiresIn    *int64 `json:"expiresIn"`
	}
	if err := json.Unmarshal(body, &data); err != nil {
		return "", fmt.Errorf("kiro: refresh response is not valid json")
	}
	if strings.TrimSpace(data.AccessToken) == "" {
		return "", fmt.Errorf("kiro: refresh response missing accessToken")
	}
	cred.AccessToken = data.AccessToken
	if data.RefreshToken != "" {
		cred.RefreshToken = data.RefreshToken
	}
	if data.ProfileARN != "" {
		cred.ProfileARN = data.ProfileARN
	}
	if err := cred.Validate(); err != nil {
		return "", fmt.Errorf("kiro: invalid refreshed credential: %w", err)
	}
	ttl := defaultTTL
	if data.ExpiresIn != nil {
		ttl = time.Duration(*data.ExpiresIn) * time.Second
	}
	cred.Expiry = time.Now().UTC().Add(ttl - time.Minute)
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.flights[acc.Name] != f {
		return "", context.Canceled
	}
	// Store 不回调 Manager;持锁直到落库与缓存更新结束,使 Reset 成为屏障。
	if err := m.store.UpdateCredential(ctx, acc.Name, cred); err != nil {
		return "", fmt.Errorf("kiro: persist refreshed token: %w", err)
	}
	m.kiroCredentials[acc.Name] = cred
	delete(m.invalid, acc.Name)
	return cred.AccessToken, nil
}
