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
	"github.com/aceaura/model-surge-upstream/backend/chat"
	"github.com/aceaura/model-surge-upstream/backend/config"
	"github.com/aceaura/model-surge-upstream/backend/httpapi"
	"github.com/aceaura/model-surge-upstream/backend/model"
	"github.com/aceaura/model-surge-upstream/backend/provider"
	"github.com/aceaura/model-surge-upstream/backend/proxyplane"
	"github.com/aceaura/model-surge-upstream/backend/proxysettings"
	"github.com/aceaura/model-surge-upstream/backend/quota"
	"github.com/aceaura/model-surge-upstream/backend/resolve"
	"github.com/aceaura/model-surge-upstream/backend/ringlog"
	"github.com/aceaura/model-surge-upstream/backend/store"
	"github.com/aceaura/model-surge-upstream/backend/upmodels"
)

const shutdownGrace = 10 * time.Second

func main() {
	if err := run(); err != nil {
		log.Fatalf("fatal: %v", err)
	}
}

func run() error {
	// 进程日志页的数据源：标准 log 与结构化推送都进环形缓冲。
	ringlog.CaptureStdLog()

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
	resolver := loggedResolver{inner: resolve.NewResolver(accounts, models)}
	quotas := quota.New(accounts, cfg.QuotaTTL)
	upstream := upmodels.New(accounts, cfg.QuotaTTL)

	// 代理转发面：独立端口、独立密钥，配置落库、运行期可改。
	// 启动时按已存配置开监听；应用失败（如端口被占）只告警，
	// 管理面不可用才是致命问题，转发面不是。
	proxyRepo := proxysettings.NewRepo(db.Pool())
	proxySup := loggedApply{inner: proxyplane.NewSupervisor(resolver)}
	defer proxySup.inner.Close()
	if s, err := proxyRepo.Get(ctx); err != nil {
		log.Printf("proxyplane: load settings: %v", err)
	} else if err := proxySup.Apply(s); err != nil {
		log.Printf("proxyplane: apply saved settings: %v", err)
	}

	// 对话页：会话与消息落库，补全走解析出的上游目标。
	chats := chat.NewService(chat.NewRepo(db.Pool()), resolver)

	handler := withRequestLog(httpapi.NewServer(httpapi.Deps{
		Accounts:       accounts,
		Models:         models,
		Resolver:       resolver,
		Quota:          quotas,
		UpstreamModels: upstream,
		ProxySettings:  proxyRepo,
		ProxyApply:     proxySup,
		Chat:           chats,
		Health:         health{db: db, cache: c},
		AdminKey:       cfg.AdminKey,
		DeliveryKey:    cfg.DeliveryKey,
	}))

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

// loggedResolver 给模型解析加进程日志：管理面下发、转发面转发、对话补全
// 三处都经解析落地，日志页因此能看到「哪个模型落到哪个账号」。
type loggedResolver struct{ inner *resolve.Resolver }

func (l loggedResolver) Resolve(ctx context.Context, modelID string) (resolve.ResolvedTarget, error) {
	t, err := l.inner.Resolve(ctx, modelID)
	if err != nil {
		ringlog.Push(ringlog.LevelWarn, "resolve", fmt.Sprintf("model=%s failed: %v", modelID, err))
		return t, err
	}
	ringlog.Push(ringlog.LevelInfo, "resolve",
		fmt.Sprintf("model=%s → account=%s protocol=%s native=%s", modelID, t.Account, t.Protocol, t.NativeModel))
	return t, nil
}

func (l loggedResolver) List(ctx context.Context) ([]resolve.Listing, error) {
	return l.inner.List(ctx)
}

// loggedApply 把转发面配置应用结果记进进程日志：成功记端口与监听范围，
// 失败（如端口被占）记原因，运维者在日志页即可看到重绑失败的来龙去脉。
type loggedApply struct{ inner *proxyplane.Supervisor }

func (l loggedApply) Apply(s proxysettings.Settings) error {
	err := l.inner.Apply(s)
	if err != nil {
		ringlog.Push(ringlog.LevelWarn, "proxy", fmt.Sprintf("apply settings failed: %v", err))
		return err
	}
	state := "off"
	if s.APIKey != "" {
		state = fmt.Sprintf("on port=%d lan_open=%v", s.Port, s.LanOpen)
	}
	ringlog.Push(ringlog.LevelInfo, "proxy", "applied settings: "+state)
	return err
}

// withRequestLog 把管理面/下发面请求记进进程日志（方法、路径、状态、耗时）。
// 高频轮询端点（健康检查、日志页自身拉取）排除：否则日志页每次拉取
// 都会产生一条「拉取日志」的日志，自我刷屏。
func withRequestLog(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/healthz" || r.URL.Path == "/admin/logs" {
			next.ServeHTTP(w, r)
			return
		}
		start := time.Now()
		sw := &statusWriter{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(sw, r)
		level := ringlog.LevelInfo
		if sw.status >= 400 {
			level = ringlog.LevelWarn
		}
		ringlog.Push(level, "http", fmt.Sprintf("%s %s → %d (%s)",
			r.Method, r.URL.Path, sw.status, time.Since(start).Round(time.Millisecond)))
	})
}

// statusWriter 只旁路记录状态码：默认 200，覆盖 WriteHeader 的调用。
type statusWriter struct {
	http.ResponseWriter
	status int
}

func (s *statusWriter) WriteHeader(code int) {
	s.status = code
	s.ResponseWriter.WriteHeader(code)
}
