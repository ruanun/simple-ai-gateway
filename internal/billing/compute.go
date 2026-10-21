package billing

import (
	"math"

	"github.com/ruanun/simple-ai-gateway/internal/model"
)

// Compute 计算成本（USD 微量整数）。归一口径(P12d)：PromptTokens 为总输入，
// 缓存读按 cached 价、缓存写按 cache_write 价、其余按 input 价；completion 全按 output 价。
// 缓存价未配置(<=0)回退 input 价——宁可多收不漏收；input/output 为 0 保持显式免费语义。
//
// 单位推导：per_1m 是「USD / 1,000,000 token」。
// 成本(micros) = Σ(token × price)，最后四舍五入取整。
func Compute(u Usage, p model.ModelPrice) int64 {
	cachedPrice := p.CachedInputPer1M
	if cachedPrice <= 0 {
		cachedPrice = p.InputPer1M
	}
	writePrice := p.CacheWritePer1M
	if writePrice <= 0 {
		writePrice = p.InputPer1M
	}
	nonCached := u.PromptTokens - u.CachedTokens - u.CacheCreationTokens
	if nonCached < 0 {
		nonCached = 0
	}
	micros := 0.0
	micros += float64(nonCached) * p.InputPer1M
	micros += float64(u.CachedTokens) * cachedPrice
	micros += float64(u.CacheCreationTokens) * writePrice
	micros += float64(u.CompletionTokens) * p.OutputPer1M
	return int64(math.Round(micros))
}

// ComputeTimes 返回按次计费的单次成本(micros)。
func ComputeTimes(p model.ModelPrice) int64 {
	if p.PerCallMicros < 0 {
		return 0
	}
	return p.PerCallMicros
}

// ApplyMultiplier 对基础成本(micros)乘以渠道倍率并四舍五入;mult<=0 视为 1.0(不变)。
func ApplyMultiplier(base int64, mult float64) int64 {
	if mult <= 0 {
		mult = 1
	}
	return int64(math.Round(float64(base) * mult))
}
