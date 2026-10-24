// Package config 从环境变量加载服务配置。必需项缺失时立即报错并指名，
// 避免服务带着半份配置启动后在首个请求上才暴露问题。
package config

import (
	"fmt"
	"os"
	"strings"
	"time"
)

type Config struct {
	PGDSN       string
	AdminKey    string
	DeliveryKey string
	RedisAddr   string
	Listen      string
	CacheTTL    time.Duration
	QuotaTTL    time.Duration
}

const (
	defaultListen   = ":8080"
	defaultCacheTTL = 5 * time.Minute
	defaultQuotaTTL = 60 * time.Second
)

// Getenv 便于测试注入环境。
type Getenv func(string) string

func Load() (Config, error) { return LoadFrom(os.Getenv) }

func LoadFrom(getenv Getenv) (Config, error) {
	var missing []string
	required := func(key string) string {
		v := strings.TrimSpace(getenv(key))
		if v == "" {
			missing = append(missing, key)
		}
		return v
	}

	cfg := Config{
		PGDSN:       required("MSU_PG_DSN"),
		AdminKey:    required("MSU_ADMIN_KEY"),
		DeliveryKey: required("MSU_DELIVERY_KEY"),
		RedisAddr:   strings.TrimSpace(getenv("MSU_REDIS_ADDR")),
		Listen:      defaultListen,
		CacheTTL:    defaultCacheTTL,
		QuotaTTL:    defaultQuotaTTL,
	}
	if len(missing) > 0 {
		return Config{}, fmt.Errorf("missing required config: %s", strings.Join(missing, ", "))
	}

	if v := strings.TrimSpace(getenv("MSU_LISTEN")); v != "" {
		cfg.Listen = v
	}
	var err error
	if cfg.CacheTTL, err = duration(getenv, "MSU_CACHE_TTL", defaultCacheTTL); err != nil {
		return Config{}, err
	}
	if cfg.QuotaTTL, err = duration(getenv, "MSU_QUOTA_TTL", defaultQuotaTTL); err != nil {
		return Config{}, err
	}
	return cfg, nil
}

func duration(getenv Getenv, key string, fallback time.Duration) (time.Duration, error) {
	raw := strings.TrimSpace(getenv(key))
	if raw == "" {
		return fallback, nil
	}
	d, err := time.ParseDuration(raw)
	if err != nil {
		return 0, fmt.Errorf("invalid %s: %v", key, err)
	}
	if d <= 0 {
		return 0, fmt.Errorf("invalid %s: must be positive", key)
	}
	return d, nil
}
