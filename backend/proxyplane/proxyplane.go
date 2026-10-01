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
	"github.com/aceaura/model-surge-upstream/backend/compact"
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
	resolver  Resolver
	sink      UsageSink
	compactor *compact.Runner

	mu       sync.Mutex
	srv      *http.Server
	settings proxysettings.Settings
}

// NewSupervisor 装配转发面运行时。compactor 为 nil 表示上下文压缩关闭
// （全局默认未配 MSU_COMPACT_MODE 时即 passive，行为等价关闭）。
func NewSupervisor(resolver Resolver, sink UsageSink, compactor *compact.Runner) *Supervisor {
	return &Supervisor{resolver: resolver, sink: sink, compactor: compactor}
}

// Apply 使一份配置生效。密钥为空 = 关闭转发面；配置与当前一致 = 无操作，
// 不中断在飞的流式连接。换端口时先 bind 新监听成功再停旧的，应用失败不
// 影响在跑的配置；同端口换绑（如只切换监听范围）只能先停后绑——新旧地址
// 重叠时共存 bind 必然冲突，此时绑不上会尝试恢复旧监听。
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
	// 同端口的新旧地址必然重叠（:12344 与 127.0.0.1:12344 不能同时 bind），
	// 只能先停旧的。换端口则保持「先 bind 后停」的热切换。
	samePort := s.srv != nil && s.settings.Port == settings.Port
	if samePort {
		s.stopLocked()
	}

	ln, err := net.Listen("tcp", addr)
	if err != nil {
		if samePort {
			// 旧监听已停，尽力恢复，让在跑的配置不因一次失败的应用而消失。
			s.restoreLocked()
		}
		return apperr.New(apperr.InvalidRequest,
			fmt.Sprintf("proxy listen %s failed: %v", addr, err))
	}

	s.stopLocked()
	s.serveLocked(ln, settings)
	return nil
}

// serveLocked 在已绑定的监听上起服务并记录为当前配置。
func (s *Supervisor) serveLocked(ln net.Listener, settings proxysettings.Settings) {
	srv := &http.Server{
		Handler:           NewHandler(settings.APIKey, s.resolver, s.sink).WithCompactor(s.compactor),
		ReadHeaderTimeout: 30 * time.Second,
	}
	go func() {
		if err := srv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Printf("proxyplane: serve %s: %v", settings.ListenAddr(), err)
		}
	}()
	s.srv = srv
	s.settings = settings
	log.Printf("proxyplane: listening on %s", settings.ListenAddr())
}

// restoreLocked 尝试按当前记录的配置重新开监听（同端口换绑失败后的挽回）。
// 恢复失败只记日志：错误已在 Apply 返回给调用方。
func (s *Supervisor) restoreLocked() {
	if !s.settings.Enabled() {
		return
	}
	ln, err := net.Listen("tcp", s.settings.ListenAddr())
	if err != nil {
		log.Printf("proxyplane: restore %s failed: %v", s.settings.ListenAddr(), err)
		return
	}
	s.serveLocked(ln, s.settings)
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
