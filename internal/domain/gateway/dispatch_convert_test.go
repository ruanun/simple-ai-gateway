package gateway

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ruanun/simple-ai-gateway/internal/breaker"
	"github.com/ruanun/simple-ai-gateway/internal/ratelimit"
	"github.com/ruanun/simple-ai-gateway/internal/router"
	"github.com/stretchr/testify/require"
)

// convDeps 造一套最小派发依赖。
func convDeps(lookup func(id uint64, m string) (ChannelInfo, bool)) Deps {
	return Deps{
		Breaker: breaker.NewMemoryBreaker(breaker.Params{Window: time.Minute, Cooldown: time.Minute, FailThreshold: 5, ErrorRate: 0.5, MinSamples: 20, ProbeBackoffMax: 8}),
		Limiter: ratelimit.NewMemoryLimiter(),
		Lookup:  lookup,
		Now:     time.Now,
	}
}

// TestDispatchConvertOpenAIToAnthropic openai 入站经转换候选打 anthropic 上游：
// 出站体必须是 anthropic 协议（含 max_tokens/messages 块结构）、model 已改写、
// 走 /v1/messages + x-api-key；客户端拿到 openai chat.completion；Outcome.Protocol=anthropic。
func TestDispatchConvertOpenAIToAnthropic(t *testing.T) {
	var gotPath, gotKey string
	var gotBody map[string]any
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotKey = r.Header.Get("x-api-key")
		b, _ := io.ReadAll(r.Body)
		require.NoError(t, json.Unmarshal(b, &gotBody))
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"msg_1","type":"message","role":"assistant","model":"claude-3-5-sonnet",` +
			`"content":[{"type":"text","text":"你好"}],"stop_reason":"end_turn",` +
			`"usage":{"input_tokens":10,"output_tokens":4}}`))
	}))
	defer up.Close()

	d := convDeps(func(id uint64, m string) (ChannelInfo, bool) {
		return ChannelInfo{BaseURL: up.URL, UpstreamKey: "sk-ant-up", Timeout: 5, ProviderType: "anthropic"}, true
	})
	cands := []router.Candidate{{ChannelID: 1, Model: "claude-3-5-sonnet", ProviderType: "anthropic",
		Outbound: "anthropic", Inbound: "openai", NeedsConvert: true}}
	w := httptest.NewRecorder()
	body := []byte(`{"model":"smart","messages":[{"role":"user","content":"hi"}]}`)
	out := Dispatch(w, cands, body, d)

	require.Equal(t, 200, out.Status)
	require.Equal(t, "anthropic", out.Protocol)
	require.Equal(t, "/v1/messages", gotPath)
	require.Equal(t, "sk-ant-up", gotKey)
	require.Equal(t, "claude-3-5-sonnet", gotBody["model"], "出站 model 必须按候选 c.Model 改写")
	require.NotNil(t, gotBody["max_tokens"], "anthropic 出站必须带 max_tokens")
	msgs := gotBody["messages"].([]any)
	blk := msgs[0].(map[string]any)["content"].([]any)[0].(map[string]any)
	require.Equal(t, "text", blk["type"], "出站体是 anthropic 块结构")
	// 客户端拿到 openai 格式
	require.Contains(t, w.Body.String(), `"chat.completion"`)
	// usage 上游原生解析
	require.True(t, out.UsageOK)
	require.Equal(t, 10, out.Usage.PromptTokens)
	require.Equal(t, 4, out.Usage.CompletionTokens)
}

// TestDispatchStripThinking 渠道标记不支持 thinking：透传（非转换）出站体剥离
// thinking/reasoning_effort，model 仍改写、messages 保留。
func TestDispatchStripThinking(t *testing.T) {
	var gotBody map[string]any
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		require.NoError(t, json.Unmarshal(b, &gotBody))
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"msg_1","type":"message","role":"assistant","model":"claude-3-5-sonnet",` +
			`"content":[{"type":"text","text":"hi"}],"stop_reason":"end_turn","usage":{"input_tokens":1,"output_tokens":1}}`))
	}))
	defer up.Close()

	d := convDeps(func(id uint64, m string) (ChannelInfo, bool) {
		return ChannelInfo{BaseURL: up.URL, UpstreamKey: "sk-ant", Timeout: 5, ProviderType: "anthropic"}, true
	})
	// 透传候选（NeedsConvert=false）+ StripThinking
	cands := []router.Candidate{{ChannelID: 1, Model: "claude-3-5-sonnet", ProviderType: "anthropic",
		Outbound: "anthropic", Inbound: "anthropic", NeedsConvert: false, StripThinking: true}}
	w := httptest.NewRecorder()
	body := []byte(`{"model":"smart","messages":[{"role":"user","content":"hi"}],` +
		`"thinking":{"type":"enabled","budget_tokens":1024},"reasoning_effort":"high"}`)
	out := Dispatch(w, cands, body, d)

	require.Equal(t, 200, out.Status)
	require.NotContains(t, gotBody, "thinking", "透传出站应剥离 thinking")
	require.NotContains(t, gotBody, "reasoning_effort", "透传出站应剥离 reasoning_effort")
	require.Equal(t, "claude-3-5-sonnet", gotBody["model"], "model 仍按候选改写")
	require.NotNil(t, gotBody["messages"], "messages 保留")
}

// TestDispatchConvertStream anthropic SSE 上游 → openai chunk 客户端（流式）。
func TestDispatchConvertStream(t *testing.T) {
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req map[string]any
		b, _ := io.ReadAll(r.Body)
		require.NoError(t, json.Unmarshal(b, &req))
		require.Equal(t, true, req["stream"], "stream 标志转换后保留")
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "event: message_start\n"+
			`data: {"type":"message_start","message":{"id":"msg_1","model":"claude-3-5-sonnet","usage":{"input_tokens":20}}}`+"\n\n"+
			"event: content_block_delta\n"+
			`data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"hi"}}`+"\n\n"+
			"event: message_delta\n"+
			`data: {"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":8}}`+"\n\n"+
			"event: message_stop\n"+`data: {"type":"message_stop"}`+"\n\n")
	}))
	defer up.Close()

	d := convDeps(func(id uint64, m string) (ChannelInfo, bool) {
		return ChannelInfo{BaseURL: up.URL, UpstreamKey: "k", Timeout: 5, ProviderType: "anthropic"}, true
	})
	cands := []router.Candidate{{ChannelID: 1, Model: "claude-3-5-sonnet", ProviderType: "anthropic",
		Outbound: "anthropic", Inbound: "openai", NeedsConvert: true}}
	w := httptest.NewRecorder()
	body := []byte(`{"model":"smart","stream":true,"messages":[{"role":"user","content":"hi"}]}`)
	out := Dispatch(w, cands, body, d)

	require.Equal(t, 200, out.Status)
	require.True(t, out.Stream)
	require.True(t, out.UsageOK)
	require.Equal(t, 20, out.Usage.PromptTokens)
	require.Equal(t, 8, out.Usage.CompletionTokens)
	require.Contains(t, w.Body.String(), `"chat.completion.chunk"`)
	require.Contains(t, w.Body.String(), "data: [DONE]")
}

// TestDispatchConvertBadRespFailover 转换候选上游 200 但响应体不合协议 → failover 到下一透传候选。
func TestDispatchConvertBadRespFailover(t *testing.T) {
	bad := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"garbage":1}`))
	}))
	defer bad.Close()
	good := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"ok","choices":[{"index":0,"message":{"role":"assistant","content":"hi"},"finish_reason":"stop"}]}`))
	}))
	defer good.Close()

	infos := map[uint64]ChannelInfo{
		1: {BaseURL: bad.URL, Timeout: 5, ProviderType: "anthropic"},
		2: {BaseURL: good.URL, Timeout: 5, ProviderType: "openai"},
	}
	d := convDeps(func(id uint64, m string) (ChannelInfo, bool) { i, ok := infos[id]; return i, ok })
	cands := []router.Candidate{
		{ChannelID: 1, Model: "m", ProviderType: "anthropic", Outbound: "anthropic", Inbound: "openai", NeedsConvert: true},
		{ChannelID: 2, Model: "m", ProviderType: "openai", Outbound: "openai", Inbound: "openai"},
	}
	w := httptest.NewRecorder()
	out := Dispatch(w, cands, []byte(`{"model":"m","messages":[{"role":"user","content":"hi"}]}`), d)
	require.Equal(t, 200, out.Status)
	require.Equal(t, uint64(2), out.ChannelID, "响应转换失败应 failover 到透传候选")
	require.Equal(t, "openai", out.Protocol)
	require.Contains(t, w.Body.String(), `"id":"ok"`)
}

// TestDispatchConvertBadRespNoAccountRetry 响应转换失败是渠道级配置问题：
// 即使渠道配了 MaxRetries，也不换账号重试同渠道（坏渠道只被请求 1 次），直接 failover 下一候选。
func TestDispatchConvertBadRespNoAccountRetry(t *testing.T) {
	var badHits int32
	bad := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&badHits, 1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"garbage":1}`))
	}))
	defer bad.Close()
	good := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"ok","choices":[{"index":0,"message":{"role":"assistant","content":"hi"},"finish_reason":"stop"}]}`))
	}))
	defer good.Close()

	infos := map[uint64]ChannelInfo{
		1: {BaseURL: bad.URL, Timeout: 5, ProviderType: "anthropic", MaxRetries: 2},
		2: {BaseURL: good.URL, Timeout: 5, ProviderType: "openai"},
	}
	d := convDeps(func(id uint64, m string) (ChannelInfo, bool) { i, ok := infos[id]; return i, ok })
	cands := []router.Candidate{
		{ChannelID: 1, Model: "m", ProviderType: "anthropic", Outbound: "anthropic", Inbound: "openai", NeedsConvert: true},
		{ChannelID: 2, Model: "m", ProviderType: "openai", Outbound: "openai", Inbound: "openai"},
	}
	w := httptest.NewRecorder()
	out := Dispatch(w, cands, []byte(`{"model":"m","messages":[{"role":"user","content":"hi"}]}`), d)
	require.Equal(t, 200, out.Status)
	require.Equal(t, uint64(2), out.ChannelID)
	require.Equal(t, int32(1), atomic.LoadInt32(&badHits), "协议误配是渠道级问题,不应换账号重试同渠道")
}

// TestDispatch502FillsLastProtocol 全候选失败时 Outcome.Protocol=最后一次实际尝试的出站协议（观察项①）。
func TestDispatch502FillsLastProtocol(t *testing.T) {
	bad := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(500) }))
	defer bad.Close()
	d := convDeps(func(id uint64, m string) (ChannelInfo, bool) {
		return ChannelInfo{BaseURL: bad.URL, Timeout: 5, ProviderType: "anthropic"}, true
	})
	cands := []router.Candidate{{ChannelID: 1, Model: "m", ProviderType: "anthropic",
		Outbound: "anthropic", Inbound: "openai", NeedsConvert: true}}
	w := httptest.NewRecorder()
	out := Dispatch(w, cands, []byte(`{"model":"m","messages":[{"role":"user","content":"hi"}]}`), d)
	require.Equal(t, http.StatusBadGateway, out.Status)
	require.Equal(t, "anthropic", out.Protocol, "失败也要记实际出站协议(日志 UpstreamProtocol 语义)")
}
