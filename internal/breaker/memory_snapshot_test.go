package breaker

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestMemorySnapshotOpenAndReset(t *testing.T) {
	b := NewMemoryBreaker(p(1))
	now := time.Unix(1000, 0)
	b.RecordResult(1, now, false, true) // 429 → 开
	snap := b.Snapshot([]uint64{1, 2}, now)
	require.Len(t, snap, 1) // 只有 id=1 非闭合
	require.Equal(t, "open", snap[1].State)
	require.Equal(t, now.Add(60*time.Second).Unix(), snap[1].OpenUntil)

	b.Reset(1)
	require.Empty(t, b.Snapshot([]uint64{1}, now)) // 重置后闭合、不再出现
	require.True(t, b.Allow(1, now))
}

func TestMemorySnapshotHalfOpen(t *testing.T) {
	b := NewMemoryBreaker(p(1))
	now := time.Unix(1000, 0)
	b.RecordResult(1, now, false, true) // 开，openUntil=+60s
	later := now.Add(61 * time.Second)  // 冷却已过
	snap := b.Snapshot([]uint64{1}, later)
	require.Equal(t, "half", snap[1].State) // 到期即将半开 → 显示 half
}

func TestMemorySnapshotDisabled(t *testing.T) {
	b := NewMemoryBreaker(Params{FailThreshold: 0, ErrorRate: 0}) // 禁用
	now := time.Unix(1000, 0)
	require.Empty(t, b.Snapshot([]uint64{1}, now))
}

// TestMemoryLazyExpire 冷却到期后长期无流量的状态应惰性过期视为闭合，
// 对齐 RedisBreaker 的键 TTL(Cooldown+1min)：否则内存模式 Recovering 恒 true、
// Snapshot 恒报 half，选择器持续降权且管理端显示与 Redis 模式分叉。
func TestMemoryLazyExpire(t *testing.T) {
	b := NewMemoryBreaker(p(1))
	now := time.Unix(1000, 0)
	b.RecordResult(1, now, false, true) // 开，openUntil=+60s

	// 宽限内（openUntil 后 <1min）：仍视为半开
	within := now.Add(60*time.Second + 59*time.Second)
	require.True(t, b.Recovering(1, within))
	require.Equal(t, "half", b.Snapshot([]uint64{1}, within)[1].State)

	// 过期线后（openUntil+60s）：视为闭合并清除状态
	expired := now.Add(60*time.Second + 61*time.Second)
	require.False(t, b.Recovering(1, expired))
	require.Empty(t, b.Snapshot([]uint64{1}, expired))
	require.True(t, b.Allow(1, expired))
}

// TestMemoryLazyExpireRecordResult 过期状态上报结果应按闭合重新起步：
// 若仍按半开探测处理，一次失败会直接指数退避重开；按闭合滑窗计则未达阈值(3)不开。
func TestMemoryLazyExpireRecordResult(t *testing.T) {
	b := NewMemoryBreaker(p(3))
	now := time.Unix(1000, 0)
	b.RecordResult(1, now, false, true) // 429 → 开，openUntil=+60s
	expired := now.Add(60*time.Second + 61*time.Second)
	b.RecordResult(1, expired, false, false) // 过期后一次普通失败
	require.True(t, b.Allow(1, expired), "单次失败未达阈值，应仍闭合")
}
