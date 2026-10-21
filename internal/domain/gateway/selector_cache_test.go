package gateway

import (
	"testing"
	"time"

	"github.com/ruanun/simple-ai-gateway/internal/breaker"
	"github.com/ruanun/simple-ai-gateway/internal/crypto"
	"github.com/ruanun/simple-ai-gateway/internal/dbtest"
	"github.com/ruanun/simple-ai-gateway/internal/domain/account"
	"github.com/ruanun/simple-ai-gateway/internal/domain/channel"
	"github.com/ruanun/simple-ai-gateway/internal/model"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

const cacheTestAESKey = "0123456789abcdef0123456789abcdef"

// mustChannel 造一个启用状态的测试渠道（与 selector_test.go/selector_headers_test.go 的内联造数惯例一致）。
func mustChannel(t *testing.T, g *gorm.DB) model.Channel {
	t.Helper()
	require.NoError(t, g.AutoMigrate(&model.Channel{}, &model.Account{}))
	ch := model.Channel{Name: "c", Type: "openai", BaseURL: "http://up", Status: 1, Timeout: 5}
	require.NoError(t, g.Create(&ch).Error)
	return ch
}

// mustAccount 造一个启用状态的测试账号，归属 channelID。
func mustAccount(t *testing.T, g *gorm.DB, channelID uint64) model.Account {
	t.Helper()
	enc, err := crypto.Encrypt(cacheTestAESKey, "sk-1")
	require.NoError(t, err)
	acc := model.Account{ChannelID: channelID, Name: "a1", APIKeyEnc: enc, Weight: 1, Status: 1}
	require.NoError(t, g.Create(&acc).Error)
	return acc
}

// TestSelectorCacheTTL 缓存生效期内不感知 DB 变更，过期后感知。
func TestSelectorCacheTTL(t *testing.T) {
	g := dbtest.OpenInMemory(t)
	ch := mustChannel(t, g)      // Status=1
	_ = mustAccount(t, g, ch.ID) // Status=1

	cur := time.Unix(1000, 0)
	now := func() time.Time { return cur }
	sel := NewAccountSelector(channel.NewService(channel.NewRepo(g), cacheTestAESKey), cacheTestAESKey,
		account.NewService(account.NewRepo(g)), breaker.NewMemoryBreaker(breaker.Params{}), nil, now, nil, 1)
	sel.SetCacheTTL(2 * time.Second)

	_, ok := sel.Lookup(ch.ID, "m1")
	require.True(t, ok)

	// 绕过缓存直接禁用渠道
	require.NoError(t, g.Model(&model.Channel{}).Where("id = ?", ch.ID).Update("status", 0).Error)

	_, ok = sel.Lookup(ch.ID, "m1")
	require.True(t, ok, "TTL 内应命中缓存，仍可用")

	cur = cur.Add(3 * time.Second)
	_, ok = sel.Lookup(ch.ID, "m1")
	require.False(t, ok, "TTL 过期后应感知禁用")
}

// TestSelectorCacheDisabledByDefault 未 SetCacheTTL 时行为与改造前逐位一致（立即感知变更）。
func TestSelectorCacheDisabledByDefault(t *testing.T) {
	g := dbtest.OpenInMemory(t)
	ch := mustChannel(t, g)
	_ = mustAccount(t, g, ch.ID)
	sel := NewAccountSelector(channel.NewService(channel.NewRepo(g), cacheTestAESKey), cacheTestAESKey,
		account.NewService(account.NewRepo(g)), breaker.NewMemoryBreaker(breaker.Params{}), nil, nil, nil, 1)
	_, ok := sel.Lookup(ch.ID, "m1")
	require.True(t, ok)
	require.NoError(t, g.Model(&model.Channel{}).Where("id = ?", ch.ID).Update("status", 0).Error)
	_, ok = sel.Lookup(ch.ID, "m1")
	require.False(t, ok)
}
