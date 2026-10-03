package store

import (
	"context"
	"fmt"
	"math"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/aceaura/model-surge-upstream/backend/provider"
	"github.com/aceaura/model-surge-upstream/backend/usage"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestUsageLogsFreshInput(t *testing.T) {
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL is required")
	}
	ctx := context.Background()
	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		t.Fatal(err)
	}
	cfg.MaxConns = 1
	cfg.ConnConfig.RuntimeParams["timezone"] = "UTC"
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	ddl, err := schemaFS.ReadFile("schema.sql")
	if err != nil {
		t.Fatal(err)
	}
	start := strings.Index(string(ddl), "CREATE TABLE IF NOT EXISTS usage_logs (")
	if start < 0 {
		t.Fatal("usage_logs schema not found")
	}
	table := string(ddl)[start:]
	end := strings.Index(table, ");")
	if end < 0 {
		t.Fatal("usage_logs schema terminator not found")
	}
	table = table[:end+2]
	table = strings.Replace(table, "CREATE TABLE IF NOT EXISTS", "CREATE TEMP TABLE", 1)
	if _, err := pool.Exec(ctx, table); err != nil {
		t.Fatal(err)
	}
	s := &Store{pool: pool}
	bucket := time.Date(2026, 1, 2, 3, 0, 0, 0, time.UTC)
	assertTotals := func(t *testing.T, label string, got, want UsageTotals) {
		t.Helper()
		if got.Requests != want.Requests || got.Success != want.Success ||
			got.Input != want.Input || got.Output != want.Output ||
			got.CacheRead != want.CacheRead || got.CacheWrite != want.CacheWrite ||
			got.RealTotal != want.RealTotal || math.Abs(got.HitRate-want.HitRate) > 1e-12 {
			t.Fatalf("%s totals=%+v, want %+v", label, got, want)
		}
	}
	check := func(t *testing.T, name, protocol string, records []usage.Usage) {
		t.Helper()
		account := "account-" + name
		model := "model-" + name
		want := UsageTotals{Requests: int64(len(records)), Success: int64(len(records))}
		for i, u := range records {
			requestID := fmt.Sprintf("%s-%d", name, i)
			l := &UsageLog{
				RequestID: requestID, Source: "proxy", Protocol: protocol,
				ModelID: model, Account: account, NativeModel: u.Model,
				InputTokens: u.InputTokens, OutputTokens: u.OutputTokens,
				CacheRead: u.CacheReadTokens, CacheWrite: u.CacheWriteTokens,
				InputSemantics: int(u.Semantics), StatusCode: 200,
			}
			if err := s.RecordUsage(ctx, l); err != nil {
				t.Fatal(err)
			}
			if _, err := pool.Exec(ctx,
				"UPDATE usage_logs SET created_at=$1 WHERE account=$2 AND request_id=$3",
				bucket.Add(time.Duration(i+1)*time.Minute), account, requestID); err != nil {
				t.Fatal(err)
			}
			var raw usage.Usage
			if err := pool.QueryRow(ctx,
				`SELECT input_tokens, output_tokens, cache_read_tokens, cache_write_tokens, input_semantics
				 FROM usage_logs WHERE account=$1 AND request_id=$2`, account, requestID).
				Scan(&raw.InputTokens, &raw.OutputTokens, &raw.CacheReadTokens, &raw.CacheWriteTokens, &raw.Semantics); err != nil {
				t.Fatal(err)
			}
			if raw.InputTokens != u.InputTokens || raw.OutputTokens != u.OutputTokens ||
				raw.CacheReadTokens != u.CacheReadTokens || raw.CacheWriteTokens != u.CacheWriteTokens || raw.Semantics != u.Semantics {
				t.Fatalf("stored usage=%+v, want raw %+v", raw, u)
			}
			want.Input += u.FreshInput()
			want.Output += u.OutputTokens
			want.CacheRead += u.CacheReadTokens
			want.CacheWrite += u.CacheWriteTokens
			want.RealTotal += u.RealTotal()
		}
		den := want.Input + want.CacheRead + want.CacheWrite
		if den > 0 {
			want.HitRate = float64(want.CacheRead) / float64(den)
		}
		filter := UsageFilter{Account: account, Start: bucket, End: bucket.Add(time.Hour - time.Nanosecond)}
		logs, total, err := s.UsageLogs(ctx, filter, 10, 0)
		if err != nil {
			t.Fatal(err)
		}
		if total != int64(len(records)) || len(logs) != len(records) {
			t.Fatalf("total=%d logs=%d, want %d", total, len(logs), len(records))
		}
		for i, got := range logs {
			u := records[len(records)-1-i]
			if got.InputTokens != u.FreshInput() || got.OutputTokens != u.OutputTokens ||
				got.CacheRead != u.CacheReadTokens || got.CacheWrite != u.CacheWriteTokens ||
				got.InputSemantics != int(u.Semantics) || got.Account != account || got.ModelID != model {
				t.Fatalf("log=%+v, want normalized %+v", got, u)
			}
			logTotals := UsageTotals{Input: got.InputTokens, Output: got.OutputTokens, CacheRead: got.CacheRead, CacheWrite: got.CacheWrite}
			finalize(&logTotals)
			assertTotals(t, "log", logTotals, UsageTotals{
				Input: u.FreshInput(), Output: u.OutputTokens, CacheRead: u.CacheReadTokens,
				CacheWrite: u.CacheWriteTokens, RealTotal: u.RealTotal(), HitRate: u.HitRate(),
			})
		}
		summary, err := s.UsageSummary(ctx, filter)
		if err != nil {
			t.Fatal(err)
		}
		assertTotals(t, "summary", summary, want)
		trend, err := s.UsageTrend(ctx, filter, "hour")
		if err != nil {
			t.Fatal(err)
		}
		if len(trend) != 1 || !trend[0].Bucket.Equal(bucket) {
			t.Fatalf("trend=%+v, want one bucket at %s", trend, bucket)
		}
		assertTotals(t, "trend", trend[0].UsageTotals, want)
		for _, group := range []struct {
			name string
			key  string
			get  func(context.Context, UsageFilter) ([]UsageGroup, error)
		}{
			{"account", account, s.UsageByAccount},
			{"model", model, s.UsageByModel},
		} {
			groups, err := group.get(ctx, filter)
			if err != nil {
				t.Fatal(err)
			}
			if len(groups) != 1 || groups[0].Key != group.key {
				t.Fatalf("%s groups=%+v, want key %s", group.name, groups, group.key)
			}
			assertTotals(t, group.name, groups[0].UsageTotals, want)
		}
	}
	tests := []struct {
		name      string
		semantics usage.Semantics
		input     int64
		read      int64
		write     int64
		want      int64
	}{
		{"gpt-cache-hit", usage.SemanticsTotal, 43600, 41300, 0, 2300},
		{"total-cache-read-and-write", usage.SemanticsTotal, 10000, 6000, 2000, 2000},
		{"kimi-fresh", usage.SemanticsFresh, 100, 6000, 2000, 100},
		{"total-no-cache", usage.SemanticsTotal, 2600, 0, 0, 2600},
		{"total-fully-cached", usage.SemanticsTotal, 10000, 10000, 0, 0},
		{"inconsistent-cache-read-clamps-zero", usage.SemanticsTotal, 100, 200, 0, 0},
		{"inconsistent-cache-write-clamps-zero", usage.SemanticsTotal, 100, 0, 200, 0},
		{"inconsistent-cache-sum-clamps-zero", usage.SemanticsTotal, 100, 60, 60, 0},
		{"unknown-zero-preserves-input", 0, 100, 200, 30, 100},
		{"unknown-semantics-preserves-input", 99, 100, 200, 30, 100},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			u := usage.Usage{
				InputTokens: tt.input, OutputTokens: 37, CacheReadTokens: tt.read,
				CacheWriteTokens: tt.write, Semantics: tt.semantics,
			}
			if u.FreshInput() != tt.want {
				t.Fatalf("FreshInput=%d, want %d", u.FreshInput(), tt.want)
			}
			check(t, tt.name, "test", []usage.Usage{u})
		})
	}
	protocolTests := []struct {
		name     string
		protocol string
		body     string
		want     usage.Usage
	}{
		{
			"anthropic-kimi", provider.ProtocolAnthropic,
			`{"model":"kimi-k2","usage":{"input_tokens":100,"output_tokens":37,"cache_read_input_tokens":6000,"cache_creation_input_tokens":2000}}`,
			usage.Usage{InputTokens: 100, OutputTokens: 37, CacheReadTokens: 6000, CacheWriteTokens: 2000, Semantics: usage.SemanticsFresh, Model: "kimi-k2"},
		},
		{
			"chat-gpt", provider.ProtocolChatCompletions,
			`{"model":"gpt-5","usage":{"prompt_tokens":43600,"completion_tokens":37,"prompt_tokens_details":{"cached_tokens":41300}}}`,
			usage.Usage{InputTokens: 43600, OutputTokens: 37, CacheReadTokens: 41300, Semantics: usage.SemanticsTotal, Model: "gpt-5"},
		},
		{
			"responses-codex", provider.ProtocolResponses,
			`{"model":"gpt-5-codex","usage":{"input_tokens":10000,"output_tokens":53,"input_tokens_details":{"cached_tokens":8000}}}`,
			usage.Usage{InputTokens: 10000, OutputTokens: 53, CacheReadTokens: 8000, Semantics: usage.SemanticsTotal, Model: "gpt-5-codex"},
		},
		{
			"gemini-thinking", provider.ProtocolGemini,
			`{"modelVersion":"gemini-2.5-pro","usageMetadata":{"promptTokenCount":1000,"cachedContentTokenCount":800,"candidatesTokenCount":30,"thoughtsTokenCount":70,"totalTokenCount":1100}}`,
			usage.Usage{InputTokens: 1000, OutputTokens: 100, CacheReadTokens: 800, Semantics: usage.SemanticsTotal, Model: "gemini-2.5-pro"},
		},
		{
			"chat-deepseek", provider.ProtocolChatCompletions,
			`{"model":"deepseek-chat","usage":{"prompt_tokens":10000,"completion_tokens":71,"prompt_cache_hit_tokens":6000,"prompt_cache_miss_tokens":4000}}`,
			usage.Usage{InputTokens: 10000, OutputTokens: 71, CacheReadTokens: 6000, Semantics: usage.SemanticsTotal, Model: "deepseek-chat"},
		},
	}
	parsed := make([]usage.Usage, len(protocolTests))
	for i, tt := range protocolTests {
		t.Run(tt.name, func(t *testing.T) {
			u, ok := usage.FromResponse(tt.protocol, []byte(tt.body))
			if !ok || u != tt.want {
				t.Fatalf("FromResponse=(%+v, %t), want %+v", u, ok, tt.want)
			}
			parsed[i] = u
			check(t, tt.name, tt.protocol, []usage.Usage{u})
		})
	}
	t.Run("two-protocol-records-summed", func(t *testing.T) {
		check(t, "two-protocol-records-summed", "mixed", parsed[:2])
	})
}
