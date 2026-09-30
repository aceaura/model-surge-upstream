// Package chat 承载管理面「对话」页：会话与消息落库，发送时按所选模型的
// 出站协议构造上游请求、取回回复并落库。
//
// 上游调用是无状态透传：每轮把整段历史按协议形态重放，不在服务端维护
// 上游侧会话（各家也没有可依赖的通用会话语义）。
package chat

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/aceaura/model-surge-upstream/backend/apperr"
	"github.com/aceaura/model-surge-upstream/backend/resolve"
	"github.com/aceaura/model-surge-upstream/backend/ringlog"
)

// Session 是一次对话。ModelID 记最近一次发送所用模型，供界面回显选择器。
type Session struct {
	ID        string    `json:"id"`
	Title     string    `json:"title"`
	ModelID   string    `json:"model_id"`
	UpdatedAt time.Time `json:"updated_at"`
}

// Message 是一条对话消息。Role 取 user / assistant。
type Message struct {
	ID        int64     `json:"id"`
	SessionID string    `json:"session_id"`
	Role      string    `json:"role"`
	Content   string    `json:"content"`
	CreatedAt time.Time `json:"created_at"`
}

const (
	RoleUser      = "user"
	RoleAssistant = "assistant"

	// 首条用户消息截取为会话标题的长度（按 rune）。
	titleRunes = 24
)

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
		`SELECT id, session_id, role, content, created_at FROM chat_messages
		 WHERE session_id=$1 ORDER BY id ASC`, sessionID)
	if err != nil {
		return nil, apperr.Wrap(apperr.StorageError, "list chat messages", err)
	}
	defer rows.Close()
	out := []Message{}
	for rows.Next() {
		var m Message
		if err := rows.Scan(&m.ID, &m.SessionID, &m.Role, &m.Content, &m.CreatedAt); err != nil {
			return nil, apperr.Wrap(apperr.StorageError, "scan chat message", err)
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
func (r *Repo) append(ctx context.Context, sessionID, role, content string) (Message, error) {
	now := time.Now().UTC()
	var id int64
	err := r.pool.QueryRow(ctx,
		`INSERT INTO chat_messages (session_id, role, content, created_at)
		 VALUES ($1,$2,$3,$4) RETURNING id`, sessionID, role, content, now).Scan(&id)
	if err != nil {
		return Message{}, apperr.Wrap(apperr.StorageError, "append chat message", err)
	}
	if role == RoleUser {
		title := truncate(content, titleRunes)
		_, _ = r.pool.Exec(ctx,
			`UPDATE chat_sessions SET updated_at=$2,
				title = CASE WHEN title = '' OR title = '新对话' THEN $3 ELSE title END
			 WHERE id=$1`, sessionID, now, title)
	} else {
		_, _ = r.pool.Exec(ctx, `UPDATE chat_sessions SET updated_at=$2 WHERE id=$1`, sessionID, now)
	}
	return Message{ID: id, SessionID: sessionID, Role: role, Content: content, CreatedAt: now}, nil
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

// Service 把仓储、模型解析与上游调用串成对话页需要的操作。
type Service struct {
	repo     *Repo
	resolver Resolver
}

// NewService 组装对话服务。
func NewService(repo *Repo, resolver Resolver) *Service {
	return &Service{repo: repo, resolver: resolver}
}

func (s *Service) ListSessions(ctx context.Context) ([]Session, error) {
	return s.repo.ListSessions(ctx)
}

func (s *Service) CreateSession(ctx context.Context) (Session, error) {
	return s.repo.CreateSession(ctx)
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
// 上游失败时用户消息已落库（对话页可见自己发出去的话），错误原样返回。
func (s *Service) Send(ctx context.Context, sessionID, modelID, content string) ([]Message, error) {
	if content == "" {
		return nil, apperr.New(apperr.InvalidRequest, "message content is required")
	}
	if _, err := s.repo.GetSession(ctx, sessionID); err != nil {
		return nil, err
	}
	target, err := s.resolver.Resolve(ctx, modelID)
	if err != nil {
		return nil, err
	}
	history, err := s.repo.ListMessages(ctx, sessionID)
	if err != nil {
		return nil, err
	}
	user, err := s.repo.append(ctx, sessionID, RoleUser, content)
	if err != nil {
		return nil, err
	}
	history = append(history, user)

	reply, err := Complete(ctx, target, history)
	if err != nil {
		ringlog.Push(ringlog.LevelWarn, "chat",
			fmt.Sprintf("session=%s model=%s upstream failed: %v", sessionID, modelID, err))
		return nil, err
	}
	if _, err := s.repo.append(ctx, sessionID, RoleAssistant, reply); err != nil {
		return nil, err
	}
	if err := s.repo.setModel(ctx, sessionID, modelID); err != nil {
		return nil, apperr.Wrap(apperr.StorageError, "record session model", err)
	}
	ringlog.Push(ringlog.LevelInfo, "chat",
		fmt.Sprintf("session=%s model=%s account=%s reply=%d chars", sessionID, modelID, target.Account, len([]rune(reply))))
	return s.repo.ListMessages(ctx, sessionID)
}
