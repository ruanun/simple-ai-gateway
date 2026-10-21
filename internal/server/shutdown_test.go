package server

import (
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/ruanun/simple-ai-gateway/internal/config"
	"github.com/ruanun/simple-ai-gateway/internal/db"
	"github.com/ruanun/simple-ai-gateway/internal/dbtest"
	"github.com/ruanun/simple-ai-gateway/internal/model"
	"github.com/stretchr/testify/require"
)

// TestNewWithShutdownStopsBackground 验证 NewWithShutdown 返回可用的关闭函数：
// 一次调用即停止 Cleaner/Aggregator 定时任务并 flush 审计队列，不 panic(E5)。
func TestNewWithShutdownStopsBackground(t *testing.T) {
	gin.SetMode(gin.TestMode)
	cfg := config.Config{}
	cfg.Storage.Local.Root = t.TempDir() // 审计正文本地存储落临时目录，避免测试残留
	cfg.Security.APIKeyPrefix = "sk-saig"
	cfg.Security.ChannelKeyAESKey = "0123456789abcdef0123456789abcdef"
	cfg.JWT.Secret = "shutdown-secret"
	cfg.JWT.ExpireHours = 1

	g := dbtest.OpenInMemory(t)
	require.NoError(t, db.Migrate(g, model.Migrations("admin", "sag12345")))

	r, shutdown, err := NewWithShutdown(cfg, g, nil)
	require.NoError(t, err)
	require.NotNil(t, r)
	require.NotNil(t, shutdown)
	// 停后台任务 + flush 审计：三处 Stop 各调一次，不应 double-close panic。
	require.NotPanics(t, shutdown)
}
