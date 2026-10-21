package apikey

import (
	"testing"

	"github.com/ruanun/simple-ai-gateway/internal/model"
	"github.com/stretchr/testify/require"
)

func TestMapModel(t *testing.T) {
	k := model.APIKey{ModelMappings: `{"gpt-4":"gpt-4o"}`}
	require.Equal(t, "gpt-4o", MapModel(k, "gpt-4"))
	require.Equal(t, "claude", MapModel(k, "claude"))    // 未命中原样
	require.Equal(t, "x", MapModel(model.APIKey{}, "x")) // 空映射原样
}

func TestAllowsModel(t *testing.T) {
	require.True(t, AllowsModel(model.APIKey{}, "anything")) // 空白名单=不限
	k := model.APIKey{ModelWhitelist: "gpt-4o,claude-3-5-sonnet"}
	require.True(t, AllowsModel(k, "gpt-4o"))
	require.False(t, AllowsModel(k, "o1"))
}
