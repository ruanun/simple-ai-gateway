package billing

import (
	"testing"

	"github.com/ruanun/simple-ai-gateway/internal/model"
	"github.com/stretchr/testify/require"
)

func TestCompute(t *testing.T) {
	p := model.ModelPrice{InputPer1M: 2.5, OutputPer1M: 10, CachedInputPer1M: 1.25}
	u := Usage{PromptTokens: 1_000_000, CompletionTokens: 1_000_000}
	// 1M input @2.5 + 1M output @10 = 12.5 USD = 12_500_000 micros
	require.Equal(t, int64(12_500_000), Compute(u, p))
}

func TestComputeCachedDiscount(t *testing.T) {
	p := model.ModelPrice{InputPer1M: 2.0, OutputPer1M: 0, CachedInputPer1M: 0.5}
	// 100万 prompt 其中 100万 cached：cached 部分按 cached 价，非 cached 按 input 价
	u := Usage{PromptTokens: 1_000_000, CachedTokens: 1_000_000}
	require.Equal(t, int64(500_000), Compute(u, p)) // 全 cached @0.5 = 0.5 USD
}

// TestComputeCacheWrite 缓存写按 cache_write 价计（P12d 黄金值）。
func TestComputeCacheWrite(t *testing.T) {
	p := model.ModelPrice{InputPer1M: 3, OutputPer1M: 15, CachedInputPer1M: 0.3, CacheWritePer1M: 3.75}
	// 总输入 1000(其中读 700/写 200/普通 100)，输出 50
	u := Usage{PromptTokens: 1000, CachedTokens: 700, CacheCreationTokens: 200, CompletionTokens: 50}
	// 100×3 + 700×0.3 + 200×3.75 + 50×15 = 300+210+750+750 = 2010 micros
	require.Equal(t, int64(2010), Compute(u, p))
}

// TestComputeCacheFallbackToInput 缓存价未配置(0)回退入价——宁多收不漏收（P12d）。
func TestComputeCacheFallbackToInput(t *testing.T) {
	p := model.ModelPrice{InputPer1M: 2, OutputPer1M: 0}
	u := Usage{PromptTokens: 1000, CachedTokens: 600, CacheCreationTokens: 300}
	// 全部 1000 token 按入价 2：100×2 + 600×2 + 300×2 = 2000
	require.Equal(t, int64(2000), Compute(u, p))
}

// TestComputeExplicitFreeModel input/output 全 0 的显式免费模型仍为 0（回退只作用于缓存价）。
func TestComputeExplicitFreeModel(t *testing.T) {
	p := model.ModelPrice{}
	u := Usage{PromptTokens: 1000, CachedTokens: 600, CacheCreationTokens: 300, CompletionTokens: 100}
	require.Equal(t, int64(0), Compute(u, p))
}

func TestComputeTimes(t *testing.T) {
	p := model.ModelPrice{Type: "times", PerCallMicros: 40000} // $0.04/次
	require.Equal(t, int64(40000), ComputeTimes(p))
}

func TestApplyMultiplier(t *testing.T) {
	require.Equal(t, int64(750), ApplyMultiplier(750, 1))   // 1.0 不变
	require.Equal(t, int64(1500), ApplyMultiplier(750, 2))  // 2.0 翻倍
	require.Equal(t, int64(375), ApplyMultiplier(750, 0.5)) // 0.5 折半
	require.Equal(t, int64(750), ApplyMultiplier(750, 0))   // 0 视为 1.0
	require.Equal(t, int64(750), ApplyMultiplier(750, -3))  // 负视为 1.0
	require.Equal(t, int64(2), ApplyMultiplier(3, 0.5))     // 1.5 → 2(四舍五入,half away from zero)
	require.Equal(t, int64(51), ApplyMultiplier(101, 0.5))  // 50.5 → 51
}
