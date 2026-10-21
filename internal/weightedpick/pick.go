// Package weightedpick 提供加权随机选择的泛型核心：启用过滤 → 额外过滤（熔断/排除集等由调用方注入）
// → 半开软探测（概率跳过 + 全跳回退）→ 最高优先级分层 → ID 稳定排序 → 权重下限 1 的加权随机。
// accountpick / proxypick 是它的薄适配层，两者的差异（exclude 排除集、keyed 账号×模型冷却）经 skip 回调注入。
package weightedpick

import "sort"

// Accessors 元素取值器：从元素中取 ID/状态/优先级/权重（model.Account / model.Proxy 各自适配）。
type Accessors[T any] struct {
	ID       func(T) uint64
	Status   func(T) int8
	Priority func(T) int
	Weight   func(T) int
}

// Pick 从 items 中选一个可用元素：启用（Status==1）+ 通过 skip 过滤，按优先级分层取最高层，层内加权随机。
// 返回指向内部副本的指针；无可用元素返回 nil。
//   - skip：额外过滤回调（可为 nil），返回 true 表示跳过该元素。在 Status 检查之后按 items 原顺序逐个调用，
//     调用方在其中编排熔断/排除集/keyed 冷却等检查的先后（brk.Allow 有副作用，顺序须由调用方保证）。
//   - recovering：半开判定回调，nil 表示不做软探测（对应原实现 brk==nil 的情形）。软探测以 (1-recoverFactor)
//     概率跳过半开元素，恢复期少打流量；全被跳过则回退保留(避免半开期无元素可选)。仅对半开元素消耗一次 rng(1000)。
//   - rng(n) 返回 [0,n) 随机数（可注入便于测试）；nil 时跳过软探测、加权随机取 0。
//   - recoverFactor 半开软探测系数；1 等价不跳过（旧行为）。
func Pick[T any](items []T, get Accessors[T], skip func(T) bool, recovering func(T) bool, rng func(n int) int, recoverFactor float64) *T {
	// 过滤：启用 + 通过额外过滤
	avail := make([]T, 0, len(items))
	for _, it := range items {
		if get.Status(it) != 1 {
			continue
		}
		if skip != nil && skip(it) {
			continue
		}
		avail = append(avail, it)
	}
	if len(avail) == 0 {
		return nil
	}
	// 半开元素软探测：以 (1-recoverFactor) 概率跳过半开元素，恢复期少打流量。
	// 全被跳过则回退保留(避免半开期无元素可选)。
	if recovering != nil && recoverFactor < 1 && rng != nil {
		kept := make([]T, 0, len(avail))
		for _, it := range avail {
			if recovering(it) && rng(1000) >= int(recoverFactor*1000) {
				continue // 跳过该半开元素
			}
			kept = append(kept, it)
		}
		if len(kept) > 0 {
			avail = kept
		}
	}
	// 取最高优先级层
	maxPrio := get.Priority(avail[0])
	for _, it := range avail {
		if p := get.Priority(it); p > maxPrio {
			maxPrio = p
		}
	}
	layer := make([]T, 0, len(avail))
	for _, it := range avail {
		if get.Priority(it) == maxPrio {
			layer = append(layer, it)
		}
	}
	// 稳定排序保证可复现
	sort.Slice(layer, func(i, j int) bool { return get.ID(layer[i]) < get.ID(layer[j]) })
	total := 0
	for _, it := range layer {
		total += max(get.Weight(it), 1)
	}
	pick := 0
	if rng != nil {
		pick = rng(total)
	}
	acc := 0
	for i := range layer {
		acc += max(get.Weight(layer[i]), 1)
		if pick < acc {
			return &layer[i]
		}
	}
	return &layer[len(layer)-1]
}
