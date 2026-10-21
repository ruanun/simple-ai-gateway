package ratelimit

import (
	"context"
	"fmt"
	"time"

	"github.com/redis/go-redis/v9"
)

// RedisLimiter 基于 Redis 的固定分钟窗口限流实现（每分钟一个计数桶，原子 INCR）。
type RedisLimiter struct {
	cli *redis.Client
}

// NewRedisLimiter 创建 Redis 限流器。
func NewRedisLimiter(cli *redis.Client) *RedisLimiter {
	return &RedisLimiter{cli: cli}
}

// Allow 判断当前请求是否允许通过；limit <= 0 表示不限流。
func (l *RedisLimiter) Allow(key string, limit int, now time.Time) bool {
	if limit <= 0 {
		return true
	}
	rkey := redisKey(key, now)
	ctx := context.Background()
	pipe := l.cli.Pipeline()
	incr := pipe.Incr(ctx, rkey)
	pipe.Expire(ctx, rkey, 2*time.Minute)
	_, _ = pipe.Exec(ctx)
	return incr.Val() <= int64(limit)
}

// Exceeded 见 Limiter.Exceeded；Redis 读失败按未超限处理（fail-open，与 Allow 一致）。
func (l *RedisLimiter) Exceeded(key string, limit int, now time.Time) bool {
	if limit <= 0 {
		return false
	}
	n, err := l.cli.Get(context.Background(), redisKey(key, now)).Int64()
	if err != nil {
		return false
	}
	return n >= int64(limit)
}

// releaseScript 计数 >0 才减一：键已过期/不存在时不能 DECR 出负数（负数会让本窗口多放行）。
var releaseScript = redis.NewScript(`
local v = tonumber(redis.call('GET', KEYS[1]) or '0')
if v > 0 then return redis.call('DECR', KEYS[1]) end
return 0`)

// Release 见 Limiter.Release；Redis 失败静默（最坏是本窗口少退一次，偏保守）。
func (l *RedisLimiter) Release(key string, now time.Time) {
	_ = releaseScript.Run(context.Background(), l.cli, []string{redisKey(key, now)}).Err()
}

// redisKey 固定分钟窗口计数键。
func redisKey(key string, now time.Time) string {
	return fmt.Sprintf("ratelimit:%s:%d", key, now.Unix()/60)
}
