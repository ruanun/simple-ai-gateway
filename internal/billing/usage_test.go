package billing

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestParseUsageJSON(t *testing.T) {
	body := []byte(`{"id":"x","usage":{"prompt_tokens":100,"completion_tokens":50,"total_tokens":150,"prompt_tokens_details":{"cached_tokens":20},"completion_tokens_details":{"reasoning_tokens":10}}}`)
	u, ok := ParseUsageJSON(body)
	require.True(t, ok)
	require.Equal(t, 100, u.PromptTokens)
	require.Equal(t, 50, u.CompletionTokens)
	require.Equal(t, 20, u.CachedTokens)
	require.Equal(t, 10, u.ReasoningTokens)
}

func TestParseUsageJSONMissing(t *testing.T) {
	_, ok := ParseUsageJSON([]byte(`{"id":"x"}`))
	require.False(t, ok)
}

func TestParseAnthropicUsageJSON(t *testing.T) {
	body := []byte(`{"id":"msg_1","type":"message","usage":{"input_tokens":100,"output_tokens":50}}`)
	u, ok := ParseAnthropicUsageJSON(body)
	require.True(t, ok)
	require.Equal(t, 100, u.PromptTokens)
	require.Equal(t, 50, u.CompletionTokens)
	require.Equal(t, 150, u.TotalTokens)

	_, ok = ParseAnthropicUsageJSON([]byte(`{"id":"msg_1","type":"message"}`))
	require.False(t, ok)
}

// TestParseAnthropicUsageWithCache 缓存字段归一为总量口径：Prompt=总输入(含缓存)，Cached/CacheCreation 为子集（P12d）。
func TestParseAnthropicUsageWithCache(t *testing.T) {
	body := []byte(`{"usage":{"input_tokens":100,"output_tokens":50,"cache_creation_input_tokens":200,"cache_read_input_tokens":700}}`)
	u, ok := ParseAnthropicUsageJSON(body)
	require.True(t, ok)
	require.Equal(t, 1000, u.PromptTokens, "总输入=input+cache_read+cache_creation")
	require.Equal(t, 700, u.CachedTokens)
	require.Equal(t, 200, u.CacheCreationTokens)
	require.Equal(t, 50, u.CompletionTokens)
	require.Equal(t, 1050, u.TotalTokens)
}

// TestParseAnthropicUsageCacheOnly 纯缓存命中(input=output=0 但缓存非 0)不得误判为无 usage。
func TestParseAnthropicUsageCacheOnly(t *testing.T) {
	body := []byte(`{"usage":{"input_tokens":0,"output_tokens":0,"cache_read_input_tokens":500}}`)
	u, ok := ParseAnthropicUsageJSON(body)
	require.True(t, ok)
	require.Equal(t, 500, u.PromptTokens)
	require.Equal(t, 500, u.CachedTokens)
}

func TestParseResponsesUsageJSON(t *testing.T) {
	body := []byte(`{"usage":{"input_tokens":1000,"output_tokens":200,"input_tokens_details":{"cached_tokens":700},"output_tokens_details":{"reasoning_tokens":50}}}`)
	u, ok := ParseResponsesUsageJSON(body)
	require.True(t, ok)
	require.Equal(t, 1000, u.PromptTokens) // responses 的 input_tokens 已含 cached（子集语义）
	require.Equal(t, 200, u.CompletionTokens)
	require.Equal(t, 700, u.CachedTokens)
	require.Equal(t, 50, u.ReasoningTokens)
	require.Equal(t, 1200, u.TotalTokens)
}
