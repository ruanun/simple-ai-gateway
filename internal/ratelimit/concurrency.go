package ratelimit

import (
	"context"
	"crypto/rand"
	"sync"
	"time"

	"github.com/redis/go-redis/v9"
)

// Concurrency 在途并发上限：Acquire 占一个槽位，请求结束调用 release 归还。
// 用于 App 级并发封顶——配额是「先查后扣」的软限额，并发越高、临界点超额越大，
// 限制同时在途的请求数即把超额封在「并发上限 × 单次成本」以内。
type Concurrency interface {
	// Acquire limit<=0 恒放行；ok=false 表示已达上限。release 幂等，可安全多次调用。
	Acquire(key string, limit int) (release func(), ok bool)
}

func noop() {}

// MemoryConcurrency 进程内实现（单实例部署，不跨实例共享）。
type MemoryConcurrency struct {
	mu sync.Mutex
	n  map[string]int
}

// NewMemoryConcurrency 构造内存并发计数器。
func NewMemoryConcurrency() *MemoryConcurrency {
	return &MemoryConcurrency{n: map[string]int{}}
}

// Acquire 见 Concurrency.Acquire。
func (m *MemoryConcurrency) Acquire(key string, limit int) (func(), bool) {
	if limit <= 0 {
		return noop, true
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.n[key] >= limit {
		return nil, false
	}
	m.n[key]++
	return sync.OnceFunc(func() { // 幂等：重复调用不会多还槽位
		m.mu.Lock()
		defer m.mu.Unlock()
		if m.n[key] <= 1 {
			delete(m.n, key) // 归零即删，避免按 App 建的键只增不减
		} else {
			m.n[key]--
		}
	}), true
}

// releaseTimeout Redis 归还槽位的超时：归还在请求收尾路径上同步执行，Redis 卡顿时不能无限拖住请求；
// 超时未归还的槽位由租约兜底失效。
const releaseTimeout = 2 * time.Second

// concurrencyLease Redis 槽位租约：进程崩溃/网络中断导致未归还的槽位最多占用这么久后自动失效，
// 不会永久吃掉 App 的并发额度。取值覆盖绝大多数长流式请求；超长请求在租约过期后不再占位，
// 只会让该 App 临界点略微多放行，属可接受的退化。
const concurrencyLease = 15 * time.Minute

// acquireScript 原子地：清掉过期租约 → 判断在途数 → 未满则登记本次租约。
// KEYS[1]=有序集合键；ARGV: 1=当前毫秒, 2=租约到期毫秒, 3=上限, 4=成员 ID, 5=键 TTL 毫秒。
var acquireScript = redis.NewScript(`
redis.call('ZREMRANGEBYSCORE', KEYS[1], '-inf', ARGV[1])
if redis.call('ZCARD', KEYS[1]) >= tonumber(ARGV[3]) then return 0 end
redis.call('ZADD', KEYS[1], ARGV[2], ARGV[4])
redis.call('PEXPIRE', KEYS[1], ARGV[5])
return 1`)

// RedisConcurrency 多实例共享实现：每个在途请求是有序集合里的一个带到期时间的成员。
type RedisConcurrency struct {
	cli *redis.Client
	now func() time.Time
}

// NewRedisConcurrency 构造 Redis 并发计数器。
func NewRedisConcurrency(cli *redis.Client) *RedisConcurrency {
	return &RedisConcurrency{cli: cli, now: time.Now}
}

// Acquire 见 Concurrency.Acquire。Redis 故障时放行（fail-open，与限流器一致，不因缓存故障拒绝全部业务）。
func (r *RedisConcurrency) Acquire(key string, limit int) (func(), bool) {
	if limit <= 0 {
		return noop, true
	}
	member := rand.Text()
	rkey := "concurrency:" + key
	now := r.now()
	ok, err := acquireScript.Run(context.Background(), r.cli, []string{rkey},
		now.UnixMilli(), now.Add(concurrencyLease).UnixMilli(), limit, member, concurrencyLease.Milliseconds()).Int()
	if err != nil {
		return noop, true
	}
	if ok != 1 {
		return nil, false
	}
	return sync.OnceFunc(func() {
		ctx, cancel := context.WithTimeout(context.Background(), releaseTimeout)
		defer cancel()
		_ = r.cli.ZRem(ctx, rkey, member).Err()
	}), true
}
