// Package chat 承载管理面「对话」页：会话与消息落库，发送时按所选模型的
// 出站协议构造上游请求、取回回复并落库。
//
// 上游调用是无状态透传：每轮把整段历史按协议形态重放，不在服务端维护
// 上游侧会话（各家也没有可依赖的通用会话语义）。
package chat

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/aceaura/model-surge-upstream/backend/apperr"
	"github.com/aceaura/model-surge-upstream/backend/effort"
	"github.com/aceaura/model-surge-upstream/backend/kiro"
	"github.com/aceaura/model-surge-upstream/backend/resolve"
	"github.com/aceaura/model-surge-upstream/backend/ringlog"
	"github.com/aceaura/model-surge-upstream/backend/usage"
)

// Session 是一次对话。ModelID 记最近一次发送所用模型，供界面回显选择器。
type Session struct {
	ID        string    `json:"id"`
	Title     string    `json:"title"`
	ModelID   string    `json:"model_id"`
	UpdatedAt time.Time `json:"updated_at"`
}

// ImageAttachment 是消息内嵌的一张图片。Data 是不带 data: 前缀的 base64；
// mime 限于 validateImages 白名单。内嵌落库与「无状态整段重放」自洽：
// 重放构造上游请求时不需要外部取数。
type ImageAttachment struct {
	Mime string `json:"mime"`
	Data string `json:"data"`
}

// Message 是一条对话消息。Role 取 user / assistant。
// Attachments 恒非 nil（无图时为 []），助手消息目前不携带附件。
type Message struct {
	ID          int64             `json:"id"`
	SessionID   string            `json:"session_id"`
	Role        string            `json:"role"`
	Content     string            `json:"content"`
	Attachments []ImageAttachment `json:"attachments"`
	CreatedAt   time.Time         `json:"created_at"`
}

const (
	RoleUser      = "user"
	RoleAssistant = "assistant"

	// 首条用户消息截取为会话标题的长度（按 rune）。
	titleRunes = 24

	// 手工改名上限：侧栏行宽有限，再长截断后也读不全。
	maxTitleRunes = 40

	// 单条消息的图片上限与单张解码后大小上限。张数取主流视觉模型的
	// 常见下限，4 MB 与 Anthropic 单图上限对齐（base64 约 5.3 MB）。
	maxImagesPerMessage = 4
	maxImageBytes       = 4 << 20
)

// imageMimes 取四协议图片类型的交集，放行即可保证各家都能消费。
var imageMimes = map[string]bool{
	"image/png":  true,
	"image/jpeg": true,
	"image/webp": true,
	"image/gif":  true,
}

// validateImages 校验待发消息：文本与图片不可同空（纯图可发）；
// 图片限张数、限 mime 白名单，base64 须可解码且解码后限大小。
func validateImages(content string, images []ImageAttachment) error {
	if content == "" && len(images) == 0 {
		return apperr.New(apperr.InvalidRequest, "message content is required")
	}
	if len(images) > maxImagesPerMessage {
		return apperr.New(apperr.InvalidRequest,
			fmt.Sprintf("too many images (max %d)", maxImagesPerMessage))
	}
	for i, img := range images {
		if !imageMimes[img.Mime] {
			return apperr.New(apperr.InvalidRequest,
				fmt.Sprintf("image %d has unsupported mime %q", i+1, img.Mime))
		}
		raw, err := base64.StdEncoding.DecodeString(img.Data)
		if err != nil {
			return apperr.New(apperr.InvalidRequest,
				fmt.Sprintf("image %d is not valid base64", i+1))
		}
		if len(raw) == 0 {
			return apperr.New(apperr.InvalidRequest, fmt.Sprintf("image %d is empty", i+1))
		}
		if len(raw) > maxImageBytes {
			return apperr.New(apperr.InvalidRequest,
				fmt.Sprintf("image %d exceeds %d MB", i+1, maxImageBytes>>20))
		}
	}
	return nil
}

type Repo struct {
	pool *pgxpool.Pool
}

func NewRepo(pool *pgxpool.Pool) *Repo { return &Repo{pool: pool} }

func newID() string {
	b := make([]byte, 8)
	if _, err := rand.Read(b); err != nil {
		// crypto/rand 失败即环境异常，退而用时间戳保证可用且仍唯一。
		return fmt.Sprintf("%d", time.Now().UnixNano())
	}
	return hex.EncodeToString(b)
}

func (r *Repo) ListSessions(ctx context.Context) ([]Session, error) {
	rows, err := r.pool.Query(ctx,
		`SELECT id, title, model_id, updated_at FROM chat_sessions ORDER BY updated_at DESC`)
	if err != nil {
		return nil, apperr.Wrap(apperr.StorageError, "list chat sessions", err)
	}
	defer rows.Close()
	out := []Session{}
	for rows.Next() {
		var s Session
		if err := rows.Scan(&s.ID, &s.Title, &s.ModelID, &s.UpdatedAt); err != nil {
			return nil, apperr.Wrap(apperr.StorageError, "scan chat session", err)
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

func (r *Repo) CreateSession(ctx context.Context) (Session, error) {
	now := time.Now().UTC()
	s := Session{ID: newID(), Title: "新对话", UpdatedAt: now}
	_, err := r.pool.Exec(ctx,
		`INSERT INTO chat_sessions (id, title, model_id, created_at, updated_at)
		 VALUES ($1,$2,$3,$4,$4)`, s.ID, s.Title, s.ModelID, now)
	if err != nil {
		return Session{}, apperr.Wrap(apperr.StorageError, "create chat session", err)
	}
	return s, nil
}

func (r *Repo) GetSession(ctx context.Context, id string) (Session, error) {
	row := r.pool.QueryRow(ctx,
		`SELECT id, title, model_id, updated_at FROM chat_sessions WHERE id=$1`, id)
	var s Session
	if err := row.Scan(&s.ID, &s.Title, &s.ModelID, &s.UpdatedAt); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return Session{}, apperr.New(apperr.NotFound, fmt.Sprintf("chat session %q not found", id))
		}
		return Session{}, apperr.Wrap(apperr.StorageError, "read chat session", err)
	}
	return s, nil
}

// RenameSession 改会话标题。自动标题只在标题为空或「新对话」时生效，
// 故手工改过的标题不会被后续消息覆盖。
func (r *Repo) RenameSession(ctx context.Context, id, title string) (Session, error) {
	if _, err := r.GetSession(ctx, id); err != nil {
		return Session{}, err
	}
	if _, err := r.pool.Exec(ctx,
		`UPDATE chat_sessions SET title=$2 WHERE id=$1`, id, title); err != nil {
		return Session{}, apperr.Wrap(apperr.StorageError, "rename chat session", err)
	}
	return r.GetSession(ctx, id)
}

func (r *Repo) DeleteSession(ctx context.Context, id string) error {
	tag, err := r.pool.Exec(ctx, `DELETE FROM chat_sessions WHERE id=$1`, id)
	if err != nil {
		return apperr.Wrap(apperr.StorageError, "delete chat session", err)
	}
	if tag.RowsAffected() == 0 {
		return apperr.New(apperr.NotFound, fmt.Sprintf("chat session %q not found", id))
	}
	return nil
}

func (r *Repo) ListMessages(ctx context.Context, sessionID string) ([]Message, error) {
	rows, err := r.pool.Query(ctx,
		`SELECT id, session_id, role, content, attachments, created_at FROM chat_messages
		 WHERE session_id=$1 ORDER BY id ASC`, sessionID)
	if err != nil {
		return nil, apperr.Wrap(apperr.StorageError, "list chat messages", err)
	}
	defer rows.Close()
	out := []Message{}
	for rows.Next() {
		var m Message
		var atts []byte
		if err := rows.Scan(&m.ID, &m.SessionID, &m.Role, &m.Content, &atts, &m.CreatedAt); err != nil {
			return nil, apperr.Wrap(apperr.StorageError, "scan chat message", err)
		}
		m.Attachments = []ImageAttachment{}
		if len(atts) > 0 {
			if err := json.Unmarshal(atts, &m.Attachments); err != nil {
				return nil, apperr.Wrap(apperr.StorageError, "decode message attachments", err)
			}
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

func (r *Repo) ClearMessages(ctx context.Context, sessionID string) error {
	if _, err := r.GetSession(ctx, sessionID); err != nil {
		return err
	}
	if _, err := r.pool.Exec(ctx, `DELETE FROM chat_messages WHERE session_id=$1`, sessionID); err != nil {
		return apperr.Wrap(apperr.StorageError, "clear chat messages", err)
	}
	return nil
}

// append 落一条消息并刷新会话的 updated_at；首条用户消息顺带定标题。
// 纯图消息没有文本可截，标题退为「[图片]」避免被置空。
func (r *Repo) append(ctx context.Context, sessionID, role, content string, attachments []ImageAttachment) (Message, error) {
	now := time.Now().UTC()
	if attachments == nil {
		attachments = []ImageAttachment{}
	}
	attsJSON, err := json.Marshal(attachments)
	if err != nil {
		return Message{}, apperr.Wrap(apperr.InvalidJSON, "encode message attachments", err)
	}
	var id int64
	err = r.pool.QueryRow(ctx,
		`INSERT INTO chat_messages (session_id, role, content, attachments, created_at)
		 VALUES ($1,$2,$3,$4,$5) RETURNING id`, sessionID, role, content, attsJSON, now).Scan(&id)
	if err != nil {
		return Message{}, apperr.Wrap(apperr.StorageError, "append chat message", err)
	}
	if role == RoleUser {
		title := truncate(content, titleRunes)
		if title == "" {
			title = "[图片]"
		}
		_, _ = r.pool.Exec(ctx,
			`UPDATE chat_sessions SET updated_at=$2,
				title = CASE WHEN title = '' OR title = '新对话' THEN $3 ELSE title END
			 WHERE id=$1`, sessionID, now, title)
	} else {
		_, _ = r.pool.Exec(ctx, `UPDATE chat_sessions SET updated_at=$2 WHERE id=$1`, sessionID, now)
	}
	return Message{ID: id, SessionID: sessionID, Role: role, Content: content, Attachments: attachments, CreatedAt: now}, nil
}

// setModel 记录会话最近使用的模型（界面选择器回显）。
func (r *Repo) setModel(ctx context.Context, sessionID, modelID string) error {
	_, err := r.pool.Exec(ctx, `UPDATE chat_sessions SET model_id=$2 WHERE id=$1`, sessionID, modelID)
	return err
}

func truncate(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "…"
}

// Resolver 是 chat 需要的最小解析能力。声明在消费方，
// 装配层可传入带日志的装饰器而不必改 resolve 包。
type Resolver interface {
	Resolve(ctx context.Context, modelID string) (resolve.ResolvedTarget, error)
}

// TokenInvalidator 作废本次请求使用的令牌，供重新解析时续期。
type TokenInvalidator interface {
	Invalidate(name, token string)
}

// Service 把仓储、模型解析与上游调用串成对话页需要的操作。
type Service struct {
	repo        *Repo
	resolver    Resolver
	record      UsageRecorder
	invalidator TokenInvalidator
}

// UsageRecord 对话一轮补全的旁路用量记录：成功失败都记。
type UsageRecord struct {
	Target       resolve.ResolvedTarget
	Usage        usage.Usage
	StatusCode   int
	DurationMS   int64
	ErrorMessage string
}

// UsageRecorder 接收对话用量。实现方自行起 goroutine，nil 表示不统计。
type UsageRecorder func(ctx context.Context, rec UsageRecord)

// NewService 组装对话服务。
func NewService(repo *Repo, resolver Resolver, record UsageRecorder) *Service {
	return &Service{repo: repo, resolver: resolver, record: record}
}

func (s *Service) WithTokenInvalidator(invalidator TokenInvalidator) *Service {
	s.invalidator = invalidator
	return s
}

func (s *Service) ListSessions(ctx context.Context) ([]Session, error) {
	return s.repo.ListSessions(ctx)
}

func (s *Service) CreateSession(ctx context.Context) (Session, error) {
	return s.repo.CreateSession(ctx)
}

// RenameSession 手工改会话标题：去首尾空白、非空、限长。
func (s *Service) RenameSession(ctx context.Context, id, title string) (Session, error) {
	t := strings.TrimSpace(title)
	if t == "" {
		return Session{}, apperr.New(apperr.InvalidRequest, "session title is required")
	}
	if len([]rune(t)) > maxTitleRunes {
		return Session{}, apperr.New(apperr.InvalidRequest,
			fmt.Sprintf("session title too long (max %d runes)", maxTitleRunes))
	}
	return s.repo.RenameSession(ctx, id, t)
}

func (s *Service) DeleteSession(ctx context.Context, id string) error {
	return s.repo.DeleteSession(ctx, id)
}

func (s *Service) Messages(ctx context.Context, id string) (Session, []Message, error) {
	sess, err := s.repo.GetSession(ctx, id)
	if err != nil {
		return Session{}, nil, err
	}
	msgs, err := s.repo.ListMessages(ctx, id)
	if err != nil {
		return Session{}, nil, err
	}
	return sess, msgs, nil
}

func (s *Service) ClearMessages(ctx context.Context, id string) error {
	return s.repo.ClearMessages(ctx, id)
}

// Send 追加用户消息 → 带整段历史上游补全 → 追加助手回复。
// images 为用户消息内嵌的图片（可为空）；effort 为推理档（空=默认，
// 须在模型的有效支持列表内，见 effort 包）。上游失败时用户消息已落库
// （对话页可见自己发出去的话），错误原样返回。
func (s *Service) Send(ctx context.Context, sessionID, modelID, content, effortLevel string, images []ImageAttachment) ([]Message, error) {
	if err := validateImages(content, images); err != nil {
		return nil, err
	}
	if _, err := s.repo.GetSession(ctx, sessionID); err != nil {
		return nil, err
	}
	target, err := s.resolver.Resolve(ctx, modelID)
	if err != nil {
		return nil, err
	}
	if effortLevel != "" && !effort.ContainsValue(target.Efforts, effortLevel) {
		return nil, apperr.New(apperr.InvalidRequest,
			fmt.Sprintf("model %q does not support reasoning effort %q (supported: %s)",
				modelID, effortLevel, strings.Join(effort.Values(target.Efforts), ", ")))
	}
	history, err := s.repo.ListMessages(ctx, sessionID)
	if err != nil {
		return nil, err
	}
	user, err := s.repo.append(ctx, sessionID, RoleUser, content, images)
	if err != nil {
		return nil, err
	}
	history = append(history, user)

	reply, target, err := s.complete(ctx, target, modelID, sessionID, effortLevel, history)
	if err != nil {
		ringlog.Push(ringlog.LevelWarn, "chat",
			fmt.Sprintf("session=%s model=%s account=%s upstream failed: %v", sessionID, modelID, target.Account, err))
		return nil, err
	}
	if _, err := s.repo.append(ctx, sessionID, RoleAssistant, reply, nil); err != nil {
		return nil, err
	}
	if err := s.repo.setModel(ctx, sessionID, modelID); err != nil {
		return nil, apperr.Wrap(apperr.StorageError, "record session model", err)
	}
	ringlog.Push(ringlog.LevelInfo, "chat",
		fmt.Sprintf("session=%s model=%s account=%s reply=%d chars", sessionID, modelID, target.Account, len([]rune(reply))))
	return s.repo.ListMessages(ctx, sessionID)
}

func (s *Service) complete(ctx context.Context, target resolve.ResolvedTarget, modelID, sessionKey, effortLevel string, history []Message) (string, resolve.ResolvedTarget, error) {
	start := time.Now()
	isKiro := target.ProviderID == kiro.ProviderID
	reply, u, status, err := Complete(ctx, target, sessionKey, effortLevel, history)
	safeError := "Kiro upstream completion failed"
	if isKiro && status == http.StatusForbidden && s.invalidator != nil {
		token := ""
		for name, auth := range target.Headers {
			if strings.EqualFold(name, "Authorization") && len(auth) >= 7 && strings.EqualFold(auth[:7], "Bearer ") {
				token = strings.TrimSpace(auth[7:])
				break
			}
		}
		s.invalidator.Invalidate(target.Account, token)
		fresh, resolveErr := s.resolver.Resolve(ctx, modelID)
		if resolveErr != nil {
			err = resolveErr
			safeError = "resolve Kiro model after token invalidation failed"
		} else {
			target = fresh
			reply, u, status, err = Complete(ctx, target, sessionKey, effortLevel, history)
		}
	}
	if isKiro && err != nil {
		// 不保留上游响应或续期错误的 cause，避免凭据进入返回值与日志。
		err = apperr.New(apperr.CodeOf(err), fmt.Sprintf("%s (HTTP %d)", safeError, status))
	}
	elapsed := time.Since(start)
	if s.record != nil {
		errMsg := ""
		if err != nil {
			errMsg = err.Error()
		}
		// 统计是旁路：上下文脱钩请求生命周期，落库不随响应结束而取消。
		s.record(context.WithoutCancel(ctx), UsageRecord{
			Target:       target,
			Usage:        u,
			StatusCode:   status,
			DurationMS:   elapsed.Milliseconds(),
			ErrorMessage: errMsg,
		})
	}
	return reply, target, err
}
