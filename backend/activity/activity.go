// Package activity 跟踪账号最近一次数据面/对话面请求的时刻,供定时任务
// (额度轮询)跳过空闲账号。纯进程内存态,重启清零:重启后首轮轮询照常
// 真实查询,语义无损。
package activity

import (
	"sync"
	"time"
)

type Tracker struct {
	idle time.Duration

	mu   sync.Mutex
	last map[string]time.Time
}

// New 以 idle 为空闲窗口建追踪器:窗口内无请求即视为空闲。
func New(idle time.Duration) *Tracker {
	return &Tracker{idle: idle, last: map[string]time.Time{}}
}

// Touch 记录账号刚服务了一次请求(转发面/对话面,成败都算:请求到达即激活)。
func (t *Tracker) Touch(account string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.last[account] = time.Now()
}

// Active 报告账号在空闲窗口内是否有请求。从未有请求的账号视为空闲。
func (t *Tracker) Active(account string) bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	at, ok := t.last[account]
	return ok && time.Since(at) < t.idle
}
