// Package ringlog 持有进程日志的内存环形缓冲，供管理面「进程日志」页拉取。
//
// 两个入口：结构化推送（Push，带级别与来源）与标准库 log 的接管
// （CaptureStdLog）。接管后标准 log 的行同时写 stderr 与环形缓冲，
// docker logs 与日志页看到的内容一致；时间戳前缀由本包解析，
// 调用方不需要改任何 log.Printf 调用点。
//
// 缓冲只存进程生命周期内的日志：重启即空，日志页展示的是「本次运行」，
// 与 KiroaaS 进程日志的语义一致，不引入日志落盘与轮转的负担。
package ringlog

import (
	"io"
	"log"
	"os"
	"strings"
	"sync"
	"time"
)

// 级别取值。info 是默认级；warn/error 由推送方按语义显式指定。
const (
	LevelInfo  = "info"
	LevelWarn  = "warn"
	LevelError = "error"
)

// capacity 是环形缓冲容量。日志页轮询增量拉取，容量只需覆盖
// 运维者回看最近事件的范围；超出后最旧的条目被丢弃。
const capacity = 2000

// Entry 是一条日志。Seq 单调递增，客户端用它做增量拉取_cursor。
type Entry struct {
	Seq    int64     `json:"seq"`
	At     time.Time `json:"at"`
	Level  string    `json:"level"`
	Source string    `json:"source"`
	Msg    string    `json:"msg"`
}

var (
	mu      sync.Mutex
	buf     = make([]Entry, 0, capacity)
	nextSeq int64 = 1
)

// Push 追加一条日志。source 是产生方（如 http / resolve / chat），
// 空串归一到 server。
func Push(level, source, msg string) {
	if source == "" {
		source = "server"
	}
	if level == "" {
		level = LevelInfo
	}
	mu.Lock()
	defer mu.Unlock()
	pushLocked(Entry{Seq: nextSeq, At: time.Now(), Level: level, Source: source, Msg: msg})
}

func pushLocked(e Entry) {
	nextSeq++
	if len(buf) == capacity {
		// 环满：丢弃最旧一条。copy 前移比重新分配合算且保序。
		copy(buf, buf[1:])
		buf = buf[:len(buf)-1]
	}
	buf = append(buf, e)
}

// Since 返回 Seq 大于 since 的条目副本（升序）。since=0 即全量。
func Since(since int64) []Entry {
	mu.Lock()
	defer mu.Unlock()
	out := make([]Entry, 0, 64)
	for _, e := range buf {
		if e.Seq > since {
			out = append(out, e)
		}
	}
	return out
}

// LastSeq 是当前最大 Seq，空缓冲为 0。
func LastSeq() int64 {
	mu.Lock()
	defer mu.Unlock()
	return nextSeq - 1
}

// Clear 清空缓冲。Seq 不回绕：清空后客户端持有的 cursor 依然有效，
// 不会把清空前的旧条目再拉一遍。
func Clear() {
	mu.Lock()
	defer mu.Unlock()
	buf = buf[:0]
}

// stdWriter 接管标准 log 的输出：整行解析后入环，残缺行暂存等补齐。
type stdWriter struct {
	partial string
}

var std = &stdWriter{}

// CaptureStdLog 把标准库 log 的输出同时写到 stderr 与环形缓冲。
// 保留 LstdFlags 时间戳前缀（stderr 可读性），本包负责把它解析回去。
func CaptureStdLog() {
	log.SetFlags(log.LstdFlags)
	log.SetOutput(io.MultiWriter(os.Stderr, std))
}

const stdTimeLayout = "2006/01/02 15:04:05"

func (w *stdWriter) Write(p []byte) (int, error) {
	w.partial += string(p)
	for {
		head, tail, found := strings.Cut(w.partial, "\n")
		if !found {
			break
		}
		w.partial = tail
		w.ingest(head)
	}
	return len(p), nil
}

func (w *stdWriter) ingest(line string) {
	line = strings.TrimRight(line, "\r")
	if line == "" {
		return
	}
	msg := line
	if t, rest, ok := cutTime(line); ok {
		msg = rest
		_ = t // 时间取推送时刻即可：标准 log 与入环同进程同刻
	}
	Push(levelOf(msg), "server", msg)
}

// cutTime 剥离标准 log 的「2006/01/02 15:04:05 」前缀。
func cutTime(line string) (time.Time, string, bool) {
	if len(line) < len(stdTimeLayout)+1 {
		return time.Time{}, line, false
	}
	t, err := time.ParseInLocation(stdTimeLayout, line[:len(stdTimeLayout)], time.Local)
	if err != nil {
		return time.Time{}, line, false
	}
	return t, strings.TrimLeft(line[len(stdTimeLayout):], " "), true
}

// levelOf 从消息前缀推断级别：约定 warn/error 开头的行升级，
// 其余（含 proxyplane 的常规事件）保持 info。
func levelOf(msg string) string {
	switch {
	case strings.HasPrefix(msg, "warn:"), strings.HasPrefix(msg, "WARN:"):
		return LevelWarn
	case strings.HasPrefix(msg, "error:"), strings.HasPrefix(msg, "ERROR:"),
		strings.HasPrefix(msg, "fatal:"):
		return LevelError
	default:
		return LevelInfo
	}
}
