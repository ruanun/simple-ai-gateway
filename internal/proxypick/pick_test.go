package proxypick

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
	ps := []model.Proxy{
		{BaseModel: model.BaseModel{ID: 1}, Weight: 70, Status: 1},
		{BaseModel: model.BaseModel{ID: 2}, Weight: 30, Status: 1},
	}
	brk := newTestBreaker(3, time.Minute, time.Minute)
	now := time.Unix(1_000_000, 0)
	c := map[uint64]int{}
	for i := 0; i < 1000; i++ {
		p := Pick(ps, brk, now, seq(i), 1.0)
		require.NotNil(t, p)
		c[p.ID]++
	}
	require.Greater(t, c[1], c[2])
	require.Greater(t, c[2], 0)
}

func TestPickSkipsTripped(t *testing.T) {
	ps := []model.Proxy{
		{BaseModel: model.BaseModel{ID: 1}, Weight: 1, Status: 1},
		{BaseModel: model.BaseModel{ID: 2}, Weight: 1, Status: 1},
	}
	brk := newTestBreaker(1, time.Minute, time.Minute)
	now := time.Unix(1_000_000, 0)
	brk.RecordResult(1, now, false, true)
	for i := 0; i < 50; i++ {
		require.Equal(t, uint64(2), Pick(ps, brk, now, seq(i), 1.0).ID)
	}
}

func TestPickLayered(t *testing.T) {
	ps := []model.Proxy{
		{BaseModel: model.BaseModel{ID: 1}, Weight: 1, Priority: 1, Status: 1}, // 主
		{BaseModel: model.BaseModel{ID: 2}, Weight: 1, Priority: 0, Status: 1}, // 降级
	}
	brk := newTestBreaker(1, time.Minute, time.Minute)
	now := time.Unix(1_000_000, 0)
	require.Equal(t, uint64(1), Pick(ps, brk, now, seq(1), 1.0).ID) // 高优先层优先

	brk.RecordResult(1, now, false, true)                           // 主熔断
	require.Equal(t, uint64(2), Pick(ps, brk, now, seq(1), 1.0).ID) // 降级到 P0
}

func TestPickNone(t *testing.T) {
	require.Nil(t, Pick(nil, newTestBreaker(1, time.Minute, time.Minute), time.Unix(1_000_000, 0), seq(1), 1.0))
}

// TestPickRecoverFactorSkips recoverFactor=0 时，半开代理在有健康备选代理的情况下应被概率跳过。
func TestPickRecoverFactorSkips(t *testing.T) {
	now := time.Unix(1000, 0)
	brk := breaker.NewMemoryBreaker(breaker.Params{Window: 30 * time.Second, Cooldown: 60 * time.Second, FailThreshold: 1, ErrorRate: 0.5, MinSamples: 20, ProbeBackoffMax: 8})
	brk.RecordResult(2, now, false, false) // 代理2 开
	later := now.Add(61 * time.Second)
	_ = brk.Allow(2, later) // 触发转半开(Recovering)
	ps := []model.Proxy{{BaseModel: model.BaseModel{ID: 1}, Status: 1, Weight: 1}, {BaseModel: model.BaseModel{ID: 2}, Status: 1, Weight: 1}}
	hits := map[uint64]int{}
	for i := 0; i < 200; i++ {
		rng := func(n int) int { return i % n } // i<200<1000 → rng(1000)=i≥0，factor=0 时半开代理2必跳
		p := Pick(ps, brk, later, rng, 0.0)     // recoverFactor=0
		if p != nil {
			hits[p.ID]++
		}
	}
	require.Equal(t, 0, hits[2], "recover_factor=0：有健康代理1时半开代理2应被跳过")
	require.Equal(t, 200, hits[1])
}
