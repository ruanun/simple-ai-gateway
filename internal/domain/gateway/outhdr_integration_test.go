package gateway

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/ruanun/simple-ai-gateway/internal/breaker"
	"github.com/ruanun/simple-ai-gateway/internal/ratelimit"
	"github.com/ruanun/simple-ai-gateway/internal/router"
	"github.com/stretchr/testify/require"
)

// Dispatch 应把客户端 anthropic-beta / UA 透传、渠道 custom_headers 合并、按 auth_style 分流后发给上游。
func TestDispatchForwardsGovernedHeaders(t *testing.T) {
	var gotBeta, gotUA, gotReferer, gotAuth, gotAPIKey string
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotBeta = r.Header.Get("Anthropic-Beta")
		gotUA = r.Header.Get("User-Agent")
		gotReferer = r.Header.Get("HTTP-Referer")
		gotAuth = r.Header.Get("Authorization")
		gotAPIKey = r.Header.Get("X-Api-Key")
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(200)
		_, _ = w.Write([]byte(`{"id":"x"}`))
	}))
	defer up.Close()

	clientHdr := http.Header{}
	clientHdr.Set("Anthropic-Beta", "context-1m-2025")
	clientHdr.Set("User-Agent", "claude-cli/9.9")

	d := Deps{
		Breaker: breaker.NewMemoryBreaker(breaker.Params{Window: time.Minute, Cooldown: time.Minute, FailThreshold: 3, ErrorRate: 0.5, MinSamples: 20, ProbeBackoffMax: 8}),
		Limiter: ratelimit.NewMemoryLimiter(),
		Now:     func() time.Time { return time.Unix(1_000_000, 0) },
	}
	d.ClientHeader = clientHdr
	d.Lookup = func(channelID uint64, modelName string) (ChannelInfo, bool) {
		return ChannelInfo{
			BaseURL: up.URL, UpstreamKey: "sk-real", Timeout: 5, ProviderType: "anthropic",
			AuthStyle:     "bearer",
			CustomHeaders: map[string]string{"HTTP-Referer": "https://app.test"},
		}, true
	}

	cands := []router.Candidate{{ChannelID: 1, Model: "claude-3", Outbound: "anthropic"}}
	rec := httptest.NewRecorder()
	out := Dispatch(rec, cands, []byte(`{"model":"claude-3","messages":[]}`), d)

	require.Equal(t, 200, out.Status)
	require.Equal(t, "context-1m-2025", gotBeta)     // 客户端 beta 透传
	require.Equal(t, "claude-cli/9.9", gotUA)        // 客户端 UA 透传
	require.Equal(t, "https://app.test", gotReferer) // 渠道自定义头合并
	require.Equal(t, "Bearer sk-real", gotAuth)      // auth_style=bearer → 只 Authorization
	require.Empty(t, gotAPIKey)                      // bearer 模式无 x-api-key
}

// Dispatch 在渠道未配置 auth_style（即默认 auto）时，非官方域名（如中转站/测试 mock）
// 应同时发 Authorization: Bearer 与 X-Api-Key（both 分支）——这是生产最常见的默认路径。
func TestDispatchAutoAuthSendsBoth(t *testing.T) {
	var gotAuth, gotAPIKey string
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		gotAPIKey = r.Header.Get("X-Api-Key")
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(200)
		_, _ = w.Write([]byte(`{"id":"x"}`))
	}))
	defer up.Close()

	d := Deps{
		Breaker: breaker.NewMemoryBreaker(breaker.Params{Window: time.Minute, Cooldown: time.Minute, FailThreshold: 3, ErrorRate: 0.5, MinSamples: 20, ProbeBackoffMax: 8}),
		Limiter: ratelimit.NewMemoryLimiter(),
		Now:     func() time.Time { return time.Unix(1_000_000, 0) },
	}
	d.Lookup = func(channelID uint64, modelName string) (ChannelInfo, bool) {
		return ChannelInfo{
			BaseURL: up.URL, UpstreamKey: "sk-real", Timeout: 5, ProviderType: "anthropic",
			// AuthStyle 留空 → auto；up.URL 天然非 api.anthropic.com → both 分支。
		}, true
	}

	cands := []router.Candidate{{ChannelID: 1, Model: "claude-3", Outbound: "anthropic"}}
	rec := httptest.NewRecorder()
	out := Dispatch(rec, cands, []byte(`{"model":"claude-3","messages":[]}`), d)

	require.Equal(t, 200, out.Status)
	require.Equal(t, "Bearer sk-real", gotAuth) // auto+非官方域名 → both：Authorization 也发
	require.Equal(t, "sk-real", gotAPIKey)      // auto+非官方域名 → both：X-Api-Key 也发
}
