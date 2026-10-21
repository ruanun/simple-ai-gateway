package session

import (
	"context"
	"fmt"
	"strconv"
	"time"

	"github.com/redis/go-redis/v9"
)

// RedisStickyStore 基于 Redis 的会话粘连存储实现。
type RedisStickyStore struct {
	cli *redis.Client
	ttl time.Duration
}

// NewRedisStickyStore 创建 Redis 会话粘连存储。
func NewRedisStickyStore(cli *redis.Client, ttl time.Duration) *RedisStickyStore {
	return &RedisStickyStore{cli: cli, ttl: ttl}
}

func redisStickyKey(sessionID string, channelID uint64) string {
	return fmt.Sprintf("sticky:%s:%d", sessionID, channelID)
}

// Get 获取指定会话在指定渠道的粘连上游账号 ID。
func (s *RedisStickyStore) Get(sessionID string, channelID uint64) (uint64, bool) {
	ctx := context.Background()
	val, err := s.cli.Get(ctx, redisStickyKey(sessionID, channelID)).Result()
	if err != nil {
		return 0, false
	}
	id, err := strconv.ParseUint(val, 10, 64)
	if err != nil {
		return 0, false
	}
	return id, true
}

// Set 设置指定会话在指定渠道的粘连上游账号 ID，带 TTL 自动过期。
func (s *RedisStickyStore) Set(sessionID string, channelID uint64, accountID uint64) {
	ctx := context.Background()
	s.cli.Set(ctx, redisStickyKey(sessionID, channelID), strconv.FormatUint(accountID, 10), s.ttl)
}

// Delete 删除指定会话的粘连记录。
func (s *RedisStickyStore) Delete(sessionID string, channelID uint64) {
	ctx := context.Background()
	s.cli.Del(ctx, redisStickyKey(sessionID, channelID))
}
