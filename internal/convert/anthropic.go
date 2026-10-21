package convert

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"

	"github.com/ruanun/simple-ai-gateway/internal/convert/ir"
)

// AnthropicCodec Anthropic Messages 协议编解码器。
type AnthropicCodec struct{}

// anDefaultMaxTokens openai 入站未带 max_tokens 时的出站默认值（anthropic 必填）。
const anDefaultMaxTokens = 4096

// anBlock anthropic 内容块（请求/响应共用解析结构）。
type anBlock struct {
	Type      string          `json:"type"`
	Text      string          `json:"text"`
	Thinking  string          `json:"thinking"` // thinking 块的推理文本
	ID        string          `json:"id"`
	Name      string          `json:"name"`
	Input     json.RawMessage `json:"input"`
	ToolUseID string          `json:"tool_use_id"`
	Content   json.RawMessage `json:"content"` // tool_result 内容（string 或块数组）
	IsError   bool            `json:"is_error"`
	Source    json.RawMessage `json:"source"` // image 块的来源（base64/url）
}

// DecodeRequest 解析 anthropic messages 请求为 ir。
func (AnthropicCodec) DecodeRequest(body []byte) (*ir.Request, error) {
	var p struct {
		Model    string          `json:"model"`
		System   json.RawMessage `json:"system"` // string 或块数组
		Messages []struct {
			Role    string          `json:"role"`
			Content json.RawMessage `json:"content"`
		} `json:"messages"`
		Tools []struct {
			Name        string          `json:"name"`
			Description string          `json:"description"`
			InputSchema json.RawMessage `json:"input_schema"`
		} `json:"tools"`
		ToolChoice *struct {
			Type string `json:"type"`
			Name string `json:"name"`
		} `json:"tool_choice"`
		MaxTokens     int      `json:"max_tokens"`
		Temperature   *float64 `json:"temperature"`
		TopP          *float64 `json:"top_p"`
		StopSequences []string `json:"stop_sequences"`
		Stream        bool     `json:"stream"`
	}
	if err := json.Unmarshal(body, &p); err != nil {
		return nil, fmt.Errorf("convert: 解析 anthropic 请求失败: %w", err)
	}
	if len(p.Messages) == 0 {
		return nil, fmt.Errorf("convert: anthropic 请求缺少 messages")
	}
	out := &ir.Request{Model: p.Model, System: strings.Join(anTextParts(p.System), "\n\n"), MaxTokens: p.MaxTokens,
		Temperature: p.Temperature, TopP: p.TopP, Stop: p.StopSequences, Stream: p.Stream}
	for _, t := range p.Tools {
		out.Tools = append(out.Tools, ir.Tool{Name: t.Name, Description: t.Description, Parameters: t.InputSchema})
	}
	if p.ToolChoice != nil {
		switch p.ToolChoice.Type {
		case "auto":
			out.ToolChoice = &ir.ToolChoice{Type: "auto"}
		case "any":
			out.ToolChoice = &ir.ToolChoice{Type: "required"}
		case "tool":
			out.ToolChoice = &ir.ToolChoice{Type: "tool", Name: p.ToolChoice.Name}
		case "none":
			out.ToolChoice = &ir.ToolChoice{Type: "none"}
		}
	}
	for _, m := range p.Messages {
		switch m.Role {
		case "user", "assistant":
			out.Messages = append(out.Messages, ir.Message{Role: m.Role, Content: anContentBlocks(m.Content)})
		case "system":
			// Claude Code 会在对话流中插入 system 消息（system-reminder）；anthropic 规范里
			// messages 只含 user/assistant。转成 user 消息保留在原位置、包 <system-reminder> 标签，
			// 避免转发到非 anthropic 上游时静默丢失这部分上下文。参照 CLIProxyAPI。
			if txt := anSystemReminder(m.Content); txt != "" {
				out.Messages = append(out.Messages, ir.Message{Role: "user", Content: []ir.Block{{Type: "text", Text: txt}}})
			}
		default:
			slog.Warn("convert: 丢弃不支持的 anthropic 消息角色", "role", m.Role)
		}
	}
	if len(out.Messages) == 0 {
		return nil, fmt.Errorf("convert: 转换后无有效对话消息(仅 system/不支持的内容)")
	}
	return out, nil
}

// EncodeRequest 把 ir 编码为 anthropic messages 请求。max_tokens 缺省补 anDefaultMaxTokens。
func (AnthropicCodec) EncodeRequest(r *ir.Request) ([]byte, error) {
	msgs := make([]map[string]any, 0, len(r.Messages))
	for _, m := range r.Messages {
		var blocks []map[string]any
		for _, b := range m.Content {
			switch b.Type {
			case "text":
				if b.Text != "" {
					blocks = append(blocks, map[string]any{"type": "text", "text": b.Text})
				}
			case "tool_use":
				blocks = append(blocks, map[string]any{"type": "tool_use", "id": b.ToolID,
					"name": b.ToolName, "input": rawOrEmptyObj(b.ToolInput)})
			case "tool_result":
				tr := map[string]any{"type": "tool_result", "tool_use_id": b.ToolID, "content": b.Text}
				if b.IsError {
					tr["is_error"] = true
				}
				blocks = append(blocks, tr)
			case "image":
				if src := anImageSource(b); src != nil {
					blocks = append(blocks, map[string]any{"type": "image", "source": src})
				}
			default:
				slog.Warn("convert: 丢弃内容块", "type", b.Type)
			}
		}
		if len(blocks) == 0 {
			continue
		}
		if n := len(msgs); n > 0 && msgs[n-1]["role"] == m.Role {
			// anthropic 要求 user/assistant 严格交替；openai 允许连续同角色，合并进前一条
			prev := msgs[n-1]["content"].([]map[string]any)
			msgs[n-1]["content"] = append(prev, blocks...)
			continue
		}
		msgs = append(msgs, map[string]any{"role": m.Role, "content": blocks})
	}
	// 防御：编码期丢空（如纯图片消息全被丢弃）时报错，避免出站空 messages 被上游 400
	if len(msgs) == 0 {
		return nil, fmt.Errorf("convert: 编码后无有效对话消息")
	}
	mt := r.MaxTokens
	if mt <= 0 {
		mt = anDefaultMaxTokens
	}
	out := map[string]any{"model": r.Model, "messages": msgs, "max_tokens": mt}
	if r.System != "" {
		out["system"] = r.System
	}
	if len(r.Tools) > 0 {
		ts := make([]map[string]any, 0, len(r.Tools))
		for _, t := range r.Tools {
			tm := map[string]any{"name": t.Name}
			if t.Description != "" {
				tm["description"] = t.Description
			}
			if len(t.Parameters) > 0 {
				tm["input_schema"] = t.Parameters
			} else {
				// input_schema 是 anthropic 必填；openai 无参工具（无 parameters）转过来时
				// 补一个空对象 schema，避免上游 400。
				tm["input_schema"] = map[string]any{"type": "object", "properties": map[string]any{}}
			}
			ts = append(ts, tm)
		}
		out["tools"] = ts
	}
	if tc := r.ToolChoice; tc != nil {
		switch tc.Type {
		case "auto":
			out["tool_choice"] = map[string]any{"type": "auto"}
		case "required":
			out["tool_choice"] = map[string]any{"type": "any"}
		case "tool":
			out["tool_choice"] = map[string]any{"type": "tool", "name": tc.Name}
		case "none":
			out["tool_choice"] = map[string]any{"type": "none"}
		}
	}
	if r.Temperature != nil {
		out["temperature"] = *r.Temperature
	}
	if r.TopP != nil {
		out["top_p"] = *r.TopP
	}
	if len(r.Stop) > 0 {
		out["stop_sequences"] = r.Stop
	}
	if r.Stream {
		out["stream"] = true
	}
	return json.Marshal(out)
}

// DecodeResponse 解析 anthropic message（非流式）为 ir。
func (AnthropicCodec) DecodeResponse(body []byte) (*ir.Response, error) {
	var p struct {
		ID         string    `json:"id"`
		Model      string    `json:"model"`
		Content    []anBlock `json:"content"`
		StopReason string    `json:"stop_reason"`
		Usage      *struct {
			InputTokens              int `json:"input_tokens"`
			OutputTokens             int `json:"output_tokens"`
			CacheReadInputTokens     int `json:"cache_read_input_tokens"`
			CacheCreationInputTokens int `json:"cache_creation_input_tokens"`
		} `json:"usage"`
	}
	if err := json.Unmarshal(body, &p); err != nil {
		return nil, fmt.Errorf("convert: 解析 anthropic 响应失败: %w", err)
	}
	// max_tokens 立即截断时 anthropic 可能返回空 content 但带 stop_reason，
	// 只有两者皆空才视为非法（否则会把合法响应误判失败触发 failover）
	if len(p.Content) == 0 && p.StopReason == "" {
		return nil, fmt.Errorf("convert: 不是合法的 anthropic 响应(缺 content/stop_reason)")
	}
	out := &ir.Response{ID: p.ID, Model: p.Model, StopReason: anStopToIR(p.StopReason)}
	for _, b := range p.Content {
		switch b.Type {
		case "text":
			out.Content = append(out.Content, ir.Block{Type: "text", Text: b.Text})
		case "tool_use":
			out.Content = append(out.Content, ir.Block{Type: "tool_use", ToolID: b.ID,
				ToolName: b.Name, ToolInput: rawOrEmptyObj(b.Input)})
		case "thinking":
			out.Reasoning += b.Thinking
		default:
			slog.Warn("convert: 丢弃 anthropic 响应内容块", "type", b.Type)
		}
	}
	if p.Usage != nil {
		out.Usage = ir.Usage{InputTokens: p.Usage.InputTokens, OutputTokens: p.Usage.OutputTokens,
			CacheReadTokens: p.Usage.CacheReadInputTokens, CacheCreationTokens: p.Usage.CacheCreationInputTokens}
		out.HasUsage = true
	}
	return out, nil
}

// EncodeResponse 把 ir 编码为 anthropic message（非流式）。
func (AnthropicCodec) EncodeResponse(r *ir.Response) ([]byte, error) {
	blocks := make([]map[string]any, 0, len(r.Content)+1)
	// 推理文本（openai reasoning_content）→ thinking 块，置于正文之前（anthropic 原生顺序）。
	// 转换场景无法生成签名，signature 置空（客户端普遍容忍）。
	if r.Reasoning != "" {
		blocks = append(blocks, map[string]any{"type": "thinking", "thinking": r.Reasoning, "signature": ""})
	}
	for _, b := range r.Content {
		switch b.Type {
		case "text":
			if b.Text != "" {
				blocks = append(blocks, map[string]any{"type": "text", "text": b.Text})
			}
		case "tool_use":
			blocks = append(blocks, map[string]any{"type": "tool_use", "id": sanitizeAnthropicToolID(b.ToolID),
				"name": b.ToolName, "input": rawOrEmptyObj(b.ToolInput)})
		}
	}
	out := map[string]any{
		"id":            idOr(r.ID, "msg_convert"),
		"type":          "message",
		"role":          "assistant",
		"model":         r.Model,
		"content":       blocks,
		"stop_reason":   irToAnStop(r.StopReason),
		"stop_sequence": nil,
	}
	if r.HasUsage {
		out["usage"] = map[string]any{"input_tokens": r.Usage.InputTokens, "output_tokens": r.Usage.OutputTokens}
	}
	return json.Marshal(out)
}

// claudeBillingPrefix Claude Code 每请求注入的计费/指纹头前缀，对模型无意义，转换时过滤。
const claudeBillingPrefix = "x-anthropic-billing-header:"

// isBillingText 判断文本是否为 Claude Code 计费/指纹头（跳过前导空白后比较前缀）。
func isBillingText(s string) bool {
	return strings.HasPrefix(strings.TrimLeft(s, " \t\r\n"), claudeBillingPrefix)
}

// anTextParts 提取 anthropic content（string 或块数组）中的 text 段，
// 跳过空白与 Claude Code 计费/指纹头。用于顶层 system 与 messages 内 system 的文本归集。
func anTextParts(raw json.RawMessage) []string {
	if len(raw) == 0 || string(raw) == "null" {
		return nil
	}
	var s string
	if json.Unmarshal(raw, &s) == nil {
		if strings.TrimSpace(s) == "" || isBillingText(s) {
			return nil
		}
		return []string{s}
	}
	var blocks []anBlock
	if json.Unmarshal(raw, &blocks) != nil {
		return nil
	}
	var parts []string
	for _, b := range blocks {
		if b.Type != "text" || strings.TrimSpace(b.Text) == "" || isBillingText(b.Text) {
			continue
		}
		parts = append(parts, b.Text)
	}
	return parts
}

// anSystemReminder 把 messages 数组里的 system 值转为供非 anthropic 上游的 user 可读文本，
// 包裹 <system-reminder> 标签；无有效文本返回空。参照 CLIProxyAPI 的 system-reminder 处理。
func anSystemReminder(raw json.RawMessage) string {
	parts := anTextParts(raw)
	if len(parts) == 0 {
		return ""
	}
	return "<system-reminder>\n" + strings.Join(parts, "\n") + "\n</system-reminder>"
}

// sanitizeAnthropicToolID 清洗工具调用 id 使其满足严格客户端约束 ^[a-zA-Z0-9_-]+$，
// 非法字符替为 _。openai 兼容上游可能吐出含 :、. 或非 ASCII 的 tool id，会被 Claude Code
// 拒收、导致工具回合断裂。空串原样返回，由调用方决定 fallback。
func sanitizeAnthropicToolID(id string) string {
	if id == "" {
		return ""
	}
	var b strings.Builder
	b.Grow(len(id))
	changed := false
	for _, r := range id {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '_', r == '-':
			b.WriteRune(r)
		default:
			b.WriteByte('_')
			changed = true
		}
	}
	if !changed {
		return id
	}
	return b.String()
}

// anToolResultText 提取 tool_result 内容为纯文本（openai tool 消息 content 只能是 string）。
// string 原样；块数组取 text 段（换行连接）；其余结构化 JSON（MCP 工具常见对象/数组）
// 退化为原始 JSON 字符串，避免结构化结果转换后变成空内容。
func anToolResultText(raw json.RawMessage) string {
	if len(raw) == 0 || string(raw) == "null" {
		return ""
	}
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return s
	}
	var blocks []anBlock
	if json.Unmarshal(raw, &blocks) == nil {
		var parts []string
		for _, b := range blocks {
			if b.Type == "text" && b.Text != "" {
				parts = append(parts, b.Text)
			}
		}
		if len(parts) > 0 {
			return strings.Join(parts, "\n")
		}
	}
	// 非 string、非(含 text 的)块数组：结构化 JSON 对象/纯图片块等，退化为原始 JSON 保留
	return string(raw)
}

// anContentBlocks 把 anthropic content（string 或块数组）解析为 ir 块；image 等不支持块丢弃并记日志。
func anContentBlocks(raw json.RawMessage) []ir.Block {
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return []ir.Block{{Type: "text", Text: s}}
	}
	var blocks []anBlock
	if json.Unmarshal(raw, &blocks) != nil {
		return nil
	}
	var out []ir.Block
	for _, b := range blocks {
		switch b.Type {
		case "text":
			out = append(out, ir.Block{Type: "text", Text: b.Text})
		case "tool_use":
			out = append(out, ir.Block{Type: "tool_use", ToolID: b.ID, ToolName: b.Name, ToolInput: rawOrEmptyObj(b.Input)})
		case "tool_result":
			out = append(out, ir.Block{Type: "tool_result", ToolID: b.ToolUseID, Text: anToolResultText(b.Content), IsError: b.IsError})
		case "image":
			if blk, ok := anImageBlock(b.Source); ok {
				out = append(out, blk)
			}
		default:
			slog.Warn("convert: 丢弃 anthropic 内容块", "type", b.Type)
		}
	}
	return out
}

// anImageBlock 解析 anthropic image 块的 source（base64/url）为 ir 图片块；无法识别返回 ok=false。
func anImageBlock(raw json.RawMessage) (ir.Block, bool) {
	var s struct {
		Type      string `json:"type"`
		MediaType string `json:"media_type"`
		Data      string `json:"data"`
		URL       string `json:"url"`
	}
	if len(raw) == 0 || json.Unmarshal(raw, &s) != nil {
		return ir.Block{}, false
	}
	switch s.Type {
	case "base64":
		if s.Data == "" {
			return ir.Block{}, false
		}
		return ir.Block{Type: "image", ImageMediaType: s.MediaType, ImageData: s.Data}, true
	case "url":
		if s.URL == "" {
			return ir.Block{}, false
		}
		return ir.Block{Type: "image", ImageURL: s.URL}, true
	}
	return ir.Block{}, false
}

// anImageSource 把 ir 图片块编码为 anthropic image.source（base64/url）；空块返回 nil。
func anImageSource(b ir.Block) map[string]any {
	if b.ImageData != "" {
		return map[string]any{"type": "base64", "media_type": b.ImageMediaType, "data": b.ImageData}
	}
	if b.ImageURL != "" {
		return map[string]any{"type": "url", "url": b.ImageURL}
	}
	return nil
}

// anStopToIR anthropic stop_reason → ir StopReason。
func anStopToIR(s string) string {
	switch s {
	case "max_tokens":
		return "length"
	case "tool_use":
		return "tool_use"
	case "stop_sequence":
		return "stop_sequence"
	default:
		return "stop" // end_turn / refusal 等兜底
	}
}

// irToAnStop ir StopReason → anthropic stop_reason。
// 丢失面：content_filter 无 anthropic 对应值、降级为 end_turn
// （anthropic 客户端无法区分内容过滤与正常收尾）。
func irToAnStop(s string) string {
	switch s {
	case "length":
		return "max_tokens"
	case "tool_use":
		return "tool_use"
	case "stop_sequence":
		return "stop_sequence"
	default:
		return "end_turn"
	}
}
