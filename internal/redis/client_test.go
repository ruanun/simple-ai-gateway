package redis

import (
	"testing"

	"github.com/ruanun/simple-ai-gateway/internal/config"
	"github.com/stretchr/testify/require"
)

func TestNewClientDisabled(t *testing.T) {
	cli := NewClient(config.RedisConfig{Enabled: false})
	require.Nil(t, cli)
}

func TestNewClientBadAddr(t *testing.T) {
	cli := NewClient(config.RedisConfig{Enabled: true, Addr: "127.0.0.1:19999"})
	require.Nil(t, cli)
}
