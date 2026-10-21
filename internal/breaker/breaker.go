package breaker

import "time"

// Params 单级熔断器参数。
type Params struct {
	Window          time.Duration
	Cooldown        time.Duration
	FailThreshold   int     // 窗口内失败数达此值即开(<=0 且 ErrorRate<=0 视为禁用)
	ErrorRate       float64 // 窗口内失败率达此值(且样本>=MinSamples)即开
	MinSamples      int
	ProbeBackoffMax int // 半开探测失败倍数 2^n 的 n 封顶
}

// Disabled 阈值与错误率都<=0 → 熔断禁用。
func (p Params) Disabled() bool { return p.FailThreshold <= 0 && p.ErrorRate <= 0 }

// Status 单条熔断状态快照（供管理端展示）。State ∈ "open"(熔断) | "half"(半开)；closed 不产出。
type Status struct {
	State      string // "open" | "half"
	OpenUntil  int64  // 冷却到期 unix 秒，前端据此算倒计时
	ProbeLevel int    // 退避级别（连续探测失败次数）
}

// Breaker 熔断器：Allow 是否可选、Recovering 是否半开(供选择器降权)、RecordResult 记结果、
// Snapshot 只读快照、Reset 手动清零。
type Breaker interface {
	Allow(id uint64, now time.Time) bool
	Recovering(id uint64, now time.Time) bool
	RecordResult(id uint64, now time.Time, ok bool, is429 bool)
	Snapshot(ids []uint64, now time.Time) map[uint64]Status // 仅返回非 closed 项
	Reset(ids ...uint64)                                    // 清零回 closed
}
