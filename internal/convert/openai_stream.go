package convert

import (
	"bytes"
	"fmt"
	"io"
	"time"

	"encoding/json"

	"github.com/ruanun/simple-ai-gateway/internal/convert/ir"
)

// NewStreamDecoder 返回 openai chunk SSE → ir 事件的解码器。
func (OpenAICodec) NewStreamDecoder(r io.Reader) StreamDecoder {
	return &oaStreamDecoder{s: newSSEScanner(r)}
}

// oaStreamDecoder 一个 chunk 可能承载多个语义事件（首块=start+增量），用 queue 逐个吐出。
type oaStreamDecoder struct {
	s       *sseScanner
	started bool
	queue   []*ir.StreamEvent
}

func (d *oaStreamDecoder) Next() (*ir.StreamEvent, error) {
	for {
		if len(d.queue) > 0 {
			ev := d.queue[0]
			d.queue = d.queue[1:]
			return ev, nil
		}
		_, data, err := d.s.next()
		if err != nil {
			// 扫描器错误原样透传：io.EOF 表示流正常读尽，其余为真实读取失败
			return nil, err
		}
		if bytes.Equal(data, []byte("[DONE]")) {
			return &ir.StreamEvent{Type: "done"}, nil
		}
		var p struct {
			ID      string `json:"id"`
			Model   string `json:"model"`
			Choices []struct {
				Delta struct {
					Content          string `json:"content"`
					ReasoningContent string `json:"reasoning_content"` // DeepSeek-R1 等 openai 兼容推理模型的思考增量
					ToolCalls        []struct {
						Index    int    `json:"index"`
						ID       string `json:"id"`
						Function struct {
							Name      string `json:"name"`
							Arguments string `json:"arguments"`
						} `json:"function"`
					} `json:"tool_calls"`
				} `json:"delta"`
				FinishReason *string `json:"finish_reason"`
			} `json:"choices"`
			Usage *struct {
				PromptTokens        int `json:"prompt_tokens"`
				CompletionTokens    int `json:"completion_tokens"`
				PromptTokensDetails struct {
					CachedTokens int `json:"cached_tokens"`
				} `json:"prompt_tokens_details"`
				CompletionTokensDetails struct {
					ReasoningTokens int `json:"reasoning_tokens"`
				} `json:"completion_tokens_details"`
			} `json:"usage"`
		}
		if json.Unmarshal(data, &p) != nil {
			return nil, fmt.Errorf("convert: 解析 openai chunk 失败: %s", truncate(data, 200))
		}
		if !d.started {
			d.started = true
			d.queue = append(d.queue, &ir.StreamEvent{Type: "start", ID: p.ID, Model: p.Model})
		}
		if p.Usage != nil {
			d.queue = append(d.queue, &ir.StreamEvent{Type: "usage", HasUsage: true,
				Usage: ir.Usage{
					InputTokens:     p.Usage.PromptTokens,
					OutputTokens:    p.Usage.CompletionTokens,
					CacheReadTokens: p.Usage.PromptTokensDetails.CachedTokens,
					ReasoningTokens: p.Usage.CompletionTokensDetails.ReasoningTokens,
				}})
		}
		if len(p.Choices) > 0 {
			ch := p.Choices[0]
			// 思考增量先于正文（推理模型语义顺序），否则 openai→anthropic 转换丢全部思考内容
			if ch.Delta.ReasoningContent != "" {
				d.queue = append(d.queue, &ir.StreamEvent{Type: "reasoning_delta", Text: ch.Delta.ReasoningContent})
			}
			if ch.Delta.Content != "" {
				d.queue = append(d.queue, &ir.StreamEvent{Type: "text_delta", Text: ch.Delta.Content})
			}
			for _, tc := range ch.Delta.ToolCalls {
				if tc.ID != "" || tc.Function.Name != "" {
					d.queue = append(d.queue, &ir.StreamEvent{Type: "tool_start", Index: tc.Index,
						ToolID: tc.ID, ToolName: tc.Function.Name})
				}
				if tc.Function.Arguments != "" {
					d.queue = append(d.queue, &ir.StreamEvent{Type: "tool_delta", Index: tc.Index,
						ArgsDelta: tc.Function.Arguments})
				}
			}
			if ch.FinishReason != nil && *ch.FinishReason != "" {
				d.queue = append(d.queue, &ir.StreamEvent{Type: "finish", StopReason: oaFinishToIR(*ch.FinishReason)})
			}
		}
		// 本 chunk 无语义事件（如仅 role 增量）→ 继续读下一条
	}
}

// truncate 错误信息截断，防日志爆量。
func truncate(b []byte, n int) string {
	if len(b) <= n {
		return string(b)
	}
	return string(b[:n]) + "..."
}

// NewStreamEncoder 返回 ir 事件 → openai chunk SSE 的编码器。
func (OpenAICodec) NewStreamEncoder(w io.Writer) StreamEncoder {
	return &oaStreamEncoder{w: w}
}

// oaStreamEncoder usage 组装：anthropic 上游时 input 来自 start、output 来自 finish；
// 未来其他协议若发 usage 整块事件则整体覆盖。usage 附加块在 done 时写出（include_usage 语义）。
type oaStreamEncoder struct {
	w             io.Writer
	id, model     string
	inTokens      int
	outTokens     int
	cacheRead     int
	cacheCreation int
	haveIn        bool
	haveOut       bool
}

// chunk 写一个 chat.completion.chunk SSE 帧。emptyChoices=true 用于 usage 附加块。
func (e *oaStreamEncoder) chunk(delta map[string]any, finish any, usage map[string]any, emptyChoices bool) error {
	body := map[string]any{
		"id":      idOr(e.id, "chatcmpl-convert"),
		"object":  "chat.completion.chunk",
		"created": time.Now().Unix(),
		"model":   e.model,
	}
	if emptyChoices {
		body["choices"] = []any{}
	} else {
		body["choices"] = []map[string]any{{"index": 0, "delta": delta, "finish_reason": finish}}
	}
	if usage != nil {
		body["usage"] = usage
	}
	b, err := json.Marshal(body)
	if err != nil {
		return err
	}
	_, err = fmt.Fprintf(e.w, "data: %s\n\n", b)
	return err
}

func (e *oaStreamEncoder) Write(ev *ir.StreamEvent) error {
	switch ev.Type {
	case "start":
		e.id, e.model = ev.ID, ev.Model
		if ev.HasUsage {
			e.inTokens, e.haveIn = ev.Usage.InputTokens, true
			e.cacheRead, e.cacheCreation = ev.Usage.CacheReadTokens, ev.Usage.CacheCreationTokens
		}
		return e.chunk(map[string]any{"role": "assistant", "content": ""}, nil, nil, false)
	case "text_delta":
		return e.chunk(map[string]any{"content": ev.Text}, nil, nil, false)
	case "reasoning_delta":
		return e.chunk(map[string]any{"reasoning_content": ev.Text}, nil, nil, false)
	case "tool_start":
		return e.chunk(map[string]any{"tool_calls": []map[string]any{{
			"index": ev.Index, "id": idOr(ev.ToolID, fmt.Sprintf("call_convert_%d", ev.Index)), "type": "function",
			"function": map[string]any{"name": ev.ToolName, "arguments": ""}}}}, nil, nil, false)
	case "tool_delta":
		return e.chunk(map[string]any{"tool_calls": []map[string]any{{
			"index": ev.Index, "function": map[string]any{"arguments": ev.ArgsDelta}}}}, nil, nil, false)
	case "finish":
		if ev.HasUsage {
			e.outTokens, e.haveOut = ev.Usage.OutputTokens, true
		}
		return e.chunk(map[string]any{}, irToOAFinish(ev.StopReason), nil, false)
	case "usage":
		if ev.HasUsage {
			e.inTokens, e.outTokens = ev.Usage.InputTokens, ev.Usage.OutputTokens
			e.haveIn, e.haveOut = true, true
		}
		return nil
	case "done":
		if e.haveIn || e.haveOut {
			prompt := e.inTokens + e.cacheRead + e.cacheCreation
			u := map[string]any{
				"prompt_tokens":     prompt,
				"completion_tokens": e.outTokens,
				"total_tokens":      prompt + e.outTokens,
			}
			if e.cacheRead > 0 {
				u["prompt_tokens_details"] = map[string]any{"cached_tokens": e.cacheRead}
			}
			if err := e.chunk(nil, nil, u, true); err != nil {
				return err
			}
		}
		_, err := io.WriteString(e.w, "data: [DONE]\n\n")
		return err
	}
	return nil
}

// WriteError 流中途失败：openai 客户端惯例发 error 对象块后收尾 [DONE]。
func (e *oaStreamEncoder) WriteError(msg string) error {
	b, _ := json.Marshal(map[string]any{"error": map[string]any{"message": msg, "type": "upstream_error"}})
	if _, err := fmt.Fprintf(e.w, "data: %s\n\n", b); err != nil {
		return err
	}
	_, err := io.WriteString(e.w, "data: [DONE]\n\n")
	return err
}
