package session

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestStickyMapGetSetDelete(t *testing.T) {
	m := NewMemoryStickyStore(5 * time.Minute)
	// 未设置 → 不命中
	_, ok := m.Get("s1", 1)
	require.False(t, ok)
	// 设置后命中
	m.Set("s1", 1, 42)
	acc, ok := m.Get("s1", 1)
	require.True(t, ok)
	require.Equal(t, uint64(42), acc)
	// 不同 session 不命中
	_, ok = m.Get("s2", 1)
	require.False(t, ok)
	// 不同 channel 不命中
	_, ok = m.Get("s1", 2)
	require.False(t, ok)
	// 删除后不命中
	m.Delete("s1", 1)
	_, ok = m.Get("s1", 1)
	require.False(t, ok)
}

func TestStickyMapExpiry(t *testing.T) {
	m := NewMemoryStickyStore(50 * time.Millisecond)
	m.Set("s1", 1, 42)
	acc, ok := m.Get("s1", 1)
	require.True(t, ok)
	require.Equal(t, uint64(42), acc)
	time.Sleep(60 * time.Millisecond)
	_, ok = m.Get("s1", 1)
	require.False(t, ok) // 已过期
}
