package redis

import (
	"context"
	"log/slog"

	goredis "github.com/redis/go-redis/v9"
)

const (
	// ChannelReloadPrice 价格表变更通知频道。
	ChannelReloadPrice = "saig:reload:price"
	// ChannelReloadRouter 路由表变更通知频道。
	ChannelReloadRouter = "saig:reload:router"
	// ChannelReloadAPIKey API Key 鉴权缓存失效通知频道。
	ChannelReloadAPIKey = "saig:reload:apikey"
)

// Publish 向指定频道发布一条 reload 通知；cli 为 nil 时静默跳过。
func Publish(cli *goredis.Client, channel string) error {
	if cli == nil {
		return nil
	}
	return cli.Publish(context.Background(), channel, "reload").Err()
}

// Subscribe 订阅指定频道，收到消息后在后台 goroutine 调用 onMessage；cli 为 nil 时静默跳过。
func Subscribe(cli *goredis.Client, channel string, onMessage func()) {
	if cli == nil {
		return
	}
	go func() {
		sub := cli.Subscribe(context.Background(), channel)
		ch := sub.Channel()
		for range ch {
			slog.Info("收到 reload 通知", "channel", channel)
			onMessage()
		}
	}()
}
