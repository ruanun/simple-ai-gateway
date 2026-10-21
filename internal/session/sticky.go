package session

// StickyStore 会话粘连存储接口：session_id+channel_id → account_id。
type StickyStore interface {
	Get(sessionID string, channelID uint64) (accountID uint64, ok bool)
	Set(sessionID string, channelID uint64, accountID uint64)
	Delete(sessionID string, channelID uint64)
}
