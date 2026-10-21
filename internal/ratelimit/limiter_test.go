package ratelimit

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestFixedWindow(t *testing.T) {
	l := NewMemoryLimiter()
	t0 := time.Unix(1_000_000, 0)
	require.True(t, l.Allow("k", 2, t0))
	require.True(t, l.Allow("k", 2, t0))
	require.False(t, l.Allow("k", 2, t0))                 // 第3次超限
	require.True(t, l.Allow("k", 2, t0.Add(time.Minute))) // 新窗口放行
}

func TestUnlimited(t *testing.T) {
	l := NewMemoryLimiter()
	t0 := time.Unix(1_000_000, 0)
	for i := 0; i < 100; i++ {
		require.True(t, l.Allow("k", 0, t0)) // limit<=0 => 不限
	}
}

// 过期窗口的桶必须被回收：否则按 IP 建键的匿名端点（登录限流）可被海量源地址
// 撑爆 map，造成无界内存增长。
func TestMemoryLimiterSweepsStaleBuckets(t *testing.T) {
	l := NewMemoryLimiter()
	t0 := time.Unix(1_000_000, 0)
	for i := 0; i < 1000; i++ {
		l.Allow("ip:"+time.Duration(i).String(), 10, t0)
	}
	require.Equal(t, 1000, len(l.buckets))
	// 进入下一窗口后任意一次调用即回收上一窗口全部桶，只剩本次的新桶。
	require.True(t, l.Allow("other", 10, t0.Add(time.Minute)))
	require.Equal(t, 1, len(l.buckets))
	// 同窗口内的计数不受清扫影响。
	require.True(t, l.Allow("other", 2, t0.Add(time.Minute)))
	require.False(t, l.Allow("other", 2, t0.Add(time.Minute)))
}

// Exceeded 只读判定本窗口是否已达上限，不计数。
func TestMemoryLimiterExceeded(t *testing.T) {
	l := NewMemoryLimiter()
	t0 := time.Unix(1_000_000, 0)
	require.False(t, l.Exceeded("k", 2, t0))
	l.Allow("k", 2, t0)
	require.False(t, l.Exceeded("k", 2, t0))
	require.False(t, l.Exceeded("k", 2, t0), "Exceeded 不得计数")
	l.Allow("k", 2, t0)
	require.True(t, l.Exceeded("k", 2, t0))
	require.False(t, l.Exceeded("k", 2, t0.Add(time.Minute)), "新窗口重置")
	require.False(t, l.Exceeded("k", 0, t0), "limit<=0 不限")
}

// Release 退还当前窗口一次计数；计数为 0 或窗口不符时不动。
func TestMemoryLimiterRelease(t *testing.T) {
	l := NewMemoryLimiter()
	t0 := time.Unix(1_000_000, 0)
	l.Release("k", t0) // 无桶时安全
	require.True(t, l.Allow("k", 1, t0))
	require.False(t, l.Allow("k", 1, t0))
	l.Release("k", t0)
	require.True(t, l.Allow("k", 1, t0), "退还后应可再放行一次")
	l.Release("k", t0.Add(time.Minute)) // 窗口不符不动
	require.False(t, l.Allow("k", 1, t0))
}

// 并发槽位：达上限拒绝、归还后可再占、release 幂等、归零删键、limit<=0 恒放行。
func TestMemoryConcurrency(t *testing.T) {
	c := NewMemoryConcurrency()
	r1, ok := c.Acquire("app:1", 2)
	require.True(t, ok)
	r2, ok := c.Acquire("app:1", 2)
	require.True(t, ok)
	_, ok = c.Acquire("app:1", 2)
	require.False(t, ok, "达上限应拒绝")
	_, ok = c.Acquire("app:2", 2)
	require.True(t, ok, "不同 App 互不影响")

	r1()
	r1() // 幂等：重复归还不能多还
	r3, ok := c.Acquire("app:1", 2)
	require.True(t, ok, "归还后可再占")
	_, ok = c.Acquire("app:1", 2)
	require.False(t, ok, "重复 release 不得多还槽位")

	r2()
	r3()
	require.NotContains(t, c.n, "app:1", "归零应删键")

	for i := 0; i < 5; i++ {
		_, ok := c.Acquire("app:3", 0)
		require.True(t, ok, "limit<=0 恒放行")
	}
}
