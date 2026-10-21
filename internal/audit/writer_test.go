package audit

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/ruanun/simple-ai-gateway/internal/dbtest"
	"github.com/ruanun/simple-ai-gateway/internal/model"
	"github.com/ruanun/simple-ai-gateway/internal/storage"
	"github.com/stretchr/testify/require"
)

var fixedNow = time.Date(2026, 9, 3, 12, 0, 0, 0, time.UTC)

func newTestWriter(t *testing.T, store storage.Store, maxBody, workers int) *AsyncWriter {
	t.Helper()
	g := dbtest.OpenInMemory(t)
	require.NoError(t, g.AutoMigrate(&model.AuditRecord{}))
	w := NewAsyncWriter(g, store, 16, maxBody, workers)
	w.now = func() time.Time { return fixedNow }
	return w
}

// 读回并解压某 key。
func mustGunzip(t *testing.T, store storage.Store, key string) string {
	t.Helper()
	raw, err := store.Get(context.Background(), key)
	require.NoError(t, err, "key=%s", key)
	b, err := gunzipBytes(raw)
	require.NoError(t, err)
	return string(b)
}

// TestGzipRoundTrip 压缩/解压往返；解压非 gzip 数据报错。
func TestGzipRoundTrip(t *testing.T) {
	src := []byte(`{"model":"gpt-4o","messages":[{"role":"user","content":"hi"}]}`)
	got, err := gunzipBytes(gzipBytes(src))
	require.NoError(t, err)
	require.Equal(t, src, got)
	_, err = gunzipBytes([]byte("not gzip"))
	require.Error(t, err)
	got, err = gunzipBytes(gzipBytes(nil))
	require.NoError(t, err)
	require.Empty(t, got)
}

// TestObjectKeys key 按 UTC 日期分层，请求/响应各一。
func TestObjectKeys(t *testing.T) {
	at := time.Date(2026, 9, 3, 23, 30, 0, 0, time.FixedZone("CST", 8*3600)) // UTC 为 09-03 15:30
	req, resp := objectKeys("abc123", at)
	require.Equal(t, "audit/2026/09/03/abc123/request.gz", req)
	require.Equal(t, "audit/2026/09/03/abc123/response.gz", resp)
}

// TestAsyncWriterPersists 一条审计 → 两个 gzip 对象 + 一行记录（key/元数据齐全）。
func TestAsyncWriterPersists(t *testing.T) {
	mem := storage.NewMemory()
	w := newTestWriter(t, mem, 0, 1)
	reqBody, respBody := []byte(`{"model":"gpt-4o"}`), []byte(`{"id":"ok"}`)
	w.Submit(Entry{
		RequestID: "req-1", AppID: 1, Model: "gpt-4o", RequestType: "chat", Status: 200,
		RequestBody: reqBody, ResponseBody: respBody,
	})
	w.Stop()

	var rec model.AuditRecord
	require.NoError(t, w.g.Where("request_id = ?", "req-1").First(&rec).Error)
	require.Equal(t, "audit/2026/09/03/req-1/request.gz", rec.RequestKey)
	require.Equal(t, "audit/2026/09/03/req-1/response.gz", rec.ResponseKey)
	require.Equal(t, len(reqBody), rec.RequestSize)
	require.Equal(t, len(reqBody), rec.RequestOrigSize)
	require.False(t, rec.RequestTruncated)
	sum := sha256.Sum256(reqBody)
	require.Equal(t, hex.EncodeToString(sum[:]), rec.RequestSHA256)
	require.Equal(t, len(respBody), rec.ResponseSize)

	require.Equal(t, string(reqBody), mustGunzip(t, mem, rec.RequestKey))
	require.Equal(t, string(respBody), mustGunzip(t, mem, rec.ResponseKey))
	require.Equal(t, 2, mem.Len())
	require.Zero(t, w.Dropped())
}

// TestSubmitTruncatesBigBody 按上限截断正文 + 记原长/摘要/截断标志；对象存的是截断后内容。
func TestSubmitTruncatesBigBody(t *testing.T) {
	mem := storage.NewMemory()
	w := newTestWriter(t, mem, 10, 1)
	req := []byte("0123456789ABCDEF") // 16 > 10
	resp := []byte("short")
	sum := sha256.Sum256(req)
	w.Submit(Entry{RequestID: "big", AppID: 1, RequestBody: req, ResponseBody: resp})
	w.Stop()

	var rec model.AuditRecord
	require.NoError(t, w.g.Where("request_id = ?", "big").First(&rec).Error)
	require.Equal(t, 10, rec.RequestSize)
	require.Equal(t, 16, rec.RequestOrigSize)
	require.True(t, rec.RequestTruncated)
	require.Equal(t, hex.EncodeToString(sum[:]), rec.RequestSHA256, "SHA256 为原始全文摘要")
	require.Equal(t, "0123456789", mustGunzip(t, mem, rec.RequestKey))
	require.Equal(t, 5, rec.ResponseOrigSize)
	require.False(t, rec.ResponseTruncated)
	require.Equal(t, "short", mustGunzip(t, mem, rec.ResponseKey))
}

// 传输中已截断的响应体（ResponseOverflow 非空）：原长/摘要取溢出元数据（全文口径），
// 截断标志置位；正文再按 max_body_bytes 截断（此处未配上限则存捕获前缀原样）。
func TestSubmitHonorsResponseOverflow(t *testing.T) {
	for _, max := range []int{0, 4} {
		mem := storage.NewMemory()
		w := newTestWriter(t, mem, max, 1)
		id := fmt.Sprintf("ovf-%d", max)
		w.Submit(Entry{RequestID: id, AppID: 1, RequestBody: []byte("q"), ResponseBody: []byte("prefix"),
			ResponseOverflow: &Overflow{OrigSize: 999, SHA256: "abc"}})
		w.Stop()

		var rec model.AuditRecord
		require.NoError(t, w.g.Where("request_id = ?", id).First(&rec).Error)
		require.True(t, rec.ResponseTruncated, "max=%d", max)
		require.Equal(t, 999, rec.ResponseOrigSize, "max=%d", max)
		require.Equal(t, "abc", rec.ResponseSHA256, "max=%d", max)
		want := "prefix"
		if max > 0 {
			want = want[:max]
		}
		require.Equal(t, want, mustGunzip(t, mem, rec.ResponseKey), "max=%d", max)
		require.False(t, rec.RequestTruncated)
	}
}

// TestWriteOnePersistsCacheDetail 落库带缓存读写与推理 token 明细。
func TestWriteOnePersistsCacheDetail(t *testing.T) {
	w := newTestWriter(t, storage.NewMemory(), 0, 1)
	w.writeOne(Entry{
		RequestID: "r1", Model: "claude-3", RequestType: "messages", Status: 200,
		PromptTokens: 1000, CompletionTokens: 200, CachedTokens: 700, CacheCreationTokens: 100, ReasoningTokens: 50,
	})
	w.Stop()
	var rec model.AuditRecord
	require.NoError(t, w.g.Where("request_id = ?", "r1").First(&rec).Error)
	require.Equal(t, 700, rec.CachedTokens)
	require.Equal(t, 100, rec.CacheCreationTokens)
	require.Equal(t, 50, rec.ReasoningTokens)
}

// failingStore 指定第 N 次 Put 失败，其余委托给 Memory；用于验证失败回滚。
type failingStore struct {
	*storage.Memory
	failOnPut int // 第几次 Put 失败（从 1 数）；0 = 不失败
	puts      int
}

func (f *failingStore) Put(ctx context.Context, key string, data []byte) error {
	f.puts++
	if f.puts == f.failOnPut {
		return errors.New("模拟存储故障")
	}
	return f.Memory.Put(ctx, key, data)
}

// TestWriteOneRequestPutFails 请求体 Put 失败 → 无对象、无行、dropped+1。
func TestWriteOneRequestPutFails(t *testing.T) {
	fs := &failingStore{Memory: storage.NewMemory(), failOnPut: 1}
	w := newTestWriter(t, fs, 0, 1)
	w.writeOne(Entry{RequestID: "f1", RequestBody: []byte("a"), ResponseBody: []byte("b")})
	w.Stop()
	require.Equal(t, 0, fs.Len())
	var n int64
	w.g.Model(&model.AuditRecord{}).Count(&n)
	require.Zero(t, n)
	require.Equal(t, int64(1), w.Dropped())
}

// TestWriteOneResponsePutFails 响应体 Put 失败 → 已写的请求体对象被回滚删除、无行、dropped+1。
func TestWriteOneResponsePutFails(t *testing.T) {
	fs := &failingStore{Memory: storage.NewMemory(), failOnPut: 2}
	w := newTestWriter(t, fs, 0, 1)
	w.writeOne(Entry{RequestID: "f2", RequestBody: []byte("a"), ResponseBody: []byte("b")})
	w.Stop()
	require.Equal(t, 0, fs.Len(), "请求体对象应被回滚删除")
	var n int64
	w.g.Model(&model.AuditRecord{}).Count(&n)
	require.Zero(t, n)
	require.Equal(t, int64(1), w.Dropped())
}

// TestWriteOneInsertFails 记录落库失败（request_id 唯一冲突）→ 本次两对象被删，首条记录与对象不受影响。
func TestWriteOneInsertFails(t *testing.T) {
	mem := storage.NewMemory()
	w := newTestWriter(t, mem, 0, 1)
	w.writeOne(Entry{RequestID: "dup", RequestBody: []byte("a"), ResponseBody: []byte("b")})
	require.Equal(t, 2, mem.Len())
	// 第二次同 request_id、不同日期 → key 不同、insert 撞唯一索引
	w.now = func() time.Time { return fixedNow.AddDate(0, 0, 1) }
	w.writeOne(Entry{RequestID: "dup", RequestBody: []byte("c"), ResponseBody: []byte("d")})
	w.Stop()

	require.Equal(t, 2, mem.Len(), "失败写入的两个对象应被删除")
	require.Equal(t, []string{"audit/2026/09/03/dup/request.gz", "audit/2026/09/03/dup/response.gz"}, mem.Keys())
	var n int64
	w.g.Model(&model.AuditRecord{}).Count(&n)
	require.Equal(t, int64(1), n)
	require.Equal(t, int64(1), w.Dropped())
}

// TestAsyncWriterWorkersConcurrent 4 个 worker 并发消费 100 条全部落地，Stop 排空。
// 不用 newTestWriter：需要 200 的缓冲避免本测试触发丢弃（worker 已在读 channel，事后替换 w.ch 会挂死）。
func TestAsyncWriterWorkersConcurrent(t *testing.T) {
	mem := storage.NewMemory()
	g := dbtest.OpenInMemory(t)
	require.NoError(t, g.AutoMigrate(&model.AuditRecord{}))
	w := NewAsyncWriter(g, mem, 200, 0, 4)
	w.now = func() time.Time { return fixedNow }
	for i := 0; i < 100; i++ {
		w.Submit(Entry{RequestID: fmt.Sprintf("c-%03d", i), RequestBody: []byte("x"), ResponseBody: []byte("y")})
	}
	w.Stop()
	var n int64
	w.g.Model(&model.AuditRecord{}).Count(&n)
	require.Equal(t, int64(100), n)
	require.Equal(t, 200, mem.Len())
	require.Zero(t, w.Dropped())
}

// TestAsyncWriterDropsWhenFull 容量 0 且无消费者 → Submit 必丢弃并计数。
func TestAsyncWriterDropsWhenFull(t *testing.T) {
	w := &AsyncWriter{ch: make(chan Entry)}
	for i := 0; i < 5; i++ {
		w.Submit(Entry{RequestID: "x"})
	}
	require.Equal(t, int64(5), w.Dropped())
}

// TestSubmitAfterStopDropsNotPanic Stop 后 Submit 不 panic 且计入 dropped。
func TestSubmitAfterStopDropsNotPanic(t *testing.T) {
	w := newTestWriter(t, storage.NewMemory(), 0, 1)
	w.Stop()
	before := w.Dropped()
	require.NotPanics(t, func() { w.Submit(Entry{RequestID: "after-stop"}) })
	require.Equal(t, before+1, w.Dropped())
}

// TestSubmitRacingCloseRecovers closed 判定与 send 之间被并发 close 插入时，recover 兜底当丢弃。
func TestSubmitRacingCloseRecovers(t *testing.T) {
	w := &AsyncWriter{ch: make(chan Entry, 1)}
	close(w.ch)
	before := w.Dropped()
	require.NotPanics(t, func() { w.Submit(Entry{RequestID: "racing-close"}) })
	require.Equal(t, before+1, w.Dropped())
}

// TestNewAsyncWriterDefaults 0/负值归一化：bufSize 回落 256、workers 回落 4（不为 0 否则永不消费），
// maxBodyBytes 保持 0（不截断语义，不得被当成「未配置」而改写）。
func TestNewAsyncWriterDefaults(t *testing.T) {
	g := dbtest.OpenInMemory(t)
	require.NoError(t, g.AutoMigrate(&model.AuditRecord{}))
	w := NewAsyncWriter(g, storage.NewMemory(), 0, 0, 0)
	require.Equal(t, 256, cap(w.ch), "bufSize<=0 应回落 256")
	require.Equal(t, 0, w.maxBodyBytes, "maxBodyBytes=0 即不截断，不得被归一化改写")

	w.Submit(Entry{RequestID: "defaults-1", RequestBody: []byte("a"), ResponseBody: []byte("b")})
	w.Stop() // workers 归一化失败（0 个消费者）时这里会因条目永不落库而在下面断言失败
	var n int64
	w.g.Model(&model.AuditRecord{}).Count(&n)
	require.Equal(t, int64(1), n)
	require.Zero(t, w.Dropped())
}
