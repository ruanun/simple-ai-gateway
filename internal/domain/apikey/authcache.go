package apikey

import (
	"sync"
	"time"

	"github.com/ruanun/simple-ai-gateway/internal/model"
)

// authCache 是 API Key 鉴权热路径的进程内缓存：key_hash -> 认证快照。
//
// 只缓存"无日成本上限"的 key：有日成本上限的 key 每次回表，保证 daily_cost_used 与
// daily_cost_date 是实时的（日限额是硬性财务约束，不能用 5s 陈旧值换性能）。
// 负缓存（不存在/停用/过期）同样缓存，避免无效 key 洪峰反复打 DB。
type authCache struct {
	mu    sync.Mutex
	ttl   time.Duration
	max   int
	items map[string]authCacheEntry
}

type authCacheEntry struct {
	rec      *model.APIKey
	notFound bool
	at       time.Time
}

func newAuthCache(ttl time.Duration, max int) *authCache {
	if ttl <= 0 {
		return nil
	}
	if max <= 0 {
		max = 10000
	}
	return &authCache{ttl: ttl, max: max, items: make(map[string]authCacheEntry, min(max, 1024))}
}

// get 返回 (快照, 是否命中, 是否负缓存)。命中时会做 TTL 检查与惰性删除。
func (c *authCache) get(hash string) (*model.APIKey, bool, bool) {
	if c == nil {
		return nil, false, false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	e, ok := c.items[hash]
	if !ok {
		return nil, false, false
	}
	if time.Since(e.at) > c.ttl {
		delete(c.items, hash)
		return nil, false, false
	}
	if e.notFound {
		return nil, true, true
	}
	cp := *e.rec
	return &cp, true, false
}

func (c *authCache) set(hash string, rec *model.APIKey) {
	if c == nil || rec == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.evictLocked()
	c.items[hash] = authCacheEntry{rec: rec, at: time.Now()}
}

func (c *authCache) setNotFound(hash string) {
	if c == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.evictLocked()
	c.items[hash] = authCacheEntry{notFound: true, at: time.Now()}
}

func (c *authCache) delete(hash string) {
	if c == nil {
		return
	}
	c.mu.Lock()
	delete(c.items, hash)
	c.mu.Unlock()
}

func (c *authCache) clear() {
	if c == nil {
		return
	}
	c.mu.Lock()
	c.items = make(map[string]authCacheEntry, min(c.max, 1024))
	c.mu.Unlock()
}

// evictLocked 先清过期项；仍达到上限时删一个任意项（鉴权缓存是纯优化，丢条目只会回表）。
func (c *authCache) evictLocked() {
	if len(c.items) < c.max {
		return
	}
	now := time.Now()
	for k, e := range c.items {
		if now.Sub(e.at) > c.ttl {
			delete(c.items, k)
		}
	}
	if len(c.items) < c.max {
		return
	}
	for k := range c.items {
		delete(c.items, k)
		return
	}
}
