package breaker

import (
	"sync"
	"time"
)

const (
	stClosed int8 = iota
	stOpen
	stHalfOpen
)

type state struct {
	status     int8
	succ       []int64 // 窗口内成功时间戳
	fails      []int64 // 窗口内失败时间戳
	openUntil  int64
	probeLevel int
}

const memoryBreakerShards = 16

// MemoryBreaker 进程内状态机熔断器：错误率/计数开闸、冷却后半开、探测失败指数退避。
// 状态按 id 分 16 片持锁，避免高并发下全部渠道/账号/代理的熔断读写在单锁上排队。
type MemoryBreaker struct {
	shards [memoryBreakerShards]breakerShard
	p      Params
}

type breakerShard struct {
	mu     sync.Mutex
	states map[uint64]*state
}

// expireGraceSec 冷却到期后再保留状态的秒数，对齐 RedisBreaker 的键 TTL(Cooldown+1min)：
// 冷却到期后长期无流量探测的 open/half 状态惰性删除、视为已痊愈——否则内存模式下
// Recovering 恒 true、Snapshot 恒报 half，选择器持续降权且与 Redis 模式行为/展示分叉。
const expireGraceSec = 60

// expired 状态是否已过惰性过期线（须持对应分片锁调用）。
func expired(s *state, now time.Time) bool {
	return s.status != stClosed && now.Unix() >= s.openUntil+expireGraceSec
}

// recoveringLocked 半开或 open 已到冷却期（须持分片锁调用）——Recovering 与 Snapshot 的统一判定。
func recoveringLocked(s *state, now time.Time) bool {
	return s.status == stHalfOpen || (s.status == stOpen && now.Unix() >= s.openUntil)
}

// NewMemoryBreaker 构造。
func NewMemoryBreaker(p Params) *MemoryBreaker {
	b := &MemoryBreaker{p: p}
	for i := range b.shards {
		b.shards[i].states = map[uint64]*state{}
	}
	return b
}

func (b *MemoryBreaker) shard(id uint64) *breakerShard {
	return &b.shards[id%memoryBreakerShards]
}

// Allow 闭合或半开(冷却到期)→ 可选；open 冷却到期时转半开。
func (b *MemoryBreaker) Allow(id uint64, now time.Time) bool {
	if b.p.Disabled() {
		return true
	}
	sh := b.shard(id)
	sh.mu.Lock()
	defer sh.mu.Unlock()
	s := sh.states[id]
	if s == nil || s.status == stClosed {
		return true
	}
	if expired(s, now) { // 惰性过期 → 闭合
		delete(sh.states, id)
		return true
	}
	if now.Unix() >= s.openUntil {
		s.status = stHalfOpen
		return true
	}
	return false
}

// Recovering 处于半开(或 open 已到期即将转半开)→ 供选择器降权。
func (b *MemoryBreaker) Recovering(id uint64, now time.Time) bool {
	if b.p.Disabled() {
		return false
	}
	sh := b.shard(id)
	sh.mu.Lock()
	defer sh.mu.Unlock()
	s := sh.states[id]
	if s == nil {
		return false
	}
	if expired(s, now) { // 惰性过期 → 闭合
		delete(sh.states, id)
		return false
	}
	return recoveringLocked(s, now)
}

// RecordResult 记结果：半开态视为探测结果(成功闭/失败退避重开)；闭合态按 is429/错误率/计数判开。
func (b *MemoryBreaker) RecordResult(id uint64, now time.Time, ok, is429 bool) {
	if b.p.Disabled() {
		return
	}
	sh := b.shard(id)
	sh.mu.Lock()
	defer sh.mu.Unlock()
	s := sh.states[id]
	if s != nil && expired(s, now) { // 惰性过期 → 按闭合重新起步
		delete(sh.states, id)
		s = nil
	}
	if s == nil {
		s = &state{}
		sh.states[id] = s
	}
	switch s.status {
	case stHalfOpen:
		if ok {
			delete(sh.states, id) // 探测成功 → 闭合清零
			return
		}
		mult := int64(1) << minInt(s.probeLevel, b.p.ProbeBackoffMax) // wasProbe 退避：只有探测失败才涨
		s.openUntil = now.Add(b.p.Cooldown * time.Duration(mult)).Unix()
		s.status = stOpen
		s.probeLevel++
		s.succ, s.fails = nil, nil
		return
	case stOpen:
		return // 并发 straggler，已开，忽略
	}
	// stClosed
	if is429 {
		b.open(s, now)
		return
	}
	cut := now.Add(-b.p.Window).Unix()
	nowU := now.Unix()
	if ok {
		// pruneAppend 已按同一 cut 裁剪并追加 nowU(>=cut)，无需再裁一次。
		s.succ = pruneAppend(s.succ, cut, nowU)
		return
	}
	s.fails = pruneAppend(s.fails, cut, nowU)
	s.succ = pruneAppend(s.succ, cut, -1) // 失败路径同步裁剪成功窗口，让 shouldOpen 按最新样本数算错误率
	if b.shouldOpen(s) {
		b.open(s, now)
	}
}

func (b *MemoryBreaker) open(s *state, now time.Time) {
	s.openUntil = now.Add(b.p.Cooldown).Unix()
	s.status = stOpen
	s.probeLevel = 0
	s.succ, s.fails = nil, nil
}

func (b *MemoryBreaker) shouldOpen(s *state) bool {
	f := len(s.fails)
	if b.p.FailThreshold > 0 && f >= b.p.FailThreshold {
		return true
	}
	total := f + len(s.succ)
	if b.p.ErrorRate > 0 && total >= b.p.MinSamples && float64(f)/float64(total) >= b.p.ErrorRate {
		return true
	}
	return false
}

// pruneAppend 保留 >=cut 的时间戳；add>=0 时追加。
func pruneAppend(ts []int64, cut, add int64) []int64 {
	kept := ts[:0]
	for _, t := range ts {
		if t >= cut {
			kept = append(kept, t)
		}
	}
	if add >= 0 {
		kept = append(kept, add)
	}
	return kept
}

// Snapshot 返回入参 id 中处于非闭合(open/half)的熔断状态；闭合/无记录项省略。
func (b *MemoryBreaker) Snapshot(ids []uint64, now time.Time) map[uint64]Status {
	out := map[uint64]Status{}
	if b.p.Disabled() {
		return out
	}
	for _, id := range ids {
		sh := b.shard(id)
		sh.mu.Lock()
		s := sh.states[id]
		if s == nil || s.status == stClosed {
			sh.mu.Unlock()
			continue
		}
		if expired(s, now) { // 惰性过期 → 闭合，不产出
			delete(sh.states, id)
			sh.mu.Unlock()
			continue
		}
		st := Status{OpenUntil: s.openUntil, ProbeLevel: s.probeLevel}
		if recoveringLocked(s, now) {
			st.State = "half" // 与 Recovering 语义一致：半开或 open 已到期
		} else {
			st.State = "open"
		}
		sh.mu.Unlock()
		out[id] = st
	}
	return out
}

// Reset 清除入参 id 的熔断状态，恢复为闭合。
func (b *MemoryBreaker) Reset(ids ...uint64) {
	if b.p.Disabled() {
		return
	}
	for _, id := range ids {
		sh := b.shard(id)
		sh.mu.Lock()
		delete(sh.states, id)
		sh.mu.Unlock()
	}
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}
