// Package writegate 按 id 节流低价值展示字段的 DB 回写：窗口内跳过重复写，
// 减少高频链路上的同步 UPDATE（SQLite 单写者下与请求日志/配额的写锁争用）。
// 配对不变量由写入方 Service 持有：成功路径 Allow 放行才写；关键状态变化
// （如失败自增 fail_count）时 MarkDirty 撤销记账，保证下一次成功立即回写。
package writegate

import (
	"sync"
	"time"
)

// HealthWriteInterval 健康展示字段（last_used_at / fail_count 清零）成功路径的回写节流窗口。
// 这些字段仅供管理端展示（健康判定走 breaker 不读库），展示滞后上限即此窗口。
const HealthWriteInterval = 60 * time.Second

// Gate 按 id 的时间窗写节流。线程安全（可在请求链路并发调用）。
type Gate struct {
	interval time.Duration
	mu       sync.Mutex
	last     map[uint64]time.Time
}

// New 创建节流窗口为 interval 的 Gate。
func New(interval time.Duration) *Gate {
	return &Gate{interval: interval, last: map[uint64]time.Time{}}
}

// Allow 首次 / 距上次放行超窗 / 被 MarkDirty 撤销过记账时返回 true 并记账；窗口内返回 false。
func (g *Gate) Allow(id uint64, now time.Time) bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	if t, ok := g.last[id]; ok && now.Sub(t) < g.interval {
		return false
	}
	g.last[id] = now
	return true
}

// MarkDirty 撤销 id 的节流记账，下一次 Allow 必放行。
func (g *Gate) MarkDirty(id uint64) {
	g.mu.Lock()
	defer g.mu.Unlock()
	delete(g.last, id)
}
