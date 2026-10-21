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
	"github.com/ruanun/simple-ai-gateway/internal/session"
	"github.com/stretchr/testify/require"
)

// TestLookupFillsOutboundHeaderFields 验证 selector.Lookup 把渠道 auth_style 透传、
// custom_headers(JSON) 解析进 ChannelInfo（出站请求头治理数据流打通，非粘连路径 lookup）。
func TestLookupFillsOutboundHeaderFields(t *testing.T) {
	g := dbtest.OpenInMemory(t)
	require.NoError(t, g.AutoMigrate(&model.Channel{}, &model.Account{}))

	aes := "0123456789abcdef0123456789abcdef"
	ch := model.Channel{
		Name: "c", Type: "anthropic", Status: 1, Timeout: 30,
		AuthStyle:     "bearer",
		CustomHeaders: `{"HTTP-Referer":"https://app.test"}`,
	}
	require.NoError(t, g.Create(&ch).Error)

	enc, err := crypto.Encrypt(aes, "sk-1")
	require.NoError(t, err)
	acc := model.Account{ChannelID: ch.ID, Name: "a1", APIKeyEnc: enc, Weight: 1, Status: 1}
	require.NoError(t, g.Create(&acc).Error)

	accSvc := account.NewService(account.NewRepo(g))
	fixed := time.Unix(1_700_000_000, 0)
	sel := NewAccountSelector(channel.NewService(channel.NewRepo(g), aes), aes, accSvc,
		breaker.NewMemoryBreaker(breaker.Params{Window: time.Minute, Cooldown: time.Minute, FailThreshold: 3, ErrorRate: 0.5, MinSamples: 20, ProbeBackoffMax: 8}), nil,
		func() time.Time { return fixed }, nil, 1.0)

	info, ok := sel.Lookup(ch.ID, "claude-3")
	require.True(t, ok)
	require.Equal(t, "bearer", info.AuthStyle)
	require.Equal(t, "https://app.test", info.CustomHeaders["HTTP-Referer"])
}

// TestLookupWithSessionFillsOutboundHeaderFields 验证 LookupWithSession 的粘连路径
// （lookupForAccount 内部实现）同样把 auth_style/custom_headers 填进 ChannelInfo。
func TestLookupWithSessionFillsOutboundHeaderFields(t *testing.T) {
	g := dbtest.OpenInMemory(t)
	require.NoError(t, g.AutoMigrate(&model.Channel{}, &model.Account{}))

	aes := "0123456789abcdef0123456789abcdef"
	ch := model.Channel{
		Name: "c", Type: "anthropic", Status: 1, Timeout: 30,
		AuthStyle:     "x-api-key",
		CustomHeaders: `{"X-Extra":"v1"}`,
	}
	require.NoError(t, g.Create(&ch).Error)

	enc, err := crypto.Encrypt(aes, "sk-1")
	require.NoError(t, err)
	acc := model.Account{ChannelID: ch.ID, Name: "a1", APIKeyEnc: enc, Weight: 1, Status: 1}
	require.NoError(t, g.Create(&acc).Error)

	accSvc := account.NewService(account.NewRepo(g))
	fixed := time.Unix(1_700_000_000, 0)
	sticky := session.NewMemoryStickyStore(time.Hour)
	sel := NewAccountSelector(channel.NewService(channel.NewRepo(g), aes), aes, accSvc,
		breaker.NewMemoryBreaker(breaker.Params{Window: time.Minute, Cooldown: time.Minute, FailThreshold: 3, ErrorRate: 0.5, MinSamples: 20, ProbeBackoffMax: 8}), nil,
		func() time.Time { return fixed }, sticky, 1.0)

	// 首次调用建立粘连（走 lookup 正常路径写入 sticky map）。
	_, ok := sel.LookupWithSession("sess", ch.ID, "claude-3")
	require.True(t, ok)
	// 第二次调用命中粘连，走 lookupForAccount 分支。
	info, ok := sel.LookupWithSession("sess", ch.ID, "claude-3")
	require.True(t, ok)
	require.Equal(t, "x-api-key", info.AuthStyle)
	require.Equal(t, "v1", info.CustomHeaders["X-Extra"])
}
