package store

import (
	"context"
	"fmt"
	"strings"
	"time"
)

// UsageLog 一次上游请求的用量明细。InputSemantics 取值见 usage.Semantics
// （1=输入含缓存，2=净输入），由写入方按协议填好。
type UsageLog struct {
	ID             int64     `json:"id"`
	RequestID      string    `json:"request_id"`
	Source         string    `json:"source"`
	Protocol       string    `json:"protocol"`
	ModelID        string    `json:"model_id"`
	Account        string    `json:"account"`
	NativeModel    string    `json:"native_model"`
	InputTokens    int64     `json:"input_tokens"`
	OutputTokens   int64     `json:"output_tokens"`
	CacheRead      int64     `json:"cache_read_tokens"`
	CacheWrite     int64     `json:"cache_write_tokens"`
	InputSemantics int       `json:"-"`
	StatusCode     int       `json:"status_code"`
	IsStreaming    bool      `json:"is_streaming"`
	LatencyMS      *int64    `json:"latency_ms"`
	DurationMS     *int64    `json:"duration_ms"`
	ErrorMessage   string    `json:"error_message"`
	CreatedAt      time.Time `json:"created_at"`
}

// UsageFilter 用量查询的过滤条件。Start/End 零值表示不限该侧。
type UsageFilter struct {
	Start   time.Time
	End     time.Time
	Model   string
	Account string
	Source  string
}

// UsageTotals 一组用量聚合值。InputTokens 已是归一后的净输入。
type UsageTotals struct {
	Requests   int64   `json:"requests"`
	Success    int64   `json:"success"`
	Input      int64   `json:"input_tokens"`
	Output     int64   `json:"output_tokens"`
	CacheRead  int64   `json:"cache_read_tokens"`
	CacheWrite int64   `json:"cache_write_tokens"`
	RealTotal  int64   `json:"real_total_tokens"`
	HitRate    float64 `json:"cache_hit_rate"`
}

// UsageBucket 趋势图的一个时间桶。
type UsageBucket struct {
	Bucket time.Time `json:"bucket"`
	UsageTotals
}

// UsageGroup 按模型或按账号的一行聚合。
type UsageGroup struct {
	Key string `json:"key"`
	UsageTotals
}

// freshInput 把存储的输入 token 归一为净输入：total 语义扣缓存两桶，
// 且只在够扣时扣（防御上游报数自相矛盾）。与 CC Switch 的 fresh_input_sql 同口径。
func freshInput(alias string) string {
	p := ""
	if alias != "" {
		p = alias + "."
	}
	return fmt.Sprintf(
		"CASE WHEN %[1]sinput_semantics = 2 THEN %[1]sinput_tokens "+
			"WHEN %[1]sinput_semantics = 1 AND %[1]sinput_tokens >= %[1]scache_read_tokens + %[1]scache_write_tokens "+
			"THEN %[1]sinput_tokens - %[1]scache_read_tokens - %[1]scache_write_tokens "+
			"ELSE %[1]sinput_tokens END", p)
}

// totalsSelect 聚合 SELECT 列：请求数/成功数/四桶/净输入。
func totalsSelect(alias string) string {
	f := freshInput(alias)
	p := ""
	if alias != "" {
		p = alias + "."
	}
	return fmt.Sprintf(
		"COUNT(*) , "+
			"COALESCE(SUM(CASE WHEN %[1]sstatus_code >= 200 AND %[1]sstatus_code < 300 THEN 1 ELSE 0 END),0), "+
			"COALESCE(SUM(%[2]s),0), "+
			"COALESCE(SUM(%[1]soutput_tokens),0), "+
			"COALESCE(SUM(%[1]scache_read_tokens),0), "+
			"COALESCE(SUM(%[1]scache_write_tokens),0)", p, f)
}

// where 把过滤条件拼成 WHERE 子句与参数。
func (f UsageFilter) where(alias string) (string, []any) {
	p := ""
	if alias != "" {
		p = alias + "."
	}
	var conds []string
	var args []any
	if !f.Start.IsZero() {
		conds = append(conds, p+"created_at >= $"+itoa(len(args)+1))
		args = append(args, f.Start)
	}
	if !f.End.IsZero() {
		conds = append(conds, p+"created_at <= $"+itoa(len(args)+1))
		args = append(args, f.End)
	}
	if f.Model != "" {
		conds = append(conds, p+"model_id = $"+itoa(len(args)+1))
		args = append(args, f.Model)
	}
	if f.Account != "" {
		conds = append(conds, p+"account = $"+itoa(len(args)+1))
		args = append(args, f.Account)
	}
	if f.Source != "" {
		conds = append(conds, p+"source = $"+itoa(len(args)+1))
		args = append(args, f.Source)
	}
	if len(conds) == 0 {
		return "", nil
	}
	return " WHERE " + strings.Join(conds, " AND "), args
}

func itoa(n int) string { return fmt.Sprintf("%d", n) }

// finalize 由四桶+请求数补出真实消耗与命中率。
func finalize(t *UsageTotals) {
	t.RealTotal = t.Input + t.Output + t.CacheWrite + t.CacheRead
	den := t.Input + t.CacheWrite + t.CacheRead
	if den > 0 {
		t.HitRate = float64(t.CacheRead) / float64(den)
	}
}

func scanTotals(scan func(...any) error) (UsageTotals, error) {
	var t UsageTotals
	err := scan(&t.Requests, &t.Success, &t.Input, &t.Output, &t.CacheRead, &t.CacheWrite)
	finalize(&t)
	return t, err
}

// RecordUsage 写一条用量明细。统计是旁路：调用方应忽略返回的错误，
// 不让落库失败影响转发主路径。
func (s *Store) RecordUsage(ctx context.Context, l *UsageLog) error {
	const q = `INSERT INTO usage_logs
		(request_id, source, protocol, model_id, account, native_model,
		 input_tokens, output_tokens, cache_read_tokens, cache_write_tokens,
		 input_semantics, status_code, is_streaming, latency_ms, duration_ms, error_message)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16)`
	_, err := s.pool.Exec(ctx, q,
		l.RequestID, l.Source, l.Protocol, l.ModelID, l.Account, l.NativeModel,
		l.InputTokens, l.OutputTokens, l.CacheRead, l.CacheWrite,
		l.InputSemantics, l.StatusCode, l.IsStreaming, l.LatencyMS, l.DurationMS, l.ErrorMessage)
	return err
}

// UsageSummary 区间内的总指标。
func (s *Store) UsageSummary(ctx context.Context, f UsageFilter) (UsageTotals, error) {
	where, args := f.where("")
	q := "SELECT " + totalsSelect("") + " FROM usage_logs" + where
	row := s.pool.QueryRow(ctx, q, args...)
	return scanTotals(row.Scan)
}

// UsageTrend 按小时或按天分桶的趋势。空桶补 0，保证前端折线连续；
// 桶序列覆盖 [Start, End]，调用方需保证两者非零。
func (s *Store) UsageTrend(ctx context.Context, f UsageFilter, granularity string) ([]UsageBucket, error) {
	trunc := "hour"
	step := time.Hour
	if granularity == "day" {
		trunc = "day"
		step = 24 * time.Hour
	}
	where, args := f.where("")
	q := fmt.Sprintf(
		"SELECT date_trunc('%s', created_at) AS b, %s FROM usage_logs%s GROUP BY b ORDER BY b",
		trunc, totalsSelect(""), where)
	rows, err := s.pool.Query(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	byBucket := map[time.Time]UsageTotals{}
	for rows.Next() {
		var b time.Time
		t, err := scanTotals(func(dest ...any) error {
			return rows.Scan(append([]any{&b}, dest...)...)
		})
		if err != nil {
			return nil, err
		}
		// 归一到 UTC 再作键：pgx 扫出的 timestamptz 与 f.Start 派生的键
		// 可能 loc 指针不同，time.Time 的 == 会判不等，真实桶会被零桶盖掉。
		byBucket[b.UTC()] = t
	}
	if rows.Err() != nil {
		return nil, rows.Err()
	}

	// 空桶补 0：从 Start 所在桶走到 End 所在桶。
	out := []UsageBucket{}
	if f.Start.IsZero() || f.End.IsZero() || f.End.Before(f.Start) {
		for b, t := range byBucket {
			out = append(out, UsageBucket{Bucket: b, UsageTotals: t})
		}
		sortBuckets(out)
		return out, nil
	}
	for b := truncateTo(f.Start.UTC(), granularity); !b.After(f.End); b = b.Add(step) {
		out = append(out, UsageBucket{Bucket: b, UsageTotals: byBucket[b]})
	}
	return out, nil
}

func truncateTo(t time.Time, granularity string) time.Time {
	if granularity == "day" {
		return time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, t.Location())
	}
	return t.Truncate(time.Hour)
}

func sortBuckets(bs []UsageBucket) {
	for i := 1; i < len(bs); i++ {
		for j := i; j > 0 && bs[j].Bucket.Before(bs[j-1].Bucket); j-- {
			bs[j], bs[j-1] = bs[j-1], bs[j]
		}
	}
}

// UsageByModel 按命名模型聚合，按真实消耗降序。
func (s *Store) UsageByModel(ctx context.Context, f UsageFilter) ([]UsageGroup, error) {
	return s.usageGroupBy(ctx, f, "model_id")
}

// UsageByAccount 按账号聚合，按真实消耗降序。
func (s *Store) UsageByAccount(ctx context.Context, f UsageFilter) ([]UsageGroup, error) {
	return s.usageGroupBy(ctx, f, "account")
}

func (s *Store) usageGroupBy(ctx context.Context, f UsageFilter, column string) ([]UsageGroup, error) {
	where, args := f.where("")
	q := fmt.Sprintf("SELECT %s, %s FROM usage_logs%s GROUP BY %s ORDER BY SUM(output_tokens) DESC, %s",
		column, totalsSelect(""), where, column, column)
	rows, err := s.pool.Query(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []UsageGroup
	for rows.Next() {
		var g UsageGroup
		t, err := scanTotals(func(dest ...any) error {
			return rows.Scan(append([]any{&g.Key}, dest...)...)
		})
		if err != nil {
			return nil, err
		}
		g.UsageTotals = t
		out = append(out, g)
	}
	return out, rows.Err()
}

// UsageLogs 明细分页，按时间倒序。返回行与总数。
func (s *Store) UsageLogs(ctx context.Context, f UsageFilter, limit, offset int) ([]UsageLog, int64, error) {
	where, args := f.where("")
	var total int64
	if err := s.pool.QueryRow(ctx, "SELECT COUNT(*) FROM usage_logs"+where, args...).Scan(&total); err != nil {
		return nil, 0, err
	}
	q := fmt.Sprintf(
		`SELECT id, request_id, source, protocol, model_id, account, native_model,
		        input_tokens, output_tokens, cache_read_tokens, cache_write_tokens,
		        input_semantics, status_code, is_streaming, latency_ms, duration_ms, error_message, created_at
		 FROM usage_logs%s ORDER BY created_at DESC, id DESC LIMIT $%s OFFSET $%s`,
		where, itoa(len(args)+1), itoa(len(args)+2))
	args = append(args, limit, offset)
	rows, err := s.pool.Query(ctx, q, args...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	var out []UsageLog
	for rows.Next() {
		var l UsageLog
		if err := rows.Scan(&l.ID, &l.RequestID, &l.Source, &l.Protocol, &l.ModelID, &l.Account,
			&l.NativeModel, &l.InputTokens, &l.OutputTokens, &l.CacheRead, &l.CacheWrite,
			&l.InputSemantics, &l.StatusCode, &l.IsStreaming, &l.LatencyMS, &l.DurationMS,
			&l.ErrorMessage, &l.CreatedAt); err != nil {
			return nil, 0, err
		}
		out = append(out, l)
	}
	return out, total, rows.Err()
}
