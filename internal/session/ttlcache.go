package session

import (
	"sync"
	"time"
)

// defaultMaxEntries 内存粘连表容量上限。粘连只是命中上游缓存/会话亲和的优化，
// 被淘汰的会话下次重新选号即可，故上限取宽松值、淘汰谁无关紧要。
const defaultMaxEntries = 100_000

// minSweepInterval 过期清扫最小间隔：ttl 被配成极小值时避免每次写入都全表扫描。
const minSweepInterval = time.Minute

// ttlCache 带 TTL 与容量上限的并发安全 map，供内存模式的会话粘连表使用。
// sessionID 由客户端请求内容决定（可每请求造新值、写入后永不再读），只靠 Get 惰性过期
// 会让 map 无界增长，故写入路径兜底回收：每隔 max(TTL, 1min) 扫一遍过期条目，达容量上限再
// 按 ipgeo 同款策略删到 3/4。不起后台 goroutine，无生命周期负担。
type ttlCache[V any] struct {
	mu        sync.Mutex
	data      map[string]ttlEntry[V]
	ttl       time.Duration
	max       int
	lastSweep time.Time
	now       func() time.Time // 测试注入时钟
}

type ttlEntry[V any] struct {
	val       V
	expiresAt time.Time
}

func newTTLCache[V any](ttl time.Duration, max int) *ttlCache[V] {
	return &ttlCache[V]{data: map[string]ttlEntry[V]{}, ttl: ttl, max: max, now: time.Now}
}

// Get 取未过期的值；已过期则顺手删除。
func (c *ttlCache[V]) Get(k string) (V, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	e, ok := c.data[k]
	if !ok {
		var zero V
		return zero, false
	}
	if c.now().After(e.expiresAt) {
		delete(c.data, k)
		var zero V
		return zero, false
	}
	return e.val, true
}

// Set 写入并刷新 TTL；写入前按需回收过期条目 / 超限条目。
func (c *ttlCache[V]) Set(k string, v V) {
	now := c.now()
	c.mu.Lock()
	defer c.mu.Unlock()
	if now.Sub(c.lastSweep) >= max(c.ttl, minSweepInterval) {
		c.dropExpiredLocked(now)
		c.lastSweep = now
	}
	if _, exists := c.data[k]; !exists && len(c.data) >= c.max {
		c.evictLocked(now)
	}
	c.data[k] = ttlEntry[V]{val: v, expiresAt: now.Add(c.ttl)}
}

// Delete 删除条目。
func (c *ttlCache[V]) Delete(k string) {
	c.mu.Lock()
	delete(c.data, k)
	c.mu.Unlock()
}

// dropExpiredLocked 删除全部过期条目（须持 c.mu）。
func (c *ttlCache[V]) dropExpiredLocked(now time.Time) {
	for k, e := range c.data {
		if now.After(e.expiresAt) {
			delete(c.data, k)
		}
	}
}

// evictLocked 达上限时回收：先删过期；仍超则借 map 随机遍历任意删到 3/4 容量（须持 c.mu）。
func (c *ttlCache[V]) evictLocked(now time.Time) {
	c.dropExpiredLocked(now)
	target := c.max * 3 / 4
	for k := range c.data {
		if len(c.data) <= target {
			break
		}
		delete(c.data, k)
	}
}
