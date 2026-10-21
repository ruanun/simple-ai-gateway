package breaker

import (
	"context"
	"strconv"
	"strings"
	"time"

	"github.com/redis/go-redis/v9"
)

// RedisKeyed 基于 Redis 的 Keyed 实现：多实例共享账号×模型冷却状态。
// open 键存冷却截止时间戳；level 键存退避等级(TTL 2×max，保证到期探测再失败能继续升级)。
// 语义与 MemoryKeyed 一致：冷却窗口内重复失败不升级(GET 预检，跨实例竞态下可能偶发双升级，
// 有 backoff 封顶兜底，属可接受的 best-effort，与 RedisBreaker 的非原子风格一致)。
type RedisKeyed struct {
	cli  *redis.Client
	base time.Duration
	max  time.Duration
}

// NewRedisKeyed 构造 Redis 字符串键熔断器。
func NewRedisKeyed(cli *redis.Client, base, max time.Duration) *RedisKeyed {
	return &RedisKeyed{cli: cli, base: base, max: max}
}

func kOpenKey(key string) string  { return "breaker:k:" + key + ":open" }
func kLevelKey(key string) string { return "breaker:k:" + key + ":level" }

// Allow 键当前是否可用；Redis 不可用或无记录时放行(fail-open，与 RedisBreaker 一致)。
func (b *RedisKeyed) Allow(key string, now time.Time) bool {
	val, err := b.cli.Get(context.Background(), kOpenKey(key)).Result()
	if err != nil {
		return true
	}
	until, err := strconv.ParseInt(val, 10, 64)
	if err != nil {
		return true
	}
	return now.Unix() >= until
}

// RecordQuotaFailure 记一次配额失败：冷却中直接吸收(no-op)；否则按当前等级退避冷却并升级；
// retryAfter 更大时以其为准（尊重上游 Retry-After）。
func (b *RedisKeyed) RecordQuotaFailure(key string, now time.Time, retryAfter time.Duration) {
	ctx := context.Background()
	// 冷却中不升级不延长（吸收并发 429 风暴，与 MemoryKeyed 对齐）。
	if val, err := b.cli.Get(ctx, kOpenKey(key)).Result(); err == nil {
		if until, perr := strconv.ParseInt(val, 10, 64); perr == nil && now.Unix() < until {
			return
		}
	}
	level, _ := b.cli.Get(ctx, kLevelKey(key)).Int()
	cd := backoff(b.base, b.max, level)
	if retryAfter > b.max { // 封顶：拒绝上游给出的离谱 Retry-After，避免绕过 max 冷却整个键
		retryAfter = b.max
	}
	if retryAfter > cd {
		cd = retryAfter
	}
	until := now.Add(cd).Unix()
	b.cli.Set(ctx, kOpenKey(key), strconv.FormatInt(until, 10), cd+time.Minute)
	levelTTL := 2 * b.max
	if b.max <= 0 { // 防御：误配 max<=0 时兜底一个合理 TTL，避免 level 键立即/永不过期
		levelTTL = time.Hour
	}
	b.cli.Set(ctx, kLevelKey(key), strconv.Itoa(level+1), levelTTL)
}

// RecordSuccess 清除冷却与退避等级。
func (b *RedisKeyed) RecordSuccess(key string) {
	b.cli.Del(context.Background(), kOpenKey(key), kLevelKey(key))
}

// SnapshotPrefix SCAN prefix 下的 open 键并取冷却截止；Redis 不可用时返回空（fail-open，与 Allow 一致）。
// 管理端低频操作，SCAN 开销可接受。
func (b *RedisKeyed) SnapshotPrefix(prefix string, now time.Time) map[string]int64 {
	ctx := context.Background()
	out := map[string]int64{}
	iter := b.cli.Scan(ctx, 0, "breaker:k:"+prefix+"*:open", 100).Iterator()
	for iter.Next(ctx) {
		full := iter.Val() // breaker:k:<key>:open
		key := strings.TrimSuffix(strings.TrimPrefix(full, "breaker:k:"), ":open")
		val, err := b.cli.Get(ctx, full).Result()
		if err != nil {
			continue
		}
		until, perr := strconv.ParseInt(val, 10, 64)
		if perr != nil || now.Unix() >= until {
			continue
		}
		out[key] = until
	}
	return out
}

// ResetPrefix SCAN 并删除 prefix 下全部 open/level 键。
func (b *RedisKeyed) ResetPrefix(prefix string) {
	ctx := context.Background()
	iter := b.cli.Scan(ctx, 0, "breaker:k:"+prefix+"*", 100).Iterator()
	var keys []string
	for iter.Next(ctx) {
		keys = append(keys, iter.Val())
	}
	if len(keys) > 0 {
		b.cli.Del(ctx, keys...)
	}
}
