package requestlog

import (
	"testing"

	"github.com/ruanun/simple-ai-gateway/internal/dbtest"
	"github.com/ruanun/simple-ai-gateway/internal/model"
	"github.com/stretchr/testify/require"
)

// TestAsyncRecordFlushOnStop 异步模式下 Record 入队，Stop 后全部落库。
func TestAsyncRecordFlushOnStop(t *testing.T) {
	g := dbtest.OpenInMemory(t)
	require.NoError(t, g.AutoMigrate(&model.RequestLog{}))
	svc := NewService(NewRepo(g))
	svc.EnableAsync(16)
	for i := 0; i < 5; i++ {
		svc.Record(&model.RequestLog{Model: "m", Status: 200})
	}
	svc.Stop() // flush
	var n int64
	require.NoError(t, g.Model(&model.RequestLog{}).Count(&n).Error)
	require.EqualValues(t, 5, n)
}

// TestSyncRecordDefault 未启用异步时 Record 同步落库（现行为），Stop 为 no-op。
func TestSyncRecordDefault(t *testing.T) {
	g := dbtest.OpenInMemory(t)
	require.NoError(t, g.AutoMigrate(&model.RequestLog{}))
	svc := NewService(NewRepo(g))
	svc.Record(&model.RequestLog{Model: "m", Status: 200})
	var n int64
	require.NoError(t, g.Model(&model.RequestLog{}).Count(&n).Error)
	require.EqualValues(t, 1, n)
	svc.Stop()
}

// TestRecordAfterStopDropsNotPanic 验证 Stop 后 Record 不向已关闭通道 send（不 panic），且静默丢弃
// （优雅关闭超时后仍有在途请求补 Record 即触发这条路径）。
func TestRecordAfterStopDropsNotPanic(t *testing.T) {
	g := dbtest.OpenInMemory(t)
	require.NoError(t, g.AutoMigrate(&model.RequestLog{}))
	svc := NewService(NewRepo(g))
	svc.EnableAsync(16)
	svc.Stop()

	require.NotPanics(t, func() {
		svc.Record(&model.RequestLog{Model: "after-stop", Status: 200})
	})
	var n int64
	require.NoError(t, g.Model(&model.RequestLog{}).Count(&n).Error)
	require.EqualValues(t, 0, n)
}

// TestRecordRacingCloseRecovers 验证 closed 判定与 send 之间被并发 close 插入时（竞态窗口），
// Record 靠 recover 兜底把 panic 当丢弃吞掉，不反噬调用方。白盒模拟：closed 仍为 false 但通道已关闭。
func TestRecordRacingCloseRecovers(t *testing.T) {
	svc := &Service{ch: make(chan *model.RequestLog, 1)}
	close(svc.ch) // 模拟 Stop() 在 closed.Load() 之后、send 之前完成 close

	require.NotPanics(t, func() {
		svc.Record(&model.RequestLog{Model: "racing-close", Status: 200})
	})
}
