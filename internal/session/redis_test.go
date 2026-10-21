package session

import (
	"testing"
	"time"

	"github.com/ruanun/simple-ai-gateway/internal/redistest"
	"github.com/stretchr/testify/require"
)

func TestRedisStickyStoreGetSetDelete(t *testing.T) {
	cli := redistest.Client(t)
	s := NewRedisStickyStore(cli, 5*time.Minute)
	_, ok := s.Get("s1", 1)
	require.False(t, ok)
	s.Set("s1", 1, 42)
	acc, ok := s.Get("s1", 1)
	require.True(t, ok)
	require.Equal(t, uint64(42), acc)
	s.Delete("s1", 1)
	_, ok = s.Get("s1", 1)
	require.False(t, ok)
}

func TestRedisStickyStoreTTL(t *testing.T) {
	cli := redistest.Client(t)
	s := NewRedisStickyStore(cli, 100*time.Millisecond)
	s.Set("s1", 1, 42)
	acc, ok := s.Get("s1", 1)
	require.True(t, ok)
	require.Equal(t, uint64(42), acc)
	time.Sleep(150 * time.Millisecond)
	_, ok = s.Get("s1", 1)
	require.False(t, ok)
}
