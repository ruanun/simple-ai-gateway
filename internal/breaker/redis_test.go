package breaker

import (
	"testing"
	"time"

	"github.com/ruanun/simple-ai-gateway/internal/redistest"
	"github.com/stretchr/testify/require"
)

func TestRedisCountOpen(t *testing.T) {
	cli := redistest.Client(t)
	b := NewRedisBreaker(cli, "ch", p(3))
	now := time.Unix(1000, 0)
	require.True(t, b.Allow(1, now))
	for i := 0; i < 3; i++ {
		b.RecordResult(1, now, false, false)
	}
	require.False(t, b.Allow(1, now))
	require.True(t, b.Allow(1, now.Add(61*time.Second)))
}

func TestRedis429ImmediateOpen(t *testing.T) {
	cli := redistest.Client(t)
	b := NewRedisBreaker(cli, "ch", p(5))
	now := time.Unix(1000, 0)
	b.RecordResult(1, now, false, true)
	require.False(t, b.Allow(1, now))
}

func TestRedisHalfOpenProbeFailBacksOff(t *testing.T) {
	cli := redistest.Client(t)
	b := NewRedisBreaker(cli, "ch", p(1))
	now := time.Unix(1000, 0)
	b.RecordResult(1, now, false, false)
	t1 := now.Add(61 * time.Second)
	require.True(t, b.Allow(1, t1))     // 半开
	b.RecordResult(1, t1, false, false) // 探测失败重开(1×cooldown)
	require.False(t, b.Allow(1, t1.Add(59*time.Second)))
	require.True(t, b.Allow(1, t1.Add(61*time.Second)))
}

func TestRedisPrefixIsolation(t *testing.T) {
	cli := redistest.Client(t)
	ch := NewRedisBreaker(cli, "ch", p(1))
	acc := NewRedisBreaker(cli, "acc", p(1))
	now := time.Unix(1000, 0)
	ch.RecordResult(5, now, false, true) // 渠道 #5 熔断
	require.False(t, ch.Allow(5, now))
	require.True(t, acc.Allow(5, now))                 // 账号 #5 不受影响（前缀隔离）
	require.Empty(t, acc.Snapshot([]uint64{5}, now))   // 账号快照为空
	require.NotEmpty(t, ch.Snapshot([]uint64{5}, now)) // 渠道快照有开路项
}
