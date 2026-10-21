package gateway

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"maps"
	"net/http"
	"time"

	"github.com/ruanun/simple-ai-gateway/internal/audit"
	"github.com/ruanun/simple-ai-gateway/internal/billing"
)

type Result struct {
	Status       int
	Stream       bool
	FirstTokenMs int64 // 流式首 Token 延迟(ms)，非流式为 0
	// CaptureOverflow 流式审计捕获超出在途上限时的全文元数据（第四返回值此时仅为前缀）；未溢出为 nil。
	CaptureOverflow *audit.Overflow
}

// DialWith 用传入的 http.Client 向上游发起请求并返回响应，不向客户端写任何东西。
// client 由调用方按是否走代理 / 是否流式构造（见 proxydial.BuildClient / clientForInfo）。
//
// ctx 承载「首字节预算」（见 firstByteBudget）：调用方必须在拿到响应头后立即停掉预算定时器，
// 使 body 传输不受预算约束——否则流式 SSE 会在预算到期时被截断。
// 预算走 ctx 而非 Transport 字段，是因为 Transport 按代理 ID 全局缓存复用、直连更是共享
// http.DefaultTransport，ResponseHeaderTimeout 这类字段无法按请求设置。
func DialWith(ctx context.Context, client *http.Client, a Adapter, spec HeaderSpec, body []byte) (*http.Response, error) {
	req, err := a.BuildRequest(spec, body)
	if err != nil {
		return nil, err
	}
	return client.Do(req.WithContext(withSpecSecrets(ctx, spec)))
}

// relayBuffered 承接 Relay / RelayAnthropic 共有的前半段：Content-Type 回写 + WriteHeader 后，
// 处理两类需整体缓冲的分支（handled=true 表示已回写完毕，调用方直接返回）：
//   - 上游错误响应（4xx/5xx）：整体缓冲、脱敏凭证后回写，避免上游把 key 片段透传客户端；
//     返回原始 body 供审计（脱敏仅作用于客户端可见副本）。错误不作流式处理。
//   - 非流式正常响应：整读回写。
//
// usage 解析经 parse 注入（openai/anthropic 口径不同）。流式正常响应返回 handled=false，
// 由调用方各自做 SSE 透传。
func relayBuffered(w http.ResponseWriter, resp *http.Response, stream bool, parse func([]byte) (billing.Usage, bool)) (res *Result, u billing.Usage, uok bool, body []byte, handled bool) {
	if ct := resp.Header.Get("Content-Type"); ct != "" {
		w.Header().Set("Content-Type", ct)
	}
	w.WriteHeader(resp.StatusCode)
	if resp.StatusCode >= 400 {
		body, _ := io.ReadAll(resp.Body)
		_, _ = w.Write(scrubResp(resp, body))
		u, ok := parse(body)
		return &Result{Status: resp.StatusCode, Stream: false}, u, ok, body, true
	}
	if !stream {
		body, _ := io.ReadAll(resp.Body)
		_, _ = w.Write(body)
		u, ok := parse(body)
		return &Result{Status: resp.StatusCode, Stream: false}, u, ok, body, true
	}
	return nil, billing.Usage{}, false, nil, false
}

// relayT0 流式首 Token 计时起点：dialStart 变参有值且非零用它（从拨号起算），否则回退当前时间。
func relayT0(dialStart []time.Time) time.Time {
	if len(dialStart) > 0 && !dialStart[0].IsZero() {
		return dialStart[0]
	}
	return time.Now()
}

// Relay 把已选定的上游响应回写客户端（流式逐块 flush / 非流式整体读取再写），
// 同时旁路解析 token usage 并返回已写出的响应体（供审计回放）：
//   - 非流式：读完整 body → ParseUsageJSON，返回该 body。
//   - 流式：边读边写客户端，对每行 `data: {json}` 探测 usage，保留最后一个能解析到的块；
//     同时把所有写出的行累积到 buffer 一并返回。
//
// 解析不到 usage 时返回零值 Usage 与 ok=false。第四个返回值为已写出的完整响应体。
// stripInjected=true 时（客户端自身未请求 usage，但网关注入了 include_usage）：仍旁路提取 usage 计费，
// 但把「空 choices + usage」的注入收尾块从客户端流与 captured 中剥离，保持透传语义。
// capture=false 时不攒捕获体（第四返回值为 nil），供审计关闭的请求省内存。
func Relay(w http.ResponseWriter, resp *http.Response, stream, stripInjected, capture bool, dialStart ...time.Time) (*Result, billing.Usage, bool, []byte) {
	defer resp.Body.Close()
	if res, u, ok, body, handled := relayBuffered(w, resp, stream, billing.ParseUsageJSON); handled {
		return res, u, ok, body
	}

	var lastUsage billing.Usage
	found := false
	var firstTokenMs int64
	firstDataSeen := false
	t0 := relayT0(dialStart)
	captured, overflow := relaySSE(w, resp.Body, capture, func(trimmed []byte) bool {
		if !bytes.HasPrefix(trimmed, []byte("data:")) {
			return false
		}
		if !firstDataSeen {
			firstTokenMs = time.Since(t0).Milliseconds()
			firstDataSeen = true
		}
		payload := bytes.TrimSpace(trimmed[len("data:"):])
		if bytes.Equal(payload, []byte("[DONE]")) {
			return false
		}
		if u, ok := billing.ParseUsageJSON(payload); ok {
			lastUsage, found = u, true
			if stripInjected && isUsageOnlyChunk(payload) {
				return true // 客户端没要 usage：提取后剥离注入产生的用量块
			}
		}
		return false
	})
	return &Result{Status: resp.StatusCode, Stream: true, FirstTokenMs: firstTokenMs, CaptureOverflow: overflow}, lastUsage, found, captured
}

// RelayAnthropic 透传 Anthropic 上游响应并提取 usage(input_tokens/output_tokens)。
// 非流式:整体读→解析 usage→回写客户端。
// 流式:逐行转发 SSE + 从 message_start/message_delta 事件旁路提取 usage。
// capture=false 时不攒捕获体（第四返回值为 nil），供审计关闭的请求省内存。
func RelayAnthropic(w http.ResponseWriter, resp *http.Response, stream, capture bool, dialStart ...time.Time) (*Result, billing.Usage, bool, []byte) {
	defer resp.Body.Close()
	if res, u, ok, body, handled := relayBuffered(w, resp, stream, billing.ParseAnthropicUsageJSON); handled {
		return res, u, ok, body
	}

	// 流式透传:逐行原样转发 + 旁路提取 usage
	var usage billing.Usage
	usageOK := false
	var firstTokenMs int64
	firstDataSeen := false
	t0 := relayT0(dialStart)
	var lastEventType string
	captured, overflow := relaySSE(w, resp.Body, capture, func(trimmed []byte) bool {
		if bytes.HasPrefix(trimmed, []byte("event:")) {
			lastEventType = string(bytes.TrimSpace(trimmed[len("event:"):]))
		}
		if !bytes.HasPrefix(trimmed, []byte("data:")) {
			return false
		}
		if !firstDataSeen {
			firstTokenMs = time.Since(t0).Milliseconds()
			firstDataSeen = true
		}
		payload := bytes.TrimSpace(trimmed[len("data:"):])
		switch lastEventType {
		case "message_start":
			var ev struct {
				Message *struct {
					Usage *billing.AnthropicUsageFields `json:"usage"`
				} `json:"message"`
			}
			if json.Unmarshal(payload, &ev) == nil && ev.Message != nil && ev.Message.Usage != nil {
				mu := ev.Message.Usage
				// 归一为总量口径(P12d)：与 ParseAnthropicUsageJSON 同走 billing.NormalizeAnthropic；
				// output 此时未知传 0，CompletionTokens/TotalTokens 由 message_delta 与收尾补齐。
				n := billing.NormalizeAnthropic(mu.InputTokens, mu.CacheReadInputTokens, mu.CacheCreationInputTokens, 0)
				usage.PromptTokens, usage.CachedTokens, usage.CacheCreationTokens = n.PromptTokens, n.CachedTokens, n.CacheCreationTokens
				usageOK = true
			}
		case "message_delta":
			var ev struct {
				Usage *struct {
					OutputTokens int `json:"output_tokens"`
				} `json:"usage"`
			}
			if json.Unmarshal(payload, &ev) == nil && ev.Usage != nil {
				usage.CompletionTokens = ev.Usage.OutputTokens
				usageOK = true
			}
		}
		return false
	})
	usage.TotalTokens = usage.PromptTokens + usage.CompletionTokens
	return &Result{Status: resp.StatusCode, Stream: true, FirstTokenMs: firstTokenMs, CaptureOverflow: overflow}, usage, usageOK, captured
}

// 出站体 include_usage 处置：保持不动 / 注入 / 移除。
const (
	usageKeep int8 = iota
	usageInject
	usageRemove
)

// bodyRewrite 出站体单趟改写选项，rewriteBody 一次解析内按序应用：
// model 改写（别名/映射后按候选 c.Model，见 rewriteBody 注释）→ thinking 剥离 → include_usage 注入/移除。
type bodyRewrite struct {
	model         string
	stripThinking bool
	streamUsage   int8
}

// streamUsageMode openai 流式出站的 include_usage 决策：未关闭 → 注入逼上游回真实 usage；
// 渠道关闭(DisableStreamUsage) → 移除；非 openai 或非流式 → 不动。
func streamUsageMode(provType string, streamReq, disable bool) int8 {
	if provType != "openai" || !streamReq {
		return usageKeep
	}
	if disable {
		return usageRemove
	}
	return usageInject
}

// noop 是否无任何改写选项。
func (o bodyRewrite) noop() bool {
	return o.model == "" && !o.stripThinking && o.streamUsage == usageKeep
}

// rewriteInbound 基于入口已解析的对象改写出站体（派发主路径），每个候选只做一次 Marshal、不再解析。
// in.obj 为 nil 时原样返回：Prepare 已拒绝无法解析的请求体，此分支仅作防御。
func rewriteInbound(in inbound, o bodyRewrite) []byte {
	if o.noop() || in.obj == nil {
		return in.raw
	}
	return rewriteObj(in.raw, maps.Clone(in.obj), o)
}

// rewriteBody 解析字节再改写，用于未经入口解析的输入（如协议转换的产物）。解析失败原样返回。
func rewriteBody(body []byte, o bodyRewrite) []byte {
	if o.noop() {
		return body
	}
	m, err := decodeBodyObject(body)
	if err != nil {
		return body
	}
	return rewriteObj(body, m, o)
}

// rewriteObj 在 m 上按选项改写后序列化；无实际变更返回 orig（零拷贝、不重排 JSON 字段）。
// m 的顶层可被修改，嵌套的 stream_options 一律换新 map 而不原地改——rewriteInbound 传入的是
// 共享解析结果的浅拷贝，原地改嵌套对象会污染后续候选。
func rewriteObj(orig []byte, m map[string]any, o bodyRewrite) []byte {
	changed := false
	if o.model != "" {
		if cur, _ := m["model"].(string); cur != o.model {
			m["model"] = o.model
			changed = true
		}
	}
	if o.stripThinking {
		for _, k := range []string{"thinking", "reasoning", "reasoning_effort"} {
			if _, ok := m[k]; ok {
				delete(m, k)
				changed = true
			}
		}
	}
	switch o.streamUsage {
	case usageInject:
		// stream_options 缺失、为 null 或非对象时一律改写为对象：计费依赖上游回带 usage，
		// 若对 null/非对象放手不管，客户端带 "stream_options":null 即可让本次流式请求按 0 计费。
		so, _ := m["stream_options"].(map[string]any)
		if iu, _ := so["include_usage"].(bool); !iu {
			so = maps.Clone(so)
			if so == nil {
				so = map[string]any{}
			}
			so["include_usage"] = true
			m["stream_options"] = so
			changed = true
		}
	case usageRemove:
		so, ok := m["stream_options"].(map[string]any)
		if !ok {
			break
		}
		if _, has := so["include_usage"]; !has {
			break
		}
		so = maps.Clone(so)
		delete(so, "include_usage")
		if len(so) == 0 {
			delete(m, "stream_options")
		} else {
			m["stream_options"] = so
		}
		changed = true
	}
	if !changed {
		return orig
	}
	b, err := json.Marshal(m)
	if err != nil {
		return orig
	}
	return b
}

// stripThinkingFields 删除思考/推理字段（thinking/reasoning/reasoning_effort），
// 供渠道标记「不支持 thinking」时使用（单选项包装，语义见 rewriteBody）。
func stripThinkingFields(body []byte) []byte {
	return rewriteBody(body, bodyRewrite{stripThinking: true})
}

// injectStreamUsage 置 stream_options.include_usage=true（单选项包装，语义见 rewriteBody）。
func injectStreamUsage(body []byte) []byte {
	return rewriteBody(body, bodyRewrite{streamUsage: usageInject})
}

// removeStreamUsage 移除 stream_options.include_usage（单选项包装，语义见 rewriteBody）。
func removeStreamUsage(body []byte) []byte {
	return rewriteBody(body, bodyRewrite{streamUsage: usageRemove})
}

// isUsageOnlyChunk 判断一个 openai 流式 data 块是否为「空 choices + 有 usage」的收尾用量块
// （include_usage 注入后上游在 [DONE] 前发的最后一块）。用于剥离注入产生的 usage 块。
func isUsageOnlyChunk(payload []byte) bool {
	var p struct {
		Choices []json.RawMessage `json:"choices"`
		Usage   json.RawMessage   `json:"usage"`
	}
	if json.Unmarshal(payload, &p) != nil {
		return false
	}
	return len(p.Choices) == 0 && len(p.Usage) > 0
}
