// Command server 启动配置中心。装配顺序：config → store → cache → 仓储 → 接口。
package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/aceaura/model-surge-upstream/backend/account"
	"github.com/aceaura/model-surge-upstream/backend/apperr"
	"github.com/aceaura/model-surge-upstream/backend/cache"
	"github.com/aceaura/model-surge-upstream/backend/config"
	"github.com/aceaura/model-surge-upstream/backend/httpapi"
	"github.com/aceaura/model-surge-upstream/backend/model"
	"github.com/aceaura/model-surge-upstream/backend/provider"
	"github.com/aceaura/model-surge-upstream/backend/quota"
	"github.com/aceaura/model-surge-upstream/backend/resolve"
	"github.com/aceaura/model-surge-upstream/backend/store"
)

const shutdownGrace = 10 * time.Second

func main() {
	if err := run(); err != nil {
		log.Fatalf("fatal: %v", err)
	}
}

func run() error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}

	ctx := context.Background()
	db, err := store.Open(ctx, cfg.PGDSN)
	if err != nil {
		return err
	}
	defer db.Close()

	var backend cache.Backend
	if cfg.RedisAddr != "" {
		backend = cache.NewRedis(cfg.RedisAddr)
	}
	c := cache.New(backend, cfg.CacheTTL)

	accounts := account.NewRepo(db.Pool(), c)
	models := model.NewRepo(db.Pool(), c, accountLookup(accounts))
	resolver := resolve.NewResolver(accounts, models)
	quotas := quota.New(accounts, cfg.QuotaTTL)

	handler := httpapi.NewServer(httpapi.Deps{
		Accounts:    accounts,
		Models:      models,
		Resolver:    resolver,
		Quota:       quotas,
		Health:      health{db: db, cache: c},
		AdminKey:    cfg.AdminKey,
		DeliveryKey: cfg.DeliveryKey,
	})

	srv := &http.Server{
		Addr:              cfg.Listen,
		Handler:           handler,
		ReadHeaderTimeout: 10 * time.Second,
	}

	shutdown := make(chan os.Signal, 1)
	signal.Notify(shutdown, os.Interrupt, syscall.SIGTERM)

	errs := make(chan error, 1)
	go func() {
		log.Printf("listening on %s (redis: %v)", cfg.Listen, cfg.RedisAddr != "")
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errs <- err
		}
	}()

	select {
	case err := <-errs:
		return err
	case sig := <-shutdown:
		log.Printf("shutting down on %s", sig)
		grace, cancel := context.WithTimeout(context.Background(), shutdownGrace)
		defer cancel()
		return srv.Shutdown(grace)
	}
}

// accountLookup 把账号仓储包成 model 包需要的最小能力，
// 让依赖方向停留在装配层而不下沉到仓储之间。
func accountLookup(accounts *account.Repo) model.AccountLookup {
	return func(ctx context.Context, name string) (provider.Spec, error) {
		acc, err := accounts.Get(ctx, name)
		if err != nil {
			return provider.Spec{}, err
		}
		spec, ok := acc.Spec()
		if !ok {
			return provider.Spec{}, apperr.New(apperr.InvalidProvider,
				fmt.Sprintf("account %q references unknown provider %q", name, acc.ProviderID))
		}
		return spec, nil
	}
}

type health struct {
	db    *store.Store
	cache *cache.Cache
}

func (h health) PingDB(ctx context.Context) error    { return h.db.Ping(ctx) }
func (h health) CacheReady(ctx context.Context) bool { return h.cache.Ready(ctx) }
