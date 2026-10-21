package gateway

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/ruanun/simple-ai-gateway/internal/router"
	"github.com/stretchr/testify/require"
)

// openai 流式 mock：断言是否收到 include_usage，回一个内容块 + usage 收尾块 + [DONE]。
func streamUsageMock(t *testing.T, wantInclude bool) *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			StreamOptions *struct {
				IncludeUsage bool `json:"include_usage"`
			} `json:"stream_options"`
		}
		b, _ := io.ReadAll(r.Body)
		require.NoError(t, json.Unmarshal(b, &req))
		got := req.StreamOptions != nil && req.StreamOptions.IncludeUsage
		require.Equal(t, wantInclude, got, "出站 include_usage 注入判定")
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(200)
		fl, _ := w.(http.Flusher)
		_, _ = w.Write([]byte("data: {\"choices\":[{\"delta\":{\"content\":\"hi\"}}]}\n\n"))
		fl.Flush()
		_, _ = w.Write([]byte("data: {\"choices\":[],\"usage\":{\"prompt_tokens\":12,\"completion_tokens\":7,\"total_tokens\":19}}\n\n"))
		fl.Flush()
		_, _ = w.Write([]byte("data: [DONE]\n\n"))
		fl.Flush()
	}))
}

// 默认开关（DisableStreamUsage=false）：注入 include_usage、拿到 usage 计费、客户端剥离 usage 块。
func TestDispatchInjectStreamUsage(t *testing.T) {
	up := streamUsageMock(t, true)
	defer up.Close()
	d := convDeps(func(id uint64, m string) (ChannelInfo, bool) {
		return ChannelInfo{BaseURL: up.URL, UpstreamKey: "k", Timeout: 5, ProviderType: "openai"}, true
	})
	cands := []router.Candidate{{ChannelID: 1, Model: "m", ProviderType: "openai", Outbound: "openai", Inbound: "openai"}}
	w := httptest.NewRecorder()
	out := Dispatch(w, cands, []byte(`{"model":"m","stream":true,"messages":[{"role":"user","content":"hi"}]}`), d)

	require.Equal(t, 200, out.Status)
	require.True(t, out.UsageOK)
	require.Equal(t, 12, out.Usage.PromptTokens)
	require.Equal(t, 7, out.Usage.CompletionTokens)
	require.Contains(t, w.Body.String(), `"content":"hi"`)
	require.NotContains(t, w.Body.String(), `"usage"`, "客户端没要 usage 应被剥离")
}

// 渠道关闭注入（DisableStreamUsage=true）：断言出站请求体未带 include_usage（mock 内校验 wantInclude=false）。
func TestDispatchDisableStreamUsage(t *testing.T) {
	up := streamUsageMock(t, false)
	defer up.Close()
	d := convDeps(func(id uint64, m string) (ChannelInfo, bool) {
		return ChannelInfo{BaseURL: up.URL, UpstreamKey: "k", Timeout: 5, ProviderType: "openai"}, true
	})
	cands := []router.Candidate{{ChannelID: 1, Model: "m", ProviderType: "openai", Outbound: "openai", Inbound: "openai", DisableStreamUsage: true}}
	w := httptest.NewRecorder()
	out := Dispatch(w, cands, []byte(`{"model":"m","stream":true,"messages":[{"role":"user","content":"hi"}]}`), d)
	require.Equal(t, 200, out.Status)
	// mock 仍回了 usage 块（内部会解析到），但关键断言在 mock 内：出站未注入 include_usage。
}

// 客户端自己要了 usage：注入无副作用，但客户端应看到 usage 块（不剥离）。
func TestDispatchClientWantsUsageNotStripped(t *testing.T) {
	up := streamUsageMock(t, true)
	defer up.Close()
	d := convDeps(func(id uint64, m string) (ChannelInfo, bool) {
		return ChannelInfo{BaseURL: up.URL, UpstreamKey: "k", Timeout: 5, ProviderType: "openai"}, true
	})
	cands := []router.Candidate{{ChannelID: 1, Model: "m", ProviderType: "openai", Outbound: "openai", Inbound: "openai"}}
	w := httptest.NewRecorder()
	out := Dispatch(w, cands, []byte(`{"model":"m","stream":true,"stream_options":{"include_usage":true},"messages":[{"role":"user","content":"hi"}]}`), d)
	require.Equal(t, 200, out.Status)
	require.True(t, out.UsageOK)
	require.Contains(t, w.Body.String(), `"usage"`, "客户端主动要了 usage 不应剥离")
}

// 客户端 /messages(anthropic) → 转换到 openai 上游流式：注入生效 + usage 从转换流提取。
func TestDispatchConvertToOpenAIStreamUsage(t *testing.T) {
	up := streamUsageMock(t, true) // openai 上游，断言收到 include_usage
	defer up.Close()
	d := convDeps(func(id uint64, m string) (ChannelInfo, bool) {
		return ChannelInfo{BaseURL: up.URL, UpstreamKey: "k", Timeout: 5, ProviderType: "openai"}, true
	})
	cands := []router.Candidate{{ChannelID: 1, Model: "m", ProviderType: "openai", Outbound: "openai", Inbound: "anthropic", NeedsConvert: true}}
	w := httptest.NewRecorder()
	out := Dispatch(w, cands, []byte(`{"model":"m","stream":true,"messages":[{"role":"user","content":"hi"}]}`), d)
	require.Equal(t, 200, out.Status)
	require.True(t, out.UsageOK, "转换路径也应从 openai 上游注入的 usage 块提取到用量")
	require.Equal(t, 12, out.Usage.PromptTokens)
}

// 转换到 openai 上游 + 渠道关闭注入：convert.Request 会强制加 include_usage，dispatch 必须移除，
// 使 DisableStreamUsage 开关对转换流量同样生效（出站请求体不得带 include_usage）。
func TestDispatchConvertToOpenAIDisableStreamUsage(t *testing.T) {
	up := streamUsageMock(t, false) // 断言出站未带 include_usage
	defer up.Close()
	d := convDeps(func(id uint64, m string) (ChannelInfo, bool) {
		return ChannelInfo{BaseURL: up.URL, UpstreamKey: "k", Timeout: 5, ProviderType: "openai"}, true
	})
	cands := []router.Candidate{{ChannelID: 1, Model: "m", ProviderType: "openai", Outbound: "openai", Inbound: "anthropic", NeedsConvert: true, DisableStreamUsage: true}}
	w := httptest.NewRecorder()
	out := Dispatch(w, cands, []byte(`{"model":"m","stream":true,"messages":[{"role":"user","content":"hi"}]}`), d)
	require.Equal(t, 200, out.Status)
}
