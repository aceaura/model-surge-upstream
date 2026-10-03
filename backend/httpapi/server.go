// Package httpapi 暴露管理面与下发面两组接口。管理面改配置，下发面读目标，
// 两者密钥独立。数据面的聊天流量由 proxyplane 包的独立监听承载，不走这里。
package httpapi

import (
	"context"
	"net/http"
	"time"

	"github.com/aceaura/model-surge-upstream/backend/account"
	"github.com/aceaura/model-surge-upstream/backend/chat"
	"github.com/aceaura/model-surge-upstream/backend/model"
	"github.com/aceaura/model-surge-upstream/backend/provider"
	"github.com/aceaura/model-surge-upstream/backend/proxysettings"
	"github.com/aceaura/model-surge-upstream/backend/quota"
	"github.com/aceaura/model-surge-upstream/backend/resolve"
	"github.com/aceaura/model-surge-upstream/backend/store"
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
	// HeadersFor 供连通性检测构造与转发面一致的上游头(oauth 账号
	// 含活体 token 与 codex 身份头)。
	HeadersFor(ctx context.Context, spec provider.Spec, acc account.Account) (map[string]string, error)
}

type Quota interface {
	Query(ctx context.Context, accountName string) (quota.Report, error)
	TestScript(ctx context.Context, accountName, code string, timeoutSeconds int) (quota.Report, error)
	// Cached 无视存活期回缓存报告,供 auto=1 轮询对空闲账号短路。
	Cached(accountName string) (quota.Report, bool)
	Forget(name string)
}

// Activity 报告账号在给定空闲窗口内是否有数据面/对话面请求,供额度定时
// 轮询跳过空闲账号。nil 表示不装配,轮询行为与此前一致(始终真实查询)。
type Activity interface {
	Active(accountName string, idle time.Duration) bool
}

// UpstreamModels 查询上游账号实际可用的模型清单。
type UpstreamModels interface {
	List(ctx context.Context, accountName string) (upmodels.Report, error)
	// DeclaredEfforts 取账号某原生模型在上游声明的推理档(原值,声明序);
	// 不可查/查询失败/未声明都回 nil(无声明即不支持)。
	DeclaredEfforts(ctx context.Context, accountName, nativeModel string) []string
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

// Chat 承载管理面对话页的会话与补全能力。
type Chat interface {
	ListSessions(ctx context.Context) ([]chat.Session, error)
	CreateSession(ctx context.Context) (chat.Session, error)
	RenameSession(ctx context.Context, id, title string) (chat.Session, error)
	DeleteSession(ctx context.Context, id string) error
	Messages(ctx context.Context, id string) (chat.Session, []chat.Message, error)
	ClearMessages(ctx context.Context, id string) error
	Send(ctx context.Context, sessionID, modelID, content, effort string, images []chat.ImageAttachment) ([]chat.Message, error)
}

// Health 报告依赖就绪状态。Redis 只是缓存，不影响 ready。
type Health interface {
	PingDB(ctx context.Context) error
	CacheReady(ctx context.Context) bool
}

// UsageStats 用量统计的查询能力，*store.Store 满足该接口。
type UsageStats interface {
	UsageSummary(ctx context.Context, f store.UsageFilter) (store.UsageTotals, error)
	UsageTrend(ctx context.Context, f store.UsageFilter, granularity string) ([]store.UsageBucket, error)
	UsageByModel(ctx context.Context, f store.UsageFilter) ([]store.UsageGroup, error)
	UsageByAccount(ctx context.Context, f store.UsageFilter) ([]store.UsageGroup, error)
	UsageLogs(ctx context.Context, f store.UsageFilter, limit, offset int) ([]store.UsageLog, int64, error)
}

// OAuthState 暴露 OAuth 登录态的授权健康(oauth.Manager 实现;nil 表示
// 未装配 OAuth,账号视图的 needs_reauth 恒 false)。
type OAuthState interface {
	NeedsReauth(name string) bool
	Reset(name string)
}

type Deps struct {
	Accounts       Accounts
	Models         Models
	Resolver       Resolver
	Quota          Quota
	UpstreamModels UpstreamModels
	ProxySettings  ProxySettings
	ProxyApply     ProxyApply
	Chat           Chat
	Usage          UsageStats
	Health         Health
	OAuth          OAuthState
	Activity       Activity
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
	admin.HandleFunc("POST /admin/accounts/{name}/quota-test", h.testQuotaScript)
	admin.HandleFunc("GET /admin/accounts/{name}/upstream-models", h.upstreamModels)
	admin.HandleFunc("GET /admin/models", h.listModels)
	admin.HandleFunc("POST /admin/models", h.createModel)
	admin.HandleFunc("GET /admin/models/{id...}", h.getModel)
	admin.HandleFunc("PUT /admin/models/{id...}", h.updateModel)
	admin.HandleFunc("DELETE /admin/models/{id...}", h.deleteModel)
	// 连通性检测挂独立前缀：模型 id 含 / 必须吃 {id...} 通配，而 Go 路由
	// 不允许通配段后接 /test 静态段。
	admin.HandleFunc("POST /admin/model-test/{id...}", h.testModel)
	// 账号检测同理：账号 name 无字符集约束、可含 /，挂独立前缀吃通配。
	admin.HandleFunc("POST /admin/account-test/{name...}", h.testAccount)
	admin.HandleFunc("GET /admin/proxy-settings", h.getProxySettings)
	admin.HandleFunc("PUT /admin/proxy-settings", h.putProxySettings)
	admin.HandleFunc("GET /admin/logs", h.listLogs)
	admin.HandleFunc("DELETE /admin/logs", h.clearLogs)
	admin.HandleFunc("GET /admin/chat/sessions", h.listChatSessions)
	admin.HandleFunc("POST /admin/chat/sessions", h.createChatSession)
	admin.HandleFunc("PATCH /admin/chat/sessions/{id}", h.renameChatSession)
	admin.HandleFunc("DELETE /admin/chat/sessions/{id}", h.deleteChatSession)
	admin.HandleFunc("GET /admin/chat/sessions/{id}/messages", h.listChatMessages)
	admin.HandleFunc("POST /admin/chat/sessions/{id}/messages", h.sendChatMessage)
	admin.HandleFunc("DELETE /admin/chat/sessions/{id}/messages", h.clearChatMessages)
	admin.HandleFunc("GET /admin/usage/summary", h.usageSummary)
	admin.HandleFunc("GET /admin/usage/trend", h.usageTrend)
	admin.HandleFunc("GET /admin/usage/models", h.usageModels)
	admin.HandleFunc("GET /admin/usage/accounts", h.usageAccounts)
	admin.HandleFunc("GET /admin/usage/logs", h.usageLogs)
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
