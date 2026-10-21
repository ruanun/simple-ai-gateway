// Package logtest 为测试提供全局日志捕获：接管 slog.Default() 并把输出解析成结构化行。
// 仅在测试中引用，避免把 testing 链接进生产二进制（与 internal/dbtest、internal/redistest 同型）。
//
// 走 logger.InitWriter 而非裸 slog handler：request_id 由 logger 的 ctxHandler 从 ctx 自动附加，
// 而那正是各处日志断言真正要验的东西，绕过它等于没测。
// 因为接管的是进程级 slog.Default()，用本包的测试不能 t.Parallel()。
//
// 作者: ruan
package logtest

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"strings"
	"sync"
	"testing"

	"github.com/ruanun/simple-ai-gateway/internal/logger"
)

// Log 一次日志捕获会话。自身即 io.Writer，交给 logger.InitWriter 接收日志。
type Log struct {
	t   *testing.T
	mu  sync.Mutex // 后台 goroutine 也可能写日志，读写都要锁
	buf bytes.Buffer
}

// Capture 接管全局日志直到测试结束（t.Cleanup 自动还原），级别放到 debug 以免断言目标被过滤掉。
func Capture(t *testing.T) *Log {
	t.Helper()
	prev := slog.Default()
	t.Cleanup(func() { slog.SetDefault(prev) })
	l := &Log{t: t}
	logger.InitWriter(l, "debug", "json")
	return l
}

func (l *Log) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.buf.Write(p)
}

func (l *Log) text() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.buf.String()
}

// Lines 解析已捕获的全部日志行，无日志时返回 nil。
func (l *Log) Lines() []map[string]any {
	l.t.Helper()
	var out []map[string]any
	for line := range strings.SplitSeq(strings.TrimSpace(l.text()), "\n") {
		if line == "" {
			continue
		}
		var m map[string]any
		if err := json.Unmarshal([]byte(line), &m); err != nil {
			l.t.Fatalf("日志行不是合法 JSON: %v\n%s", err, line)
		}
		out = append(out, m)
	}
	return out
}

// Line 取 msg 等于给定值的第一行；没有则返回 nil（供断言"不应产生某条日志"）。
func (l *Log) Line(msg string) map[string]any {
	l.t.Helper()
	for _, m := range l.Lines() {
		if m["msg"] == msg {
			return m
		}
	}
	return nil
}

// Only 取唯一一行；行数不为 1 直接失败——避免"解析整个 buffer"式断言在多打一行时
// 报成误导性的 JSON 解析错误。
func (l *Log) Only() map[string]any {
	l.t.Helper()
	lines := l.Lines()
	if len(lines) != 1 {
		l.t.Fatalf("期望恰好 1 行日志，实际 %d 行:\n%s", len(lines), l.text())
	}
	return lines[0]
}
