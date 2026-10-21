package breaker

import (
	"strconv"
	"strings"
	"sync"
	"time"
)

// Keyed 字符串键熔断器：面向「账号×模型」等复合维度的配额冷却。
// 配额类失败(429/403)走指数退避冷却(base×2^level，封顶 max)，成功清零。
// 冷却到期后放行（无单探测门控）；到期后的首次失败以更高等级重新进入更长冷却，
// 同一冷却窗口内的重复失败不升级等级（吸收并发 429 风暴）。
// 实现允许长期闲置后退避等级衰减归零（如 Redis 实现的 level 键 TTL 到期自动清零）。
type Keyed interface {
	Allow(key string, now time.Time) bool
	// RecordQuotaFailure 记一次配额失败。retryAfter 为上游 Retry-After 响应头解析出的建议等待
	// 时长（无则传 0）：实际冷却时长取 max(指数退避, retryAfter)，尊重服务端明确给出的等待建议。
	RecordQuotaFailure(key string, now time.Time, retryAfter time.Duration)
	RecordSuccess(key string)
	// SnapshotPrefix 返回 prefix 下仍在冷却中的键→冷却截止(unix 秒)，供管理端展示配额冷却
	// （低频管理操作，实现可全量遍历/SCAN）。
	SnapshotPrefix(prefix string, now time.Time) map[string]int64
	// ResetPrefix 清除 prefix 下全部键的冷却与退避等级，供管理端手动重置。
	ResetPrefix(prefix string)
}

// AccountModelKey 账号×模型冷却键（统一格式，Memory/Redis 共用）。
func AccountModelKey(accountID uint64, modelName string) string {
	return AccountModelPrefix(accountID) + modelName
}

// AccountModelPrefix 某账号全部模型冷却键的公共前缀（SnapshotPrefix/ResetPrefix 用）。
func AccountModelPrefix(accountID uint64) string {
	return "am:" + strconv.FormatUint(accountID, 10) + ":"
}

// backoff 计算第 level 次配额失败的冷却时长：base×2^level，封顶 max。
func backoff(base, max time.Duration, level int) time.Duration {
	if base >= max { // 防御：误配 base>max 时仍守住封顶语义
		return max
	}
	d := base
	for i := 0; i < level; i++ {
		d *= 2
		if d >= max {
			return max
		}
	}
	return d
}

// keyedState 单键退避状态。
type keyedState struct {
	openUntil int64 // 冷却截止(unix 秒)
	level     int   // 退避等级(连续配额失败次数)
}

// MemoryKeyed 进程内 Keyed 实现。
type MemoryKeyed struct {
	mu     sync.Mutex
	states map[string]*keyedState
	base   time.Duration
	max    time.Duration
}

// NewMemoryKeyed 构造进程内字符串键熔断器。
func NewMemoryKeyed(base, max time.Duration) *MemoryKeyed {
	return &MemoryKeyed{states: map[string]*keyedState{}, base: base, max: max}
}

// Allow 键当前是否可用（未在冷却中）。
func (b *MemoryKeyed) Allow(key string, now time.Time) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	s := b.states[key]
	if s == nil {
		return true
	}
	return now.Unix() >= s.openUntil
}

// RecordQuotaFailure 记一次配额失败：按当前等级退避冷却并升级；retryAfter 更大时以其为准。
// 键仍在冷却中时直接返回（不升级、不延长），每个冷却窗口最多升级一次，
// 避免并发 429 各自回调把等级瞬间推到封顶。
func (b *MemoryKeyed) RecordQuotaFailure(key string, now time.Time, retryAfter time.Duration) {
	b.mu.Lock()
	defer b.mu.Unlock()
	s := b.states[key]
	if s == nil {
		s = &keyedState{}
		b.states[key] = s
	}
	if now.Unix() < s.openUntil { // 冷却中：吸收重复失败
		return
	}
	d := backoff(b.base, b.max, s.level)
	if retryAfter > b.max { // 封顶：拒绝上游给出的离谱 Retry-After（如恶意/故障上游回 999999999），避免绕过 max 冷却整个键
		retryAfter = b.max
	}
	if retryAfter > d {
		d = retryAfter
	}
	s.openUntil = now.Add(d).Unix()
	s.level++
}

// RecordSuccess 清除冷却与退避等级。
func (b *MemoryKeyed) RecordSuccess(key string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	delete(b.states, key)
}

// SnapshotPrefix 返回 prefix 下仍在冷却中的键→冷却截止；已到期的键不产出。
func (b *MemoryKeyed) SnapshotPrefix(prefix string, now time.Time) map[string]int64 {
	b.mu.Lock()
	defer b.mu.Unlock()
	out := map[string]int64{}
	for k, s := range b.states {
		if strings.HasPrefix(k, prefix) && now.Unix() < s.openUntil {
			out[k] = s.openUntil
		}
	}
	return out
}

// ResetPrefix 清除 prefix 下全部键。
func (b *MemoryKeyed) ResetPrefix(prefix string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	for k := range b.states {
		if strings.HasPrefix(k, prefix) {
			delete(b.states, k)
		}
	}
}
