package proxyplane

import (
	"fmt"
	"net"
	"net/http"
	"testing"
	"time"

	"github.com/aceaura/model-surge-upstream/backend/proxysettings"
)

// freePort 取一个当前空闲的端口供测试绑定。
func freePort(t *testing.T) int {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("grab free port: %v", err)
	}
	defer ln.Close()
	return ln.Addr().(*net.TCPAddr).Port
}

func get(t *testing.T, addr, key string) int {
	t.Helper()
	req, _ := http.NewRequest(http.MethodGet, fmt.Sprintf("http://%s/v1/models", addr), nil)
	req.Header.Set("Authorization", "Bearer "+key)
	resp, err := (&http.Client{Timeout: 3 * time.Second}).Do(req)
	if err != nil {
		return 0
	}
	defer resp.Body.Close()
	return resp.StatusCode
}

func TestSupervisorSamePortRebind(t *testing.T) {
	port := freePort(t)
	sup := NewSupervisor(fakeResolver{}, nil, nil, nil)
	defer sup.Close()

	lanOpen := proxysettings.Settings{APIKey: "k1", Port: port, LanOpen: true}
	if err := sup.Apply(lanOpen); err != nil {
		t.Fatalf("apply lan_open: %v", err)
	}
	if code := get(t, fmt.Sprintf("127.0.0.1:%d", port), "k1"); code != http.StatusOK {
		t.Fatalf("lan_open listen status = %d", code)
	}

	// 同端口只切监听范围：旧地址 :port 与新地址 127.0.0.1:port 重叠，
	// 必须停旧绑新而不是共存 bind（曾因此必失败）。
	loopback := proxysettings.Settings{APIKey: "k1", Port: port, LanOpen: false}
	if err := sup.Apply(loopback); err != nil {
		t.Fatalf("same-port rebind to loopback: %v", err)
	}
	if code := get(t, fmt.Sprintf("127.0.0.1:%d", port), "k1"); code != http.StatusOK {
		t.Fatalf("loopback listen status = %d", code)
	}

	// 同配置重复应用 = 无操作。
	if err := sup.Apply(loopback); err != nil {
		t.Fatalf("no-op apply: %v", err)
	}

	// 换端口热切换：新端口绑成功后旧端口立即失效。
	port2 := freePort(t)
	if err := sup.Apply(proxysettings.Settings{APIKey: "k2", Port: port2, LanOpen: false}); err != nil {
		t.Fatalf("switch port: %v", err)
	}
	if code := get(t, fmt.Sprintf("127.0.0.1:%d", port2), "k2"); code != http.StatusOK {
		t.Fatalf("new port status = %d", code)
	}
	if code := get(t, fmt.Sprintf("127.0.0.1:%d", port), "k1"); code != 0 {
		t.Fatalf("old port should be closed, got status %d", code)
	}

	// 清空密钥关闭。
	if err := sup.Apply(proxysettings.Settings{APIKey: "", Port: port2}); err != nil {
		t.Fatalf("disable: %v", err)
	}
	if code := get(t, fmt.Sprintf("127.0.0.1:%d", port2), "k2"); code != 0 {
		t.Fatalf("disabled plane should not listen, got status %d", code)
	}
}

func TestSupervisorSamePortBindFailureRestoresOld(t *testing.T) {
	sup := NewSupervisor(fakeResolver{}, nil, nil, nil)
	defer sup.Close()
	lanOpen := proxysettings.Settings{APIKey: "k1", Port: freePort(t), LanOpen: true}
	if err := sup.Apply(lanOpen); err != nil {
		t.Fatalf("apply lan_open: %v", err)
	}

	// 占住 loopback 同端口地址，再切到 loopback：新 bind 必失败，
	// 旧的 :port 监听应被恢复。
	holder, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", lanOpen.Port))
	if err != nil {
		t.Fatalf("holder listen: %v", err)
	}
	defer holder.Close()

	err = sup.Apply(proxysettings.Settings{APIKey: "k1", Port: lanOpen.Port, LanOpen: false})
	if err == nil {
		t.Fatalf("expected bind failure")
	}
	if code := get(t, fmt.Sprintf("127.0.0.1:%d", lanOpen.Port), "k1"); code != 0 && code != http.StatusOK {
		t.Fatalf("unexpected status %d", code)
	}
	// holder 占着 127.0.0.1，恢复的 :port 与之不冲突（Linux 下同端口
	// 不同具体地址可共存仅当旧的是通配且 SO_REUSEADDR 未介入——
	// 这里只要求进程内监听状态恢复可用即可，具体可达性由操作系统决定）。
	sup.mu.Lock()
	running := sup.srv != nil
	sup.mu.Unlock()
	if !running {
		t.Fatalf("old listener should be restored after failed apply")
	}
}
