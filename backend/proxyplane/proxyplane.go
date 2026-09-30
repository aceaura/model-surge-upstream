// Package proxyplane 是代理转发面的运行时：一个独立于管理面的 HTTP 监听。
// 它把「我命名的模型」按三种协议原生形态暴露给客户端——不做协议之间的
// 转化，只做四件事：按路径前缀识别协议、把请求体里的模型别名换成
// native_model、叠加 defaults/overrides、换掉认证头，其余字节原样流过。
//
// Supervisor 管理监听的生命周期：管理面应用配置时重绑端口，进程退出时
// 优雅关闭。Apply 先 bind 新监听成功再停旧的，应用失败不影响在跑的配置。
package proxyplane

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net"
	"net/http"
	"sync"
	"time"

	"github.com/aceaura/model-surge-upstream/backend/apperr"
	"github.com/aceaura/model-surge-upstream/backend/proxysettings"
	"github.com/aceaura/model-surge-upstream/backend/resolve"
)

// Resolver 是转发面需要的模型解析能力，与 httpapi 下发面同源。
type Resolver interface {
	Resolve(ctx context.Context, modelID string) (resolve.ResolvedTarget, error)
	List(ctx context.Context) ([]resolve.Listing, error)
}

const stopGrace = 5 * time.Second

type Supervisor struct {
	resolver Resolver

	mu       sync.Mutex
	srv      *http.Server
	settings proxysettings.Settings
}

func NewSupervisor(resolver Resolver) *Supervisor {
	return &Supervisor{resolver: resolver}
}

// Apply 使一份配置生效。密钥为空 = 关闭转发面；配置与当前一致 = 无操作，
// 不中断在飞的流式连接。bind 失败时旧监听保持不动。
func (s *Supervisor) Apply(settings proxysettings.Settings) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if settings == s.settings {
		return nil
	}
	if !settings.Enabled() {
		s.stopLocked()
		s.settings = settings
		log.Printf("proxyplane: disabled")
		return nil
	}

	addr := settings.ListenAddr()
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return apperr.Wrap(apperr.InvalidRequest,
			fmt.Sprintf("proxy listen %s failed", addr), err)
	}

	s.stopLocked()
	srv := &http.Server{
		Handler:           NewHandler(settings.APIKey, s.resolver),
		ReadHeaderTimeout: 30 * time.Second,
	}
	go func() {
		if err := srv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Printf("proxyplane: serve %s: %v", addr, err)
		}
	}()
	s.srv = srv
	s.settings = settings
	log.Printf("proxyplane: listening on %s", addr)
	return nil
}

// Close 停掉转发面监听，进程退出时调用。
func (s *Supervisor) Close() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.stopLocked()
}

func (s *Supervisor) stopLocked() {
	if s.srv == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), stopGrace)
	defer cancel()
	_ = s.srv.Shutdown(ctx)
	s.srv = nil
}
