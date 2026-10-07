package quota

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/aceaura/model-surge-upstream/backend/apperr"
	"github.com/aceaura/model-surge-upstream/backend/provider"
)

const opencodeGoBody = `{"usage":{` +
	`"rolling":{"status":"ok","percent":0,"resetsAt":"2026-10-07T12:00:00.123Z"},` +
	`"weekly":{"status":"rate-limited","percent":100,"resetsAt":"2026-10-14T20:00:00+08:00"},` +
	`"monthly":{"status":"ok","percent":"37.5","resetsAt":"2026-11-01T00:00:00Z"}}}`

func TestOpenCodeGoHTTP(t *testing.T) {
	for _, custom := range []bool{false, true} {
		t.Run(fmt.Sprintf("custom-base-%t", custom), func(t *testing.T) {
			wantPath := "/zen/go/v1/usage"
			if custom {
				wantPath = "/custom/go/v1/usage"
			}
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != http.MethodGet || r.URL.Path != wantPath || r.URL.RawQuery != "" {
					t.Errorf("request = %s %s, want GET %s", r.Method, r.URL, wantPath)
				}
				if !custom && r.Header.Get("X-Original-Host") != "opencode.ai" {
					t.Errorf("default host = %q", r.Header.Get("X-Original-Host"))
				}
				if auth := r.Header.Get("Authorization"); auth != "Bearer sk-abcdefghijkl" {
					t.Errorf("auth = %q", auth)
				}
				if r.Header.Get("Accept") != "application/json" {
					t.Errorf("accept = %q", r.Header.Get("Accept"))
				}
				if r.Header.Get("User-Agent") != "ModelSurgeUpstream/1.0" || r.Header.Get("x-opencode-session") != "" {
					t.Errorf("usage request headers = %v", r.Header)
				}
				if r.Header.Get("X-Api-Key") != "" {
					t.Error("usage endpoint must use Bearer, not x-api-key")
				}
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(opencodeGoBody))
			}))
			defer srv.Close()
			acc := acct("go-1", "opencode.global.subscribe.go", "")
			q := builtinQuota(srv.URL, acc)
			if custom {
				acc.BaseURL = srv.URL + "/custom/go///"
				q = New(fakeAccounts{acc.Name: acc}, time.Minute)
				q.SetClient(srv.Client())
			}
			report, err := q.Query(context.Background(), acc.Name)
			if err != nil {
				t.Fatal(err)
			}
			if !report.Queryable || report.Account != acc.Name || len(report.Meters) != 3 {
				t.Fatalf("report = %+v, want three usage windows", report)
			}
			for i, want := range []struct {
				label string
				used  float64
				reset provider.ResetRule
				at    string
			}{
				{"5小时", 0, provider.ResetRolling, "2026-10-07T12:00:00.123Z"},
				{"7天", 100, provider.ResetRolling, "2026-10-14T12:00:00Z"},
				{"本月", 37.5, provider.ResetMonthly, "2026-11-01T00:00:00Z"},
			} {
				m := report.Meters[i]
				if m.Kind != provider.MeterUsage || m.Unit != provider.UnitPercent ||
					m.Label != want.label || m.Reset != want.reset || m.Used == nil ||
					*m.Used != want.used || m.Total == nil || *m.Total != 100 {
					t.Errorf("meter[%d] = %+v, want %+v", i, m, want)
				}
				if m.ResetAt == nil || m.ResetAt.Format(time.RFC3339Nano) != want.at {
					t.Errorf("meter[%d] reset_at = %v, want %s", i, m.ResetAt, want.at)
				}
				if m.Currency != "" || m.Remaining != nil {
					t.Errorf("meter[%d] must not invent currency or a balance: %+v", i, m)
				}
			}
		})
	}
}

func TestOpenCodeGoPartialWindows(t *testing.T) {
	for _, tt := range []struct {
		name string
		body string
		used float64
	}{
		{"missing", `{"usage":{"weekly":{"status":"ok","percent":12}}}`, 12},
		{"invalid", `{"usage":{"rolling":{"percent":"broken"},"weekly":{"percent":12,"resetsAt":"invalid"},"monthly":{"percent":null}}}`, 12},
		{"no-percent", `{"usage":{"rolling":{"status":"rate-limited"},"weekly":{"percent":0},"monthly":{}}}`, 0},
		{"wrong-shape", `{"usage":{"rolling":[],"weekly":{"percent":12,"resetsAt":123},"monthly":false}}`, 12},
	} {
		t.Run(tt.name, func(t *testing.T) {
			meters, err := opencodeGoUsageMeters([]byte(tt.body))
			if err != nil {
				t.Fatal(err)
			}
			if len(meters) != 1 || meters[0].Label != "7天" || meters[0].Used == nil || *meters[0].Used != tt.used {
				t.Fatalf("meters = %+v, missing/invalid windows must not become zero", meters)
			}
			if meters[0].ResetAt != nil {
				t.Errorf("reset_at = %v, missing/invalid timestamp must stay absent", meters[0].ResetAt)
			}
		})
	}
}

func TestOpenCodeGoInvalidPercent(t *testing.T) {
	for _, percent := range []string{
		`null`, `true`, `[]`, `{}`, `""`, `"broken"`, `"12invalid"`,
		`-1`, `"-0.5"`, `"NaN"`, `"Inf"`, `"-Inf"`, `"1e309"`, `101`, `"100.1"`,
	} {
		t.Run(percent, func(t *testing.T) {
			body := `{"usage":{"rolling":{"percent":` + percent + `},"weekly":{"percent":25}}}`
			meters, err := opencodeGoUsageMeters([]byte(body))
			if err != nil || len(meters) != 1 || meters[0].Label != "7天" {
				t.Fatalf("meters = %+v, err = %v, invalid window must be skipped, not clamped", meters, err)
			}
		})
	}
}

func TestOpenCodeGoFailures(t *testing.T) {
	for _, tt := range []struct {
		name   string
		status int
		body   string
	}{
		{"401", 401, `{"error":{"type":"AuthError"}}`},
		{"403", 403, `{"error":{"type":"EntitlementError"}}`},
		{"500", 500, opencodeGoBody},
		{"bad-json", 200, `{"usage":`},
		{"oversized", 200, `{"padding":"` + strings.Repeat("x", bodyLimit) + `","usage":{"rolling":{"percent":1}}}`},
		{"missing-usage", 200, `{}`},
		{"null", 200, `null`},
		{"wrong-shape", 200, `{"usage":[]}`},
		{"empty-windows", 200, `{"usage":{}}`},
		{"all-invalid", 200, `{"usage":{"rolling":{"percent":-1},"weekly":{"percent":"NaN"},"monthly":{"percent":101}}}`},
		{"old-shape", 200, `{"rollingUsage":{"usagePercent":15,"resetInSec":18000}}`},
	} {
		t.Run(tt.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				// Rate limit headers cannot rescue a response without valid usage windows.
				w.Header().Set("X-Ratelimit-Limit-Requests", "100")
				w.WriteHeader(tt.status)
				_, _ = w.Write([]byte(tt.body))
			}))
			defer srv.Close()
			acc := acct("go-1", "opencode.global.subscribe.go", srv.URL)
			q := New(fakeAccounts{acc.Name: acc}, time.Minute)
			_, err := q.Query(context.Background(), acc.Name)
			if !apperr.Is(err, apperr.QuotaUnavailable) {
				t.Fatalf("err = %v, want QuotaUnavailable", err)
			}
			if tt.status != http.StatusOK && !strings.Contains(err.Error(), fmt.Sprint(tt.status)) {
				t.Errorf("err = %v, want upstream status %d", err, tt.status)
			}
			if _, ok := q.Cached(acc.Name); ok {
				t.Error("failed usage query must not cache an empty success")
			}
		})
	}
}

func TestOpenCodeGoRateLimitHeaders(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("X-Ratelimit-Limit-Requests", "1000")
		w.Header().Set("X-Ratelimit-Remaining-Requests", "990")
		w.Header().Set("X-Ratelimit-Reset-Requests", "2026-10-07T12:00:00Z")
		w.Header().Set("X-Ratelimit-Limit-Tokens", "2000")
		w.Header().Set("X-Ratelimit-Remaining-Tokens", "1500")
		_, _ = w.Write([]byte(opencodeGoBody))
	}))
	defer srv.Close()
	acc := acct("go-1", "opencode.global.subscribe.go", srv.URL)
	q := New(fakeAccounts{acc.Name: acc}, time.Minute)
	report, err := q.Query(context.Background(), acc.Name)
	if err != nil {
		t.Fatal(err)
	}
	if len(report.Meters) != 5 {
		t.Fatalf("meters = %+v, want three usage and two rate limit meters", report.Meters)
	}
	for i, want := range []struct {
		unit      provider.MeterUnit
		total     float64
		remaining float64
	}{
		{provider.UnitRequests, 1000, 990},
		{provider.UnitTokens, 2000, 1500},
	} {
		m := report.Meters[i+3]
		if m.Kind != provider.MeterRateLimit || m.Unit != want.unit || m.Reset != provider.ResetRolling ||
			m.Total == nil || *m.Total != want.total || m.Remaining == nil || *m.Remaining != want.remaining {
			t.Errorf("rate limit meter = %+v, want %+v", m, want)
		}
	}
	if at := report.Meters[3].ResetAt; at == nil || at.Format(time.RFC3339) != "2026-10-07T12:00:00Z" {
		t.Errorf("rate limit reset_at = %v", at)
	}
}

func TestOpenCodeGoRequestErrors(t *testing.T) {
	for _, base := range []string{"http://[invalid", "https://unreachable.invalid"} {
		t.Run(base, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			cancel()
			acc := acct("go-1", "opencode.global.subscribe.go", base)
			q := New(fakeAccounts{acc.Name: acc}, time.Minute)
			if _, err := q.Query(ctx, acc.Name); !apperr.Is(err, apperr.QuotaUnavailable) {
				t.Fatalf("err = %v, want QuotaUnavailable", err)
			}
		})
	}
}

func TestOpenCodeGoCustomHeaders(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer custom-key" || r.Header.Get("User-Agent") != "custom-client/1.0" || r.Header.Get("X-Gateway") != "custom" {
			t.Errorf("custom headers lost: %v", r.Header)
		}
		if r.Header.Get("x-opencode-session") != "" {
			t.Error("usage query must not create an inference session")
		}
		_, _ = w.Write([]byte(opencodeGoBody))
	}))
	defer srv.Close()
	acc := acct("go-1", "opencode.global.subscribe.go", srv.URL)
	acc.Headers = map[string]string{"Authorization": "Bearer custom-key", "User-Agent": "custom-client/1.0", "X-Gateway": "custom"}
	q := New(fakeAccounts{acc.Name: acc}, time.Minute)
	if _, err := q.Query(context.Background(), acc.Name); err != nil {
		t.Fatal(err)
	}
}

func TestOpenCodeRegistry(t *testing.T) {
	if query := builtinQuotas["opencode.global.subscribe.go"]; query == nil {
		t.Error("Go must have a builtin quota query")
	}
	if _, ok := builtinQuotas["opencode.global.api.zen"]; ok {
		t.Error("Zen must not be registered without API balance evidence")
	}
}
