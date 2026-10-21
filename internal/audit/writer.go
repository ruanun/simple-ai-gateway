package audit

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"

	"github.com/ruanun/simple-ai-gateway/internal/model"
	"github.com/ruanun/simple-ai-gateway/internal/storage"
	"gorm.io/gorm"
)

// storeOpTimeout 单次存储层调用上限，防止 S3 停滞把 worker 卡死。
const storeOpTimeout = 30 * time.Second

// Entry 一条待落库的审计数据（由 handler 旁路投递）。
type Entry struct {
	RequestID           string
	APIKeyID            uint64
	AppID               uint64
	ChannelID           uint64
	AccountID           uint64
	Model               string
	RequestType         string
	Status              int
	LatencyMs           int64
	FirstTokenMs        int64
	SessionID           string
	Stream              bool
	PromptTokens        int
	CompletionTokens    int
	CachedTokens        int
	CacheCreationTokens int
	ReasoningTokens     int
	CostMicros          int64
	ClientIP            string
	UserAgent           string
	RequestBody         []byte
	ResponseBody        []byte
	EventCount          int
	// ResponseOverflow 非空表示响应体已在传输中按 MaxCaptureBytes 截断（ResponseBody 仅为前缀），
	// 原长/摘要以它为准（全文口径）。由流式捕获填充，其余路径留 nil。
	ResponseOverflow *Overflow
	// 以下由 Submit 填充：正文截断元数据
	RequestOrigSize   int
	RequestTruncated  bool
	RequestSHA256     string
	ResponseOrigSize  int
	ResponseTruncated bool
	ResponseSHA256    string
}

// MaxCaptureBytes 流式响应审计捕获的在途硬上限（64 MiB）。audit.max_body_bytes 只在 Submit
// 时截断、缺省还不截断，挡不住传输过程中整条流驻留内存；超过此上限即停止累积（客户端不受影响），
// 以 Overflow 如实记录截断。正常响应远小于此值，取证完整性不受影响。
const MaxCaptureBytes = 64 << 20

// Overflow 传输中截断的响应体的全文元数据：原始长度与全文 SHA-256（hex）。
type Overflow struct {
	OrigSize int
	SHA256   string
}

// captureBody 返回「有界正文 + 原始长度 + 是否截断 + 全文 SHA-256」。
// 截断路径必须 bytes.Clone：切断对大底层数组的引用，避免整段原始 body 随 Entry 滞留内存；
// max <= 0（不截断，缺省）时直接移交 src——调用方投递后不再持有/修改该切片，
// 全量复制起不到任何“有界”作用，只是请求 goroutine 上的无谓 memcpy。
func captureBody(src []byte, max int) (body []byte, origSize int, truncated bool, digest string) {
	origSize = len(src)
	sum := sha256.Sum256(src)
	digest = hex.EncodeToString(sum[:])
	if max > 0 && len(src) > max {
		return bytes.Clone(src[:max]), origSize, true, digest
	}
	return src, origSize, false, digest
}

// AsyncWriter 非阻塞接收审计条目，worker 池后台把正文写入存储层、元数据落库。
// 正文不进数据库：每条先 Put 请求体/响应体两个 gzip 对象，再 insert 一行 AuditRecord。
type AsyncWriter struct {
	g            *gorm.DB
	store        storage.Store
	maxBodyBytes int
	ch           chan Entry
	dropped      atomic.Int64
	closed       atomic.Bool
	wg           sync.WaitGroup
	once         sync.Once
	now          func() time.Time // 可注入，测试固定 key 日期
}

// NewAsyncWriter 创建并启动 workers 个后台消费 goroutine。0/负值语义在此归一，装配点直接透传 config：
// bufSize<=0 回落 256；maxBodyBytes<=0 不截断（缺省，取证完整优先）；workers<=0 回落 4
// （S3 每条两次网络 Put，单消费者吞吐只有个位数每秒，缓冲很快溢出）。
func NewAsyncWriter(g *gorm.DB, store storage.Store, bufSize, maxBodyBytes, workers int) *AsyncWriter {
	if bufSize <= 0 {
		bufSize = 256
	}
	if workers <= 0 {
		workers = 4
	}
	w := &AsyncWriter{g: g, store: store, maxBodyBytes: maxBodyBytes, ch: make(chan Entry, bufSize), now: time.Now}
	w.wg.Add(workers)
	for i := 0; i < workers; i++ {
		go w.loop()
	}
	return w
}

func (w *AsyncWriter) loop() {
	defer w.wg.Done()
	for e := range w.ch {
		w.writeOne(e)
	}
}

// clock 白盒测试可能不经构造函数（now 为 nil），兜底 time.Now。
func (w *AsyncWriter) clock() time.Time {
	if w.now == nil {
		return time.Now()
	}
	return w.now()
}

// objectKeys 对象 key 按 UTC 日期分层：audit/YYYY/MM/DD/<request_id>/{request,response}.gz。
// request_id 由服务端 GenRequestID 生成（hex），客户端不可控。
func objectKeys(reqID string, at time.Time) (reqKey, respKey string) {
	base := "audit/" + at.UTC().Format("2006/01/02") + "/" + reqID + "/"
	return base + "request.gz", base + "response.gz"
}

// Submit 非阻塞投递；通道满则丢弃并计数（不阻断主链路）。
// 已 Stop 后投递直接丢弃，避免向已关闭通道 send 触发 panic。
func (w *AsyncWriter) Submit(e Entry) {
	if w.closed.Load() {
		w.dropped.Add(1)
		slog.Warn("审计投递丢弃", "dropped_total", w.dropped.Load())
		return
	}
	// 兜底：Stop() 并发 close 通道时，closed 判定与 send 之间存在窗口，向已关闭通道 send 会 panic
	// （优雅关闭超时后在途请求补投递即触发）。审计是旁路，绝不反噬主链路——panic 当作丢弃吞掉，
	// 否则还会连带跳过调用方 settleDispatch 后续的请求日志落库。与 notify.Alert 同款兜底。
	defer func() {
		if recover() != nil {
			w.dropped.Add(1)
		}
	}()
	// 投递前计算截断元数据与摘要；配置了上限时截断为有界独立副本，切断大底层数组引用。
	e.RequestBody, e.RequestOrigSize, e.RequestTruncated, e.RequestSHA256 = captureBody(e.RequestBody, w.maxBodyBytes)
	e.ResponseBody, e.ResponseOrigSize, e.ResponseTruncated, e.ResponseSHA256 = captureBody(e.ResponseBody, w.maxBodyBytes)
	if o := e.ResponseOverflow; o != nil {
		// 已在途截断：上面算的是前缀口径，改用全文口径并置截断标志。
		e.ResponseOrigSize, e.ResponseTruncated, e.ResponseSHA256 = o.OrigSize, true, o.SHA256
	}
	select {
	case w.ch <- e:
	default:
		w.dropped.Add(1)
		slog.Warn("审计投递丢弃", "dropped_total", w.dropped.Load())
	}
}

// Dropped 返回累计未持久化条目数（队列满丢弃 + 存储层/落库写入失败）。
func (w *AsyncWriter) Dropped() int64 { return w.dropped.Load() }

// Stop 关闭通道并等待已入队条目落库完成（测试/优雅退出用）。幂等。
func (w *AsyncWriter) Stop() {
	w.once.Do(func() {
		w.closed.Store(true)
		close(w.ch)
		w.wg.Wait()
	})
}

// writeOne 顺序：Put 请求体 → Put 响应体 → insert 记录。任一步失败即放弃本条并尽力删除已写对象，
// 杜绝孤儿对象；审计是旁路，失败只计数 + 记日志，不反噬主链路。
func (w *AsyncWriter) writeOne(e Entry) {
	reqKey, respKey := objectKeys(e.RequestID, w.clock())
	if err := w.put(reqKey, e.RequestBody); err != nil {
		w.fail("审计请求体写入存储层失败", e.RequestID, reqKey, err)
		return
	}
	if err := w.put(respKey, e.ResponseBody); err != nil {
		w.discard(reqKey)
		w.fail("审计响应体写入存储层失败", e.RequestID, respKey, err)
		return
	}
	rec := model.AuditRecord{
		RequestID: e.RequestID, APIKeyID: e.APIKeyID, AppID: e.AppID, ChannelID: e.ChannelID, AccountID: e.AccountID,
		Model: e.Model, RequestType: e.RequestType, Status: e.Status, LatencyMs: e.LatencyMs, FirstTokenMs: e.FirstTokenMs, SessionID: e.SessionID, Stream: e.Stream,
		PromptTokens: e.PromptTokens, CompletionTokens: e.CompletionTokens,
		CachedTokens: e.CachedTokens, CacheCreationTokens: e.CacheCreationTokens, ReasoningTokens: e.ReasoningTokens,
		CostMicros: e.CostMicros,
		ClientIP:   e.ClientIP, UserAgent: e.UserAgent, EventCount: e.EventCount,
		RequestKey: reqKey, RequestSize: len(e.RequestBody), RequestOrigSize: e.RequestOrigSize, RequestTruncated: e.RequestTruncated, RequestSHA256: e.RequestSHA256,
		ResponseKey: respKey, ResponseSize: len(e.ResponseBody), ResponseOrigSize: e.ResponseOrigSize, ResponseTruncated: e.ResponseTruncated, ResponseSHA256: e.ResponseSHA256,
	}
	if err := w.g.Create(&rec).Error; err != nil {
		w.discard(reqKey, respKey)
		w.dropped.Add(1)
		// 库写失败比对象写失败更值得升级：意味着元数据层出问题
		slog.Error("审计记录落库失败", "request_id", e.RequestID, "err", err, "dropped_total", w.dropped.Load())
	}
}

func (w *AsyncWriter) put(key string, body []byte) error {
	ctx, cancel := context.WithTimeout(context.Background(), storeOpTimeout)
	defer cancel()
	return w.store.Put(ctx, key, gzipBytes(body))
}

// discard 尽力删除已写对象；删除本身失败只记警告（残留由桶生命周期/人工兜底）。
func (w *AsyncWriter) discard(keys ...string) {
	ctx, cancel := context.WithTimeout(context.Background(), storeOpTimeout)
	defer cancel()
	if err := w.store.Delete(ctx, keys); err != nil {
		slog.Warn("审计对象回滚删除失败", "keys", keys, "err", err)
	}
}

func (w *AsyncWriter) fail(msg, reqID, key string, err error) {
	w.dropped.Add(1)
	slog.Warn(msg, "request_id", reqID, "key", key, "err", err, "dropped_total", w.dropped.Load())
}
