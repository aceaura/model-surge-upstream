// Package activity 跟踪账号最近一次数据面/对话面请求的时刻,供定时任务
// (额度轮询)跳过空闲账号。纯进程内存态,重启清零:重启后首轮轮询照常
// 真实查询,语义无损。
package activity

import (
	"sync"
	"time"
)

// DefaultIdleWindow 账号未配置停止查询间隔(quota_settings
// stop_interval_minutes)时的空闲窗口。
const DefaultIdleWindow = 5 * time.Minute

type Tracker struct {
	mu   sync.Mutex
	last map[string]time.Time
}

func New() *Tracker {
	return &Tracker{last: map[string]time.Time{}}
}

// Touch 记录账号刚服务了一次请求(转发面/对话面,成败都算:请求到达即激活)。
func (t *Tracker) Touch(account string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.last[account] = time.Now()
}

// Active 报告账号在给定空闲窗口内是否有请求。从未有请求的账号视为空闲。
// 窗口按账号传入(额度脚本的停止查询间隔),不同账号可不同。
func (t *Tracker) Active(account string, idle time.Duration) bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	at, ok := t.last[account]
	return ok && time.Since(at) < idle
}
