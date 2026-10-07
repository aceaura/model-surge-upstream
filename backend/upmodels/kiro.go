package upmodels

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/aceaura/model-surge-upstream/backend/account"
	"github.com/aceaura/model-surge-upstream/backend/apperr"
	"github.com/aceaura/model-surge-upstream/backend/kiro"
	"github.com/aceaura/model-surge-upstream/backend/provider"
)

const kiroMaxPages = 100

// kiroMaxAttempts 对齐 KiroaaS MAX_RETRIES=3:列表请求对网络错误与
// 429/5xx 重试,退避 1s/2s(BASE_RETRY_DELAY 指数翻倍)。
const kiroMaxAttempts = 3

// kiroFallbackModels 对齐 config.py FALLBACK_MODELS:列表拉取失败时回退的
// 静态已知模型表,保证基础功能可用(部分模型可能不在当前套餐内)。
// config.py:276 FALLBACK_MODELS 含 auto,经 model_resolver 隐藏 auto、
// 补别名 auto-kiro——回退列表与动态路径(下方 190 行改名)一样露出
// auto-kiro。
var kiroFallbackModels = []string{
	"auto-kiro",
	"claude-sonnet-4", "claude-sonnet-4.5", "claude-sonnet-4.6",
	"claude-haiku-4.5",
	"claude-opus-4.5", "claude-opus-4.6", "claude-opus-4.7", "claude-opus-4.8", "claude-opus-5",
	"claude-sonnet-5",
	"gpt-5.5", "gpt-5.6-sol", "gpt-5.6-terra", "gpt-5.6-luna",
	"deepseek-3.2", "glm-5", "minimax-m2.1", "minimax-m2.5", "qwen3-coder-next",
}

func (l *Lister) kiroFallbackReport(account string) Report {
	l.mu.RLock()
	cached, ok := l.cached[account]
	l.mu.RUnlock()
	if ok {
		return cached.report
	}
	out := make([]Entry, 0, len(kiroFallbackModels))
	for _, id := range kiroFallbackModels {
		out = append(out, Entry{ID: id})
	}
	return Report{Account: account, Queryable: true, Models: out, At: time.Now().UTC()}
}

// kiroRetryable 判定列表请求是否值得重试:网络错误、429、5xx。
func kiroRetryable(err error, status int) bool {
	if err != nil {
		var verifyErr *tls.CertificateVerificationError
		var authorityErr x509.UnknownAuthorityError
		var hostnameErr x509.HostnameError
		var invalidErr x509.CertificateInvalidError
		var recordErr tls.RecordHeaderError
		if errors.As(err, &verifyErr) || errors.As(err, &authorityErr) ||
			errors.As(err, &hostnameErr) || errors.As(err, &invalidErr) || errors.As(err, &recordErr) {
			return false
		}
		message := strings.ToLower(err.Error())
		if strings.Contains(message, "ssl") || strings.Contains(message, "tls") || strings.Contains(message, "certificate") {
			return false
		}
		var netErr net.Error
		if errors.As(err, &netErr) {
			return true
		}
		return errors.Is(err, io.ErrUnexpectedEOF) || errors.Is(err, io.EOF)
	}
	return status == http.StatusTooManyRequests || status >= 500
}

var kiroRuntimeHost = regexp.MustCompile(`^https://runtime\.([a-z0-9-]+)\.kiro\.dev/?$`)

// Custom endpoints must stay intact; the SSO region is not necessarily the API region.
func kiroControlBase(spec provider.Spec, acc account.Account) string {
	base := acc.EffectiveBaseURL(spec)
	if acc.BaseURL == "" || strings.TrimRight(base, "/") == kiro.DefaultBaseURL {
		region := strings.TrimSpace(acc.Credential.APIRegion)
		if region == "" {
			parts := strings.Split(acc.Credential.ProfileARN, ":")
			if len(parts) >= 6 && parts[0] == "arn" {
				region = parts[3]
			}
		}
		if region != "" {
			base = "https://runtime." + region + ".kiro.dev"
		}
	}
	if match := kiroRuntimeHost.FindStringSubmatch(base); match != nil {
		return "https://q." + match[1] + ".amazonaws.com"
	}
	return strings.TrimRight(base, "/")
}

func (l *Lister) fetchKiro(ctx context.Context, spec provider.Spec, acc account.Account) (Report, error) {
	if l.headerSource == nil {
		return Report{}, apperr.New(apperr.UpstreamUnavailable, "Kiro models require a token header source")
	}
	var headers map[string]string
	var endpoint *url.URL
	query := url.Values{}
	loadHeaders := func() error {
		var err error
		headers, err = l.headerSource.HeadersFor(ctx, spec, acc)
		if err != nil {
			return err
		}
		acc, err = l.accounts.Get(ctx, acc.Name)
		if err != nil {
			return err
		}
		auth := ""
		for key, value := range headers {
			if strings.EqualFold(key, "Authorization") {
				auth = value
			}
		}
		if !strings.HasPrefix(auth, "Bearer ") || strings.TrimSpace(strings.TrimPrefix(auth, "Bearer ")) == "" {
			return apperr.New(apperr.UpstreamUnavailable, "Kiro models require a nonempty access token")
		}
		endpoint, err = url.Parse(kiroControlBase(spec, acc) + "/ListAvailableModels")
		if err != nil {
			return apperr.Wrap(apperr.UpstreamUnavailable, "build Kiro models URL", err)
		}
		query.Set("origin", "AI_EDITOR")
		query.Del("profileArn")
		if acc.Credential.ClientID == "" && acc.Credential.ProfileARN != "" {
			query.Set("profileArn", acc.Credential.ProfileARN)
		}
		return nil
	}
	if err := loadHeaders(); err != nil {
		return Report{}, err
	}
	refreshed := false
	out := []Entry{}
	gotValid := false
	seenModels, seenTokens := map[string]bool{}, map[string]bool{}
	for page := 0; page < kiroMaxPages; page++ {
		endpoint.RawQuery = query.Encode()
		var body []byte
		for attempt := 0; attempt < kiroMaxAttempts; attempt++ {
			if attempt > 0 {
				timer := time.NewTimer(time.Duration(1<<uint(attempt-1)) * time.Second)
				select {
				case <-ctx.Done():
					timer.Stop()
					return Report{}, ctx.Err()
				case <-timer.C:
				}
			}
			req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint.String(), nil)
			if err != nil {
				return Report{}, apperr.Wrap(apperr.UpstreamUnavailable, "build Kiro models request", err)
			}
			for key, value := range headers {
				if strings.EqualFold(key, kiro.HeaderProvider) || strings.EqualFold(key, kiro.HeaderProfileARN) {
					continue
				}
				req.Header.Set(key, value)
			}
			req.Header.Set("Accept", "application/json")
			resp, err := l.client.Do(req)
			if err != nil {
				if !kiroRetryable(err, 0) || attempt == kiroMaxAttempts-1 {
					return l.kiroFallbackReport(acc.Name), nil
				}
				continue
			}
			data, readErr := io.ReadAll(io.LimitReader(resp.Body, bodyLimit+1))
			resp.Body.Close()
			if readErr != nil {
				if attempt == kiroMaxAttempts-1 {
					return l.kiroFallbackReport(acc.Name), nil
				}
				continue
			}
			if resp.StatusCode < 200 || resp.StatusCode >= 300 {
				if resp.StatusCode == http.StatusForbidden && !refreshed && l.invalidator != nil && attempt < kiroMaxAttempts-1 {
					refreshed = true
					l.invalidator.Invalidate(acc.Name, strings.TrimPrefix(req.Header.Get("Authorization"), "Bearer "))
					if err := loadHeaders(); err != nil {
						return l.kiroFallbackReport(acc.Name), nil
					}
					endpoint.RawQuery = query.Encode()
					continue
				}
				if kiroRetryable(nil, resp.StatusCode) && attempt < kiroMaxAttempts-1 {
					continue
				}
				// account_manager.py:534-538: 拉取失败回退静态已知模型表。
				return l.kiroFallbackReport(acc.Name), nil
			}
			if len(data) > bodyLimit {
				return l.kiroFallbackReport(acc.Name), nil
			}
			body = data
			break
		}
		if body == nil {
			return l.kiroFallbackReport(acc.Name), nil
		}
		var payload struct {
			Models    json.RawMessage `json:"models"`
			NextToken string          `json:"nextToken"`
		}
		if err := json.Unmarshal(body, &payload); err != nil {
			return l.kiroFallbackReport(acc.Name), nil
		}
		var models []map[string]any
		if len(payload.Models) == 0 || string(payload.Models) == "null" {
			return l.kiroFallbackReport(acc.Name), nil
		}
		if err := json.Unmarshal(payload.Models, &models); err != nil {
			return l.kiroFallbackReport(acc.Name), nil
		}
		for _, model := range models {
			if _, has := model["modelId"]; !has {
				return l.kiroFallbackReport(acc.Name), nil
			}
		}
		gotValid = true
		for _, model := range parseEntries(body) {
			// config.py HIDDEN_FROM_LIST=["auto"] + model_resolver.py:394-395:
			// auto 自身不入列表,但其别名 auto-kiro 出现在列表中。
			if model.ID == "auto" {
				model.ID = "auto-kiro"
			}
			if !seenModels[model.ID] {
				seenModels[model.ID] = true
				out = append(out, model)
			}
		}
		if payload.NextToken == "" {
			break
		}
		if seenTokens[payload.NextToken] {
			break // 重复 token:翻页到头或卡死,用已收集的结果
		}
		seenTokens[payload.NextToken] = true
		query.Set("nextToken", payload.NextToken)
	}
	if !seenModels["auto-kiro"] {
		out = append(out, Entry{ID: "auto-kiro"})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	if !gotValid {
		return l.kiroFallbackReport(acc.Name), nil
	}
	return Report{Account: acc.Name, Queryable: true, Models: out, At: time.Now().UTC()}, nil
}
