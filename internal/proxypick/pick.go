// Package proxypick 提供代理组内代理选择：跳过熔断、按优先级分层取最高层、层内加权随机。
// 选择流程本体在 internal/weightedpick 泛型核心；本包只注入代理的熔断过滤。
package proxypick

import (
	"time"

	"github.com/ruanun/simple-ai-gateway/internal/breaker"
	"github.com/ruanun/simple-ai-gateway/internal/model"
	"github.com/ruanun/simple-ai-gateway/internal/weightedpick"
)

// Pick 从代理列表选一个可用代理：跳过熔断，按 priority 取最高层，层内加权随机。
// rng(n) 返回 [0,n) 随机数（可注入便于测试）。无可用代理返回 nil。
// recoverFactor 半开代理软探测系数：以 (1-recoverFactor) 概率跳过半开代理，恢复期少打流量；1 等价不跳过（旧行为）。
func Pick(proxies []model.Proxy, brk breaker.Breaker, now time.Time, rng func(n int) int, recoverFactor float64) *model.Proxy {
	skip := func(p model.Proxy) bool {
		return brk != nil && !brk.Allow(p.ID, now)
	}
	// brk 为 nil 时不做半开软探测（与原实现一致）。
	var recovering func(model.Proxy) bool
	if brk != nil {
		recovering = func(p model.Proxy) bool { return brk.Recovering(p.ID, now) }
	}
	return weightedpick.Pick(proxies, weightedpick.Accessors[model.Proxy]{
		ID:       func(p model.Proxy) uint64 { return p.ID },
		Status:   func(p model.Proxy) int8 { return p.Status },
		Priority: func(p model.Proxy) int { return p.Priority },
		Weight:   func(p model.Proxy) int { return p.Weight },
	}, skip, recovering, rng, recoverFactor)
}
