// Package config 从环境变量加载服务配置。必需项缺失时立即报错并指名，
// 避免服务带着半份配置启动后在首个请求上才暴露问题。
package config

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/aceaura/model-surge-upstream/backend/compact"
)

type Config struct {
	PGDSN       string
	AdminKey    string
	DeliveryKey string
	RedisAddr   string
	Listen      string
	CacheTTL    time.Duration
	QuotaTTL    time.Duration
	// Compact 是上下文压缩的全局默认，模型级 compact JSON 可逐项覆盖。
	Compact compact.Defaults
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
	if cfg.Compact, err = compactDefaults(getenv); err != nil {
		return Config{}, err
	}
	return cfg, nil
}

// compactDefaults 解析 MSU_COMPACT_* 环境变量为压缩全局默认。
// 模式默认 passive（只记录不生效），模型级配置可覆盖每一项。
func compactDefaults(getenv Getenv) (compact.Defaults, error) {
	d := compact.DefaultConfig()
	if v := strings.TrimSpace(getenv("MSU_COMPACT_MODE")); v != "" {
		switch m := compact.Mode(v); m {
		case compact.ModePassive, compact.ModeError, compact.ModeAuto:
			d.Mode = m
		default:
			return compact.Defaults{}, fmt.Errorf("invalid MSU_COMPACT_MODE: %q (want passive|error|auto)", v)
		}
	}
	if v := strings.TrimSpace(getenv("MSU_COMPACT_THRESHOLD")); v != "" {
		f, err := strconv.ParseFloat(v, 64)
		if err != nil || f <= 0 || f > 1 {
			return compact.Defaults{}, fmt.Errorf("invalid MSU_COMPACT_THRESHOLD: %q (want 0<x<=1)", v)
		}
		d.Threshold = f
	}
	if v := strings.TrimSpace(getenv("MSU_COMPACT_KEEP_TURNS")); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 {
			return compact.Defaults{}, fmt.Errorf("invalid MSU_COMPACT_KEEP_TURNS: %q (want positive int)", v)
		}
		d.KeepTurns = n
	}
	if v := strings.TrimSpace(getenv("MSU_COMPACT_MAX_SUMMARY_TOKENS")); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 {
			return compact.Defaults{}, fmt.Errorf("invalid MSU_COMPACT_MAX_SUMMARY_TOKENS: %q (want positive int)", v)
		}
		d.MaxSummaryTokens = n
	}
	var err error
	if d.Timeout, err = duration(getenv, "MSU_COMPACT_TIMEOUT", d.Timeout); err != nil {
		return compact.Defaults{}, err
	}
	return d, nil
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
