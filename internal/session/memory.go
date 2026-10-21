package session

import (
	"strconv"
	"time"
)

// MemoryStickyStore 进程内会话粘连映射：session_id+channel_id → account_id，带 TTL 与容量上限。
type MemoryStickyStore struct {
	c *ttlCache[uint64]
}

// NewMemoryStickyStore 构造进程内会话粘连映射，ttl 为粘连超时时长。
func NewMemoryStickyStore(ttl time.Duration) *MemoryStickyStore {
	return &MemoryStickyStore{c: newTTLCache[uint64](ttl, defaultMaxEntries)}
}

func stickyKey(sessionID string, channelID uint64) string {
	return sessionID + ":" + strconv.FormatUint(channelID, 10)
}

// Get 查粘连账号；过期返回 false（并删除）。
func (m *MemoryStickyStore) Get(sessionID string, channelID uint64) (uint64, bool) {
	return m.c.Get(stickyKey(sessionID, channelID))
}

// Set 写入粘连映射（刷新 TTL）。
func (m *MemoryStickyStore) Set(sessionID string, channelID uint64, accountID uint64) {
	m.c.Set(stickyKey(sessionID, channelID), accountID)
}

// Delete 删除粘连映射（账号不健康时逃逸）。
func (m *MemoryStickyStore) Delete(sessionID string, channelID uint64) {
	m.c.Delete(stickyKey(sessionID, channelID))
}
