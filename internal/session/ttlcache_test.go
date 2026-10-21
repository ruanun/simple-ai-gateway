package session

import (
	"strconv"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// fakeClock 可手动推进的时钟，驱动 TTL 过期与定期清扫。
type fakeClock struct{ t time.Time }

func (f *fakeClock) now() time.Time { return f.t }

// 写入即过期、此后永不再读的会话（sessionID 由客户端请求内容决定，可每请求造新值）
// 必须随后续写入被回收，否则内存模式下 map 无界增长。
func TestTTLCacheSweepsExpiredOnSet(t *testing.T) {
	clk := &fakeClock{t: time.Unix(1_000_000, 0)}
	c := newTTLCache[uint64](time.Minute, 1000)
	c.now = clk.now
	for i := 0; i < 100; i++ {
		c.Set("s"+strconv.Itoa(i), 1)
	}
	require.Equal(t, 100, len(c.data))
	clk.t = clk.t.Add(2 * time.Minute) // 全部过期，且已超过一个清扫间隔
	c.Set("fresh", 2)
	require.Equal(t, 1, len(c.data))
	v, ok := c.Get("fresh")
	require.True(t, ok)
	require.Equal(t, uint64(2), v)
}

// 未过期条目也不能无限堆积：达容量上限即回收到 3/4。
func TestTTLCacheBoundedByMaxEntries(t *testing.T) {
	c := newTTLCache[uint64](time.Hour, 100)
	for i := 0; i < 1000; i++ {
		c.Set("s"+strconv.Itoa(i), uint64(i))
		require.LessOrEqual(t, len(c.data), 100)
	}
	// 最新写入的条目必定保留。
	v, ok := c.Get("s999")
	require.True(t, ok)
	require.Equal(t, uint64(999), v)
}

// 过期条目在 Get 时即删除（此前 MemoryChannelSticky 只判过期不删）。
func TestTTLCacheGetDeletesExpired(t *testing.T) {
	clk := &fakeClock{t: time.Unix(1_000_000, 0)}
	c := newTTLCache[uint64](time.Minute, 1000)
	c.now = clk.now
	c.Set("a", 1)
	clk.t = clk.t.Add(time.Minute + time.Second)
	_, ok := c.Get("a")
	require.False(t, ok)
	require.Equal(t, 0, len(c.data))
}

// 两个内存粘连实现都必须走有界缓存。
func TestMemoryStickyStoresAreBounded(t *testing.T) {
	s := NewMemoryStickyStore(time.Hour)
	s.c.max = 50
	ch := NewMemoryChannelSticky(time.Hour)
	ch.c.max = 50
	for i := 0; i < 500; i++ {
		id := "s" + strconv.Itoa(i)
		s.Set(id, 1, 1)
		ch.Set(id, 1)
	}
	require.LessOrEqual(t, len(s.c.data), 50)
	require.LessOrEqual(t, len(ch.c.data), 50)
}
