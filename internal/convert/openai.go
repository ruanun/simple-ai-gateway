package convert

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/ruanun/simple-ai-gateway/internal/convert/ir"
)

// OpenAICodec OpenAI chat completions 协议编解码器。
type OpenAICodec struct{}

// oaToolCall openai 工具调用（请求 assistant 消息 / 响应 message 共用）。
type oaToolCall struct {
	ID       string `json:"id"`
	Type     string `json:"type"`
	Function struct {
		Name      string `json:"name"`
		Arguments string `json:"arguments"`
	} `json:"function"`
}

// DecodeRequest 解析 openai chat 请求为 ir。system/developer 消息抽为 System；
// role:tool 归一为 user 消息内 tool_result 块（连续 tool 消息并入同一条 user）。
func (OpenAICodec) DecodeRequest(body []byte) (*ir.Request, error) {
	var p struct {
		Model    string `json:"model"`
		Messages []struct {
			Role       string          `json:"role"`
			Content    json.RawMessage `json:"content"`
			ToolCalls  []oaToolCall    `json:"tool_calls"`
			ToolCallID string          `json:"tool_call_id"`
		} `json:"messages"`
		Tools []struct {
			Type     string `json:"type"`
			Function struct {
				Name        string          `json:"name"`
				Description string          `json:"description"`
				Parameters  json.RawMessage `json:"parameters"`
			} `json:"function"`
		} `json:"tools"`
		ToolChoice          json.RawMessage `json:"tool_choice"`
		MaxTokens           int             `json:"max_tokens"`
		MaxCompletionTokens int             `json:"max_completion_tokens"`
		Temperature         *float64        `json:"temperature"`
		TopP                *float64        `json:"top_p"`
		Stop                json.RawMessage `json:"stop"`
		Stream              bool            `json:"stream"`
	}
	if err := json.Unmarshal(body, &p); err != nil {
		return nil, fmt.Errorf("convert: 解析 openai 请求失败: %w", err)
	}
	if len(p.Messages) == 0 {
		return nil, fmt.Errorf("convert: openai 请求缺少 messages")
	}
	out := &ir.Request{Model: p.Model, MaxTokens: p.MaxTokens,
		Temperature: p.Temperature, TopP: p.TopP, Stream: p.Stream}
	if out.MaxTokens == 0 {
		out.MaxTokens = p.MaxCompletionTokens
	}
	out.Stop = decodeStringOrList(p.Stop)
	for _, t := range p.Tools {
		if t.Type != "" && t.Type != "function" {
			slog.Warn("convert: 丢弃不支持的 openai 工具类型", "type", t.Type)
			continue
		}
		out.Tools = append(out.Tools, ir.Tool{Name: t.Function.Name, Description: t.Function.Description, Parameters: t.Function.Parameters})
	}
	out.ToolChoice = decodeOAToolChoice(p.ToolChoice)
	var sys []string
	for _, m := range p.Messages {
		switch m.Role {
		case "system", "developer":
			if s := oaContentText(m.Content); s != "" {
				sys = append(sys, s)
			}
		case "user":
			out.Messages = append(out.Messages, ir.Message{Role: "user", Content: oaUserBlocks(m.Content)})
		case "assistant":
			var blocks []ir.Block
			if s := oaContentText(m.Content); s != "" {
				blocks = append(blocks, ir.Block{Type: "text", Text: s})
			}
			for _, tc := range m.ToolCalls {
				blocks = append(blocks, ir.Block{Type: "tool_use", ToolID: tc.ID,
					ToolName: tc.Function.Name, ToolInput: rawOrEmptyObj(json.RawMessage(tc.Function.Arguments))})
			}
			out.Messages = append(out.Messages, ir.Message{Role: "assistant", Content: blocks})
		case "tool":
			blk := ir.Block{Type: "tool_result", ToolID: m.ToolCallID, Text: oaContentText(m.Content)}
			// 连续 role:tool 并入同一条 user 消息（anthropic 语义：tool_result 须在紧随 tool_use 的 user 消息里）
			if n := len(out.Messages); n > 0 && out.Messages[n-1].Role == "user" &&
				len(out.Messages[n-1].Content) > 0 && out.Messages[n-1].Content[0].Type == "tool_result" {
				out.Messages[n-1].Content = append(out.Messages[n-1].Content, blk)
			} else {
				out.Messages = append(out.Messages, ir.Message{Role: "user", Content: []ir.Block{blk}})
			}
		default:
			slog.Warn("convert: 丢弃不支持的 openai 消息角色", "role", m.Role)
		}
	}
	if len(out.Messages) == 0 {
		return nil, fmt.Errorf("convert: 转换后无有效对话消息(仅 system/不支持的内容)")
	}
	out.System = strings.Join(sys, "\n\n")
	return out, nil
}

// EncodeRequest 把 ir 编码为 openai chat 请求。tool_result 块展开为独立 role:tool 消息；
// 流式出站强制 stream_options.include_usage=true（末块回传 usage，计费依赖）。
func (OpenAICodec) EncodeRequest(r *ir.Request) ([]byte, error) {
	msgs := make([]map[string]any, 0, len(r.Messages)+1)
	if r.System != "" {
		msgs = append(msgs, map[string]any{"role": "system", "content": r.System})
	}
	for _, m := range r.Messages {
		switch m.Role {
		case "user":
			var texts []string
			var imgs []map[string]any
			for _, b := range m.Content {
				switch b.Type {
				case "tool_result":
					msgs = append(msgs, map[string]any{"role": "tool", "tool_call_id": b.ToolID, "content": b.Text})
				case "text":
					if b.Text != "" {
						texts = append(texts, b.Text)
					}
				case "image":
					if part := oaImagePart(b); part != nil {
						imgs = append(imgs, part)
					}
				default:
					slog.Warn("convert: 丢弃 user 内容块", "type", b.Type)
				}
			}
			if len(imgs) > 0 {
				// 含图片：content 用块数组（文本块在前、图片块在后）
				parts := make([]map[string]any, 0, len(texts)+len(imgs))
				for _, tx := range texts {
					parts = append(parts, map[string]any{"type": "text", "text": tx})
				}
				parts = append(parts, imgs...)
				msgs = append(msgs, map[string]any{"role": "user", "content": parts})
			} else if len(texts) > 0 {
				msgs = append(msgs, map[string]any{"role": "user", "content": strings.Join(texts, "")})
			}
		case "assistant":
			am := map[string]any{"role": "assistant"}
			var texts []string
			var calls []map[string]any
			for _, b := range m.Content {
				switch b.Type {
				case "text":
					if b.Text != "" {
						texts = append(texts, b.Text)
					}
				case "tool_use":
					calls = append(calls, map[string]any{"id": b.ToolID, "type": "function",
						"function": map[string]any{"name": b.ToolName, "arguments": string(rawOrEmptyObj(b.ToolInput))}})
				default:
					slog.Warn("convert: 丢弃 assistant 内容块", "type", b.Type)
				}
			}
			if len(texts) > 0 {
				am["content"] = strings.Join(texts, "")
			}
			if len(calls) > 0 {
				am["tool_calls"] = calls
			}
			if am["content"] == nil && len(calls) == 0 {
				continue
			}
			msgs = append(msgs, am)
		}
	}
	// 防御：除 system 外无任何对话消息（纯图片等不支持内容在编码期丢空）时报错，
	// 避免出站空 messages 被上游 400。
	if len(msgs) == 0 || (r.System != "" && len(msgs) == 1) {
		return nil, fmt.Errorf("convert: 编码后无有效对话消息")
	}
	out := map[string]any{"model": r.Model, "messages": msgs}
	if len(r.Tools) > 0 {
		ts := make([]map[string]any, 0, len(r.Tools))
		for _, t := range r.Tools {
			fn := map[string]any{"name": t.Name}
			if t.Description != "" {
				fn["description"] = t.Description
			}
			if len(t.Parameters) > 0 {
				fn["parameters"] = t.Parameters
			}
			ts = append(ts, map[string]any{"type": "function", "function": fn})
		}
		out["tools"] = ts
	}
	if tc := r.ToolChoice; tc != nil {
		switch tc.Type {
		case "auto", "none", "required":
			out["tool_choice"] = tc.Type
		case "tool":
			out["tool_choice"] = map[string]any{"type": "function", "function": map[string]any{"name": tc.Name}}
		}
	}
	if r.MaxTokens > 0 {
		out["max_tokens"] = r.MaxTokens
	}
	if r.Temperature != nil {
		out["temperature"] = *r.Temperature
	}
	if r.TopP != nil {
		out["top_p"] = *r.TopP
	}
	if len(r.Stop) > 0 {
		out["stop"] = r.Stop
	}
	if r.Stream {
		out["stream"] = true
		out["stream_options"] = map[string]any{"include_usage": true}
	}
	return json.Marshal(out)
}

// DecodeResponse 解析 openai chat.completion（非流式）为 ir，只取 choices[0]。
func (OpenAICodec) DecodeResponse(body []byte) (*ir.Response, error) {
	var p struct {
		ID      string `json:"id"`
		Model   string `json:"model"`
		Choices []struct {
			Message struct {
				Content          json.RawMessage `json:"content"`
				ReasoningContent string          `json:"reasoning_content"` // DeepSeek-R1 等推理模型的思考文本
				Refusal          string          `json:"refusal"`
				ToolCalls        []oaToolCall    `json:"tool_calls"`
			} `json:"message"`
			FinishReason string `json:"finish_reason"`
		} `json:"choices"`
		Usage *struct {
			PromptTokens     int `json:"prompt_tokens"`
			CompletionTokens int `json:"completion_tokens"`
		} `json:"usage"`
	}
	if err := json.Unmarshal(body, &p); err != nil {
		return nil, fmt.Errorf("convert: 解析 openai 响应失败: %w", err)
	}
	if len(p.Choices) == 0 {
		return nil, fmt.Errorf("convert: openai 响应缺少 choices")
	}
	ch := p.Choices[0]
	out := &ir.Response{ID: p.ID, Model: p.Model, StopReason: oaFinishToIR(ch.FinishReason),
		Reasoning: ch.Message.ReasoningContent}
	if s := oaContentText(ch.Message.Content); s != "" {
		out.Content = append(out.Content, ir.Block{Type: "text", Text: s})
	}
	// refusal 拒答文本单独成字段（content 为 null），不读会导致跨协议客户端看到空消息
	if ch.Message.Refusal != "" {
		out.Content = append(out.Content, ir.Block{Type: "text", Text: ch.Message.Refusal})
	}
	for _, tc := range ch.Message.ToolCalls {
		out.Content = append(out.Content, ir.Block{Type: "tool_use", ToolID: tc.ID,
			ToolName: tc.Function.Name, ToolInput: rawOrEmptyObj(json.RawMessage(tc.Function.Arguments))})
	}
	if p.Usage != nil {
		out.Usage = ir.Usage{InputTokens: p.Usage.PromptTokens, OutputTokens: p.Usage.CompletionTokens}
		out.HasUsage = true
	}
	return out, nil
}

// EncodeResponse 把 ir 编码为 openai chat.completion（非流式）。
func (OpenAICodec) EncodeResponse(r *ir.Response) ([]byte, error) {
	msg := map[string]any{"role": "assistant", "content": nil}
	var texts []string
	var calls []map[string]any
	for _, b := range r.Content {
		switch b.Type {
		case "text":
			if b.Text != "" {
				texts = append(texts, b.Text)
			}
		case "tool_use":
			calls = append(calls, map[string]any{"id": b.ToolID, "type": "function",
				"function": map[string]any{"name": b.ToolName, "arguments": string(rawOrEmptyObj(b.ToolInput))}})
		}
	}
	if len(texts) > 0 {
		msg["content"] = strings.Join(texts, "")
	}
	if len(calls) > 0 {
		msg["tool_calls"] = calls
	}
	if r.Reasoning != "" {
		msg["reasoning_content"] = r.Reasoning
	}
	out := map[string]any{
		"id":      idOr(r.ID, "chatcmpl-convert"),
		"object":  "chat.completion",
		"created": time.Now().Unix(),
		"model":   r.Model,
		"choices": []map[string]any{{"index": 0, "message": msg, "finish_reason": irToOAFinish(r.StopReason)}},
	}
	if r.HasUsage {
		prompt := r.Usage.InputTokens + r.Usage.CacheReadTokens + r.Usage.CacheCreationTokens
		u := map[string]any{
			"prompt_tokens":     prompt,
			"completion_tokens": r.Usage.OutputTokens,
			"total_tokens":      prompt + r.Usage.OutputTokens,
		}
		if r.Usage.CacheReadTokens > 0 {
			u["prompt_tokens_details"] = map[string]any{"cached_tokens": r.Usage.CacheReadTokens}
		}
		out["usage"] = u
	}
	return json.Marshal(out)
}

// oaContentText 提取 openai content（string 或块数组）的纯文本；非文本块丢弃并记日志。
func oaContentText(raw json.RawMessage) string {
	if len(raw) == 0 || string(raw) == "null" {
		return ""
	}
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return s
	}
	var parts []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	if json.Unmarshal(raw, &parts) != nil {
		return ""
	}
	var sb []string
	for _, p := range parts {
		if p.Type == "text" {
			sb = append(sb, p.Text)
		} else {
			slog.Warn("convert: 丢弃 openai 内容块", "type", p.Type)
		}
	}
	return strings.Join(sb, "")
}

// oaUserBlocks 解析 openai user 消息内容为 ir 块：字符串→单 text 块；
// 数组→按序 text/image 块（image_url 解析 data URI 或直传 url，其余块丢弃记日志）。
func oaUserBlocks(raw json.RawMessage) []ir.Block {
	if len(raw) == 0 || string(raw) == "null" {
		return []ir.Block{{Type: "text", Text: ""}}
	}
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return []ir.Block{{Type: "text", Text: s}}
	}
	var parts []struct {
		Type     string `json:"type"`
		Text     string `json:"text"`
		ImageURL struct {
			URL string `json:"url"`
		} `json:"image_url"`
	}
	if json.Unmarshal(raw, &parts) != nil {
		return []ir.Block{{Type: "text", Text: ""}}
	}
	var out []ir.Block
	for _, p := range parts {
		switch p.Type {
		case "text":
			out = append(out, ir.Block{Type: "text", Text: p.Text})
		case "image_url":
			out = append(out, oaImageBlock(p.ImageURL.URL))
		default:
			slog.Warn("convert: 丢弃 openai user 内容块", "type", p.Type)
		}
	}
	if len(out) == 0 {
		out = append(out, ir.Block{Type: "text", Text: ""})
	}
	return out
}

// oaImageBlock 把 openai image_url.url 解析为 ir 图片块：data URI → base64 形式；否则 url 形式。
func oaImageBlock(url string) ir.Block {
	if mt, data, ok := parseDataURI(url); ok {
		return ir.Block{Type: "image", ImageMediaType: mt, ImageData: data}
	}
	return ir.Block{Type: "image", ImageURL: url}
}

// parseDataURI 解析 data:<mime>;base64,<data>；非该形式返回 ok=false。
func parseDataURI(u string) (mediaType, data string, ok bool) {
	if !strings.HasPrefix(u, "data:") {
		return "", "", false
	}
	rest := strings.TrimPrefix(u, "data:")
	i := strings.Index(rest, ";base64,")
	if i < 0 {
		return "", "", false
	}
	return rest[:i], rest[i+len(";base64,"):], true
}

// oaImagePart 把 ir 图片块编码为 openai image_url 内容块：base64 形式拼 data URI；url 形式直传；空块返回 nil。
func oaImagePart(b ir.Block) map[string]any {
	var url string
	switch {
	case b.ImageData != "":
		url = "data:" + b.ImageMediaType + ";base64," + b.ImageData
	case b.ImageURL != "":
		url = b.ImageURL
	default:
		return nil
	}
	return map[string]any{"type": "image_url", "image_url": map[string]any{"url": url}}
}

// decodeStringOrList stop 兼容 string 或 []string。
func decodeStringOrList(raw json.RawMessage) []string {
	if len(raw) == 0 || string(raw) == "null" {
		return nil
	}
	var s string
	if json.Unmarshal(raw, &s) == nil {
		if s == "" {
			return nil
		}
		return []string{s}
	}
	var l []string
	if json.Unmarshal(raw, &l) == nil {
		return l
	}
	return nil
}

// decodeOAToolChoice openai tool_choice（"auto"/"none"/"required" 或 {type:function,function:{name}}）→ ir。
func decodeOAToolChoice(raw json.RawMessage) *ir.ToolChoice {
	if len(raw) == 0 || string(raw) == "null" {
		return nil
	}
	var s string
	if json.Unmarshal(raw, &s) == nil {
		switch s {
		case "auto", "none", "required":
			return &ir.ToolChoice{Type: s}
		}
		return nil
	}
	var o struct {
		Function struct {
			Name string `json:"name"`
		} `json:"function"`
	}
	if json.Unmarshal(raw, &o) == nil && o.Function.Name != "" {
		return &ir.ToolChoice{Type: "tool", Name: o.Function.Name}
	}
	return nil
}

// oaFinishToIR openai finish_reason → ir StopReason。
func oaFinishToIR(s string) string {
	switch s {
	case "length":
		return "length"
	case "tool_calls":
		return "tool_use"
	case "content_filter":
		return "content_filter"
	default:
		return "stop"
	}
}

// irToOAFinish ir StopReason → openai finish_reason。
func irToOAFinish(s string) string {
	switch s {
	case "length":
		return "length"
	case "tool_use":
		return "tool_calls"
	case "content_filter":
		return "content_filter"
	default:
		return "stop"
	}
}
