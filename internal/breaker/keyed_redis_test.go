package breaker

import (
	"context"
	"testing"
	"time"

	"github.com/ruanun/simple-ai-gateway/internal/redistest"
	"github.com/stretchr/testify/require"
)

// TestRedisKeyedBackoffDoubles 配额失败指数退避（与 MemoryKeyed 语义一致）。
func TestRedisKeyedBackoffDoubles(t *testing.T) {
	cli := redistest.Client(t)
	b := NewRedisKeyed(cli, time.Minute, 30*time.Minute)
	now := time.Unix(1_000_000, 0)
	key := AccountModelKey(1, "gpt-4o")

	require.True(t, b.Allow(key, now))
	b.RecordQuotaFailure(key, now, 0)
	require.False(t, b.Allow(key, now.Add(59*time.Second)))
	require.True(t, b.Allow(key, now.Add(60*time.Second)))

	probe := now.Add(60 * time.Second)
	b.RecordQuotaFailure(key, probe, 0)
	require.False(t, b.Allow(key, probe.Add(119*time.Second)))
	require.True(t, b.Allow(key, probe.Add(120*time.Second)))
}

// TestRedisKeyedNoEscalationWhileCooling 冷却窗口内重复失败不升级（与 MemoryKeyed 语义一致）。
func TestRedisKeyedNoEscalationWhileCooling(t *testing.T) {
	cli := redistest.Client(t)
	b := NewRedisKeyed(cli, time.Minute, 30*time.Minute)
	now := time.Unix(1_000_000, 0)
	key := AccountModelKey(1, "gpt-4o")
	b.RecordQuotaFailure(key, now, 0)
	b.RecordQuotaFailure(key, now, 0)
	b.RecordQuotaFailure(key, now, 0)
	require.False(t, b.Allow(key, now.Add(59*time.Second)))
	require.True(t, b.Allow(key, now.Add(60*time.Second)), "窗口内重复失败被吸收，冷却仍为 60s")

	// 到期后再失败才升级到 120s
	probe := now.Add(60 * time.Second)
	b.RecordQuotaFailure(key, probe, 0)
	require.False(t, b.Allow(key, probe.Add(119*time.Second)))
	require.True(t, b.Allow(key, probe.Add(120*time.Second)))
}

// TestRedisKeyedSuccessResets 成功清零冷却与退避等级。
func TestRedisKeyedSuccessResets(t *testing.T) {
	cli := redistest.Client(t)
	b := NewRedisKeyed(cli, time.Minute, 30*time.Minute)
	now := time.Unix(1_000_000, 0)
	key := AccountModelKey(1, "gpt-4o")
	b.RecordQuotaFailure(key, now, 0)
	b.RecordSuccess(key)
	require.True(t, b.Allow(key, now))
	b.RecordQuotaFailure(key, now, 0)
	require.False(t, b.Allow(key, now.Add(59*time.Second)))
	require.True(t, b.Allow(key, now.Add(60*time.Second)), "清零后回到 base 冷却")
}

// TestRedisKeyedBackoffCap 退避封顶：连续多次失败后冷却不超过 max（与 MemoryKeyed 语义一致）。
// 每轮等冷却到期后再失败（冷却窗口内的失败会被吸收、不升级）。
func TestRedisKeyedBackoffCap(t *testing.T) {
	cli := redistest.Client(t)
	b := NewRedisKeyed(cli, time.Minute, 30*time.Minute)
	ts := time.Unix(1_000_000, 0)
	key := AccountModelKey(1, "gpt-4o")
	for i := 0; i < 10; i++ { // 60s<<10 远超 30min
		b.RecordQuotaFailure(key, ts, 0)
		if i < 9 {
			ts = ts.Add(30 * time.Minute) // 冷却最长 30min，推进 30min 保证到期后再失败
		}
	}
	require.False(t, b.Allow(key, ts.Add(30*time.Minute-time.Second)))
	require.True(t, b.Allow(key, ts.Add(30*time.Minute)), "冷却封顶 30min")
}

// TestRedisKeyedRetryAfterClampedToMax retryAfter 远超 max 时应被封顶到 max（与 MemoryKeyed 语义一致）：
// 恶意/故障上游给出离谱 Retry-After 不应绕过 max 冷却把键锁上数十年。
func TestRedisKeyedRetryAfterClampedToMax(t *testing.T) {
	cli := redistest.Client(t)
	b := NewRedisKeyed(cli, time.Minute, 30*time.Minute)
	now := time.Unix(1_000_000, 0)
	key := AccountModelKey(1, "gpt-4o")
	b.RecordQuotaFailure(key, now, 999_999_999*time.Second) // 离谱的 Retry-After
	require.False(t, b.Allow(key, now.Add(30*time.Minute-time.Second)))
	require.True(t, b.Allow(key, now.Add(30*time.Minute)), "冷却应被封顶到 max(30min)，而非离谱的 retryAfter")
}

// TestRedisKeyedCorruptValueFailOpen open 键被写入非数字脏值时 Allow 应 fail-open 放行
// （与 Get/ParseInt 出错时的 fail-open 风格一致，避免脏数据导致永久拒绝）。
func TestRedisKeyedCorruptValueFailOpen(t *testing.T) {
	cli := redistest.Client(t)
	b := NewRedisKeyed(cli, time.Minute, 30*time.Minute)
	key := AccountModelKey(1, "gpt-4o")
	require.NoError(t, cli.Set(context.Background(), kOpenKey(key), "not-a-number", 0).Err())
	require.True(t, b.Allow(key, time.Unix(1_000_000, 0)), "open 键非数字应 fail-open 放行")
}
