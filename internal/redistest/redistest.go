package redistest

import (
	"context"
	"os"
	"testing"

	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"
)

func Client(t *testing.T) *redis.Client {
	t.Helper()
	addr := os.Getenv("TEST_REDIS_ADDR")
	if addr == "" {
		t.Skip("TEST_REDIS_ADDR not set")
	}
	cli := redis.NewClient(&redis.Options{Addr: addr})
	require.NoError(t, cli.Ping(context.Background()).Err())
	t.Cleanup(func() { cli.FlushDB(context.Background()); cli.Close() })
	return cli
}
