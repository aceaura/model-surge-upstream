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

const columns = `id, account, native_model, protocol, context_window, defaults, overrides, compact, efforts, enabled, created_at, updated_at`

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
	Enabled bool
}

func (r *Repo) Create(ctx context.Context, in Input) (Model, error) {
	m, err := r.validate(ctx, in)
	if err != nil {
		return Model{}, err
	}
	now := time.Now().UTC()
	m.CreatedAt, m.UpdatedAt = now, now

	persist := func() error {
		_, err := r.pool.Exec(ctx, `INSERT INTO models (`+columns+`)
			VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12)`,
			m.ID, m.Account, m.NativeModel, m.Protocol, m.ContextWindow,
			[]byte(m.Defaults), []byte(m.Overrides), []byte(m.Compact), []byte(m.Efforts),
			m.Enabled, m.CreatedAt, m.UpdatedAt)
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
	query := `SELECT ` + columns + ` FROM models ORDER BY id`
	args := []any{}
	if account != "" {
		query = `SELECT ` + columns + ` FROM models WHERE account=$1 ORDER BY id`
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
	m, err := r.validate(ctx, in)
	if err != nil {
		return Model{}, err
	}
	m.CreatedAt = existing.CreatedAt
	m.UpdatedAt = time.Now().UTC()

	persist := func() error {
		tag, err := r.pool.Exec(ctx, `UPDATE models SET
			account=$2, native_model=$3, protocol=$4, context_window=$5,
			defaults=$6, overrides=$7, compact=$8, efforts=$9, enabled=$10, updated_at=$11 WHERE id=$1`,
			m.ID, m.Account, m.NativeModel, m.Protocol, m.ContextWindow,
			[]byte(m.Defaults), []byte(m.Overrides), []byte(m.Compact), []byte(m.Efforts),
			m.Enabled, m.UpdatedAt)
		if err != nil {
			return apperr.Wrap(apperr.StorageError, "update model", err)
		}
		if tag.RowsAffected() == 0 {
			return apperr.New(apperr.NotFound, fmt.Sprintf("model %q not found", m.ID))
		}
		return nil
	}
	if err := cache.WriteThrough(ctx, r.cache, cache.ModelKey(m.ID), m, persist); err != nil {
		return Model{}, err
	}
	return m, nil
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
	effective, err := effort.Effective(in.Protocol, native, efforts)
	if err != nil {
		return Model{}, err
	}

	return Model{
		ID:               id,
		Account:          accountName,
		NativeModel:      native,
		Protocol:         in.Protocol,
		ContextWindow:    in.ContextWindow,
		Defaults:         defaults,
		Overrides:        overrides,
		Compact:          compactCfg,
		Efforts:          efforts,
		EffortsEffective: effective,
		Enabled:          in.Enabled,
	}, nil
}

// normalizeEfforts 把空值补成 JSON null（自动推导）；数组形态的词表
// 合法性在 effort.Effective 里统一校验。
func normalizeEfforts(raw json.RawMessage) (json.RawMessage, error) {
	if len(raw) == 0 {
		return json.RawMessage(`null`), nil
	}
	s := strings.TrimSpace(string(raw))
	if s == "null" {
		return json.RawMessage(`null`), nil
	}
	if !strings.HasPrefix(s, "[") {
		return nil, apperr.New(apperr.InvalidJSON, "efforts must be a json array of strings")
	}
	return json.RawMessage(s), nil
}

// normalizeCompact 校验 compact JSON：必须是对象；mode 只允许
// passive/error/auto；数值项给出合理范围。mode 的取值集合定义在此
// 而非 compact 包——compact 依赖 resolve、resolve 依赖本包，
// 反向引用会成环，故取值集合随存储校验落在这里。
func normalizeCompact(raw json.RawMessage) (json.RawMessage, error) {
	obj, err := normalizeObject(raw, "compact")
	if err != nil {
		return nil, err
	}
	var probe struct {
		Mode             string   `json:"mode"`
		Threshold        *float64 `json:"threshold"`
		KeepTurns        *int     `json:"keep_turns"`
		MaxSummaryTokens *int     `json:"max_summary_tokens"`
	}
	if err := json.Unmarshal(obj, &probe); err != nil {
		return nil, apperr.New(apperr.InvalidJSON, "compact must be a json object")
	}
	switch probe.Mode {
	case "", "passive", "error", "auto":
	default:
		return nil, apperr.New(apperr.InvalidRequest,
			"compact.mode must be one of: passive, error, auto")
	}
	if probe.Threshold != nil && (*probe.Threshold <= 0 || *probe.Threshold > 1) {
		return nil, apperr.New(apperr.InvalidRequest, "compact.threshold must be in (0, 1]")
	}
	if probe.KeepTurns != nil && *probe.KeepTurns < 1 {
		return nil, apperr.New(apperr.InvalidRequest, "compact.keep_turns must be positive")
	}
	if probe.MaxSummaryTokens != nil && *probe.MaxSummaryTokens < 1 {
		return nil, apperr.New(apperr.InvalidRequest, "compact.max_summary_tokens must be positive")
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
	)
	if err := s.Scan(&m.ID, &m.Account, &m.NativeModel, &m.Protocol, &m.ContextWindow,
		&defaults, &overrides, &compact, &efforts, &m.Enabled, &m.CreatedAt, &m.UpdatedAt); err != nil {
		return Model{}, err
	}
	m.Defaults = json.RawMessage(defaults)
	m.Overrides = json.RawMessage(overrides)
	m.Compact = json.RawMessage(compact)
	m.Efforts = json.RawMessage(efforts)
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
	// 库里 JSON 已落过校验,此处算错只可能是词表演进后不认识的旧档位,
	// 退回空列表(不露面选择器)比读不出模型更安全。
	effective, err := effort.Effective(m.Protocol, m.NativeModel, m.Efforts)
	if err != nil {
		effective = []string{}
	}
	m.EffortsEffective = effective
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
