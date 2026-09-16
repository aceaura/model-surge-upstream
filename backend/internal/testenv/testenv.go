// Package testenv 为集成测试提供门控的 PG/Redis 环境。
// TEST_PG_DSN 未设时集成测试跳过，保证纯单元测试可离线运行。
//
// 每个测试二进制拿到自己的 PG schema 与 Redis 逻辑库：`go test ./...` 会并行
// 跑多个包，共用一套表会让彼此的清库操作互相截断。
package testenv

import (
	"context"
	"crypto/sha1"
	"encoding/binary"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/aceaura/model-surge-upstream/backend/cache"
	"github.com/aceaura/model-surge-upstream/backend/store"
)

// redisDBCount 是 Redis 默认的逻辑库数量。
const redisDBCount = 16

// Store 打开该测试二进制专属 schema 下的库并清空两张表。
func Store(t *testing.T) *store.Store {
	t.Helper()
	dsn := os.Getenv("TEST_PG_DSN")
	if dsn == "" {
		t.Skip("TEST_PG_DSN not set")
	}
	ctx := context.Background()
	schema := schemaName()

	if err := ensureSchema(ctx, dsn, schema); err != nil {
		t.Fatalf("prepare test schema: %v", err)
	}
	scoped, err := withSearchPath(dsn, schema)
	if err != nil {
		t.Fatalf("scope dsn: %v", err)
	}

	s, err := store.Open(ctx, scoped)
	if err != nil {
		t.Fatalf("open test postgres: %v", err)
	}
	t.Cleanup(s.Close)
	if _, err := s.Pool().Exec(ctx, `TRUNCATE models, accounts CASCADE`); err != nil {
		t.Fatalf("truncate: %v", err)
	}
	return s
}

// Cache 返回真实 Redis 缓存；未配置 TEST_REDIS_ADDR 时返回无后端缓存
// （行为等价于未部署 Redis，仍可跑通全部读写路径）。
func Cache(t *testing.T) *cache.Cache {
	t.Helper()
	addr := os.Getenv("TEST_REDIS_ADDR")
	if addr == "" {
		return cache.New(nil, time.Minute)
	}
	return cache.New(redisBackend(addr), time.Minute)
}

// RedisRequired 用于必须有 Redis 才有意义的用例。
func RedisRequired(t *testing.T) cache.Backend {
	t.Helper()
	addr := os.Getenv("TEST_REDIS_ADDR")
	if addr == "" {
		t.Skip("TEST_REDIS_ADDR not set")
	}
	return redisBackend(addr)
}

func redisBackend(addr string) cache.Backend {
	return cache.NewRedisDB(addr, int(hash()%redisDBCount))
}

func ensureSchema(ctx context.Context, dsn, schema string) error {
	conn, err := pgx.Connect(ctx, dsn)
	if err != nil {
		return err
	}
	defer conn.Close(ctx)
	_, err = conn.Exec(ctx, `CREATE SCHEMA IF NOT EXISTS `+pgx.Identifier{schema}.Sanitize())
	return err
}

func withSearchPath(dsn, schema string) (string, error) {
	u, err := url.Parse(dsn)
	if err != nil {
		return "", err
	}
	q := u.Query()
	q.Set("search_path", schema)
	u.RawQuery = q.Encode()
	return u.String(), nil
}

// schemaName 由测试二进制名派生，同包多次运行落在同一 schema。
func schemaName() string {
	base := strings.ToLower(filepath.Base(os.Args[0]))
	base = strings.TrimSuffix(base, ".exe")
	base = strings.TrimSuffix(base, ".test")
	cleaned := strings.Map(func(r rune) rune {
		if r >= 'a' && r <= 'z' || r >= '0' && r <= '9' {
			return r
		}
		return '_'
	}, base)
	return fmt.Sprintf("msu_test_%s_%d", cleaned, hash()%1000)
}

func hash() uint32 {
	sum := sha1.Sum([]byte(filepath.Base(os.Args[0])))
	return binary.BigEndian.Uint32(sum[:4])
}
