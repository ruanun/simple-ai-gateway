package breaker

import (
	"context"
	"strconv"
	"sync/atomic"
	"time"

	"github.com/redis/go-redis/v9"
)

// seq 进程内序号，拼进 ZADD member 保证唯一（同一时间戳的多次成功/失败不被去重）。
var seq atomic.Uint64

// RedisBreaker 基于 Redis 的状态机熔断器：语义与 MemoryBreaker 一致(闭合/开/半开)，
// 用 Hash 存状态、两个 ZSet 存滑窗成功/失败时间戳。best-effort(非原子)，与旧实现取舍一致。
// prefix 区分渠道/账号/代理三级命名空间，避免同 id 跨级串扰。
type RedisBreaker struct {
	cli    *redis.Client
	prefix string
	p      Params
}

// NewRedisBreaker 创建 Redis 熔断器。prefix 用于隔离三级命名空间（如 "ch"/"acc"/"proxy"）。
func NewRedisBreaker(cli *redis.Client, prefix string, p Params) *RedisBreaker {
	return &RedisBreaker{cli: cli, prefix: prefix, p: p}
}

func (b *RedisBreaker) stKey(id uint64) string {
	return "breaker2:" + b.prefix + ":" + strconv.FormatUint(id, 10)
}
func (b *RedisBreaker) succKey(id uint64) string { return b.stKey(id) + ":succ" }
func (b *RedisBreaker) failKey(id uint64) string { return b.stKey(id) + ":fail" }

// Allow 闭合或半开(冷却到期)→ 可选；open 冷却到期时转半开。
func (b *RedisBreaker) Allow(id uint64, now time.Time) bool {
	if b.p.Disabled() {
		return true
	}
	ctx := context.Background()
	m := b.cli.HGetAll(ctx, b.stKey(id)).Val()
	status := m["status"]
	if status == "" || status == "closed" {
		return true
	}
	until, _ := strconv.ParseInt(m["openUntil"], 10, 64)
	if now.Unix() >= until {
		b.cli.HSet(ctx, b.stKey(id), "status", "half") // 冷却到期 → 半开
		return true
	}
	return false
}

// Recovering 处于半开(或 open 已到期即将转半开)→ 供选择器降权。
func (b *RedisBreaker) Recovering(id uint64, now time.Time) bool {
	if b.p.Disabled() {
		return false
	}
	ctx := context.Background()
	m := b.cli.HGetAll(ctx, b.stKey(id)).Val()
	if m["status"] == "half" {
		return true
	}
	if m["status"] == "open" {
		until, _ := strconv.ParseInt(m["openUntil"], 10, 64)
		return now.Unix() >= until
	}
	return false
}

// RecordResult 记结果：半开态视为探测结果(成功闭/失败退避重开)；闭合态按 is429/错误率/计数判开。
func (b *RedisBreaker) RecordResult(id uint64, now time.Time, ok, is429 bool) {
	if b.p.Disabled() {
		return
	}
	ctx := context.Background()
	sk := b.stKey(id)
	m := b.cli.HGetAll(ctx, sk).Val()
	status := m["status"]
	if status == "" {
		status = "closed"
	}
	switch status {
	case "half":
		if ok {
			b.cli.Del(ctx, sk, b.succKey(id), b.failKey(id))
			return
		}
		lvl, _ := strconv.Atoi(m["probeLevel"])
		mult := int64(1) << minInt(lvl, b.p.ProbeBackoffMax)
		until := now.Add(b.p.Cooldown * time.Duration(mult)).Unix()
		b.cli.HSet(ctx, sk, "status", "open", "openUntil", until, "probeLevel", lvl+1)
		b.cli.Del(ctx, b.succKey(id), b.failKey(id))
		b.cli.Expire(ctx, sk, b.p.Cooldown*time.Duration(mult)+time.Minute)
		return
	case "open":
		return
	}
	// closed
	if is429 {
		b.openRedis(ctx, id, now, 0)
		return
	}
	z := b.succKey(id)
	if !ok {
		z = b.failKey(id)
	}
	nowU := now.Unix()
	member := strconv.FormatInt(now.UnixNano(), 10) + "-" + strconv.FormatUint(seq.Add(1), 10)
	cutoff := now.Add(-b.p.Window).Unix()
	pipe := b.cli.Pipeline()
	pipe.ZAdd(ctx, z, redis.Z{Score: float64(nowU), Member: member})
	pipe.ZRemRangeByScore(ctx, b.succKey(id), "-inf", strconv.FormatInt(cutoff-1, 10))
	pipe.ZRemRangeByScore(ctx, b.failKey(id), "-inf", strconv.FormatInt(cutoff-1, 10))
	pipe.Expire(ctx, b.succKey(id), b.p.Window+time.Minute)
	pipe.Expire(ctx, b.failKey(id), b.p.Window+time.Minute)
	fCmd := pipe.ZCard(ctx, b.failKey(id))
	sCmd := pipe.ZCard(ctx, b.succKey(id))
	_, _ = pipe.Exec(ctx)
	if ok {
		return
	}
	f := int(fCmd.Val())
	total := f + int(sCmd.Val())
	shouldOpen := (b.p.FailThreshold > 0 && f >= b.p.FailThreshold) ||
		(b.p.ErrorRate > 0 && total >= b.p.MinSamples && float64(f)/float64(total) >= b.p.ErrorRate)
	if shouldOpen {
		b.openRedis(ctx, id, now, 0)
	}
}

func (b *RedisBreaker) openRedis(ctx context.Context, id uint64, now time.Time, probeLevel int) {
	until := now.Add(b.p.Cooldown).Unix()
	b.cli.HSet(ctx, b.stKey(id), "status", "open", "openUntil", until, "probeLevel", probeLevel)
	b.cli.Expire(ctx, b.stKey(id), b.p.Cooldown+time.Minute)
	b.cli.Del(ctx, b.succKey(id), b.failKey(id))
}

// Snapshot 批量读取入参 id 的熔断状态；闭合/无记录项省略。
func (b *RedisBreaker) Snapshot(ids []uint64, now time.Time) map[uint64]Status {
	out := map[uint64]Status{}
	if b.p.Disabled() || len(ids) == 0 {
		return out
	}
	ctx := context.Background()
	pipe := b.cli.Pipeline()
	cmds := make([]*redis.MapStringStringCmd, len(ids))
	for i, id := range ids {
		cmds[i] = pipe.HGetAll(ctx, b.stKey(id))
	}
	_, _ = pipe.Exec(ctx)
	for i, id := range ids {
		m := cmds[i].Val()
		status := m["status"]
		if status == "" || status == "closed" {
			continue
		}
		until, _ := strconv.ParseInt(m["openUntil"], 10, 64)
		lvl, _ := strconv.Atoi(m["probeLevel"])
		st := Status{OpenUntil: until, ProbeLevel: lvl}
		if status == "half" || (status == "open" && now.Unix() >= until) {
			st.State = "half"
		} else {
			st.State = "open"
		}
		out[id] = st
	}
	return out
}

// Reset 删除入参 id 的熔断状态键，恢复为闭合。
func (b *RedisBreaker) Reset(ids ...uint64) {
	if b.p.Disabled() || len(ids) == 0 {
		return
	}
	ctx := context.Background()
	keys := make([]string, 0, len(ids)*3)
	for _, id := range ids {
		keys = append(keys, b.stKey(id), b.succKey(id), b.failKey(id))
	}
	b.cli.Del(ctx, keys...)
}
