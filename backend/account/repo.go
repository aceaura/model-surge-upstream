package account

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
	"github.com/aceaura/model-surge-upstream/backend/credential"
	"github.com/aceaura/model-surge-upstream/backend/provider"
)

const columns = `name, provider_id, credential, base_url, headers, quota_settings, enabled, created_at, updated_at, sort_order`

type Repo struct {
	pool  *pgxpool.Pool
	cache *cache.Cache
}

func NewRepo(pool *pgxpool.Pool, c *cache.Cache) *Repo {
	return &Repo{pool: pool, cache: c}
}

// Input 是创建与更新的入参。Update 时 Credential 为零值表示保留原凭据，
// QuotaSettings 为 nil 表示保留原配置；要清除配置传零值 QuotaSettings 指针。
type Input struct {
	Name          string
	ProviderID    string
	Credential    credential.Credential
	BaseURL       string
	Headers       map[string]string
	QuotaSettings *QuotaSettings
	Enabled       bool
}

func (r *Repo) Create(ctx context.Context, in Input) (Account, error) {
	acc, err := validate(in)
	if err != nil {
		return Account{}, err
	}
	now := time.Now().UTC()
	acc.CreatedAt, acc.UpdatedAt = now, now

	credRaw, headersRaw, settingsRaw, err := encode(acc)
	if err != nil {
		return Account{}, err
	}
	// 新账号排到现有手动序之后;全空库从 0 起。
	if err := r.pool.QueryRow(ctx, `SELECT COALESCE(MAX(sort_order), -1) + 1 FROM accounts`).Scan(&acc.SortOrder); err != nil {
		return Account{}, apperr.Wrap(apperr.StorageError, "next sort_order", err)
	}
	persist := func() error {
		_, err := r.pool.Exec(ctx, `INSERT INTO accounts (`+columns+`)
			VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10)`,
			acc.Name, acc.ProviderID, credRaw, acc.BaseURL, headersRaw, settingsRaw, acc.Enabled, acc.CreatedAt, acc.UpdatedAt, acc.SortOrder)
		return mapWriteErr(err, "account", acc.Name)
	}
	if err := cache.WriteThrough(ctx, r.cache, cache.AccountKey(acc.Name), acc, persist); err != nil {
		return Account{}, err
	}
	return acc, nil
}

func (r *Repo) Get(ctx context.Context, name string) (Account, error) {
	return cache.ReadThrough(ctx, r.cache, cache.AccountKey(name), func() (Account, error) {
		row := r.pool.QueryRow(ctx, `SELECT `+columns+` FROM accounts WHERE name=$1`, name)
		acc, err := scan(row)
		if errors.Is(err, pgx.ErrNoRows) {
			return Account{}, apperr.New(apperr.NotFound, fmt.Sprintf("account %q not found", name))
		}
		if err != nil {
			return Account{}, apperr.Wrap(apperr.StorageError, "read account", err)
		}
		return acc, nil
	})
}

// List 直读 PG：管理面列举频率低，列表缓存的失效面不划算。
func (r *Repo) List(ctx context.Context) ([]Account, error) {
	rows, err := r.pool.Query(ctx, `SELECT `+columns+` FROM accounts ORDER BY sort_order, name`)
	if err != nil {
		return nil, apperr.Wrap(apperr.StorageError, "list accounts", err)
	}
	defer rows.Close()

	out := []Account{}
	for rows.Next() {
		acc, err := scan(rows)
		if err != nil {
			return nil, apperr.Wrap(apperr.StorageError, "scan account", err)
		}
		out = append(out, acc)
	}
	if err := rows.Err(); err != nil {
		return nil, apperr.Wrap(apperr.StorageError, "list accounts", err)
	}
	return out, nil
}

// Reorder 按给定账号名顺序把 sort_order 依次写为 0..n-1,单事务提交;
// 任一名字不存在即整批回滚报 NotFound。缓存里的 Account 带旧序号,一并失效。
func (r *Repo) Reorder(ctx context.Context, names []string) error {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return apperr.Wrap(apperr.StorageError, "begin tx", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	for i, name := range names {
		tag, err := tx.Exec(ctx, `UPDATE accounts SET sort_order=$1 WHERE name=$2`, i, name)
		if err != nil {
			return apperr.Wrap(apperr.StorageError, "reorder accounts", err)
		}
		if tag.RowsAffected() == 0 {
			return apperr.New(apperr.NotFound, fmt.Sprintf("account %q not found", name))
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return apperr.Wrap(apperr.StorageError, "commit reorder", err)
	}
	keys := make([]string, 0, len(names))
	for _, n := range names {
		keys = append(keys, cache.AccountKey(n))
	}
	r.cache.Invalidate(ctx, keys...)
	return nil
}

func (r *Repo) Update(ctx context.Context, in Input) (Account, error) {
	existing, err := r.Get(ctx, in.Name)
	if err != nil {
		return Account{}, err
	}
	if in.ProviderID == "" {
		in.ProviderID = existing.ProviderID
	}
	if in.Credential.Kind == "" {
		in.Credential = existing.Credential
	} else if in.Credential.Kind == provider.CredAPIKey &&
		existing.Credential.Kind == provider.CredAPIKey {
		in.Credential = mergeAPIKeyCredential(in.Credential, existing.Credential)
	} else if in.ProviderID == existing.ProviderID &&
		in.Credential.Kind == provider.CredKiroRefresh && existing.Credential.Kind == provider.CredKiroRefresh {
		in.Credential = mergeKiroCredential(in.Credential, existing.Credential)
	}
	if in.QuotaSettings == nil {
		in.QuotaSettings = existing.QuotaSettings
	}
	acc, err := validate(in)
	if err != nil {
		return Account{}, err
	}
	acc.CreatedAt = existing.CreatedAt
	acc.SortOrder = existing.SortOrder
	acc.UpdatedAt = time.Now().UTC()

	credRaw, headersRaw, settingsRaw, err := encode(acc)
	if err != nil {
		return Account{}, err
	}
	persist := func() error {
		tag, err := r.pool.Exec(ctx, `UPDATE accounts SET
			provider_id=$2, credential=$3, base_url=$4, headers=$5, quota_settings=$6, enabled=$7, updated_at=$8
			WHERE name=$1`,
			acc.Name, acc.ProviderID, credRaw, acc.BaseURL, headersRaw, settingsRaw, acc.Enabled, acc.UpdatedAt)
		if err != nil {
			return apperr.Wrap(apperr.StorageError, "update account", err)
		}
		if tag.RowsAffected() == 0 {
			return apperr.New(apperr.NotFound, fmt.Sprintf("account %q not found", acc.Name))
		}
		return nil
	}
	if err := cache.WriteThrough(ctx, r.cache, cache.AccountKey(acc.Name), acc, persist); err != nil {
		return Account{}, err
	}
	return acc, nil
}

// api_key 凭据按字段留空保留,避免换推理密钥时误清网页/控制台令牌。
func mergeAPIKeyCredential(in, existing credential.Credential) credential.Credential {
	if strings.TrimSpace(in.APIKey) == "" {
		in.APIKey = existing.APIKey
	}
	if in.WebRefreshToken == "" {
		in.WebRefreshToken = existing.WebRefreshToken
	}
	if strings.TrimSpace(in.ConsoleAccessToken) == "" {
		in.ConsoleAccessToken = existing.ConsoleAccessToken
	}
	return in
}

func mergeKiroCredential(in, existing credential.Credential) credential.Credential {
	if strings.TrimSpace(in.RefreshToken) == "" {
		in.RefreshToken = existing.RefreshToken
	}
	if in.ClientID != "" && in.ClientID == existing.ClientID && in.ClientSecret == "" {
		in.ClientSecret = existing.ClientSecret
	}
	if in.RefreshToken == existing.RefreshToken && in.KiroAuthRegion() == existing.KiroAuthRegion() &&
		in.ClientID == existing.ClientID && in.ClientSecret == existing.ClientSecret && in.AccessToken == "" {
		in.AccessToken, in.Expiry = existing.AccessToken, existing.Expiry
	}
	return in
}

// UpdateCredential 只回写凭据字段:OAuth 续期产物(access_token/expiry/轮换
// 的 refresh_token)落库,其余字段不动。续期单飞在 oauth 包内,这里不做去重。
func (r *Repo) UpdateCredential(ctx context.Context, name string, cred credential.Credential) error {
	acc, err := r.Get(ctx, name)
	if err != nil {
		return err
	}
	acc.Credential = cred
	acc.UpdatedAt = time.Now().UTC()
	credRaw, err := cred.Encode()
	if err != nil {
		return apperr.Wrap(apperr.InvalidCredential, "encode credential", err)
	}
	persist := func() error {
		tag, err := r.pool.Exec(ctx, `UPDATE accounts SET credential=$2, updated_at=$3 WHERE name=$1`,
			name, credRaw, acc.UpdatedAt)
		if err != nil {
			return apperr.Wrap(apperr.StorageError, "update account credential", err)
		}
		if tag.RowsAffected() == 0 {
			return apperr.New(apperr.NotFound, fmt.Sprintf("account %q not found", name))
		}
		return nil
	}
	return cache.WriteThrough(ctx, r.cache, cache.AccountKey(name), acc, persist)
}

// Delete 在单事务内先删模型再删账号，返回被级联删除的模型标识，
// 供上层失效缓存并向运维者回报影响范围。
func (r *Repo) Delete(ctx context.Context, name string) ([]string, error) {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return nil, apperr.Wrap(apperr.StorageError, "begin tx", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	rows, err := tx.Query(ctx, `DELETE FROM models WHERE account=$1 RETURNING id`, name)
	if err != nil {
		return nil, apperr.Wrap(apperr.StorageError, "cascade delete models", err)
	}
	modelIDs := []string{}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return nil, apperr.Wrap(apperr.StorageError, "scan deleted model", err)
		}
		modelIDs = append(modelIDs, id)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, apperr.Wrap(apperr.StorageError, "cascade delete models", err)
	}

	tag, err := tx.Exec(ctx, `DELETE FROM accounts WHERE name=$1`, name)
	if err != nil {
		return nil, apperr.Wrap(apperr.StorageError, "delete account", err)
	}
	if tag.RowsAffected() == 0 {
		return nil, apperr.New(apperr.NotFound, fmt.Sprintf("account %q not found", name))
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, apperr.Wrap(apperr.StorageError, "commit delete", err)
	}

	keys := make([]string, 0, len(modelIDs)+1)
	keys = append(keys, cache.AccountKey(name))
	for _, id := range modelIDs {
		keys = append(keys, cache.ModelKey(id))
	}
	r.cache.Invalidate(ctx, keys...)
	return modelIDs, nil
}

// CountModels 供删除确认文案使用。
func (r *Repo) CountModels(ctx context.Context, name string) (int, error) {
	var n int
	if err := r.pool.QueryRow(ctx, `SELECT count(*) FROM models WHERE account=$1`, name).Scan(&n); err != nil {
		return 0, apperr.Wrap(apperr.StorageError, "count models", err)
	}
	return n, nil
}

func validate(in Input) (Account, error) {
	name := strings.TrimSpace(in.Name)
	if name == "" {
		return Account{}, apperr.New(apperr.InvalidRequest, "account name is required")
	}
	spec, ok := provider.Get(in.ProviderID)
	if !ok {
		return Account{}, apperr.New(apperr.InvalidProvider,
			fmt.Sprintf("unknown provider %q, available: %s", in.ProviderID, strings.Join(provider.IDs(), ", ")))
	}
	if err := in.Credential.ValidateAgainstProvider(spec); err != nil {
		return Account{}, apperr.New(apperr.InvalidCredential, err.Error())
	}
	base := strings.TrimRight(strings.TrimSpace(in.BaseURL), "/")
	if base != "" && !strings.HasPrefix(base, "http://") && !strings.HasPrefix(base, "https://") {
		return Account{}, apperr.New(apperr.InvalidRequest, "base_url must start with http:// or https://")
	}
	headers := in.Headers
	if headers == nil {
		headers = map[string]string{}
	}
	if in.QuotaSettings != nil {
		s := *in.QuotaSettings
		if s.AutoIntervalMinutes < 0 || s.AutoIntervalMinutes > 1440 {
			return Account{}, apperr.New(apperr.InvalidRequest, "quota_settings auto_interval_minutes must be between 0 and 1440")
		}
		if s.StopIntervalMinutes < 0 || s.StopIntervalMinutes > 1440 {
			return Account{}, apperr.New(apperr.InvalidRequest, "quota_settings stop_interval_minutes must be between 0 and 1440")
		}
		in.QuotaSettings = &s
	}
	return Account{
		Name:          name,
		ProviderID:    in.ProviderID,
		Credential:    in.Credential,
		BaseURL:       base,
		Headers:       headers,
		QuotaSettings: in.QuotaSettings,
		Enabled:       in.Enabled,
	}, nil
}

func encode(a Account) (credRaw, headersRaw, settingsRaw []byte, err error) {
	if credRaw, err = a.Credential.Encode(); err != nil {
		return nil, nil, nil, apperr.Wrap(apperr.InvalidCredential, "encode credential", err)
	}
	if headersRaw, err = json.Marshal(a.Headers); err != nil {
		return nil, nil, nil, apperr.Wrap(apperr.InvalidJSON, "encode headers", err)
	}
	// 未配置落 '{}',与列默认值同形,读取侧统一按未配置处理。
	if a.QuotaSettings == nil {
		settingsRaw = []byte(`{}`)
	} else if settingsRaw, err = json.Marshal(a.QuotaSettings); err != nil {
		return nil, nil, nil, apperr.Wrap(apperr.InvalidJSON, "encode quota_settings", err)
	}
	return credRaw, headersRaw, settingsRaw, nil
}

type scanner interface {
	Scan(dest ...any) error
}

func scan(s scanner) (Account, error) {
	var (
		a           Account
		credRaw     []byte
		headersRaw  []byte
		settingsRaw []byte
	)
	if err := s.Scan(&a.Name, &a.ProviderID, &credRaw, &a.BaseURL, &headersRaw, &settingsRaw, &a.Enabled, &a.CreatedAt, &a.UpdatedAt, &a.SortOrder); err != nil {
		return Account{}, err
	}
	cred, err := credential.Decode(credRaw)
	if err != nil {
		return Account{}, fmt.Errorf("account %q: %w", a.Name, err)
	}
	a.Credential = cred
	if err := json.Unmarshal(headersRaw, &a.Headers); err != nil {
		return Account{}, fmt.Errorf("account %q headers: %w", a.Name, err)
	}
	if a.Headers == nil {
		a.Headers = map[string]string{}
	}
	var settings QuotaSettings
	if err := json.Unmarshal(settingsRaw, &settings); err != nil {
		return Account{}, fmt.Errorf("account %q quota_settings: %w", a.Name, err)
	}
	// 两间隔均为 0 不保留指针,读取形态与未配置一致。
	if !settings.Empty() {
		a.QuotaSettings = &settings
	}
	return a, nil
}

func mapWriteErr(err error, kind, id string) error {
	if err == nil {
		return nil
	}
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == "23505" {
		return apperr.New(apperr.AlreadyExists, fmt.Sprintf("%s %q already exists", kind, id))
	}
	return apperr.Wrap(apperr.StorageError, "write "+kind, err)
}
