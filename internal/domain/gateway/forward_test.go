package gateway

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// dialDirect 测试辅助：自建直连 client（流式不设整体超时）+ 最小 spec 后调 DialWith，
// 等价于已删除的兼容包装 Dial（生产代码只走 DialWith）。
func dialDirect(a Adapter, baseURL, upstreamKey string, body []byte, timeoutSec int) (*http.Response, error) {
	client := &http.Client{Timeout: time.Duration(timeoutSec) * time.Second}
	if parseInbound(body).stream {
		client.Timeout = 0 // 流式不设整体超时
	}
	spec := HeaderSpec{Protocol: a.Protocol(), BaseURL: baseURL, UpstreamKey: upstreamKey}
	// 首字节预算由 tryCandidate 计算并注入；这里的测试辅助不测预算，传 Background 即不设预算。
	return DialWith(context.Background(), client, a, spec, body)
}

func TestIsStream(t *testing.T) {
	require.True(t, parseInbound([]byte(`{"model":"m","stream":true}`)).stream)
	require.False(t, parseInbound([]byte(`{"model":"m"}`)).stream)
	require.False(t, parseInbound([]byte(`{"model":"m","stream":false}`)).stream)
	require.False(t, parseInbound([]byte("not json")).stream)
}

func TestStripThinkingFields(t *testing.T) {
	body := []byte(`{"model":"m","messages":[],"thinking":{"type":"enabled"},"reasoning":{"x":1},"reasoning_effort":"high"}`)
	var m map[string]any
	require.NoError(t, json.Unmarshal(stripThinkingFields(body), &m))
	require.NotContains(t, m, "thinking")
	require.NotContains(t, m, "reasoning")
	require.NotContains(t, m, "reasoning_effort")
	require.Contains(t, m, "model")
	require.Contains(t, m, "messages")

	// 无相关字段时原样返回（零拷贝）
	plain := []byte(`{"model":"m"}`)
	require.Equal(t, string(plain), string(stripThinkingFields(plain)))
}

func TestDialReturnsResponse(t *testing.T) {
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(200)
		_, _ = w.Write([]byte(`{"id":"x","choices":[]}`))
	}))
	defer up.Close()

	resp, err := dialDirect(OpenAIAdapter{}, up.URL, "sk-up", []byte(`{"model":"m"}`), 30)
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, 200, resp.StatusCode)
}

func TestDialUpstreamUnavailable(t *testing.T) {
	// 关闭后的 server，连接失败应返回 err
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	url := up.URL
	up.Close()

	resp, err := dialDirect(OpenAIAdapter{}, url, "sk-up", []byte(`{"model":"m"}`), 2)
	if resp != nil {
		resp.Body.Close()
	}
	require.Error(t, err)
}

func TestRelayNonStream(t *testing.T) {
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(200)
		_, _ = w.Write([]byte(`{"id":"x","choices":[]}`))
	}))
	defer up.Close()

	resp, err := dialDirect(OpenAIAdapter{}, up.URL, "sk-up", []byte(`{"model":"m"}`), 30)
	require.NoError(t, err)

	rec := httptest.NewRecorder()
	res, _, ok, body := Relay(rec, resp, false, false, true)
	require.Equal(t, 200, res.Status)
	require.False(t, res.Stream)
	require.False(t, ok) // 该响应无 usage 字段
	require.Contains(t, rec.Body.String(), `"id":"x"`)
	require.Contains(t, string(body), `"id":"x"`) // 返回已读响应体供审计
}

func TestRelayNonStreamUsage(t *testing.T) {
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(200)
		_, _ = w.Write([]byte(`{"id":"x","choices":[],"usage":{"prompt_tokens":100,"completion_tokens":50,"total_tokens":150}}`))
	}))
	defer up.Close()

	resp, err := dialDirect(OpenAIAdapter{}, up.URL, "sk-up", []byte(`{"model":"m"}`), 30)
	require.NoError(t, err)

	rec := httptest.NewRecorder()
	res, u, ok, body := Relay(rec, resp, false, false, true)
	require.Equal(t, 200, res.Status)
	require.True(t, ok)
	require.Equal(t, 100, u.PromptTokens)
	require.Equal(t, 50, u.CompletionTokens)
	require.Contains(t, rec.Body.String(), `"id":"x"`)
	require.Contains(t, string(body), `"usage"`) // 返回已读响应体供审计
}

func TestRelayStream(t *testing.T) {
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(200)
		fl, _ := w.(http.Flusher)
		_, _ = w.Write([]byte("data: {\"delta\":\"hi\"}\n\n"))
		fl.Flush()
		_, _ = w.Write([]byte("data: [DONE]\n\n"))
		fl.Flush()
	}))
	defer up.Close()

	resp, err := dialDirect(OpenAIAdapter{}, up.URL, "sk-up", []byte(`{"model":"m","stream":true}`), 30)
	require.NoError(t, err)

	rec := httptest.NewRecorder()
	res, _, ok, captured := Relay(rec, resp, true, false, true)
	require.Equal(t, 200, res.Status)
	require.True(t, res.Stream)
	require.False(t, ok) // 该流末块无 usage
	body := rec.Body.String()
	require.Contains(t, body, "data: {\"delta\":\"hi\"}")
	require.Contains(t, body, "[DONE]")
	// 流式累积的响应体应含所有写出的行（供审计回放）。
	require.Contains(t, string(captured), "data: {\"delta\":\"hi\"}")
	require.Contains(t, string(captured), "[DONE]")
}

func TestRelayStreamUsage(t *testing.T) {
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(200)
		fl, _ := w.(http.Flusher)
		_, _ = w.Write([]byte("data: {\"choices\":[{\"delta\":{\"content\":\"hi\"}}]}\n\n"))
		fl.Flush()
		// 末尾含 usage 的块（include_usage 时上游 OpenAI 协议如此）
		_, _ = w.Write([]byte("data: {\"choices\":[],\"usage\":{\"prompt_tokens\":12,\"completion_tokens\":7,\"total_tokens\":19}}\n\n"))
		fl.Flush()
		_, _ = w.Write([]byte("data: [DONE]\n\n"))
		fl.Flush()
	}))
	defer up.Close()

	resp, err := dialDirect(OpenAIAdapter{}, up.URL, "sk-up", []byte(`{"model":"m","stream":true}`), 30)
	require.NoError(t, err)

	rec := httptest.NewRecorder()
	res, u, ok, captured := Relay(rec, resp, true, false, true)
	require.Equal(t, 200, res.Status)
	require.True(t, ok)
	require.Equal(t, 12, u.PromptTokens)
	require.Equal(t, 7, u.CompletionTokens)
	require.Contains(t, rec.Body.String(), "[DONE]")
	require.Contains(t, string(captured), "[DONE]") // 累积响应体含末块
}

func TestRelayStreamStripInjected(t *testing.T) {
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
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
	defer up.Close()
	resp, err := dialDirect(OpenAIAdapter{}, up.URL, "sk-up", []byte(`{"model":"m","stream":true}`), 30)
	require.NoError(t, err)

	rec := httptest.NewRecorder()
	// stripInjected=true：提取 usage 计费，但客户端不应看到 usage 块
	res, u, ok, captured := Relay(rec, resp, true, true, true)
	require.Equal(t, 200, res.Status)
	require.True(t, ok, "内部仍提取到 usage 供计费")
	require.Equal(t, 12, u.PromptTokens)
	body := rec.Body.String()
	require.Contains(t, body, `"content":"hi"`)
	require.NotContains(t, body, `"usage"`, "客户端不应看到注入产生的 usage 块")
	require.Contains(t, body, "[DONE]")
	require.NotContains(t, string(captured), `"usage"`, "审计客户端视角也不含 usage 块")
}

func TestInjectStreamUsage(t *testing.T) {
	// 无 stream_options：注入整个对象
	var m map[string]any
	require.NoError(t, json.Unmarshal(injectStreamUsage([]byte(`{"model":"m","stream":true}`)), &m))
	so := m["stream_options"].(map[string]any)
	require.Equal(t, true, so["include_usage"])

	// 已有 stream_options 其他字段：合并保留
	require.NoError(t, json.Unmarshal(injectStreamUsage([]byte(`{"stream_options":{"foo":1}}`)), &m))
	so = m["stream_options"].(map[string]any)
	require.Equal(t, true, so["include_usage"])
	require.EqualValues(t, 1, so["foo"])

	// 已为 true：原样返回
	in := []byte(`{"stream_options":{"include_usage":true}}`)
	require.Equal(t, string(in), string(injectStreamUsage(in)))

	// 非 JSON：原样返回
	require.Equal(t, "xx", string(injectStreamUsage([]byte("xx"))))

	// stream_options 为 null / 非对象：强制改写为对象（曾放手不管 → 上游不回 usage → 按 0 计费）
	for _, so := range []string{`null`, `"x"`, `1`, `[]`} {
		require.NoError(t, json.Unmarshal(injectStreamUsage([]byte(`{"stream":true,"stream_options":`+so+`}`)), &m))
		obj, ok := m["stream_options"].(map[string]any)
		require.True(t, ok, "stream_options=%s 须被改写为对象", so)
		require.Equal(t, true, obj["include_usage"])
	}
}

// stream 判定按精确键名、只认布尔 true；非布尔由 validStreamField 拒绝。
func TestIsStreamExactKey(t *testing.T) {
	require.True(t, parseInbound([]byte(`{"stream":true}`)).stream)
	require.True(t, parseInbound([]byte(`{"stream":true,"Stream":false}`)).stream, "大写同名键不得覆盖")
	require.False(t, parseInbound([]byte(`{"Stream":true}`)).stream, "只认精确键名")
	require.False(t, parseInbound([]byte(`{"stream":"true"}`)).stream)
	require.False(t, parseInbound([]byte(`{bad`)).stream)

	for _, ok := range []string{`{}`, `{"stream":true}`, `{"stream":false}`, `{"stream":null}`, `{"Stream":"x"}`} {
		require.True(t, parseInbound([]byte(ok)).streamOK, ok)
	}
	for _, bad := range []string{`{"stream":"true"}`, `{"stream":1}`, `{"stream":{}}`, `{"stream":[]}`} {
		require.False(t, parseInbound([]byte(bad)).streamOK, bad)
	}
}

// 入口 modelOf 与出站 rewriteBody 必须同一解析口径：超大数字、键名大小写都不得造成分歧。
func TestModelOfConsistentWithRewrite(t *testing.T) {
	huge := []byte(`{"model":"gpt-4","n":1e999}`)
	require.Equal(t, "gpt-4", parseInbound(huge).model)
	out := rewriteBody(huge, bodyRewrite{model: "gpt-4o"})
	require.Contains(t, string(out), `"model":"gpt-4o"`, "超大数字不得让改写失败而原样发出")
	require.Contains(t, string(out), `1e999`, "数字按原文保留")

	// struct 解码大小写不敏感会把 MODEL 当 model；统一用 map 后只认精确键名
	require.Equal(t, "a", parseInbound([]byte(`{"model":"a","MODEL":"b"}`)).model)

	for _, bad := range []string{`[]`, `null`, `"s"`, `{"model":"a"}{"model":"b"}`, `{"model":"a"} x`, `{bad`} {
		require.Equal(t, "", parseInbound([]byte(bad)).model, "非法请求体须读不出 model: %s", bad)
	}
	require.Equal(t, "a", parseInbound([]byte(" \t{\"model\":\"a\"}\r\n ")).model, "首尾空白合法")
}

func TestRemoveStreamUsage(t *testing.T) {
	var m map[string]any
	// 移除 include_usage 后 stream_options 为空 → 整键删除
	require.NoError(t, json.Unmarshal(removeStreamUsage([]byte(`{"model":"m","stream_options":{"include_usage":true}}`)), &m))
	require.NotContains(t, m, "stream_options")

	// 保留 stream_options 的其他字段
	require.NoError(t, json.Unmarshal(removeStreamUsage([]byte(`{"stream_options":{"include_usage":true,"foo":1}}`)), &m))
	so := m["stream_options"].(map[string]any)
	require.NotContains(t, so, "include_usage")
	require.EqualValues(t, 1, so["foo"])

	// 无 include_usage / 无 stream_options / 非 JSON：原样返回
	require.Equal(t, `{"model":"m"}`, string(removeStreamUsage([]byte(`{"model":"m"}`))))
	require.Equal(t, `{"stream_options":{"foo":1}}`, string(removeStreamUsage([]byte(`{"stream_options":{"foo":1}}`))))
	require.Equal(t, "xx", string(removeStreamUsage([]byte("xx"))))
}

func TestClientWantsStreamUsage(t *testing.T) {
	require.True(t, parseInbound([]byte(`{"stream_options":{"include_usage":true}}`)).wantsUsage)
	require.False(t, parseInbound([]byte(`{"stream_options":{"include_usage":false}}`)).wantsUsage)
	require.False(t, parseInbound([]byte(`{"stream":true}`)).wantsUsage)
	require.False(t, parseInbound([]byte("xx")).wantsUsage)
}

func TestIsUsageOnlyChunk(t *testing.T) {
	require.True(t, isUsageOnlyChunk([]byte(`{"choices":[],"usage":{"prompt_tokens":1}}`)))
	require.False(t, isUsageOnlyChunk([]byte(`{"choices":[{"delta":{"content":"hi"}}]}`)))
	require.False(t, isUsageOnlyChunk([]byte(`{"choices":[]}`)))
	require.False(t, isUsageOnlyChunk([]byte("xx")))
}

// TestRewriteBodySinglePass 单趟合并：model 改写 + thinking 剥离 + include_usage 注入一次完成。
func TestRewriteBodySinglePass(t *testing.T) {
	in := []byte(`{"model":"alias-x","thinking":{"type":"enabled"},"stream":true,"messages":[]}`)
	out := rewriteBody(in, bodyRewrite{model: "gpt-4o", stripThinking: true, streamUsage: usageInject})
	var m map[string]any
	require.NoError(t, json.Unmarshal(out, &m))
	require.Equal(t, "gpt-4o", m["model"])
	require.NotContains(t, m, "thinking")
	so, ok := m["stream_options"].(map[string]any)
	require.True(t, ok)
	require.Equal(t, true, so["include_usage"])
}

// TestRewriteBodyNoChangeZeroCopy 无任何变更时返回原切片（零拷贝，不重排序 JSON）。
func TestRewriteBodyNoChangeZeroCopy(t *testing.T) {
	in := []byte(`{"model":"gpt-4o","messages":[]}`)
	out := rewriteBody(in, bodyRewrite{model: "gpt-4o", stripThinking: true, streamUsage: usageKeep})
	require.Equal(t, fmt.Sprintf("%p", in), fmt.Sprintf("%p", out))
}

// TestRewriteBodyRemoveUsage 移除 include_usage；stream_options 变空时整体删除（原 removeStreamUsage 语义）。
func TestRewriteBodyRemoveUsage(t *testing.T) {
	in := []byte(`{"model":"m","stream_options":{"include_usage":true}}`)
	out := rewriteBody(in, bodyRewrite{streamUsage: usageRemove})
	var m map[string]any
	require.NoError(t, json.Unmarshal(out, &m))
	require.NotContains(t, m, "stream_options")
}

// TestRewriteBodyInvalidJSON 解析失败原样返回（防御语义与旧函数一致）。
func TestRewriteBodyInvalidJSON(t *testing.T) {
	in := []byte(`{invalid`)
	out := rewriteBody(in, bodyRewrite{model: "x", streamUsage: usageInject})
	require.Equal(t, in, out)
}

// TestStreamUsageMode include_usage 决策表。
func TestStreamUsageMode(t *testing.T) {
	require.Equal(t, usageInject, streamUsageMode("openai", true, false))
	require.Equal(t, usageRemove, streamUsageMode("openai", true, true))
	require.Equal(t, usageKeep, streamUsageMode("openai", false, false))
	require.Equal(t, usageKeep, streamUsageMode("anthropic", true, false))
}

// TestRelayCaptureGate capture=false 时流式透传不攒 captured（审计关闭的 App 不再白攒内存），
// 客户端收流与 usage 旁路提取不受影响。
func TestRelayCaptureGate(t *testing.T) {
	sse := "data: {\"choices\":[{\"delta\":{\"content\":\"hi\"}}]}\n\n" +
		"data: {\"choices\":[],\"usage\":{\"prompt_tokens\":1,\"completion_tokens\":2,\"total_tokens\":3}}\n\n" +
		"data: [DONE]\n\n"
	mk := func() *http.Response {
		return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": []string{"text/event-stream"}},
			Body: io.NopCloser(strings.NewReader(sse))}
	}

	// capture=true：captured 为完整写出流（既有行为）
	w1 := httptest.NewRecorder()
	_, u1, ok1, cap1 := Relay(w1, mk(), true, false, true)
	require.True(t, ok1, "capture=true 应照常提取 usage")
	require.Equal(t, 3, u1.TotalTokens)
	require.NotEmpty(t, cap1, "capture=true 应返回捕获体")
	require.Equal(t, w1.Body.String(), string(cap1), "captured 应与客户端写出一致")

	// capture=false：客户端照收、usage 照提，captured 为 nil
	w2 := httptest.NewRecorder()
	_, u2, ok2, cap2 := Relay(w2, mk(), true, false, false)
	require.True(t, ok2, "capture=false 不应影响 usage 旁路提取")
	require.Equal(t, 3, u2.TotalTokens)
	require.Nil(t, cap2, "capture=false 不应攒捕获体")
	require.Equal(t, w1.Body.String(), w2.Body.String(), "客户端可见流不应受门控影响")
}

// rewriteInbound 复用入口解析结果：多个候选依次改写时不得互相污染（顶层与嵌套 stream_options 都不能原地改）。
func TestRewriteInboundDoesNotMutateShared(t *testing.T) {
	in := parseInbound([]byte(`{"model":"alias","thinking":{},"stream":true,"stream_options":{"foo":1}}`))
	a := rewriteInbound(in, bodyRewrite{model: "m1", stripThinking: true, streamUsage: usageInject})
	b := rewriteInbound(in, bodyRewrite{model: "m2", streamUsage: usageKeep})

	require.Equal(t, "alias", in.obj["model"], "共享对象顶层不得被改")
	require.Contains(t, in.obj, "thinking")
	require.NotContains(t, in.obj["stream_options"].(map[string]any), "include_usage", "嵌套对象不得被改")

	var ma, mb map[string]any
	require.NoError(t, json.Unmarshal(a, &ma))
	require.NoError(t, json.Unmarshal(b, &mb))
	require.Equal(t, "m1", ma["model"])
	require.NotContains(t, ma, "thinking")
	require.Equal(t, true, ma["stream_options"].(map[string]any)["include_usage"])
	require.Equal(t, "m2", mb["model"])
	require.Contains(t, mb, "thinking", "第二个候选不剥离 thinking")
	require.NotContains(t, mb["stream_options"].(map[string]any), "include_usage", "第一个候选的注入不得泄漏到第二个")

	// 无变更零拷贝
	same := rewriteInbound(in, bodyRewrite{model: "alias"})
	require.Equal(t, fmt.Sprintf("%p", in.raw), fmt.Sprintf("%p", same))
}
