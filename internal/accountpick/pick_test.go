package accountpick

import (
	"testing"
	"time"

	"github.com/ruanun/simple-ai-gateway/internal/breaker"
	"github.com/ruanun/simple-ai-gateway/internal/model"
	"github.com/stretchr/testify/require"
)

func seq(seed int) func(n int) int {
	s := int64(seed*2654435761 + 1)
	return func(n int) int {
		if n <= 0 {
			return 0
		}
		s = (s*1103515245 + 12345) & 0x7fffffff
		return int(s) % n
	}
}

func newTestBreaker(n int, w, c time.Duration) *breaker.MemoryBreaker {
	return breaker.NewMemoryBreaker(breaker.Params{Window: w, Cooldown: c, FailThreshold: n, ErrorRate: 0.5, MinSamples: 20, ProbeBackoffMax: 8})
}

func TestPickWeighted(t *testing.T) {
	accs := []model.Account{
		{BaseModel: model.BaseModel{ID: 1}, Weight: 70, Priority: 0, Status: 1},
		{BaseModel: model.BaseModel{ID: 2}, Weight: 30, Priority: 0, Status: 1},
	}
	brk := newTestBreaker(3, time.Minute, time.Minute)
	now := time.Unix(1_000_000, 0)
	counts := map[uint64]int{}
	for i := 0; i < 1000; i++ {
		acc := Pick(accs, brk, nil, "", now, seq(i), nil, 1.0)
		require.NotNil(t, acc)
		counts[acc.ID]++
	}
	require.Greater(t, counts[1], counts[2]) // 70 权重更常被选
	require.Greater(t, counts[2], 0)         // 30 也被选到
}

func TestPickSkipsTripped(t *testing.T) {
	accs := []model.Account{
		{BaseModel: model.BaseModel{ID: 1}, Weight: 1, Status: 1},
		{BaseModel: model.BaseModel{ID: 2}, Weight: 1, Status: 1},
	}
	brk := newTestBreaker(1, time.Minute, time.Minute)
	now := time.Unix(1_000_000, 0)
	brk.RecordResult(1, now, false, true) // 账号1 立即熔断
	for i := 0; i < 50; i++ {
		acc := Pick(accs, brk, nil, "", now, seq(i), nil, 1.0)
		require.NotNil(t, acc)
		require.Equal(t, uint64(2), acc.ID) // 永远选未熔断的2
	}
}

func TestPickLayered(t *testing.T) {
	accs := []model.Account{
		{BaseModel: model.BaseModel{ID: 1}, Weight: 1, Priority: 1, Status: 1}, // 主
		{BaseModel: model.BaseModel{ID: 2}, Weight: 1, Priority: 0, Status: 1}, // 降级
	}
	brk := newTestBreaker(1, time.Minute, time.Minute)
	now := time.Unix(1_000_000, 0)
	acc := Pick(accs, brk, nil, "", now, seq(1), nil, 1.0)
	require.Equal(t, uint64(1), acc.ID) // 高优先级层优先

	brk.RecordResult(1, now, false, true) // 主熔断
	acc = Pick(accs, brk, nil, "", now, seq(1), nil, 1.0)
	require.Equal(t, uint64(2), acc.ID) // 降级到 P0
}

func TestPickNoneAvailable(t *testing.T) {
	brk := newTestBreaker(1, time.Minute, time.Minute)
	require.Nil(t, Pick(nil, brk, nil, "", time.Unix(1_000_000, 0), seq(1), nil, 1.0))
}

// TestPickExcludesTried 验证 exclude 集中的账号被跳过（未熔断也不选回）：
// 账号1 在排除集 → 永远选账号2；排除集含全部 → 返回 nil。
func TestPickExcludesTried(t *testing.T) {
	accs := []model.Account{
		{BaseModel: model.BaseModel{ID: 1}, Weight: 1, Status: 1},
		{BaseModel: model.BaseModel{ID: 2}, Weight: 1, Status: 1},
	}
	brk := newTestBreaker(3, time.Minute, time.Minute)
	now := time.Unix(1_000_000, 0)
	for i := 0; i < 50; i++ {
		acc := Pick(accs, brk, nil, "", now, seq(i), map[uint64]bool{1: true}, 1.0)
		require.NotNil(t, acc)
		require.Equal(t, uint64(2), acc.ID) // 1 被排除，只剩 2
	}
	require.Nil(t, Pick(accs, brk, nil, "", now, seq(1), map[uint64]bool{1: true, 2: true}, 1.0)) // 全排除 → nil
}

// TestPickSkipsModelCooledAccount 账号×模型冷却中的账号被跳过；同账号其他模型不受影响。
func TestPickSkipsModelCooledAccount(t *testing.T) {
	accounts := []model.Account{
		{BaseModel: model.BaseModel{ID: 1}, Status: 1, Weight: 1, Priority: 0},
		{BaseModel: model.BaseModel{ID: 2}, Status: 1, Weight: 1, Priority: 0},
	}
	now := time.Unix(1_000_000, 0)
	keyed := breaker.NewMemoryKeyed(time.Minute, 30*time.Minute)
	keyed.RecordQuotaFailure(breaker.AccountModelKey(1, "model-a"), now, 0)

	// model-a：账号 1 冷却中 → 只能选 2
	got := Pick(accounts, nil, keyed, "model-a", now, func(n int) int { return 0 }, nil, 1.0)
	require.NotNil(t, got)
	require.Equal(t, uint64(2), got.ID)

	// model-b：账号 1 不受影响，rng 恒 0 应选 ID 小的账号 1
	got = Pick(accounts, nil, keyed, "model-b", now, func(n int) int { return 0 }, nil, 1.0)
	require.NotNil(t, got)
	require.Equal(t, uint64(1), got.ID)

	// 全冷却 → nil
	keyed.RecordQuotaFailure(breaker.AccountModelKey(2, "model-a"), now, 0)
	require.Nil(t, Pick(accounts, nil, keyed, "model-a", now, func(n int) int { return 0 }, nil, 1.0))
}

// TestPickNilKeyedSkipsCheck keyed 为 nil 或 modelName 为空时安全跳过检查。
func TestPickNilKeyedSkipsCheck(t *testing.T) {
	accounts := []model.Account{{BaseModel: model.BaseModel{ID: 1}, Status: 1, Weight: 1}}
	now := time.Unix(1_000_000, 0)
	require.NotNil(t, Pick(accounts, nil, nil, "model-a", now, func(n int) int { return 0 }, nil, 1.0))
	keyed := breaker.NewMemoryKeyed(time.Minute, 30*time.Minute)
	require.NotNil(t, Pick(accounts, nil, keyed, "", now, func(n int) int { return 0 }, nil, 1.0))
}

// TestPickRecoverFactorSkips recoverFactor=0 时，半开账号在有健康备选账号的情况下应被概率跳过。
func TestPickRecoverFactorSkips(t *testing.T) {
	now := time.Unix(1000, 0)
	brk := breaker.NewMemoryBreaker(breaker.Params{Window: 30 * time.Second, Cooldown: 60 * time.Second, FailThreshold: 1, ErrorRate: 0.5, MinSamples: 20, ProbeBackoffMax: 8})
	brk.RecordResult(2, now, false, false) // 账号2 开
	later := now.Add(61 * time.Second)
	_ = brk.Allow(2, later) // 触发转半开(Recovering)
	accts := []model.Account{{BaseModel: model.BaseModel{ID: 1}, Status: 1, Weight: 1}, {BaseModel: model.BaseModel{ID: 2}, Status: 1, Weight: 1}}
	hits := map[uint64]int{}
	for i := 0; i < 200; i++ {
		rng := func(n int) int { return i % n }              // i<200<1000 → rng(1000)=i≥0，factor=0 时半开账号2必跳
		a := Pick(accts, brk, nil, "", later, rng, nil, 0.0) // recoverFactor=0
		if a != nil {
			hits[a.ID]++
		}
	}
	require.Equal(t, 0, hits[2], "recover_factor=0：有健康账号1时半开账号2应被跳过")
	require.Equal(t, 200, hits[1])
}
