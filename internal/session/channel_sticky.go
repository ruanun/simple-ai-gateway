package session

import (
	"context"
	"fmt"
	"strconv"
	"time"

	"github.com/redis/go-redis/v9"
)

// ChannelStickyStore 渠道级会话粘连：sessionID → channelID，带 TTL。
type ChannelStickyStore interface {
	Get(sessionID string) (channelID uint64, ok bool)
	Set(sessionID string, channelID uint64)
	Delete(sessionID string)
}

// MemoryChannelSticky 进程内渠道粘连映射，带 TTL 与容量上限。
type MemoryChannelSticky struct {
	c *ttlCache[uint64]
}

// NewMemoryChannelSticky 构造进程内渠道粘连映射。
func NewMemoryChannelSticky(ttl time.Duration) *MemoryChannelSticky {
	return &MemoryChannelSticky{c: newTTLCache[uint64](ttl, defaultMaxEntries)}
}

// Get 查粘连渠道；过期返回 false（并删除）。
func (m *MemoryChannelSticky) Get(sessionID string) (uint64, bool) {
	return m.c.Get(sessionID)
}

// Set 写入粘连映射（刷新 TTL）。
func (m *MemoryChannelSticky) Set(sessionID string, channelID uint64) {
	m.c.Set(sessionID, channelID)
}

// Delete 删除粘连映射。
func (m *MemoryChannelSticky) Delete(sessionID string) {
	m.c.Delete(sessionID)
}

// RedisChannelSticky 基于 Redis 的渠道粘连存储。
type RedisChannelSticky struct {
	cli *redis.Client
	ttl time.Duration
}

func NewRedisChannelSticky(cli *redis.Client, ttl time.Duration) *RedisChannelSticky {
	return &RedisChannelSticky{cli: cli, ttl: ttl}
}

func (s *RedisChannelSticky) Get(sessionID string) (uint64, bool) {
	val, err := s.cli.Get(context.Background(), fmt.Sprintf("ch_sticky:%s", sessionID)).Result()
	if err != nil {
		return 0, false
	}
	id, err := strconv.ParseUint(val, 10, 64)
	if err != nil {
		return 0, false
	}
	return id, true
}

func (s *RedisChannelSticky) Set(sessionID string, channelID uint64) {
	s.cli.Set(context.Background(), fmt.Sprintf("ch_sticky:%s", sessionID), strconv.FormatUint(channelID, 10), s.ttl)
}

func (s *RedisChannelSticky) Delete(sessionID string) {
	s.cli.Del(context.Background(), fmt.Sprintf("ch_sticky:%s", sessionID))
}
