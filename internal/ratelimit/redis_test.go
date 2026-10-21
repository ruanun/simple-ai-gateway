package ratelimit

import (
	"testing"
	"time"

	"github.com/ruanun/simple-ai-gateway/internal/redistest"
	"github.com/stretchr/testify/require"
)

func TestRedisLimiter(t *testing.T) {
	cli := redistest.Client(t)
	l := NewRedisLimiter(cli)
	t0 := time.Unix(1_000_000, 0)
	require.True(t, l.Allow("k", 2, t0))
	require.True(t, l.Allow("k", 2, t0))
	require.False(t, l.Allow("k", 2, t0))
	require.True(t, l.Allow("k", 2, t0.Add(time.Minute)))
}

func TestRedisLimiterExceeded(t *testing.T) {
	cli := redistest.Client(t)
	l := NewRedisLimiter(cli)
	t0 := time.Unix(1_000_000, 0)
	require.False(t, l.Exceeded("k", 2, t0))
	l.Allow("k", 2, t0)
	require.False(t, l.Exceeded("k", 2, t0))
	l.Allow("k", 2, t0)
	require.True(t, l.Exceeded("k", 2, t0))
	require.False(t, l.Exceeded("k", 2, t0.Add(time.Minute)))
	require.False(t, l.Exceeded("k", 0, t0))
}

func TestRedisLimiterRelease(t *testing.T) {
	cli := redistest.Client(t)
	l := NewRedisLimiter(cli)
	t0 := time.Unix(1_000_000, 0)
	l.Release("k", t0) // 键不存在时不得减成负数
	require.True(t, l.Allow("k", 1, t0))
	require.False(t, l.Allow("k", 1, t0), "Redis 超限仍会 INCR，计数此时为 2")
	l.Release("k", t0)
	l.Release("k", t0)
	require.True(t, l.Allow("k", 1, t0), "退还两次后回到 0，应可再放行")
	l.Release("k", t0)
	l.Release("k", t0) // 已为 0，不得变负
	require.True(t, l.Allow("k", 1, t0))
	require.False(t, l.Allow("k", 1, t0), "计数未被减成负数，第二次应拒绝")
}

func TestRedisConcurrency(t *testing.T) {
	cli := redistest.Client(t)
	c := NewRedisConcurrency(cli)
	t0 := time.Unix(1_000_000, 0)
	c.now = func() time.Time { return t0 }

	r1, ok := c.Acquire("app:1", 2)
	require.True(t, ok)
	_, ok = c.Acquire("app:1", 2)
	require.True(t, ok)
	_, ok = c.Acquire("app:1", 2)
	require.False(t, ok, "达上限应拒绝")

	r1()
	r1() // 幂等
	_, ok = c.Acquire("app:1", 2)
	require.True(t, ok, "归还后可再占")
	_, ok = c.Acquire("app:1", 2)
	require.False(t, ok, "重复 release 不得多还槽位")

	// 未归还的槽位（模拟进程崩溃）在租约到期后自动失效
	c.now = func() time.Time { return t0.Add(concurrencyLease + time.Second) }
	_, ok = c.Acquire("app:1", 2)
	require.True(t, ok, "过期租约应被清理")
}
