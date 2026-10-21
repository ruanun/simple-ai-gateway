package ratelimit

import (
	"sync"
	"time"
)

type bucket struct {
	windowStart int64
	count       int
}

// MemoryLimiter 进程内固定窗口限流器（1 分钟窗口，按 key 计数）。
type MemoryLimiter struct {
	mu       sync.Mutex
	buckets  map[string]*bucket
	sweptWin int64 // 上次清扫时所处窗口，跨窗口时回收过期桶
}

// NewMemoryLimiter 构造内存限流器。
func NewMemoryLimiter() *MemoryLimiter {
	return &MemoryLimiter{buckets: map[string]*bucket{}}
}

// Allow 固定 1 分钟窗口；limit<=0 表示不限。
func (l *MemoryLimiter) Allow(key string, limit int, now time.Time) bool {
	if limit <= 0 {
		return true
	}
	win := now.Unix() / 60
	l.mu.Lock()
	defer l.mu.Unlock()
	if win != l.sweptWin {
		l.sweepLocked(win)
	}
	b := l.buckets[key]
	if b == nil || b.windowStart != win {
		l.buckets[key] = &bucket{windowStart: win, count: 1}
		return true
	}
	if b.count >= limit {
		return false
	}
	b.count++
	return true
}

// Exceeded 见 Limiter.Exceeded。
func (l *MemoryLimiter) Exceeded(key string, limit int, now time.Time) bool {
	if limit <= 0 {
		return false
	}
	win := now.Unix() / 60
	l.mu.Lock()
	defer l.mu.Unlock()
	b := l.buckets[key]
	return b != nil && b.windowStart == win && b.count >= limit
}

// Release 见 Limiter.Release。
func (l *MemoryLimiter) Release(key string, now time.Time) {
	win := now.Unix() / 60
	l.mu.Lock()
	defer l.mu.Unlock()
	if b := l.buckets[key]; b != nil && b.windowStart == win && b.count > 0 {
		b.count--
	}
}

// sweepLocked 回收非当前窗口的桶：过期窗口的计数已无意义，不删则按 IP 建键的匿名端点
// （登录限流）可被海量源地址撑爆 map。每窗口至多扫一遍，摊到每请求可忽略。
// 调用方须已持有 l.mu。
func (l *MemoryLimiter) sweepLocked(win int64) {
	for k, b := range l.buckets {
		if b.windowStart != win {
			delete(l.buckets, k)
		}
	}
	l.sweptWin = win
}
