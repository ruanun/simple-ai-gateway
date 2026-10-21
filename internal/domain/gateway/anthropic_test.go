package gateway

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestAnthropicAdapterBuildRequest(t *testing.T) {
	a := AnthropicAdapter{}
	body := []byte(`{"model":"claude-3","messages":[]}`)
	spec := HeaderSpec{Protocol: "anthropic", AuthStyle: "auto", BaseURL: "https://api.anthropic.com", UpstreamKey: "sk-ant-xxx"}
	req, err := a.BuildRequest(spec, body)
	require.NoError(t, err)
	require.Equal(t, "https://api.anthropic.com/v1/messages", req.URL.String())
	require.Equal(t, "sk-ant-xxx", req.Header.Get("x-api-key")) // 官方域名 auto → 单 x-api-key
	require.Equal(t, "2023-06-01", req.Header.Get("anthropic-version"))
	require.Equal(t, "application/json", req.Header.Get("Content-Type"))
	require.Empty(t, req.Header.Get("Authorization"))
	b, _ := io.ReadAll(req.Body)
	require.JSONEq(t, `{"model":"claude-3","messages":[]}`, string(b))
	require.Equal(t, "anthropic", a.Protocol())
}

func TestRelayAnthropicStream(t *testing.T) {
	// 模拟 Anthropic 流式 SSE 响应
	sseBody := "event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"id\":\"msg_1\",\"usage\":{\"input_tokens\":20}}}\n\n" +
		"event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"delta\":{\"type\":\"text_delta\",\"text\":\"hi\"}}\n\n" +
		"event: message_delta\ndata: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"end_turn\"},\"usage\":{\"output_tokens\":8}}\n\n" +
		"event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n"
	resp := &http.Response{
		StatusCode: 200,
		Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
		Body:       io.NopCloser(strings.NewReader(sseBody)),
	}
	w := httptest.NewRecorder()
	res, u, uok, captured := RelayAnthropic(w, resp, true, true)
	require.Equal(t, 200, res.Status)
	require.True(t, res.Stream)
	require.True(t, uok)
	require.Equal(t, 20, u.PromptTokens)
	require.Equal(t, 8, u.CompletionTokens)
	require.Equal(t, 28, u.TotalTokens)
	require.Contains(t, string(captured), "content_block_delta")
	require.Contains(t, w.Body.String(), "content_block_delta") // 原样透传
}

// TestRelayAnthropicStreamCacheTokens message_start 的缓存字段进入归一化 usage（P12d）。
func TestRelayAnthropicStreamCacheTokens(t *testing.T) {
	sseBody := "event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"id\":\"msg_1\",\"usage\":{\"input_tokens\":100,\"cache_creation_input_tokens\":200,\"cache_read_input_tokens\":700}}}\n\n" +
		"event: message_delta\ndata: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"end_turn\"},\"usage\":{\"output_tokens\":50}}\n\n" +
		"event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n"
	resp := &http.Response{
		StatusCode: 200,
		Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
		Body:       io.NopCloser(strings.NewReader(sseBody)),
	}
	w := httptest.NewRecorder()
	_, u, uok, _ := RelayAnthropic(w, resp, true, true)
	require.True(t, uok)
	require.Equal(t, 1000, u.PromptTokens, "总输入=input+cache_read+cache_creation")
	require.Equal(t, 700, u.CachedTokens)
	require.Equal(t, 200, u.CacheCreationTokens)
	require.Equal(t, 50, u.CompletionTokens)
	require.Equal(t, 1050, u.TotalTokens)
}
