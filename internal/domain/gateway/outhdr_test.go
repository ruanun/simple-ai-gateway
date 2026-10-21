package gateway

import (
	"net/http"
	"testing"

	"github.com/stretchr/testify/require"
)

// specFor 应把渠道运行参数与调用方传入的 provType/clientHeader/stream 组装成 HeaderSpec。
func TestSpecFor(t *testing.T) {
	info := ChannelInfo{
		BaseURL:       "https://relay.test",
		UpstreamKey:   "sk-real",
		AuthStyle:     "bearer",
		CustomHeaders: map[string]string{"HTTP-Referer": "https://app.test"},
	}
	ch := http.Header{}
	ch.Set("User-Agent", "ua/1")

	spec := specFor("anthropic", info, ch, true)
	require.Equal(t, "anthropic", spec.Protocol) // provType 来自调用方，非 info.ProviderType
	require.Equal(t, "https://relay.test", spec.BaseURL)
	require.Equal(t, "sk-real", spec.UpstreamKey)
	require.Equal(t, "bearer", spec.AuthStyle)
	require.Equal(t, "https://app.test", spec.CustomHeaders["HTTP-Referer"])
	require.Equal(t, ch, spec.ClientHeader)
	require.True(t, spec.Stream)
}

// 鉴权分流：protocol + authStyle + baseURL → 期望的 Authorization / X-Api-Key。
func TestBuildOutboundHeadersAuth(t *testing.T) {
	cases := []struct {
		name       string
		spec       HeaderSpec
		wantAuth   string // Authorization 期望值（""=不应存在）
		wantAPIKey string // X-Api-Key 期望值（""=不应存在）
	}{
		{"openai 恒 Bearer", HeaderSpec{Protocol: "openai", UpstreamKey: "k"}, "Bearer k", ""},
		{"anthropic auto 官方域名发单 x-api-key",
			HeaderSpec{Protocol: "anthropic", AuthStyle: "auto", BaseURL: "https://api.anthropic.com", UpstreamKey: "k"}, "", "k"},
		{"anthropic auto 中转站双发",
			HeaderSpec{Protocol: "anthropic", AuthStyle: "auto", BaseURL: "https://relay.example.com", UpstreamKey: "k"}, "Bearer k", "k"},
		{"anthropic 空 AuthStyle 视为 auto",
			HeaderSpec{Protocol: "anthropic", AuthStyle: "", BaseURL: "https://relay.example.com", UpstreamKey: "k"}, "Bearer k", "k"},
		{"anthropic bearer 只发 Authorization",
			HeaderSpec{Protocol: "anthropic", AuthStyle: "bearer", BaseURL: "https://api.anthropic.com", UpstreamKey: "k"}, "Bearer k", ""},
		{"anthropic x-api-key 只发 x-api-key",
			HeaderSpec{Protocol: "anthropic", AuthStyle: "x-api-key", BaseURL: "https://relay.example.com", UpstreamKey: "k"}, "", "k"},
		{"anthropic both 双发",
			HeaderSpec{Protocol: "anthropic", AuthStyle: "both", BaseURL: "https://api.anthropic.com", UpstreamKey: "k"}, "Bearer k", "k"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := BuildOutboundHeaders(tc.spec)
			require.Equal(t, tc.wantAuth, h.Get("Authorization"))
			require.Equal(t, tc.wantAPIKey, h.Get("X-Api-Key"))
		})
	}
}

// 定值：Content-Type 与 Accept-Encoding: identity 恒在。
func TestBuildOutboundHeadersConstants(t *testing.T) {
	h := BuildOutboundHeaders(HeaderSpec{Protocol: "openai", UpstreamKey: "k"})
	require.Equal(t, "application/json", h.Get("Content-Type"))
	require.Equal(t, "identity", h.Get("Accept-Encoding"))
}

// 白名单回填：客户端有才透传，不伪造；anthropic-version 缺失兜底。
func TestBuildOutboundHeadersPassthrough(t *testing.T) {
	src := http.Header{}
	src.Set("User-Agent", "claude-cli/1.2")
	src.Set("Anthropic-Beta", "context-1m-2025")
	src.Set("X-Stainless-Lang", "js")
	src.Set("X-Forwarded-For", "1.2.3.4") // 不在白名单，不该透传

	h := BuildOutboundHeaders(HeaderSpec{
		Protocol: "anthropic", AuthStyle: "auto", BaseURL: "https://api.anthropic.com",
		UpstreamKey: "k", ClientHeader: src,
	})
	require.Equal(t, "claude-cli/1.2", h.Get("User-Agent"))      // 透传
	require.Equal(t, "context-1m-2025", h.Get("Anthropic-Beta")) // 透传
	require.Equal(t, "js", h.Get("X-Stainless-Lang"))            // 前缀透传
	require.Equal(t, "2023-06-01", h.Get("Anthropic-Version"))   // 缺失兜底
	require.Empty(t, h.Get("X-Forwarded-For"))                   // 不透传
}

// 客户端没带的指纹头，出站也不该出现（验证不伪造）。
func TestBuildOutboundHeadersNoFabrication(t *testing.T) {
	h := BuildOutboundHeaders(HeaderSpec{
		Protocol: "anthropic", AuthStyle: "auto", BaseURL: "https://api.anthropic.com", UpstreamKey: "k",
	})
	require.Empty(t, h.Get("User-Agent"))
	require.Empty(t, h.Get("X-Stainless-Lang"))
	require.Empty(t, h.Get("Anthropic-Beta"))
}

// 多行 Anthropic-Beta：客户端可能发多行同名头（HTTP 允许），须合并成逗号分隔单行透传，
// 不能只取首行（否则第二行起的 beta 标志被静默丢弃，如 context-1m + 缓存 beta 并存场景）。
func TestBuildOutboundHeadersAnthropicBetaMultiLine(t *testing.T) {
	src := http.Header{}
	src.Add("Anthropic-Beta", "beta-1")
	src.Add("Anthropic-Beta", "beta-2")

	h := BuildOutboundHeaders(HeaderSpec{
		Protocol: "anthropic", AuthStyle: "auto", BaseURL: "https://api.anthropic.com",
		UpstreamKey: "k", ClientHeader: src,
	})
	require.Equal(t, "beta-1,beta-2", h.Get("Anthropic-Beta"))
}

// 单行 Anthropic-Beta（值本身含逗号）：Values 只返回一个元素，join 后应保持原样，不应被误拆分或重复。
func TestBuildOutboundHeadersAnthropicBetaSingleLineWithComma(t *testing.T) {
	src := http.Header{}
	src.Add("Anthropic-Beta", "beta-1,beta-2")

	h := BuildOutboundHeaders(HeaderSpec{
		Protocol: "anthropic", AuthStyle: "auto", BaseURL: "https://api.anthropic.com",
		UpstreamKey: "k", ClientHeader: src,
	})
	require.Equal(t, "beta-1,beta-2", h.Get("Anthropic-Beta"))
}

// 自定义头护栏：受保护名被剥离、CRLF 值被跳过、正常头透传。
func TestBuildOutboundHeadersCustomGuard(t *testing.T) {
	h := BuildOutboundHeaders(HeaderSpec{
		Protocol: "openai", BaseURL: "https://x.test", UpstreamKey: "realkey",
		CustomHeaders: map[string]string{
			"HTTP-Referer":    "https://app.test", // 正常 → 保留
			"Authorization":   "Bearer HACKED",    // 受保护 → 剥离
			"X-Api-Key":       "HACKED",           // 受保护 → 剥离
			"Host":            "evil.test",        // 受保护 → 剥离
			"X-Bad":           "a\r\nInjected: 1", // CRLF → 跳过
			"Accept-Encoding": "gzip",             // 受保护（定值）→ 不可覆盖 identity
			"Content-Type":    "text/plain",       // 受保护（定值）→ 不可覆盖 application/json
		},
	})
	require.Equal(t, "https://app.test", h.Get("HTTP-Referer")) // 正常头保留
	require.Equal(t, "Bearer realkey", h.Get("Authorization"))  // 鉴权仍是渠道 key，未被篡改
	require.Empty(t, h.Get("X-Api-Key"))                        // openai 不发 x-api-key，且自定义被剥离
	require.Empty(t, h.Get("Host"))                             // 受保护，未写入 header map
	require.Empty(t, h.Get("X-Bad"))                            // CRLF 值被跳过
	require.Equal(t, "identity", h.Get("Accept-Encoding"))      // 定值不被自定义头覆盖
	require.Equal(t, "application/json", h.Get("Content-Type")) // 定值不被自定义头覆盖
}

// TestSetAnthropicOfficialHosts 配置追加域名后 auto 分流按 x-api-key（并恢复默认防污染其他用例）。
func TestSetAnthropicOfficialHosts(t *testing.T) {
	SetAnthropicOfficialHosts([]string{"api.anthropic.com", "gw.example.com"})
	t.Cleanup(func() { SetAnthropicOfficialHosts([]string{"api.anthropic.com"}) })
	require.True(t, isAnthropicOfficialHost("https://gw.example.com/v1"))
	require.False(t, isAnthropicOfficialHost("https://other.example.com/v1"))
}
