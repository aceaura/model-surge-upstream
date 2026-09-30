// Package httpapi 暴露管理面与下发面两组接口。管理面改配置，下发面读目标，
// 两者密钥独立。数据面的聊天流量由 proxyplane 包的独立监听承载，不走这里。
package httpapi

import (
	"context"
	"net/http"

	"github.com/aceaura/model-surge-upstream/backend/account"
	"github.com/aceaura/model-surge-upstream/backend/model"
	"github.com/aceaura/model-surge-upstream/backend/provider"
	"github.com/aceaura/model-surge-upstream/backend/proxysettings"
	"github.com/aceaura/model-surge-upstream/backend/quota"
	"github.com/aceaura/model-surge-upstream/backend/resolve"
	"github.com/aceaura/model-surge-upstream/backend/upmodels"
)

type Accounts interface {
	Create(ctx context.Context, in account.Input) (account.Account, error)
	Get(ctx context.Context, name string) (account.Account, error)
	List(ctx context.Context) ([]account.Account, error)
	Update(ctx context.Context, in account.Input) (account.Account, error)
	Delete(ctx context.Context, name string) ([]string, error)
	CountModels(ctx context.Context, name string) (int, error)
}

type Models interface {
	Create(ctx context.Context, in model.Input) (model.Model, error)
	Get(ctx context.Context, id string) (model.Model, error)
	List(ctx context.Context, account string) ([]model.Model, error)
	Update(ctx context.Context, in model.Input) (model.Model, error)
	Delete(ctx context.Context, id string) error
}

type Resolver interface {
	Resolve(ctx context.Context, modelID string) (resolve.ResolvedTarget, error)
	List(ctx context.Context) ([]resolve.Listing, error)
}

type Quota interface {
	Query(ctx context.Context, accountName string) (quota.Report, error)
	Forget(name string)
}

// UpstreamModels 查询上游账号实际可用的模型清单。
type UpstreamModels interface {
	List(ctx context.Context, accountName string) (upmodels.Report, error)
	Forget(name string)
}

// ProxySettings 读写代理转发面配置。
type ProxySettings interface {
	Get(ctx context.Context) (proxysettings.Settings, error)
	Put(ctx context.Context, s proxysettings.Settings) error
}

// ProxyApply 使一份转发面配置立即生效（重绑独立端口的监听）。
type ProxyApply interface {
	Apply(settings proxysettings.Settings) error
}

// Health 报告依赖就绪状态。Redis 只是缓存，不影响 ready。
type Health interface {
	PingDB(ctx context.Context) error
	CacheReady(ctx context.Context) bool
}

type Deps struct {
	Accounts       Accounts
	Models         Models
	Resolver       Resolver
	Quota          Quota
	UpstreamModels UpstreamModels
	ProxySettings  ProxySettings
	ProxyApply     ProxyApply
	Health         Health
	AdminKey       string
	DeliveryKey    string
}

func NewServer(d Deps) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", handler(d).health)

	admin := http.NewServeMux()
	h := handler(d)
	admin.HandleFunc("GET /admin/providers", h.listProviders)
	admin.HandleFunc("GET /admin/accounts", h.listAccounts)
	admin.HandleFunc("POST /admin/accounts", h.createAccount)
	admin.HandleFunc("GET /admin/accounts/{name}", h.getAccount)
	admin.HandleFunc("PUT /admin/accounts/{name}", h.updateAccount)
	admin.HandleFunc("DELETE /admin/accounts/{name}", h.deleteAccount)
	// 额度与上游模型清单同时挂在管理面：桌面客户端只持管理密钥，
	// 不该为查这两项再配下发密钥。
	admin.HandleFunc("GET /admin/accounts/{name}/quota", h.quota)
	admin.HandleFunc("GET /admin/accounts/{name}/upstream-models", h.upstreamModels)
	admin.HandleFunc("GET /admin/models", h.listModels)
	admin.HandleFunc("POST /admin/models", h.createModel)
	admin.HandleFunc("GET /admin/models/{id...}", h.getModel)
	admin.HandleFunc("PUT /admin/models/{id...}", h.updateModel)
	admin.HandleFunc("DELETE /admin/models/{id...}", h.deleteModel)
	admin.HandleFunc("GET /admin/proxy-settings", h.getProxySettings)
	admin.HandleFunc("PUT /admin/proxy-settings", h.putProxySettings)
	mux.Handle("/admin/", requireKey(d.AdminKey, admin))

	delivery := http.NewServeMux()
	delivery.HandleFunc("GET /v1/models", h.deliveryModels)
	delivery.HandleFunc("POST /v1/resolve", h.resolve)
	delivery.HandleFunc("GET /v1/accounts/{name}/quota", h.quota)
	delivery.HandleFunc("GET /v1/accounts/{name}/upstream-models", h.upstreamModels)
	mux.Handle("/v1/", requireKey(d.DeliveryKey, delivery))

	return mux
}

type handler Deps

func (h handler) listProviders(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"providers": provider.All()})
}
