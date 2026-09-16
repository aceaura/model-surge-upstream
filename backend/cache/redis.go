package cache

import (
	"context"
	"time"

	"github.com/redis/go-redis/v9"
)

// opTimeout 给每次缓存操作设上限。缓存是加速手段：Redis 卡住时应尽快
// 判定为不可用并回落到 PostgreSQL，而不是把请求一起拖住。
const opTimeout = 500 * time.Millisecond

type redisBackend struct {
	client *redis.Client
}

// NewRedis 只构造客户端，不在此处 ping：Redis 不可用不该阻塞启动。
func NewRedis(addr string) Backend { return NewRedisDB(addr, 0) }

// NewRedisDB 指定逻辑库序号，供并行测试彼此隔离键空间。
func NewRedisDB(addr string, db int) Backend {
	return &redisBackend{client: redis.NewClient(&redis.Options{
		Addr:         addr,
		DB:           db,
		DialTimeout:  opTimeout,
		ReadTimeout:  opTimeout,
		WriteTimeout: opTimeout,
	})}
}

func (r *redisBackend) Get(ctx context.Context, key string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, opTimeout)
	defer cancel()
	return r.client.Get(ctx, key).Bytes()
}

func (r *redisBackend) Set(ctx context.Context, key string, value []byte, ttl time.Duration) error {
	ctx, cancel := context.WithTimeout(ctx, opTimeout)
	defer cancel()
	return r.client.Set(ctx, key, value, ttl).Err()
}

func (r *redisBackend) Del(ctx context.Context, key string) error {
	ctx, cancel := context.WithTimeout(ctx, opTimeout)
	defer cancel()
	return r.client.Del(ctx, key).Err()
}

func (r *redisBackend) Ready(ctx context.Context) bool {
	ctx, cancel := context.WithTimeout(ctx, opTimeout)
	defer cancel()
	return r.client.Ping(ctx).Err() == nil
}

func (r *redisBackend) Close() error { return r.client.Close() }
