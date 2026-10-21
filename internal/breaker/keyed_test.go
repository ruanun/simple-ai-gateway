package breaker

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// TestMemoryKeyedBackoffDoubles 配额失败按指数退避：首次 60s，第二次 120s，封顶 max。
func TestMemoryKeyedBackoffDoubles(t *testing.T) {
	b := NewMemoryKeyed(time.Minute, 30*time.Minute)
	now := time.Unix(1_000_000, 0)
	key := AccountModelKey(1, "gpt-4o")

	require.True(t, b.Allow(key, now), "无记录时放行")

	// 第 1 次配额失败 → 冷却 60s
	b.RecordQuotaFailure(key, now, 0)
	require.False(t, b.Allow(key, now.Add(59*time.Second)))
	require.True(t, b.Allow(key, now.Add(60*time.Second)), "60s 后到期放行(探测)")

	// 第 2 次配额失败(探测又失败) → 冷却 120s
	probe := now.Add(60 * time.Second)
	b.RecordQuotaFailure(key, probe, 0)
	require.False(t, b.Allow(key, probe.Add(119*time.Second)))
	require.True(t, b.Allow(key, probe.Add(120*time.Second)))
}

// TestMemoryKeyedBackoffCap 退避封顶：连续多次失败后冷却不超过 max。
// 每轮等冷却到期后再失败（冷却窗口内的失败会被吸收、不升级）。
func TestMemoryKeyedBackoffCap(t *testing.T) {
	b := NewMemoryKeyed(time.Minute, 30*time.Minute)
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

// TestMemoryKeyedSuccessResets 成功清零退避等级与冷却。
func TestMemoryKeyedSuccessResets(t *testing.T) {
	b := NewMemoryKeyed(time.Minute, 30*time.Minute)
	now := time.Unix(1_000_000, 0)
	key := AccountModelKey(1, "gpt-4o")
	b.RecordQuotaFailure(key, now, 0)
	b.RecordQuotaFailure(key, now, 0)
	b.RecordSuccess(key)
	require.True(t, b.Allow(key, now), "成功后立即放行")
	// 清零后再次失败应回到 base 冷却(60s)而非继续升级
	b.RecordQuotaFailure(key, now, 0)
	require.False(t, b.Allow(key, now.Add(59*time.Second)))
	require.True(t, b.Allow(key, now.Add(60*time.Second)))
}

// TestMemoryKeyedNoEscalationWhileCooling 冷却窗口内的重复失败被吸收：不升级等级、不延长冷却。
func TestMemoryKeyedNoEscalationWhileCooling(t *testing.T) {
	b := NewMemoryKeyed(time.Minute, 30*time.Minute)
	now := time.Unix(1_000_000, 0)
	key := AccountModelKey(1, "gpt-4o")
	for i := 0; i < 3; i++ { // 并发 429 风暴：同一时刻回调 3 次
		b.RecordQuotaFailure(key, now, 0)
	}
	require.False(t, b.Allow(key, now.Add(59*time.Second)))
	require.True(t, b.Allow(key, now.Add(60*time.Second)), "冷却仍是 60s，窗口内重复失败不升级")
	// 到期后的失败才升级 → 冷却 120s
	probe := now.Add(60 * time.Second)
	b.RecordQuotaFailure(key, probe, 0)
	require.False(t, b.Allow(key, probe.Add(119*time.Second)))
	require.True(t, b.Allow(key, probe.Add(120*time.Second)))
}

// TestMemoryKeyedKeysIndependent 不同键互不影响（账号×模型隔离）。
func TestMemoryKeyedKeysIndependent(t *testing.T) {
	b := NewMemoryKeyed(time.Minute, 30*time.Minute)
	now := time.Unix(1_000_000, 0)
	b.RecordQuotaFailure(AccountModelKey(1, "model-a"), now, 0)
	require.False(t, b.Allow(AccountModelKey(1, "model-a"), now))
	require.True(t, b.Allow(AccountModelKey(1, "model-b"), now), "同账号其他模型不受影响")
	require.True(t, b.Allow(AccountModelKey(2, "model-a"), now), "其他账号同模型不受影响")
}

// TestMemoryKeyedRetryAfterOverridesBackoff retryAfter 大于指数退避基准时以 retryAfter 为准
// （即便当前等级算出的退避远小于 retryAfter，也要尊重服务端明确给出的等待建议）。
func TestMemoryKeyedRetryAfterOverridesBackoff(t *testing.T) {
	b := NewMemoryKeyed(time.Minute, 30*time.Minute)
	now := time.Unix(1_000_000, 0)
	key := AccountModelKey(1, "gpt-4o")
	b.RecordQuotaFailure(key, now, 5*time.Minute) // 首次退避基准仅 60s，远小于 retryAfter
	require.False(t, b.Allow(key, now.Add(5*time.Minute-time.Second)))
	require.True(t, b.Allow(key, now.Add(5*time.Minute)), "冷却应取 retryAfter(5min) 而非更短的指数退避基准")
}

// TestMemoryKeyedRetryAfterClampedToMax retryAfter 远超 max 时应被封顶到 max：
// 恶意/故障上游给出离谱 Retry-After（如 999999999s ≈ 31 年）不应绕过 max 冷却把键锁上数十年。
func TestMemoryKeyedRetryAfterClampedToMax(t *testing.T) {
	b := NewMemoryKeyed(time.Minute, 30*time.Minute)
	now := time.Unix(1_000_000, 0)
	key := AccountModelKey(1, "gpt-4o")
	b.RecordQuotaFailure(key, now, 999_999_999*time.Second) // 离谱的 Retry-After
	require.False(t, b.Allow(key, now.Add(30*time.Minute-time.Second)))
	require.True(t, b.Allow(key, now.Add(30*time.Minute)), "冷却应被封顶到 max(30min)，而非离谱的 retryAfter")
}
