// Package accountpick 提供渠道内账号选择：跳过熔断账号/账号×模型冷却账号、按优先级分层取最高层、层内加权随机。
// 选择流程本体在 internal/weightedpick 泛型核心；本包只注入账号特有的过滤条件（exclude 排除集、keyed 冷却）。
package accountpick

import (
	"time"

	"github.com/ruanun/simple-ai-gateway/internal/breaker"
	"github.com/ruanun/simple-ai-gateway/internal/model"
	"github.com/ruanun/simple-ai-gateway/internal/weightedpick"
)

// Pick 从账号列表中选一个可用账号：跳过熔断账号、账号×模型冷却账号与 exclude 集中账号，
// 按优先级分层取最高层，层内加权随机。keyed/modelName 任一为空时跳过账号×模型冷却检查。
// rng(n) 返回 [0,n) 随机数（可注入便于测试）。exclude 为本轮已试过的账号 id 集（可为 nil）。无可用账号返回 nil。
// recoverFactor 半开账号软探测系数：以 (1-recoverFactor) 概率跳过半开账号，恢复期少打流量；1 等价不跳过（旧行为）。
func Pick(accounts []model.Account, brk breaker.Breaker, keyed breaker.Keyed, modelName string, now time.Time, rng func(n int) int, exclude map[uint64]bool, recoverFactor float64) *model.Account {
	// 过滤顺序有语义（brk.Allow 会触发 open→half-open 转换）：排除集 → 熔断 → 账号×模型冷却。
	skip := func(a model.Account) bool {
		if exclude != nil && exclude[a.ID] {
			return true
		}
		if brk != nil && !brk.Allow(a.ID, now) {
			return true
		}
		if keyed != nil && modelName != "" && !keyed.Allow(breaker.AccountModelKey(a.ID, modelName), now) {
			return true
		}
		return false
	}
	// brk 为 nil 时不做半开软探测（与原实现一致）。
	var recovering func(model.Account) bool
	if brk != nil {
		recovering = func(a model.Account) bool { return brk.Recovering(a.ID, now) }
	}
	return weightedpick.Pick(accounts, weightedpick.Accessors[model.Account]{
		ID:       func(a model.Account) uint64 { return a.ID },
		Status:   func(a model.Account) int8 { return a.Status },
		Priority: func(a model.Account) int { return a.Priority },
		Weight:   func(a model.Account) int { return a.Weight },
	}, skip, recovering, rng, recoverFactor)
}
