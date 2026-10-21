package gateway

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// anResp 造一个 anthropic 非流式响应。
func anRespBody() string {
	return `{"id":"msg_1","type":"message","role":"assistant","model":"claude-3-5-sonnet",` +
		`"content":[{"type":"text","text":"你好"}],"stop_reason":"end_turn",` +
		`"usage":{"input_tokens":10,"output_tokens":4}}`
}

func httpResp(status int, contentType, body string) *http.Response {
	return &http.Response{
		StatusCode: status,
		Header:     http.Header{"Content-Type": []string{contentType}},
		Body:       io.NopCloser(strings.NewReader(body)),
	}
}

// TestRelayConvertNonStream anthropic 上游 → openai 客户端（非流式）：
// 响应体转成 chat.completion，usage 按上游协议原生解析。
func TestRelayConvertNonStream(t *testing.T) {
	w := httptest.NewRecorder()
	res, u, uok, body, err := RelayConvert(w, httpResp(200, "application/json", anRespBody()), false, "anthropic", "openai", true, time.Now())
	require.NoError(t, err)
	require.Equal(t, 200, res.Status)
	require.True(t, uok)
	require.Equal(t, 10, u.PromptTokens)
	require.Equal(t, 4, u.CompletionTokens)
	require.Equal(t, 14, u.TotalTokens)
	require.Contains(t, w.Body.String(), `"chat.completion"`)
	require.Contains(t, w.Body.String(), `"你好"`)
	require.Equal(t, w.Body.String(), string(body), "captured=实际写给客户端的转换后响应(审计回放)")
}

// TestRelayConvertNonStreamBadBody 上游 200 但响应体不是本协议结构 → 返回 err 且不写客户端(failover)。
func TestRelayConvertNonStreamBadBody(t *testing.T) {
	w := httptest.NewRecorder()
	_, _, _, _, err := RelayConvert(w, httpResp(200, "application/json", `{"garbage":1}`), false, "anthropic", "openai", true, time.Now())
	require.Error(t, err)
	require.Zero(t, w.Body.Len(), "转换失败不得向客户端写任何字节")
	require.False(t, w.Flushed)
}

// TestRelayConvertUpstreamError 上游 4xx：错误信封转换后透传状态码，不触发 failover(err=nil)。
func TestRelayConvertUpstreamError(t *testing.T) {
	w := httptest.NewRecorder()
	body := `{"type":"error","error":{"type":"authentication_error","message":"bad key"}}`
	res, _, uok, _, err := RelayConvert(w, httpResp(401, "application/json", body), false, "anthropic", "openai", true, time.Now())
	require.NoError(t, err)
	require.Equal(t, 401, res.Status)
	require.False(t, uok)
	require.Contains(t, w.Body.String(), `"bad key"`)
	require.Contains(t, w.Body.String(), `"upstream_error"`)
}

// TestRelayConvertStream anthropic SSE 上游 → openai chunk 流。
func TestRelayConvertStream(t *testing.T) {
	sse := "event: message_start\n" +
		`data: {"type":"message_start","message":{"id":"msg_1","model":"claude-3-5-sonnet","usage":{"input_tokens":20}}}` + "\n\n" +
		"event: content_block_delta\n" +
		`data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"hi"}}` + "\n\n" +
		"event: message_delta\n" +
		`data: {"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":8}}` + "\n\n" +
		"event: message_stop\n" + `data: {"type":"message_stop"}` + "\n\n"
	w := httptest.NewRecorder()
	res, u, uok, body, err := RelayConvert(w, httpResp(200, "text/event-stream", sse), true, "anthropic", "openai", true, time.Now())
	require.NoError(t, err)
	require.True(t, res.Stream)
	require.Equal(t, 200, res.Status)
	require.True(t, uok)
	require.Equal(t, 20, u.PromptTokens)
	require.Equal(t, 8, u.CompletionTokens)
	out := w.Body.String()
	require.Contains(t, out, `"chat.completion.chunk"`)
	require.Contains(t, out, `"content":"hi"`)
	require.Contains(t, out, "data: [DONE]")
	require.Equal(t, out, string(body))
	require.Equal(t, "text/event-stream", w.Header().Get("Content-Type"))
}

// TestRelayConvertStreamCacheTokens anthropic 上游流的缓存 token 归一进入 billing usage（P12d）。
func TestRelayConvertStreamCacheTokens(t *testing.T) {
	sse := "event: message_start\n" +
		`data: {"type":"message_start","message":{"id":"msg_1","model":"m","usage":{"input_tokens":100,"cache_creation_input_tokens":200,"cache_read_input_tokens":700}}}` + "\n\n" +
		"event: content_block_delta\n" +
		`data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"hi"}}` + "\n\n" +
		"event: message_delta\n" +
		`data: {"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":50}}` + "\n\n" +
		"event: message_stop\n" + `data: {"type":"message_stop"}` + "\n\n"
	w := httptest.NewRecorder()
	_, u, uok, _, err := RelayConvert(w, httpResp(200, "text/event-stream", sse), true, "anthropic", "openai", true, time.Now())
	require.NoError(t, err)
	require.True(t, uok)
	require.Equal(t, 1000, u.PromptTokens, "总输入=input+cache_read+cache_creation")
	require.Equal(t, 700, u.CachedTokens)
	require.Equal(t, 200, u.CacheCreationTokens)
	require.Equal(t, 50, u.CompletionTokens)
}

// TestRelayConvertStreamFirstEventBad 首事件即坏 → err 返回、零写出(可 failover)。
func TestRelayConvertStreamFirstEventBad(t *testing.T) {
	w := httptest.NewRecorder()
	_, _, _, _, err := RelayConvert(w, httpResp(200, "text/event-stream", "event: message_start\ndata: {broken\n\n"), true, "anthropic", "openai", true, time.Now())
	require.Error(t, err)
	require.Zero(t, w.Body.Len())
}

// TestRelayConvertStreamMidFail 已写出字节后上游断流坏帧 → 发错误帧终止，err=nil(不 failover)。
func TestRelayConvertStreamMidFail(t *testing.T) {
	sse := "event: message_start\n" +
		`data: {"type":"message_start","message":{"id":"msg_1","model":"m","usage":{"input_tokens":5}}}` + "\n\n" +
		"event: content_block_delta\n" + "data: {broken\n\n"
	w := httptest.NewRecorder()
	res, _, _, _, err := RelayConvert(w, httpResp(200, "text/event-stream", sse), true, "anthropic", "openai", true, time.Now())
	require.NoError(t, err, "已写出字节的中途失败不再 failover")
	require.NotNil(t, res)
	require.Contains(t, w.Body.String(), `"upstream_error"`, "客户端收到协议错误帧")
	require.Contains(t, w.Body.String(), "data: [DONE]")
}

// failRecorder 包一层 ResponseRecorder：Write 恒失败（模拟客户端断开），并记录 WriteHeader 调用次数。
type failRecorder struct {
	*httptest.ResponseRecorder
	headerCalls int
}

func (f *failRecorder) WriteHeader(code int) {
	f.headerCalls++
	f.ResponseRecorder.WriteHeader(code)
}

func (f *failRecorder) Write([]byte) (int, error) {
	return 0, io.ErrClosedPipe
}

// TestRelayConvertStreamClientDisconnectFirstFrame 客户端在首帧即断开（Write 失败）：
// WriteHeader 已提交（提交点），必须返回 err=nil（不 failover，防重拨上游重复计费），
// 且 WriteHeader 恰被调用 1 次（failover 重拨会触发 superfluous 二次写头）。
func TestRelayConvertStreamClientDisconnectFirstFrame(t *testing.T) {
	sse := "event: message_start\n" +
		`data: {"type":"message_start","message":{"id":"msg_1","model":"m","usage":{"input_tokens":5}}}` + "\n\n" +
		"event: content_block_delta\n" +
		`data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"hi"}}` + "\n\n" +
		"event: message_delta\n" +
		`data: {"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":2}}` + "\n\n" +
		"event: message_stop\n" + `data: {"type":"message_stop"}` + "\n\n"
	w := &failRecorder{ResponseRecorder: httptest.NewRecorder()}
	res, _, _, _, err := RelayConvert(w, httpResp(200, "text/event-stream", sse), true, "anthropic", "openai", true, time.Now())
	require.NoError(t, err, "头已提交（WriteHeader 即提交点），不得返回 err 触发 failover")
	require.NotNil(t, res)
	require.Equal(t, 1, w.headerCalls, "WriteHeader 恰调用 1 次")
}

// TestRelayConvertStreamOpenAIToAnthropic openai 上游流 → anthropic 客户端：
// usage 来自 openai 的 usage 整块事件（case "usage" 整块累积分支），响应体含 message_start/message_stop。
func TestRelayConvertStreamOpenAIToAnthropic(t *testing.T) {
	sse := `data: {"id":"chatcmpl-1","object":"chat.completion.chunk","model":"gpt-4o","choices":[{"index":0,"delta":{"role":"assistant","content":""},"finish_reason":null}]}` + "\n\n" +
		`data: {"id":"chatcmpl-1","object":"chat.completion.chunk","model":"gpt-4o","choices":[{"index":0,"delta":{"content":"你好"},"finish_reason":null}]}` + "\n\n" +
		`data: {"id":"chatcmpl-1","object":"chat.completion.chunk","model":"gpt-4o","choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}` + "\n\n" +
		`data: {"id":"chatcmpl-1","object":"chat.completion.chunk","model":"gpt-4o","choices":[],"usage":{"prompt_tokens":25,"completion_tokens":18,"total_tokens":43}}` + "\n\n" +
		"data: [DONE]\n\n"
	w := httptest.NewRecorder()
	res, u, uok, body, err := RelayConvert(w, httpResp(200, "text/event-stream", sse), true, "openai", "anthropic", true, time.Now())
	require.NoError(t, err)
	require.True(t, res.Stream)
	require.True(t, uok)
	require.Equal(t, 25, u.PromptTokens)
	require.Equal(t, 18, u.CompletionTokens)
	require.Equal(t, 43, u.TotalTokens)
	out := w.Body.String()
	require.Contains(t, out, "event: message_start")
	require.Contains(t, out, "event: message_stop")
	require.Contains(t, out, `"你好"`)
	require.Equal(t, out, string(body))
}

// TestRelayConvertStreamTruncatedNoDone 上游发了内容但没发 message_stop 就干净关流（截断）：
// 已写出字节 → 发错误帧终止，err=nil（不 failover），usageOK 按已解析到的算。
func TestRelayConvertStreamTruncatedNoDone(t *testing.T) {
	sse := "event: message_start\n" +
		`data: {"type":"message_start","message":{"id":"msg_1","model":"m","usage":{"input_tokens":5}}}` + "\n\n" +
		"event: content_block_delta\n" +
		`data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"hi"}}` + "\n\n"
	w := httptest.NewRecorder()
	res, _, _, _, err := RelayConvert(w, httpResp(200, "text/event-stream", sse), true, "anthropic", "openai", true, time.Now())
	require.NoError(t, err, "已写出字节的截断不 failover")
	require.NotNil(t, res)
	require.Contains(t, w.Body.String(), `"upstream_error"`, "客户端收到协议错误帧")
}

// TestRelayConvertStreamOpenAICacheReasoning openai 上游流 → anthropic 客户端：
// usage 末块的 cached/reasoning 明细穿透到 billing.Usage（转换流式子路径不再少计）。
func TestRelayConvertStreamOpenAICacheReasoning(t *testing.T) {
	sse := `data: {"id":"chatcmpl-1","object":"chat.completion.chunk","model":"gpt-4o","choices":[{"index":0,"delta":{"role":"assistant","content":"你好"},"finish_reason":null}]}` + "\n\n" +
		`data: {"id":"chatcmpl-1","object":"chat.completion.chunk","model":"gpt-4o","choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}` + "\n\n" +
		`data: {"id":"chatcmpl-1","object":"chat.completion.chunk","model":"gpt-4o","choices":[],"usage":{"prompt_tokens":1000,"completion_tokens":200,"total_tokens":1200,"prompt_tokens_details":{"cached_tokens":700},"completion_tokens_details":{"reasoning_tokens":50}}}` + "\n\n" +
		"data: [DONE]\n\n"
	w := httptest.NewRecorder()
	_, u, uok, _, err := RelayConvert(w, httpResp(200, "text/event-stream", sse), true, "openai", "anthropic", true, time.Now())
	require.NoError(t, err)
	require.True(t, uok)
	require.Equal(t, 1000, u.PromptTokens)
	require.Equal(t, 200, u.CompletionTokens)
	require.Equal(t, 700, u.CachedTokens, "cached 明细穿透（此前恒 0）")
	require.Equal(t, 50, u.ReasoningTokens, "reasoning 明细穿透（此前恒 0）")
}
