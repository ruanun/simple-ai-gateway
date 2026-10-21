package model_test

import (
	"testing"

	"github.com/ruanun/simple-ai-gateway/internal/dbtest"
	"github.com/ruanun/simple-ai-gateway/internal/model"
	"github.com/stretchr/testify/require"
)

// 新增的 auth_style / custom_headers 两列能建表并往返持久化。
func TestChannelOutboundHeaderColumns(t *testing.T) {
	g := dbtest.OpenInMemory(t)
	require.NoError(t, g.AutoMigrate(&model.Channel{}))

	ch := model.Channel{
		Name:          "c",
		Type:          "anthropic",
		AuthStyle:     "bearer",
		CustomHeaders: `{"HTTP-Referer":"https://x.test"}`,
	}
	require.NoError(t, g.Create(&ch).Error)

	var got model.Channel
	require.NoError(t, g.First(&got, ch.ID).Error)
	require.Equal(t, "bearer", got.AuthStyle)
	require.JSONEq(t, `{"HTTP-Referer":"https://x.test"}`, got.CustomHeaders)
}
