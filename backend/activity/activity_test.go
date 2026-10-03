package activity

import (
	"testing"
	"time"
)

func TestUntouchedAccountIsIdle(t *testing.T) {
	tr := New(time.Minute)
	if tr.Active("kimi-1") {
		t.Error("从未有请求的账号应视为空闲")
	}
}

func TestTouchActivatesWithinWindow(t *testing.T) {
	tr := New(50 * time.Millisecond)
	tr.Touch("kimi-1")
	if !tr.Active("kimi-1") {
		t.Error("刚服务过请求的账号应在窗口内保持活跃")
	}
	time.Sleep(60 * time.Millisecond)
	if tr.Active("kimi-1") {
		t.Error("超过空闲窗口未再请求应回落为空闲")
	}
}

func TestTouchReactivates(t *testing.T) {
	tr := New(50 * time.Millisecond)
	tr.Touch("kimi-1")
	time.Sleep(60 * time.Millisecond)
	tr.Touch("kimi-1")
	if !tr.Active("kimi-1") {
		t.Error("再次请求应重新激活账号")
	}
	if tr.Active("kimi-2") {
		t.Error("触活一个账号不应牵连其他账号")
	}
}
