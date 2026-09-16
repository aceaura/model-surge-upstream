package config

import (
	"strings"
	"testing"
	"time"
)

func envOf(kv map[string]string) Getenv {
	return func(k string) string { return kv[k] }
}

func fullEnv() map[string]string {
	return map[string]string{
		"MSU_PG_DSN":       "postgres://u:p@localhost:5432/msu",
		"MSU_ADMIN_KEY":    "admin-secret",
		"MSU_DELIVERY_KEY": "delivery-secret",
	}
}

func TestLoadDefaults(t *testing.T) {
	cfg, err := LoadFrom(envOf(fullEnv()))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.Listen != defaultListen {
		t.Errorf("Listen = %q, want %q", cfg.Listen, defaultListen)
	}
	if cfg.CacheTTL != defaultCacheTTL {
		t.Errorf("CacheTTL = %v, want %v", cfg.CacheTTL, defaultCacheTTL)
	}
	if cfg.QuotaTTL != defaultQuotaTTL {
		t.Errorf("QuotaTTL = %v, want %v", cfg.QuotaTTL, defaultQuotaTTL)
	}
	if cfg.RedisAddr != "" {
		t.Errorf("RedisAddr = %q, want empty", cfg.RedisAddr)
	}
}

func TestLoadMissingRequired(t *testing.T) {
	for _, key := range []string{"MSU_PG_DSN", "MSU_ADMIN_KEY", "MSU_DELIVERY_KEY"} {
		env := fullEnv()
		delete(env, key)
		_, err := LoadFrom(envOf(env))
		if err == nil {
			t.Fatalf("missing %s: expected error", key)
		}
		if !strings.Contains(err.Error(), key) {
			t.Errorf("error %q should name %s", err, key)
		}
	}
}

func TestLoadMissingAllNamesEach(t *testing.T) {
	_, err := LoadFrom(envOf(nil))
	if err == nil {
		t.Fatal("expected error")
	}
	for _, key := range []string{"MSU_PG_DSN", "MSU_ADMIN_KEY", "MSU_DELIVERY_KEY"} {
		if !strings.Contains(err.Error(), key) {
			t.Errorf("error %q should name %s", err, key)
		}
	}
}

func TestLoadBlankIsMissing(t *testing.T) {
	env := fullEnv()
	env["MSU_ADMIN_KEY"] = "   "
	if _, err := LoadFrom(envOf(env)); err == nil {
		t.Fatal("whitespace-only key should count as missing")
	}
}

func TestLoadOptionalOverrides(t *testing.T) {
	env := fullEnv()
	env["MSU_LISTEN"] = ":9000"
	env["MSU_REDIS_ADDR"] = "localhost:6379"
	env["MSU_CACHE_TTL"] = "30s"
	env["MSU_QUOTA_TTL"] = "2m"
	cfg, err := LoadFrom(envOf(env))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.Listen != ":9000" || cfg.RedisAddr != "localhost:6379" {
		t.Errorf("unexpected cfg: %+v", cfg)
	}
	if cfg.CacheTTL != 30*time.Second || cfg.QuotaTTL != 2*time.Minute {
		t.Errorf("ttl = %v/%v", cfg.CacheTTL, cfg.QuotaTTL)
	}
}

func TestLoadInvalidDuration(t *testing.T) {
	for _, tc := range []struct{ key, value string }{
		{"MSU_CACHE_TTL", "nope"},
		{"MSU_QUOTA_TTL", "-1s"},
		{"MSU_QUOTA_TTL", "0"},
	} {
		env := fullEnv()
		env[tc.key] = tc.value
		_, err := LoadFrom(envOf(env))
		if err == nil {
			t.Errorf("%s=%q: expected error", tc.key, tc.value)
			continue
		}
		if !strings.Contains(err.Error(), tc.key) {
			t.Errorf("error %q should name %s", err, tc.key)
		}
	}
}
