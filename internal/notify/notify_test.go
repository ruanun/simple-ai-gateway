package notify

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func recvServer(t *testing.T, gotBody *[]byte, count *int32) *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		*gotBody = b
		atomic.AddInt32(count, 1)
		w.WriteHeader(200)
	}))
}

func TestShapePayloadFormats(t *testing.T) {
	now := time.Unix(1000, 0)
	var m map[string]any
	require.NoError(t, json.Unmarshal(shapePayload("generic", "e", "T", "M", now), &m))
	require.Equal(t, "T", m["title"])
	require.Equal(t, "M", m["message"])
	require.Equal(t, "e", m["event"])

	require.NoError(t, json.Unmarshal(shapePayload("slack", "e", "T", "M", now), &m))
	require.Equal(t, "T\nM", m["text"])

	require.NoError(t, json.Unmarshal(shapePayload("feishu", "e", "T", "M", now), &m))
	require.Equal(t, "text", m["msg_type"])
	require.Equal(t, "T\nM", m["content"].(map[string]any)["text"])

	require.NoError(t, json.Unmarshal(shapePayload("dingtalk", "e", "T", "M", now), &m))
	require.Equal(t, "T\nM", m["text"].(map[string]any)["content"])

	// 未知 format 回退 generic
	require.NoError(t, json.Unmarshal(shapePayload("xxx", "e", "T", "M", now), &m))
	require.Equal(t, "T", m["title"])
}

func TestWebhookAlertSendsThenDedups(t *testing.T) {
	var body []byte
	var count int32
	srv := recvServer(t, &body, &count)
	defer srv.Close()
	w := NewWebhookNotifier(srv.URL, "generic", time.Hour)
	defer w.Stop()

	w.Alert("all_candidates_failed:m1", "全候选失败", "model=m1")
	w.Alert("all_candidates_failed:m1", "全候选失败", "model=m1") // 同 key，去重
	w.Stop()                                                 // flush + 等发完
	require.Equal(t, int32(1), atomic.LoadInt32(&count), "同 key window 内只发一次")
	require.Contains(t, string(body), "model=m1")
}

func TestWebhookTestSendSync(t *testing.T) {
	var body []byte
	var count int32
	srv := recvServer(t, &body, &count)
	defer srv.Close()
	w := NewWebhookNotifier(srv.URL, "slack", time.Hour)
	defer w.Stop()
	st, err := w.TestSend()
	require.NoError(t, err)
	require.Equal(t, 200, st)
	require.Contains(t, string(body), "测试")
}

func TestWebhookSendFailureNoPanic(t *testing.T) {
	w := NewWebhookNotifier("http://127.0.0.1:0/nope", "generic", time.Hour) // 无效地址
	require.NotPanics(t, func() {
		w.Alert("e", "t", "m")
		st, err := w.TestSend()
		require.Error(t, err)
		require.Equal(t, 0, st)
	})
	w.Stop()
}

func TestNopNotifier(t *testing.T) {
	require.NotPanics(t, func() { NopNotifier{}.Alert("e", "t", "m") })
}

// Stop 在后台 webhook 挂死（永久 hang）时必须有界返回，不能被慢/挂的 webhook 拖垮优雅退出。
func TestStopBoundedWhenWebhookHangs(t *testing.T) {
	block := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-block // 永久挂起，模拟慢/死的 webhook
	}))
	defer func() {
		close(block)
		srv.Close()
	}()

	w := NewWebhookNotifier(srv.URL, "generic", time.Hour)
	// 多条不同 key 的告警，入队后台 loop 会逐条同步 POST 卡死在第一条上。
	for i := 0; i < 5; i++ {
		w.Alert("hang"+strconv.Itoa(i), "t", "m")
	}
	time.Sleep(20 * time.Millisecond) // 确保 loop 已取到第一条并卡在 post() 里

	start := time.Now()
	w.Stop()
	elapsed := time.Since(start)
	require.Less(t, elapsed, 8*time.Second, "Stop 应在 5s 超时附近返回，而非等全部排空")
}

// dedup_window<=0（含 0）应被钳制为默认值而非"永不去重"：同 key 连发两次仍只发一次。
func TestDedupWindowZeroClampedToDefault(t *testing.T) {
	var body []byte
	var count int32
	srv := recvServer(t, &body, &count)
	defer srv.Close()
	w := NewWebhookNotifier(srv.URL, "generic", 0)
	defer w.Stop()

	w.Alert("same-key", "t1", "m1")
	w.Alert("same-key", "t2", "m2") // 同 key，钳制后的默认窗口内应被去重
	w.Stop()
	require.Equal(t, int32(1), atomic.LoadInt32(&count), "dedup_window<=0 应钳制为默认值而非从不去重")
}

// Alert 与 Stop 并发时（向已关闭通道 send 的窗口）不得 panic（best-effort，绝不反噬主链路）。
func TestAlertStopConcurrentNoPanic(t *testing.T) {
	require.NotPanics(t, func() {
		w := NewWebhookNotifier("http://127.0.0.1:0/nope", "generic", time.Hour)
		var wg sync.WaitGroup
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < 2000; i++ {
				w.Alert("e"+strconv.Itoa(i), "t", "m") // 不同 key 绕开去重，尽量撞上 close 窗口
			}
		}()
		time.Sleep(time.Millisecond)
		w.Stop()
		wg.Wait()
		// Stop 之后再发也不应 panic
		w.Alert("after", "t", "m")
	})
}
