package breaker

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func p(failTh int) Params {
	return Params{Window: 30 * time.Second, Cooldown: 60 * time.Second, FailThreshold: failTh, ErrorRate: 0.5, MinSamples: 20, ProbeBackoffMax: 8}
}

func TestMemoryCountOpen(t *testing.T) {
	b := NewMemoryBreaker(p(3))
	now := time.Unix(1000, 0)
	require.True(t, b.Allow(1, now))
	for i := 0; i < 3; i++ {
		b.RecordResult(1, now, false, false)
	}
	require.False(t, b.Allow(1, now))                    // 达 fail_threshold → 开
	require.True(t, b.Allow(1, now.Add(61*time.Second))) // 冷却后半开放行
}

func TestMemorySuccessKeepsCountBelowThreshold(t *testing.T) {
	b := NewMemoryBreaker(p(3))
	now := time.Unix(1000, 0)
	b.RecordResult(1, now, false, false)
	b.RecordResult(1, now, true, false) // 成功记入窗口(不清零,但拉低错误率;此处只验证未开)
	b.RecordResult(1, now, false, false)
	require.True(t, b.Allow(1, now)) // 2 失败 < 阈值 3
}

func TestMemory429ImmediateOpen(t *testing.T) {
	b := NewMemoryBreaker(p(5))
	now := time.Unix(1000, 0)
	b.RecordResult(1, now, false, true) // is429 立即开
	require.False(t, b.Allow(1, now))
}

func TestMemoryErrorRateOpen(t *testing.T) {
	// 阈值高(不靠计数)，靠错误率：20 样本、失败率≥0.5
	b := NewMemoryBreaker(Params{Window: 30 * time.Second, Cooldown: 60 * time.Second, FailThreshold: 1000, ErrorRate: 0.5, MinSamples: 20, ProbeBackoffMax: 8})
	now := time.Unix(1000, 0)
	for i := 0; i < 10; i++ {
		b.RecordResult(1, now, true, false)
	}
	for i := 0; i < 9; i++ {
		b.RecordResult(1, now, false, false)
	}
	require.True(t, b.Allow(1, now)) // 19 样本 < min_samples 20
	b.RecordResult(1, now, false, false)
	require.False(t, b.Allow(1, now)) // 20 样本、10/20=0.5 ≥ rate → 开
}

func TestMemoryHalfOpenProbeSuccessCloses(t *testing.T) {
	b := NewMemoryBreaker(p(1))
	now := time.Unix(1000, 0)
	b.RecordResult(1, now, false, false) // 开
	later := now.Add(61 * time.Second)
	require.True(t, b.Allow(1, later))      // 半开放行
	require.True(t, b.Recovering(1, later)) // 处于恢复期
	b.RecordResult(1, later, true, false)   // 探测成功 → 闭合
	require.False(t, b.Recovering(1, later))
	require.True(t, b.Allow(1, later))
}

func TestMemoryHalfOpenProbeFailBacksOff(t *testing.T) {
	b := NewMemoryBreaker(p(1))
	now := time.Unix(1000, 0)
	b.RecordResult(1, now, false, false) // 开，openUntil=+60s
	t1 := now.Add(61 * time.Second)
	require.True(t, b.Allow(1, t1))                      // 半开
	b.RecordResult(1, t1, false, false)                  // 探测失败 → 重开，退避 2^1=... 首次 probeLevel=0→1×cooldown
	require.False(t, b.Allow(1, t1.Add(59*time.Second))) // 仍冷却(≥60s 才半开)
	require.True(t, b.Allow(1, t1.Add(61*time.Second)))  // 第一次退避=1×cooldown
	// 再失败一次 → probeLevel=1 → 2×cooldown
	t2 := t1.Add(61 * time.Second)
	b.RecordResult(1, t2, false, false)
	require.False(t, b.Allow(1, t2.Add(61*time.Second))) // 2×cooldown=120s，61s 仍冷却
	require.True(t, b.Allow(1, t2.Add(121*time.Second)))
}

func TestMemoryDisabled(t *testing.T) {
	b := NewMemoryBreaker(Params{FailThreshold: 0, ErrorRate: 0}) // 禁用
	now := time.Unix(1000, 0)
	for i := 0; i < 100; i++ {
		b.RecordResult(1, now, false, true)
	}
	require.True(t, b.Allow(1, now)) // 禁用恒放行
}
