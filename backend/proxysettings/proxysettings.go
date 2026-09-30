// Package proxysettings 是代理转发面配置的仓储：PostgreSQL 单行表权威。
// 配置运行期可改且应用后立即生效（重绑监听），故落库而非走环境变量。
// 不进 Redis 缓存：读取频次极低（管理面查看 + 进程启动），直读 PG 即可。
package proxysettings

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/aceaura/model-surge-upstream/backend/apperr"
)

// DefaultPort 是转发面未配置时的默认端口。
const DefaultPort = 12344

// Settings 是转发面的全部可调项。APIKey 为空表示转发面关闭——
// 密钥是客户端唯一凭据，没有密钥就没有可校验的身份，监听无意义。
type Settings struct {
	APIKey    string    `json:"api_key"`
	Port      int       `json:"port"`
	LanOpen   bool      `json:"lan_open"`
	UpdatedAt time.Time `json:"updated_at"`
}

// ListenAddr 由端口与局域网开关推导监听地址：
// 关闭时只绑回环（本机客户端可用），开启时绑全部接口（局域网可达）。
func (s Settings) ListenAddr() string {
	if s.LanOpen {
		return fmt.Sprintf(":%d", s.Port)
	}
	return fmt.Sprintf("127.0.0.1:%d", s.Port)
}

// Enabled 表示转发面是否应监听。密钥为空即关闭。
func (s Settings) Enabled() bool { return s.APIKey != "" }

// Validate 校验可写字段。密钥允许为空（= 关闭转发面）；
// 端口必须在合法范围内，不与周知保留端口混淆由运维自负。
func Validate(s Settings) error {
	if p := s.Port; p < 1 || p > 65535 {
		return apperr.New(apperr.InvalidRequest, "port must be between 1 and 65535")
	}
	if strings.ContainsAny(s.APIKey, " \t\r\n") {
		return apperr.New(apperr.InvalidRequest, "api_key must not contain whitespace")
	}
	return nil
}

type Repo struct {
	pool *pgxpool.Pool
}

func NewRepo(pool *pgxpool.Pool) *Repo { return &Repo{pool: pool} }

// Get 读取当前配置。默认行由建表脚本保证存在，读不到即存储故障。
func (r *Repo) Get(ctx context.Context) (Settings, error) {
	var s Settings
	err := r.pool.QueryRow(ctx,
		`SELECT api_key, port, lan_open, updated_at FROM proxy_settings WHERE id = 1`,
	).Scan(&s.APIKey, &s.Port, &s.LanOpen, &s.UpdatedAt)
	if err != nil {
		return Settings{}, apperr.Wrap(apperr.StorageError, "read proxy settings", err)
	}
	return s, nil
}

// Put 全量替换配置。调用方负责先校验（Validate）。
func (r *Repo) Put(ctx context.Context, s Settings) error {
	_, err := r.pool.Exec(ctx,
		`UPDATE proxy_settings SET api_key = $1, port = $2, lan_open = $3, updated_at = now() WHERE id = 1`,
		s.APIKey, s.Port, s.LanOpen)
	if err != nil {
		return apperr.Wrap(apperr.StorageError, "write proxy settings", err)
	}
	return nil
}
