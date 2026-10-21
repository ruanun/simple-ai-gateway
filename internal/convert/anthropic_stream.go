package convert

import (
	"encoding/json"
	"fmt"
	"io"

	"github.com/ruanun/simple-ai-gateway/internal/convert/ir"
)

// NewStreamDecoder 返回 anthropic 事件 SSE → ir 事件的解码器。
func (AnthropicCodec) NewStreamDecoder(r io.Reader) StreamDecoder {
	return &anStreamDecoder{s: newSSEScanner(r), toolOrdinal: map[int]int{}}
}

// anStreamDecoder anthropic 块索引与 ir 工具序号解耦：
// anthropic index 计数含文本块（如 text=0, tool=1），ir 工具序号只数工具（0 起），
// 与 openai tool_calls index 语义对齐。
type anStreamDecoder struct {
	s           *sseScanner
	toolOrdinal map[int]int // anthropic 块索引 → ir 工具序号
	nextToolOrd int
}

func (d *anStreamDecoder) Next() (*ir.StreamEvent, error) {
	for {
		event, data, err := d.s.next()
		if err != nil {
			// 扫描器错误原样透传：io.EOF 表示流正常读尽，其余为真实读取失败
			return nil, err
		}
		switch event {
		case "message_start":
			var p struct {
				Message struct {
					ID    string `json:"id"`
					Model string `json:"model"`
					Usage *struct {
						InputTokens              int `json:"input_tokens"`
						CacheCreationInputTokens int `json:"cache_creation_input_tokens"`
						CacheReadInputTokens     int `json:"cache_read_input_tokens"`
					} `json:"usage"`
				} `json:"message"`
			}
			if json.Unmarshal(data, &p) != nil {
				return nil, fmt.Errorf("convert: 解析 message_start 失败: %s", truncate(data, 200))
			}
			ev := &ir.StreamEvent{Type: "start", ID: p.Message.ID, Model: p.Message.Model}
			if p.Message.Usage != nil {
				ev.Usage.InputTokens = p.Message.Usage.InputTokens
				ev.Usage.CacheReadTokens = p.Message.Usage.CacheReadInputTokens
				ev.Usage.CacheCreationTokens = p.Message.Usage.CacheCreationInputTokens
				ev.HasUsage = true
			}
			return ev, nil
		case "content_block_start":
			var p struct {
				Index        int `json:"index"`
				ContentBlock struct {
					Type string `json:"type"`
					ID   string `json:"id"`
					Name string `json:"name"`
				} `json:"content_block"`
			}
			if json.Unmarshal(data, &p) != nil {
				return nil, fmt.Errorf("convert: 解析 content_block_start 失败: %s", truncate(data, 200))
			}
			if p.ContentBlock.Type == "tool_use" {
				ord := d.nextToolOrd
				d.nextToolOrd++
				d.toolOrdinal[p.Index] = ord
				return &ir.StreamEvent{Type: "tool_start", Index: ord,
					ToolID: p.ContentBlock.ID, ToolName: p.ContentBlock.Name}, nil
			}
			// 文本块开始无需事件（openai 侧文本没有显式 start）
		case "content_block_delta":
			var p struct {
				Index int `json:"index"`
				Delta struct {
					Type        string `json:"type"`
					Text        string `json:"text"`
					Thinking    string `json:"thinking"`
					PartialJSON string `json:"partial_json"`
				} `json:"delta"`
			}
			if json.Unmarshal(data, &p) != nil {
				return nil, fmt.Errorf("convert: 解析 content_block_delta 失败: %s", truncate(data, 200))
			}
			switch p.Delta.Type {
			case "text_delta":
				return &ir.StreamEvent{Type: "text_delta", Text: p.Delta.Text}, nil
			case "thinking_delta":
				return &ir.StreamEvent{Type: "reasoning_delta", Text: p.Delta.Thinking}, nil
			case "input_json_delta":
				// comma-ok 防御：未见 tool_use 起始的孤儿增量丢弃，
				// 否则 map 缺省 0 会把畸形流入参污染到第一个工具
				if ord, ok := d.toolOrdinal[p.Index]; ok {
					return &ir.StreamEvent{Type: "tool_delta", Index: ord,
						ArgsDelta: p.Delta.PartialJSON}, nil
				}
			}
			// thinking_delta 等未知增量 / 孤儿 tool_delta：丢弃继续
		case "message_delta":
			var p struct {
				Delta struct {
					StopReason string `json:"stop_reason"`
				} `json:"delta"`
				Usage *struct {
					OutputTokens int `json:"output_tokens"`
				} `json:"usage"`
			}
			if json.Unmarshal(data, &p) != nil {
				return nil, fmt.Errorf("convert: 解析 message_delta 失败: %s", truncate(data, 200))
			}
			ev := &ir.StreamEvent{Type: "finish", StopReason: anStopToIR(p.Delta.StopReason)}
			if p.Usage != nil {
				ev.Usage.OutputTokens = p.Usage.OutputTokens
				ev.HasUsage = true
			}
			return ev, nil
		case "message_stop":
			return &ir.StreamEvent{Type: "done"}, nil
		case "error":
			return nil, fmt.Errorf("convert: 上游流错误: %s", truncate(data, 200))
		}
		// ping / content_block_stop / 未知事件：继续
	}
}

// NewStreamEncoder 返回 ir 事件 → anthropic 事件 SSE 的编码器。
func (AnthropicCodec) NewStreamEncoder(w io.Writer) StreamEncoder {
	return &anStreamEncoder{w: w, openIndex: -1, toolIndex: map[int]int{}}
}

// anStreamEncoder 块索引状态机：文本/工具增量按需开块，切换类型先关旧块；
// finish 事件缓冲（openai 的 usage 末块在 finish 之后才到），done 时统一写
// content_block_stop + message_delta(stop_reason+usage) + message_stop。
type anStreamEncoder struct {
	w          io.Writer
	started    bool   // 是否已写出 message_start
	openIndex  int    // 当前打开的块索引；-1=无
	openKind   string // 打开块的类型："text"|"thinking"|"tool"（openIndex<0 时无意义）
	nextIndex  int
	toolIndex  map[int]int // ir 工具序号 → anthropic 块索引
	inTokens   int
	outTokens  int
	stopReason string
}

// writeMessageStart 写 message_start 帧并置 started（start 分支与 done 兜底共用）。
func (e *anStreamEncoder) writeMessageStart(id, model string) error {
	e.started = true
	return e.frame("message_start", map[string]any{"type": "message_start", "message": map[string]any{
		"id": idOr(id, "msg_convert"), "type": "message", "role": "assistant",
		"model": model, "content": []any{}, "stop_reason": nil, "stop_sequence": nil,
		"usage": map[string]any{"input_tokens": e.inTokens, "output_tokens": 0},
	}})
}

// frame 写一个 "event: X\ndata: {...}\n\n" 帧。
func (e *anStreamEncoder) frame(event string, v map[string]any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	_, err = fmt.Fprintf(e.w, "event: %s\ndata: %s\n\n", event, b)
	return err
}

func (e *anStreamEncoder) closeOpenBlock() error {
	if e.openIndex < 0 {
		return nil
	}
	idx := e.openIndex
	e.openIndex = -1
	return e.frame("content_block_stop", map[string]any{"type": "content_block_stop", "index": idx})
}

func (e *anStreamEncoder) Write(ev *ir.StreamEvent) error {
	switch ev.Type {
	case "start":
		if ev.HasUsage {
			e.inTokens = ev.Usage.InputTokens
		}
		return e.writeMessageStart(ev.ID, ev.Model)
	case "text_delta":
		if e.openIndex < 0 || e.openKind != "text" {
			if err := e.closeOpenBlock(); err != nil {
				return err
			}
			idx := e.nextIndex
			e.nextIndex++
			e.openIndex, e.openKind = idx, "text"
			if err := e.frame("content_block_start", map[string]any{"type": "content_block_start",
				"index": idx, "content_block": map[string]any{"type": "text", "text": ""}}); err != nil {
				return err
			}
		}
		return e.frame("content_block_delta", map[string]any{"type": "content_block_delta",
			"index": e.openIndex, "delta": map[string]any{"type": "text_delta", "text": ev.Text}})
	case "reasoning_delta":
		// openai 推理模型（reasoning_content 增量）→ anthropic thinking 块。
		// 转换场景无法生成签名，不发 signature_delta（客户端对缺签名的 thinking 块普遍容忍）。
		if e.openIndex < 0 || e.openKind != "thinking" {
			if err := e.closeOpenBlock(); err != nil {
				return err
			}
			idx := e.nextIndex
			e.nextIndex++
			e.openIndex, e.openKind = idx, "thinking"
			if err := e.frame("content_block_start", map[string]any{"type": "content_block_start",
				"index": idx, "content_block": map[string]any{"type": "thinking", "thinking": ""}}); err != nil {
				return err
			}
		}
		return e.frame("content_block_delta", map[string]any{"type": "content_block_delta",
			"index": e.openIndex, "delta": map[string]any{"type": "thinking_delta", "thinking": ev.Text}})
	case "tool_start":
		if err := e.closeOpenBlock(); err != nil {
			return err
		}
		idx := e.nextIndex
		e.nextIndex++
		e.toolIndex[ev.Index] = idx
		e.openIndex, e.openKind = idx, "tool"
		return e.frame("content_block_start", map[string]any{"type": "content_block_start", "index": idx,
			"content_block": map[string]any{"type": "tool_use",
				"id": idOr(sanitizeAnthropicToolID(ev.ToolID), fmt.Sprintf("toolu_convert_%d", ev.Index)), "name": ev.ToolName, "input": map[string]any{}}})
	case "tool_delta":
		idx, ok := e.toolIndex[ev.Index]
		if !ok {
			return nil // 未见 tool_start 的孤儿增量：丢弃
		}
		return e.frame("content_block_delta", map[string]any{"type": "content_block_delta", "index": idx,
			"delta": map[string]any{"type": "input_json_delta", "partial_json": ev.ArgsDelta}})
	case "finish":
		e.stopReason = ev.StopReason
		if ev.HasUsage {
			e.outTokens = ev.Usage.OutputTokens
		}
		return nil // 缓冲到 done
	case "usage":
		if ev.HasUsage {
			e.inTokens, e.outTokens = ev.Usage.InputTokens, ev.Usage.OutputTokens
		}
		return nil
	case "done":
		// 兜底：上游空流（只有 [DONE]）未发过 start，先合成最小 message_start，
		// 否则 anthropic 客户端收到无 message_start 的孤儿终止帧会报错
		if !e.started {
			if err := e.writeMessageStart("", ""); err != nil {
				return err
			}
		}
		if err := e.closeOpenBlock(); err != nil {
			return err
		}
		if err := e.frame("message_delta", map[string]any{"type": "message_delta",
			"delta": map[string]any{"stop_reason": irToAnStop(e.stopReason), "stop_sequence": nil},
			"usage": map[string]any{"input_tokens": e.inTokens, "output_tokens": e.outTokens}}); err != nil {
			return err
		}
		return e.frame("message_stop", map[string]any{"type": "message_stop"})
	}
	return nil
}

// WriteError 流中途失败：anthropic 协议错误事件。
func (e *anStreamEncoder) WriteError(msg string) error {
	return e.frame("error", map[string]any{"type": "error",
		"error": map[string]any{"type": "api_error", "message": msg}})
}
