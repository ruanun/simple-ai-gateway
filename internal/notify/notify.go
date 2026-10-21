// Package notify 提供最小告警：一个可配置 webhook，异步发送、按 format 塑形、内存 TTL 去重。
// best-effort：发送失败/队列满只 log，绝不阻塞或反噬主链路。
package notify

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"sync"
	"sync/atomic"
	"time"
)

// Notifier 告警投递接口。event 作去重键；title/message 为人读内容。
type Notifier interface {
	Alert(event, title, message string)
}

// NopNotifier 空实现（未启用告警时用），调用点无需判空。
type NopNotifier struct{}

// Alert 无操作。
func (NopNotifier) Alert(event, title, message string) {}

type item struct{ event, title, message string }

// WebhookNotifier 异步向 webhook 投递告警。
type WebhookNotifier struct {
	url      string
	format   string
	dedupWin time.Duration
	cli      *http.Client
	now      func() time.Time

	ch     chan item
	closed atomic.Bool
	wg     sync.WaitGroup
	once   sync.Once

	mu   sync.Mutex
	seen map[string]time.Time // 去重：event → 抑制到期时间
}

// NewWebhookNotifier 创建并启动后台发送 goroutine。
func NewWebhookNotifier(url, format string, dedupWindow time.Duration) *WebhookNotifier {
	if format == "" {
		format = "generic"
	}
	if dedupWindow <= 0 {
		dedupWindow = 5 * time.Minute
	}
	w := &WebhookNotifier{
		url: url, format: format, dedupWin: dedupWindow,
		cli: &http.Client{Timeout: 10 * time.Second}, now: time.Now,
		ch: make(chan item, 256), seen: map[string]time.Time{},
	}
	w.wg.Add(1)
	go w.loop()
	return w
}

func (w *WebhookNotifier) loop() {
	defer w.wg.Done()
	for it := range w.ch {
		if st, err := w.post(it.event, it.title, it.message); err != nil {
			slog.Error("alert 发送失败", "event", it.event, "status", st, "err", err)
		}
	}
}

// Alert 去重后异步投递（通道满丢弃，绝不阻塞主链路）。
func (w *WebhookNotifier) Alert(event, title, message string) {
	if w.closed.Load() || !w.shouldSend(event) {
		return
	}
	// 兜底：Stop() 并发 close 通道时，closed 判定与 send 之间存在窗口，向已关闭通道 send 会 panic。
	// 告警是 best-effort，绝不反噬主链路，故 recover 吞掉当作丢弃。
	defer func() { _ = recover() }()
	select {
	case w.ch <- item{event, title, message}:
	default:
		slog.Warn("alert 队列满，丢弃", "event", event)
	}
}

// shouldSend event 在 dedupWin 内已发过则抑制。
func (w *WebhookNotifier) shouldSend(event string) bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	now := w.now()
	// 顺手清过期条目：seen 只增不删会随模型/App/Key 基数缓慢增长（长驻进程内存泄漏）。
	// 告警低频且 map 以活跃去重键为界，全量遍历开销可忽略。
	for k, exp := range w.seen {
		if now.After(exp) {
			delete(w.seen, k)
		}
	}
	if exp, ok := w.seen[event]; ok && now.Before(exp) {
		return false
	}
	w.seen[event] = now.Add(w.dedupWin)
	return true
}

// TestSend 同步发一条 event=test 告警（绕去重），返回状态与错误，供 /admin/alert/test。
func (w *WebhookNotifier) TestSend() (int, error) {
	return w.post("test", "测试告警", "simple-ai-gateway 告警联通测试")
}

// post 按 format 塑形并同步 POST。
func (w *WebhookNotifier) post(event, title, message string) (int, error) {
	payload := shapePayload(w.format, event, title, message, w.now())
	req, err := http.NewRequest(http.MethodPost, w.url, bytes.NewReader(payload))
	if err != nil {
		return 0, err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := w.cli.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, resp.Body)
	if resp.StatusCode >= 400 {
		return resp.StatusCode, fmt.Errorf("webhook 返回 %d", resp.StatusCode)
	}
	return resp.StatusCode, nil
}

// Stop 优雅停止（关通道 + 等已入队发完，但设 5s 总超时兜底）。幂等。
// 后台 loop 逐条同步 POST（每条最多 10s 超时），队列有积压且 webhook 慢/挂时，
// 无界等待会拖慢优雅退出（256 满载最坏约 42min）。告警是 best-effort，允许丢弃排空不完的剩余队列。
func (w *WebhookNotifier) Stop() {
	w.once.Do(func() {
		w.closed.Store(true)
		close(w.ch)
		done := make(chan struct{})
		go func() { w.wg.Wait(); close(done) }()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			slog.Warn("alert Stop 排空超时(5s)，放弃剩余队列")
		}
	})
}

// shapePayload 按各家 incoming webhook 形状塑形；未知 format 回退 generic。
func shapePayload(format, event, title, message string, now time.Time) []byte {
	text := title + "\n" + message
	var v any
	switch format {
	case "slack":
		v = map[string]any{"text": text}
	case "feishu":
		v = map[string]any{"msg_type": "text", "content": map[string]any{"text": text}}
	case "dingtalk":
		v = map[string]any{"msgtype": "text", "text": map[string]any{"content": text}}
	default: // generic
		v = map[string]any{"event": event, "level": "error", "title": title, "message": message, "timestamp": now.Format(time.RFC3339)}
	}
	b, _ := json.Marshal(v)
	return b
}
