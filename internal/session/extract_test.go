package session

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestExtractAnthropicSessionID_JSON(t *testing.T) {
	body := []byte(`{"model":"claude-3","metadata":{"user_id":"{\"device_id\":\"abc\",\"session_id\":\"sess-uuid-123\"}"},"messages":[]}`)
	require.Equal(t, "sess-uuid-123", ExtractAnthropicSessionID(body))
}

func TestExtractAnthropicSessionID_Legacy(t *testing.T) {
	body := []byte(`{"model":"claude-3","metadata":{"user_id":"user_a1b2c3d4e5f6a1b2c3d4e5f6a1b2c3d4e5f6a1b2c3d4e5f6a1b2c3d4e5f6a1b2_account__session_123e4567-e89b-12d3-a456-426614174000"},"messages":[]}`)
	require.Equal(t, "123e4567-e89b-12d3-a456-426614174000", ExtractAnthropicSessionID(body))
}

func TestExtractAnthropicSessionID_NoMetadata(t *testing.T) {
	body := []byte(`{"model":"claude-3","messages":[{"role":"user","content":"hi"}]}`)
	require.Equal(t, "", ExtractAnthropicSessionID(body))
}

func TestExtractResponsesSessionID(t *testing.T) {
	body := []byte(`{"model":"gpt-4o","input":"hi","previous_response_id":"resp_abc123"}`)
	require.Equal(t, "resp_abc123", ExtractResponsesSessionID(body))
}

func TestExtractResponsesSessionID_None(t *testing.T) {
	body := []byte(`{"model":"gpt-4o","input":"hi"}`)
	require.Equal(t, "", ExtractResponsesSessionID(body))
}

func TestGenerateChatSessionHash(t *testing.T) {
	body := []byte(`{"model":"gpt-4o","messages":[{"role":"system","content":"You are helpful"},{"role":"user","content":"hello"}]}`)
	h1 := GenerateChatSessionHash("1.2.3.4", "curl", 1, body)
	require.NotEmpty(t, h1)
	// 相同输入 → 相同 hash
	h2 := GenerateChatSessionHash("1.2.3.4", "curl", 1, body)
	require.Equal(t, h1, h2)
	// 不同 IP → 不同 hash
	h3 := GenerateChatSessionHash("5.6.7.8", "curl", 1, body)
	require.NotEqual(t, h1, h3)
}

func TestGenerateChatSessionHash_NoMessages(t *testing.T) {
	body := []byte(`{"model":"gpt-4o","messages":[]}`)
	require.Equal(t, "", GenerateChatSessionHash("1.2.3.4", "curl", 1, body))
}

func TestExtractChatSessionID_PromptCacheKeyWins(t *testing.T) {
	body := []byte(`{"model":"gpt-4o","prompt_cache_key":"conv-abc","messages":[{"role":"user","content":"hello"}]}`)
	a := ExtractChatSessionID("1.2.3.4", "curl", 1, body)
	b := ExtractChatSessionID("5.6.7.8", "other", 1, body)
	require.NotEmpty(t, a)
	require.Equal(t, a, b, "prompt_cache_key 相同则跨 IP/UA 应命中同一会话")
	require.NotEqual(t, GenerateChatSessionHash("1.2.3.4", "curl", 1, body), a)
}

func TestExtractChatSessionID_FallbackToFingerprint(t *testing.T) {
	body := []byte(`{"model":"gpt-4o","messages":[{"role":"user","content":"hello"}]}`)
	require.Equal(t,
		GenerateChatSessionHash("1.2.3.4", "curl", 1, body),
		ExtractChatSessionID("1.2.3.4", "curl", 1, body))
}

func TestExtractResponsesSessionID_PromptCacheKeyWins(t *testing.T) {
	body := []byte(`{"model":"gpt-4o","prompt_cache_key":"conv-abc","previous_response_id":"resp_1"}`)
	a := ExtractResponsesSessionID(body)
	b := ExtractResponsesSessionID([]byte(`{"model":"gpt-4o","prompt_cache_key":"conv-abc","previous_response_id":"resp_2"}`))
	require.NotEmpty(t, a)
	require.Equal(t, a, b)
	require.NotEqual(t, "resp_1", a)
}

func TestGenerateChatSessionHash_ModelAffectsHash(t *testing.T) {
	body1 := []byte(`{"model":"gpt-4o","messages":[{"role":"user","content":"hello"}]}`)
	body2 := []byte(`{"model":"gpt-4o-mini","messages":[{"role":"user","content":"hello"}]}`)
	require.NotEqual(t,
		GenerateChatSessionHash("1.2.3.4", "curl", 1, body1),
		GenerateChatSessionHash("1.2.3.4", "curl", 1, body2))
}

func TestGenerateChatSessionHash_ArrayContent(t *testing.T) {
	body := []byte(`{"model":"gpt-4o","messages":[{"role":"user","content":[{"type":"text","text":"hello"}]}]}`)
	require.NotEmpty(t, GenerateChatSessionHash("1.2.3.4", "curl", 1, body))
}
