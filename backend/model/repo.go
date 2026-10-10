package model

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/aceaura/model-surge-upstream/backend/apperr"
	"github.com/aceaura/model-surge-upstream/backend/cache"
	"github.com/aceaura/model-surge-upstream/backend/effort"
	"github.com/aceaura/model-surge-upstream/backend/provider"
)

const columns = `id, account, native_model, protocol, context_window, defaults, overrides, compact, efforts, effort_format, effort_in, effort_off, effort_budgets, effort_enabled, enabled, created_at, updated_at, sort_order`

// AccountLookup 提供账号存在性与其 provider 规格。由上层注入，
// 避免 model 包横向依赖 account 包。
type AccountLookup func(ctx context.Context, name string) (provider.Spec, error)

type Repo struct {
	pool    *pgxpool.Pool
	cache   *cache.Cache
	account AccountLookup
}

func NewRepo(pool *pgxpool.Pool, c *cache.Cache, lookup AccountLookup) *Repo {
	return &Repo{pool: pool, cache: c, account: lookup}
}

type Input struct {
	ID            string
	Account       string
	NativeModel   string
	Protocol      string
	ContextWindow int
	Defaults      json.RawMessage
	Overrides     json.RawMessage
	Compact       json.RawMessage
	// Efforts 原始配置：空=跟随现状（Create 落 null 自动，Update 保留旧值），
	// "null"=恢复自动，数组=显式声明。
	Efforts json.RawMessage
	// EffortFormat 写入格式：nil=跟随现状（Create 落空串=协议内置，Update
	// 保留旧值），指向空串=回内置映射，非空=显式格式（保存期校验枚举）。
	EffortFormat *string
	// EffortIn 入口格式：nil=跟随现状（Create 落空串=auto），非空=显式声明
	// （保存期归一+校验词表）。
	EffortIn *string
	// EffortOff 关思考落定：nil=跟随现状（Create 落空串=disabled）。
	EffortOff *string
	// EffortBudgets 预算覆盖原始 JSON：空=跟随现状（Create 落 {}），
	// "{}"=清空覆盖，对象=档位值→正整数（保存期校验）。
	EffortBudgets json.RawMessage
	// EffortEnabled 推理档转换总开关:false=转发面不读不写不剥离。
	EffortEnabled bool
	Enabled       bool
	// NewID 改名目标（仅 Update 使用）：空=沿用 ID；非空且不同于 ID 时把
	// 记录主键改写为 NewID，同事务随迁 chat_sessions.model_id。
	// usage_logs 是历史流水，保留改名前的旧标识。
	NewID string
}

func (r *Repo) Create(ctx context.Context, in Input) (Model, error) {
	m, err := r.validate(ctx, in)
	if err != nil {
		return Model{}, err
	}
	now := time.Now().UTC()
	m.CreatedAt, m.UpdatedAt = now, now

	// 新模型排到现有手动序之后;全空库从 0 起。
	if err := r.pool.QueryRow(ctx, `SELECT COALESCE(MAX(sort_order), -1) + 1 FROM models`).Scan(&m.SortOrder); err != nil {
		return Model{}, apperr.Wrap(apperr.StorageError, "next sort_order", err)
	}

	persist := func() error {
		_, err := r.pool.Exec(ctx, `INSERT INTO models (`+columns+`)
			VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18)`,
			m.ID, m.Account, m.NativeModel, m.Protocol, m.ContextWindow,
			[]byte(m.Defaults), []byte(m.Overrides), []byte(m.Compact), []byte(m.Efforts),
			m.EffortFormat, m.EffortIn, m.EffortOff, []byte(marshalBudgets(m.EffortBudgets)),
			m.EffortEnabled, m.Enabled, m.CreatedAt, m.UpdatedAt, m.SortOrder)
		return mapWriteErr(err, m.ID)
	}
	if err := cache.WriteThrough(ctx, r.cache, cache.ModelKey(m.ID), m, persist); err != nil {
		return Model{}, err
	}
	return m, nil
}

func (r *Repo) Get(ctx context.Context, id string) (Model, error) {
	return cache.ReadThrough(ctx, r.cache, cache.ModelKey(id), func() (Model, error) {
		row := r.pool.QueryRow(ctx, `SELECT `+columns+` FROM models WHERE id=$1`, id)
		m, err := scan(row)
		if errors.Is(err, pgx.ErrNoRows) {
			return Model{}, apperr.New(apperr.NotFound, fmt.Sprintf("model %q not found", id))
		}
		if err != nil {
			return Model{}, apperr.Wrap(apperr.StorageError, "read model", err)
		}
		return m, nil
	})
}

// List 直读 PG。account 为空表示不过滤。
func (r *Repo) List(ctx context.Context, account string) ([]Model, error) {
	query := `SELECT ` + columns + ` FROM models ORDER BY sort_order, id`
	args := []any{}
	if account != "" {
		query = `SELECT ` + columns + ` FROM models WHERE account=$1 ORDER BY sort_order, id`
		args = append(args, account)
	}
	rows, err := r.pool.Query(ctx, query, args...)
	if err != nil {
		return nil, apperr.Wrap(apperr.StorageError, "list models", err)
	}
	defer rows.Close()

	out := []Model{}
	for rows.Next() {
		m, err := scan(rows)
		if err != nil {
			return nil, apperr.Wrap(apperr.StorageError, "scan model", err)
		}
		out = append(out, m)
	}
	if err := rows.Err(); err != nil {
		return nil, apperr.Wrap(apperr.StorageError, "list models", err)
	}
	return out, nil
}

func (r *Repo) Update(ctx context.Context, in Input) (Model, error) {
	existing, err := r.Get(ctx, in.ID)
	if err != nil {
		return Model{}, err
	}
	if in.Account == "" {
		in.Account = existing.Account
	}
	if in.NativeModel == "" {
		in.NativeModel = existing.NativeModel
	}
	if in.Protocol == "" {
		in.Protocol = existing.Protocol
	}
	if len(in.Efforts) == 0 {
		in.Efforts = existing.Efforts
	}
	if in.EffortFormat == nil {
		in.EffortFormat = &existing.EffortFormat
	}
	if in.EffortIn == nil {
		in.EffortIn = &existing.EffortIn
	}
	if in.EffortOff == nil {
		in.EffortOff = &existing.EffortOff
	}
	if len(in.EffortBudgets) == 0 {
		in.EffortBudgets = json.RawMessage(marshalBudgets(existing.EffortBudgets))
	}
	newID := strings.TrimSpace(in.NewID)
	rename := newID != "" && newID != existing.ID
	if rename {
		in.ID = newID
	}
	m, err := r.validate(ctx, in)
	if err != nil {
		return Model{}, err
	}
	m.CreatedAt = existing.CreatedAt
	m.SortOrder = existing.SortOrder
	m.UpdatedAt = time.Now().UTC()

	persist := func() error {
		if !rename {
			tag, err := r.pool.Exec(ctx, `UPDATE models SET
				account=$2, native_model=$3, protocol=$4, context_window=$5,
				defaults=$6, overrides=$7, compact=$8, efforts=$9, effort_format=$10,
				effort_in=$11, effort_off=$12, effort_budgets=$13, effort_enabled=$14,
				enabled=$15, updated_at=$16 WHERE id=$1`,
				m.ID, m.Account, m.NativeModel, m.Protocol, m.ContextWindow,
				[]byte(m.Defaults), []byte(m.Overrides), []byte(m.Compact), []byte(m.Efforts),
				m.EffortFormat, m.EffortIn, m.EffortOff, []byte(marshalBudgets(m.EffortBudgets)),
				m.EffortEnabled, m.Enabled, m.UpdatedAt)
			if err != nil {
				return apperr.Wrap(apperr.StorageError, "update model", err)
			}
			if tag.RowsAffected() == 0 {
				return apperr.New(apperr.NotFound, fmt.Sprintf("model %q not found", m.ID))
			}
			return nil
		}
		// 改名要动主键并随迁会话回显，单事务提交;目标 id 撞车报 AlreadyExists。
		tx, err := r.pool.Begin(ctx)
		if err != nil {
			return apperr.Wrap(apperr.StorageError, "begin tx", err)
		}
		defer func() { _ = tx.Rollback(ctx) }()
		tag, err := tx.Exec(ctx, `UPDATE models SET
			id=$2, account=$3, native_model=$4, protocol=$5, context_window=$6,
			defaults=$7, overrides=$8, compact=$9, efforts=$10, effort_format=$11,
			effort_in=$12, effort_off=$13, effort_budgets=$14, effort_enabled=$15,
			enabled=$16, updated_at=$17 WHERE id=$1`,
			existing.ID, m.ID, m.Account, m.NativeModel, m.Protocol, m.ContextWindow,
			[]byte(m.Defaults), []byte(m.Overrides), []byte(m.Compact), []byte(m.Efforts),
			m.EffortFormat, m.EffortIn, m.EffortOff, []byte(marshalBudgets(m.EffortBudgets)),
			m.EffortEnabled, m.Enabled, m.UpdatedAt)
		if err != nil {
			return mapWriteErr(err, m.ID)
		}
		if tag.RowsAffected() == 0 {
			return apperr.New(apperr.NotFound, fmt.Sprintf("model %q not found", existing.ID))
		}
		if _, err := tx.Exec(ctx,
			`UPDATE chat_sessions SET model_id=$2 WHERE model_id=$1`, existing.ID, m.ID); err != nil {
			return apperr.Wrap(apperr.StorageError, "rename model in chat sessions", err)
		}
		if err := tx.Commit(ctx); err != nil {
			return apperr.Wrap(apperr.StorageError, "commit rename", err)
		}
		return nil
	}
	if err := cache.WriteThrough(ctx, r.cache, cache.ModelKey(m.ID), m, persist); err != nil {
		return Model{}, err
	}
	if rename {
		r.cache.Invalidate(ctx, cache.ModelKey(existing.ID))
	}
	return m, nil
}

// Reorder 按给定模型 id 顺序把 sort_order 依次写为 0..n-1,单事务提交;
// 任一 id 不存在即整批回滚报 NotFound。缓存里的 Model 带旧序号,一并失效。
func (r *Repo) Reorder(ctx context.Context, ids []string) error {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return apperr.Wrap(apperr.StorageError, "begin tx", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	for i, id := range ids {
		tag, err := tx.Exec(ctx, `UPDATE models SET sort_order=$1 WHERE id=$2`, i, id)
		if err != nil {
			return apperr.Wrap(apperr.StorageError, "reorder models", err)
		}
		if tag.RowsAffected() == 0 {
			return apperr.New(apperr.NotFound, fmt.Sprintf("model %q not found", id))
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return apperr.Wrap(apperr.StorageError, "commit reorder", err)
	}
	keys := make([]string, 0, len(ids))
	for _, id := range ids {
		keys = append(keys, cache.ModelKey(id))
	}
	r.cache.Invalidate(ctx, keys...)
	return nil
}

// Delete 只删模型行，不影响其所属账号。
func (r *Repo) Delete(ctx context.Context, id string) error {
	tag, err := r.pool.Exec(ctx, `DELETE FROM models WHERE id=$1`, id)
	if err != nil {
		return apperr.Wrap(apperr.StorageError, "delete model", err)
	}
	if tag.RowsAffected() == 0 {
		return apperr.New(apperr.NotFound, fmt.Sprintf("model %q not found", id))
	}
	r.cache.Invalidate(ctx, cache.ModelKey(id))
	return nil
}

func (r *Repo) validate(ctx context.Context, in Input) (Model, error) {
	id := strings.TrimSpace(in.ID)
	if id == "" {
		return Model{}, apperr.New(apperr.InvalidRequest, "model id is required")
	}
	accountName := strings.TrimSpace(in.Account)
	if accountName == "" {
		return Model{}, apperr.New(apperr.InvalidRequest, "account is required")
	}
	native := strings.TrimSpace(in.NativeModel)
	if native == "" {
		return Model{}, apperr.New(apperr.InvalidRequest, "native_model is required")
	}
	if in.ContextWindow < 0 {
		return Model{}, apperr.New(apperr.InvalidRequest, "context_window must not be negative")
	}

	spec, err := r.account(ctx, accountName)
	if err != nil {
		return Model{}, err
	}
	if !spec.Supports(in.Protocol) {
		return Model{}, apperr.New(apperr.InvalidProtocol, fmt.Sprintf(
			"provider %q does not support protocol %q, supported: %s",
			spec.ID, in.Protocol, strings.Join(spec.Protocols, ", ")))
	}

	defaults, err := normalizeObject(in.Defaults, "defaults")
	if err != nil {
		return Model{}, err
	}
	overrides, err := normalizeObject(in.Overrides, "overrides")
	if err != nil {
		return Model{}, err
	}
	compactCfg, err := normalizeCompact(in.Compact)
	if err != nil {
		return Model{}, err
	}
	efforts, err := normalizeEfforts(in.Efforts)
	if err != nil {
		return Model{}, err
	}
	// 显式数组的形态合法性在写路径统一校验;有效列表(自动模式跟随上游
	// 声明)由能访问上游的层(httpapi/resolve)现算,仓储不拼凑。
	if _, err := effort.Effective(efforts, nil); err != nil {
		return Model{}, err
	}
	format := ""
	if in.EffortFormat != nil {
		format = strings.TrimSpace(*in.EffortFormat)
	}
	format = effort.NormalizeFormat(format)
	if !effort.ValidFormat(format) {
		return Model{}, apperr.New(apperr.InvalidRequest,
			"effort_format must be one of: auto, openai_chat, openai_responses, anthropic_effort, anthropic_budget, anthropic_adaptive, anthropic_off, gemini_level, gemini_budget")
	}
	effortIn := ""
	if in.EffortIn != nil {
		effortIn = strings.TrimSpace(*in.EffortIn)
	}
	effortIn = effort.NormalizeFormat(effortIn)
	if !effort.ValidEntryFormat(effortIn) {
		return Model{}, apperr.New(apperr.InvalidRequest,
			"effort_in must be one of: auto, openai_chat, openai_responses, anthropic_effort, anthropic_budget, anthropic_adaptive, anthropic_off")
	}
	effortOff := ""
	if in.EffortOff != nil {
		effortOff = strings.TrimSpace(*in.EffortOff)
	}
	if !effort.ValidOff(effortOff) {
		return Model{}, apperr.New(apperr.InvalidRequest,
			"effort_off must be one of: disabled, between_tools, omit")
	}
	budgets, err := normalizeBudgets(in.EffortBudgets)
	if err != nil {
		return Model{}, err
	}

	return Model{
		ID:            id,
		Account:       accountName,
		NativeModel:   native,
		Protocol:      in.Protocol,
		ContextWindow: in.ContextWindow,
		Defaults:      defaults,
		Overrides:     overrides,
		Compact:       compactCfg,
		Efforts:       efforts,
		EffortFormat:  format,
		EffortIn:      effortIn,
		EffortOff:     effortOff,
		EffortBudgets: budgets,
		EffortEnabled: in.EffortEnabled,
		Enabled:       in.Enabled,
	}, nil
}

// normalizeBudgets 校验预算覆盖 JSON:空=空 map;必须是对象且值为正整数。
func normalizeBudgets(raw json.RawMessage) (map[string]int, error) {
	out := map[string]int{}
	if len(raw) == 0 {
		return out, nil
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, apperr.New(apperr.InvalidJSON, fmt.Sprintf("effort_budgets must be a json object of value->positive int: %v", err))
	}
	for k, v := range out {
		if v <= 0 {
			return nil, apperr.New(apperr.InvalidRequest, fmt.Sprintf("effort_budgets[%q] must be positive", k))
		}
	}
	return out, nil
}

// marshalBudgets 预算覆盖落库序列化;nil 落 {}。
func marshalBudgets(m map[string]int) string {
	if len(m) == 0 {
		return "{}"
	}
	raw, err := json.Marshal(m)
	if err != nil {
		return "{}"
	}
	return string(raw)
}

// normalizeEfforts 把空值补成 JSON null（自动=跟随上游声明）；数组形态的
// 条目合法性在 effort.Effective 里统一校验。
func normalizeEfforts(raw json.RawMessage) (json.RawMessage, error) {
	if len(raw) == 0 {
		return json.RawMessage(`null`), nil
	}
	s := strings.TrimSpace(string(raw))
	if s == "null" {
		return json.RawMessage(`null`), nil
	}
	if !strings.HasPrefix(s, "[") {
		return nil, apperr.New(apperr.InvalidJSON, "efforts must be a json array of {name,value} entries")
	}
	return json.RawMessage(s), nil
}

// normalizeCompact 校验 compact JSON：必须是对象；mode 只允许
// passive/error；数值项给出合理范围。mode 的取值集合定义在此
// 而非 compact 包——compact 依赖 resolve、resolve 依赖本包，
// 反向引用会成环，故取值集合随存储校验落在这里。
func normalizeCompact(raw json.RawMessage) (json.RawMessage, error) {
	obj, err := normalizeObject(raw, "compact")
	if err != nil {
		return nil, err
	}
	var probe struct {
		Mode      string   `json:"mode"`
		Threshold *float64 `json:"threshold"`
	}
	if err := json.Unmarshal(obj, &probe); err != nil {
		return nil, apperr.New(apperr.InvalidJSON, "compact must be a json object")
	}
	switch probe.Mode {
	case "", "passive", "error":
	default:
		return nil, apperr.New(apperr.InvalidRequest,
			"compact.mode must be one of: passive, error")
	}
	if probe.Threshold != nil && (*probe.Threshold <= 0 || *probe.Threshold > 1) {
		return nil, apperr.New(apperr.InvalidRequest, "compact.threshold must be in (0, 1]")
	}
	return obj, nil
}

// normalizeObject 把空值补成 {}，并要求内容是 JSON object。
func normalizeObject(raw json.RawMessage, field string) (json.RawMessage, error) {
	if len(raw) == 0 {
		return json.RawMessage(`{}`), nil
	}
	var probe map[string]any
	if err := json.Unmarshal(raw, &probe); err != nil {
		return nil, apperr.New(apperr.InvalidJSON, fmt.Sprintf("%s must be a json object: %v", field, err))
	}
	if probe == nil {
		return json.RawMessage(`{}`), nil
	}
	compact, err := json.Marshal(probe)
	if err != nil {
		return nil, apperr.New(apperr.InvalidJSON, fmt.Sprintf("%s must be a json object", field))
	}
	return compact, nil
}

type scanner interface {
	Scan(dest ...any) error
}

func scan(s scanner) (Model, error) {
	var (
		m         Model
		defaults  []byte
		overrides []byte
		compact   []byte
		efforts   []byte
		budgets   []byte
	)
	if err := s.Scan(&m.ID, &m.Account, &m.NativeModel, &m.Protocol, &m.ContextWindow,
		&defaults, &overrides, &compact, &efforts, &m.EffortFormat, &m.EffortIn, &m.EffortOff,
		&budgets, &m.EffortEnabled, &m.Enabled, &m.CreatedAt, &m.UpdatedAt, &m.SortOrder); err != nil {
		return Model{}, err
	}
	m.Defaults = json.RawMessage(defaults)
	m.Overrides = json.RawMessage(overrides)
	m.Compact = json.RawMessage(compact)
	m.Efforts = json.RawMessage(efforts)
	m.EffortBudgets = map[string]int{}
	if len(budgets) > 0 {
		if err := json.Unmarshal(budgets, &m.EffortBudgets); err != nil {
			return Model{}, apperr.Wrap(apperr.StorageError, "parse effort_budgets", err)
		}
	}
	if len(m.Defaults) == 0 {
		m.Defaults = json.RawMessage(`{}`)
	}
	if len(m.Overrides) == 0 {
		m.Overrides = json.RawMessage(`{}`)
	}
	if len(m.Compact) == 0 {
		m.Compact = json.RawMessage(`{}`)
	}
	if len(m.Efforts) == 0 {
		m.Efforts = json.RawMessage(`null`)
	}
	return m, nil
}

func mapWriteErr(err error, id string) error {
	if err == nil {
		return nil
	}
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		switch pgErr.Code {
		case "23505":
			return apperr.New(apperr.AlreadyExists, fmt.Sprintf("model %q already exists", id))
		case "23503":
			return apperr.New(apperr.NotFound, "referenced account does not exist")
		}
	}
	return apperr.Wrap(apperr.StorageError, "write model", err)
}
