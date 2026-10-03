package activity

import (
	"testing"
	"time"
)

func TestUntouchedAccountIsIdle(t *testing.T) {
	tr := New()
	if tr.Active("kimi-1", time.Minute) {
		t.Error("从未有请求的账号应视为空闲")
	}
}

func TestTouchActivatesWithinWindow(t *testing.T) {
	tr := New()
	tr.Touch("kimi-1")
	if !tr.Active("kimi-1", 50*time.Millisecond) {
		t.Error("刚服务过请求的账号应在窗口内保持活跃")
	}
	time.Sleep(60 * time.Millisecond)
	if tr.Active("kimi-1", 50*time.Millisecond) {
		t.Error("超过空闲窗口未再请求应回落为空闲")
	}
	// 同一时刻更大的窗口仍判活跃:窗口按调用方传入,互不影响。
	if !tr.Active("kimi-1", time.Hour) {
		t.Error("更大的空闲窗口应仍判活跃")
	}
}

func TestTouchReactivates(t *testing.T) {
	tr := New()
	tr.Touch("kimi-1")
	time.Sleep(60 * time.Millisecond)
	tr.Touch("kimi-1")
	if !tr.Active("kimi-1", 50*time.Millisecond) {
		t.Error("再次请求应重新激活账号")
	}
	if tr.Active("kimi-2", 50*time.Millisecond) {
		t.Error("触活一个账号不应牵连其他账号")
	}
}
