// Package ir 定义协议转换的内部中间表示（枢纽格式，P12c）。
// 各协议 codec 只与 ir 互转，新增协议无需写全矩阵转换器。
package ir

import "encoding/json"

// Request 一次对话请求的中间表示。
type Request struct {
	Model       string
	System      string    // 系统提示（多段合并，\n\n 连接）
	Messages    []Message // 仅 user/assistant 两种角色；工具结果归一为 user 消息内的 tool_result 块
	Tools       []Tool
	ToolChoice  *ToolChoice
	MaxTokens   int // 0=未指定（anthropic 出站时编码器补默认值）
	Temperature *float64
	TopP        *float64
	Stop        []string
	Stream      bool
}

// Message 一条对话消息，内容为块序列。
type Message struct {
	Role    string // "user" | "assistant"
	Content []Block
}

// Block 内容块。Type 取值：
//   - "text"：       Text 有效
//   - "tool_use"：   ToolID/ToolName/ToolInput 有效（assistant 消息）
//   - "tool_result"：ToolID/Text（结果文本）/IsError 有效（user 消息）
//   - "image"：      ImageMediaType+ImageData（base64 形式）或 ImageURL（url 形式）有效（user 消息）
type Block struct {
	Type      string
	Text      string
	ToolID    string
	ToolName  string
	ToolInput json.RawMessage
	IsError   bool
	// image 块（仅请求侧 user 消息）：base64 形式填 ImageMediaType+ImageData；url 形式填 ImageURL
	ImageMediaType string
	ImageData      string
	ImageURL       string
}

// Tool 工具声明（Parameters 为 JSON Schema 原文）。
type Tool struct {
	Name        string
	Description string
	Parameters  json.RawMessage
}

// ToolChoice 工具选择策略。Type: "auto" | "none" | "required" | "tool"（指定 Name）。
type ToolChoice struct {
	Type string
	Name string
}

// Response 非流式响应的中间表示。
type Response struct {
	ID         string
	Model      string
	Content    []Block // text / tool_use
	Reasoning  string  // 推理/思考文本（anthropic thinking 块聚合，→ openai reasoning_content）
	StopReason string  // "stop" | "length" | "tool_use" | "stop_sequence" | "content_filter"
	Usage      Usage
	HasUsage   bool
}

// Usage token 用量。保持 anthropic 原生语义：InputTokens 不含缓存 token，
// CacheReadTokens/CacheCreationTokens 是平行字段（openai 侧恒 0）；
// 归一化(总量口径)由消费方(RelayConvert→billing)完成(P12d)。
type Usage struct {
	InputTokens         int
	OutputTokens        int
	CacheReadTokens     int
	CacheCreationTokens int
	ReasoningTokens     int // openai completion 推理 token(子集,观测用);anthropic 侧恒 0
}

// StreamEvent 流式增量事件的统一表示。Type 取值：
//   - "start"：      ID/Model 有效；Usage 可携带输入侧用量（HasUsage，anthropic message_start）
//   - "text_delta"： Text 有效
//   - "tool_start"： Index/ToolID/ToolName 有效（Index 为工具序号，0 起）
//   - "tool_delta"： Index/ArgsDelta 有效（工具入参 JSON 增量）
//   - "finish"：     StopReason 有效；Usage 可携带输出侧用量（HasUsage，anthropic message_delta）
//   - "usage"：      仅 Usage/HasUsage 有效（openai include_usage 末块，输入+输出整块）
//   - "done"：       流结束
type StreamEvent struct {
	Type       string
	ID         string
	Model      string
	Index      int
	Text       string
	ToolID     string
	ToolName   string
	ArgsDelta  string
	StopReason string
	Usage      Usage
	HasUsage   bool
}
