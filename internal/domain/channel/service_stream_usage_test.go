package channel

import (
	"testing"

	"github.com/ruanun/simple-ai-gateway/internal/dbtest"
	"github.com/ruanun/simple-ai-gateway/internal/model"
	"github.com/stretchr/testify/require"
)

func TestServiceCarriesDisableStreamUsage(t *testing.T) {
	g := dbtest.OpenInMemory(t)
	require.NoError(t, g.AutoMigrate(&model.Channel{}, &model.Account{}))
	s := NewService(NewRepo(g), "0123456789abcdef0123456789abcdef") // 32B AES key

	// Create 带 DisableStreamUsage=true → 持久化为 true
	ch, err := s.Create(CreateInput{Type: "openai", Name: "c", APIKey: "sk-x", DisableStreamUsage: true})
	require.NoError(t, err)
	var got model.Channel
	require.NoError(t, g.First(&got, ch.ID).Error)
	require.True(t, got.DisableStreamUsage)

	// Update 关掉 → false
	f := false
	changed, err := s.Update(ch.ID, UpdateInput{DisableStreamUsage: &f})
	require.NoError(t, err)
	require.True(t, changed)
	require.NoError(t, g.First(&got, ch.ID).Error)
	require.False(t, got.DisableStreamUsage)
}
