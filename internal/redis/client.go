package redis

import (
	"context"
	"log/slog"
	"time"

	"github.com/redis/go-redis/v9"
	"github.com/ruanun/simple-ai-gateway/internal/config"
)

// NewClient 根据配置创建 Redis 客户端；若禁用或连接失败则返回 nil。
func NewClient(cfg config.RedisConfig) *redis.Client {
	if !cfg.Enabled {
		return nil
	}
	cli := redis.NewClient(&redis.Options{
		Addr:     cfg.Addr,
		Password: cfg.Password,
		DB:       cfg.DB,
	})
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err := cli.Ping(ctx).Err(); err != nil {
		slog.Warn("redis 连接失败，回退内存实现", "err", err)
		_ = cli.Close()
		return nil
	}
	return cli
}
