package gateway

import (
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/ruanun/simple-ai-gateway/internal/audit"
	"github.com/ruanun/simple-ai-gateway/internal/billing"
	"github.com/ruanun/simple-ai-gateway/internal/convert"
)

// RelayConvert 跨协议回写（P12c）：上游 from 协议响应 → 客户端 to 协议。
// usage 一律在上游侧按 from 协议原生解析（与透传路径同口径，转换不影响计费）。
// 返回的 []byte 是实际写给客户端的（转换后）响应体，供审计做客户端视角回放。
//
// 错误语义：
//   - 返回 error 非 nil 时保证未向客户端写出任何字节，调用方可安全 failover 下一候选；
//   - 流式已写出字节后失败（含中途坏帧、上游未发 done/message_stop 就干净断连的截断流）：
//     向客户端发协议错误帧终止流并返回 error=nil（不 failover，防流拼接腐化）。
func RelayConvert(w http.ResponseWriter, resp *http.Response, stream bool, from, to string, capture bool, dialStart time.Time) (*Result, billing.Usage, bool, []byte, error) {
	defer resp.Body.Close()

	// 上游错误（非重试型 4xx，如 400/401/404）：只转错误信封，透传状态码，不走 ir。
	if resp.StatusCode >= 400 {
		body, _ := io.ReadAll(resp.Body)
		out := convert.Error(body, to)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(resp.StatusCode)
		// 客户端可见副本脱敏凭证；返回未脱敏的 out 供审计做客户端视角回放。
		_, _ = w.Write(scrubResp(resp, out))
		return &Result{Status: resp.StatusCode}, billing.Usage{}, false, out, nil
	}

	if !stream {
		body, _ := io.ReadAll(resp.Body)
		u, uok := parseUsageByProtocol(body, from)
		out, err := convert.Response(body, from, to)
		if err != nil {
			return nil, billing.Usage{}, false, nil, err // 未写出任何字节 → 可 failover
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(resp.StatusCode)
		_, _ = w.Write(out)
		return &Result{Status: resp.StatusCode}, u, uok, out, nil
	}

	fromCodec, ok := convert.Get(from)
	if !ok {
		return nil, billing.Usage{}, false, nil, fmt.Errorf("convert: 未知上游协议 %q", from)
	}
	toCodec, ok := convert.Get(to)
	if !ok {
		return nil, billing.Usage{}, false, nil, fmt.Errorf("convert: 未知客户端协议 %q", to)
	}

	fl, _ := w.(http.Flusher)
	// capture=false 时捕获落点为 io.Discard（与 relaySSE 的门控语义同款：不攒捕获体、恒返回 nil）。
	var captured *captureBuf
	sink := io.Writer(io.Discard)
	if capture {
		captured = newCaptureBuf()
		sink = captured
	}
	tw := &teeFlushWriter{w: w, buf: sink, fl: fl}
	dec := fromCodec.NewStreamDecoder(resp.Body)
	enc := toCodec.NewStreamEncoder(tw)

	var usage billing.Usage
	usageOK := false
	var firstTokenMs int64
	wrote := false
	doneSeen := false // 是否已收到上游正常收尾事件（anthropic message_stop / openai [DONE]）
	t0 := dialStart
	if t0.IsZero() {
		t0 = time.Now()
	}
	for {
		ev, err := dec.Next()
		if err == io.EOF {
			if doneSeen {
				break // 已收到正常收尾事件后的 EOF：流正常结束
			}
			if wrote {
				// 上游未发 done/message_stop 就干净断连 == 截断流：已写出字节不能 failover，
				// 与「中途坏帧」同处理——发协议错误帧终止流。
				_ = enc.WriteError("上游流提前结束")
			}
			break
		}
		if err != nil {
			if !wrote {
				return nil, billing.Usage{}, false, nil, err // 未写出 → failover
			}
			_ = enc.WriteError("上游流转换中断: " + err.Error())
			break // 已写出 → 终止流，不 failover
		}
		if ev.Type == "done" {
			doneSeen = true
		}
		// usage 上游原生口径：start(输入侧)/finish(输出侧)/usage(整块)
		if ev.HasUsage {
			switch ev.Type {
			case "start":
				// 归一为总量口径(P12d)：ir 保持 anthropic 原生语义，统一走 billing.NormalizeAnthropic；
				// output 此时未知传 0，CompletionTokens 由 finish 事件补、TotalTokens 收尾求和。
				n := billing.NormalizeAnthropic(ev.Usage.InputTokens, ev.Usage.CacheReadTokens, ev.Usage.CacheCreationTokens, 0)
				usage.PromptTokens, usage.CachedTokens, usage.CacheCreationTokens = n.PromptTokens, n.CachedTokens, n.CacheCreationTokens
				usageOK = true
			case "finish":
				usage.CompletionTokens = ev.Usage.OutputTokens
				usageOK = true
			case "usage":
				usage.PromptTokens, usage.CompletionTokens = ev.Usage.InputTokens, ev.Usage.OutputTokens
				usage.CachedTokens = ev.Usage.CacheReadTokens
				usage.ReasoningTokens = ev.Usage.ReasoningTokens
				usageOK = true
			}
		}
		if !wrote {
			w.Header().Set("Content-Type", "text/event-stream")
			w.WriteHeader(resp.StatusCode)
			firstTokenMs = time.Since(t0).Milliseconds()
			wrote = true // WriteHeader 即提交点：此后任何失败都不可 failover（头已发给客户端）
		}
		if werr := enc.Write(ev); werr != nil {
			break // 客户端断开等写错误：终止（头已提交，不 failover）
		}
	}
	if !wrote {
		// 上游一个事件都没产出（或首个事件前即 EOF）：按转换失败 failover
		return nil, billing.Usage{}, false, nil, fmt.Errorf("convert: 上游未产出任何流事件")
	}
	usage.TotalTokens = usage.PromptTokens + usage.CompletionTokens
	var capBytes []byte
	var overflow *audit.Overflow
	if captured != nil {
		capBytes, overflow = captured.result() // capture=false 恒返回 nil（契约与 relaySSE 一致）
	}
	return &Result{Status: resp.StatusCode, Stream: true, FirstTokenMs: firstTokenMs, CaptureOverflow: overflow}, usage, usageOK, capBytes, nil
}

// parseUsageByProtocol 按上游出站协议解析非流式响应 usage（转换路径计费与透传同口径）。
func parseUsageByProtocol(body []byte, proto string) (billing.Usage, bool) {
	if proto == "anthropic" {
		return billing.ParseAnthropicUsageJSON(body)
	}
	return billing.ParseUsageJSON(body)
}

// teeFlushWriter 把编码器输出同时写客户端与捕获落点（关捕获时由调用方传 io.Discard），
// 每次写后 flush（SSE 实时性）。纯 tee，不感知上层审计门控。
type teeFlushWriter struct {
	w   io.Writer
	buf io.Writer
	fl  http.Flusher
}

func (t *teeFlushWriter) Write(p []byte) (int, error) {
	_, _ = t.buf.Write(p)
	n, err := t.w.Write(p)
	if t.fl != nil {
		t.fl.Flush()
	}
	return n, err
}
