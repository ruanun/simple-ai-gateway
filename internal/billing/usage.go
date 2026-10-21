// Package billing 提供 token 用量解析与成本计算。内部成本单位统一为 USD 微量整数（1 USD = 1e6 micros）。
package billing

import "encoding/json"

// Usage 一次请求的 token 用量。归一不变式(P12d)：PromptTokens=总输入(含全部缓存 token)，
// CachedTokens(缓存读)/CacheCreationTokens(缓存写)是其子集；openai 的 CacheCreationTokens 恒 0。
type Usage struct {
	PromptTokens        int
	CompletionTokens    int
	CachedTokens        int
	CacheCreationTokens int
	ReasoningTokens     int
	TotalTokens         int
}

// AnthropicUsageFields Anthropic usage 原始字段的 JSON 声明：
// 非流式响应体（ParseAnthropicUsageJSON）与流式 message_start 事件（gateway.RelayAnthropic）共用，
// 避免匿名 struct 各写一份、字段声明漂移。
type AnthropicUsageFields struct {
	InputTokens              int `json:"input_tokens"`
	OutputTokens             int `json:"output_tokens"`
	CacheCreationInputTokens int `json:"cache_creation_input_tokens"`
	CacheReadInputTokens     int `json:"cache_read_input_tokens"`
}

// NormalizeAnthropic 把 anthropic 原始 usage 归一为总量口径（P12d）——计费口径的唯一出处：
// anthropic 的 input_tokens 不含缓存 token，故 PromptTokens = input + cache_read + cache_creation；
// CachedTokens（缓存读）= cache_read、CacheCreationTokens（缓存写）= cache_creation 为其子集；
// TotalTokens = prompt + output。流式场景 output 未知时传 0，由调用方后续补 CompletionTokens/TotalTokens。
func NormalizeAnthropic(input, cacheRead, cacheCreate, output int) Usage {
	prompt := input + cacheRead + cacheCreate
	return Usage{
		PromptTokens:        prompt,
		CompletionTokens:    output,
		CachedTokens:        cacheRead,
		CacheCreationTokens: cacheCreate,
		TotalTokens:         prompt + output,
	}
}

// ParseUsageJSON 从一段 JSON（完整响应或单个 SSE data 块）解析 usage；无 usage 返回 false。
func ParseUsageJSON(b []byte) (Usage, bool) {
	var p struct {
		Usage *struct {
			PromptTokens        int `json:"prompt_tokens"`
			CompletionTokens    int `json:"completion_tokens"`
			TotalTokens         int `json:"total_tokens"`
			PromptTokensDetails *struct {
				CachedTokens int `json:"cached_tokens"`
			} `json:"prompt_tokens_details"`
			CompletionTokensDetails *struct {
				ReasoningTokens int `json:"reasoning_tokens"`
			} `json:"completion_tokens_details"`
		} `json:"usage"`
	}
	if err := json.Unmarshal(b, &p); err != nil || p.Usage == nil {
		return Usage{}, false
	}
	u := Usage{PromptTokens: p.Usage.PromptTokens, CompletionTokens: p.Usage.CompletionTokens, TotalTokens: p.Usage.TotalTokens}
	if p.Usage.PromptTokensDetails != nil {
		u.CachedTokens = p.Usage.PromptTokensDetails.CachedTokens
	}
	if p.Usage.CompletionTokensDetails != nil {
		u.ReasoningTokens = p.Usage.CompletionTokensDetails.ReasoningTokens
	}
	if u.PromptTokens == 0 && u.CompletionTokens == 0 && u.TotalTokens == 0 {
		return Usage{}, false
	}
	return u, true
}

// ParseAnthropicUsageJSON 从 Anthropic Messages 响应体解析 usage 并归一为总量口径（P12d）：
// anthropic 的 input_tokens 不含缓存 token，PromptTokens 归一为 input+cache_read+cache_creation。
func ParseAnthropicUsageJSON(b []byte) (Usage, bool) {
	var p struct {
		Usage *AnthropicUsageFields `json:"usage"`
	}
	if err := json.Unmarshal(b, &p); err != nil || p.Usage == nil {
		return Usage{}, false
	}
	u := p.Usage
	if u.InputTokens == 0 && u.OutputTokens == 0 && u.CacheCreationInputTokens == 0 && u.CacheReadInputTokens == 0 {
		return Usage{}, false
	}
	return NormalizeAnthropic(u.InputTokens, u.CacheReadInputTokens, u.CacheCreationInputTokens, u.OutputTokens), true
}

// ParseResponsesUsageJSON 从 OpenAI Responses 协议响应体解析 usage。
// input_tokens 已含 cached_tokens（子集语义，同 chat 的 prompt_tokens），故 PromptTokens 不再叠加。
func ParseResponsesUsageJSON(b []byte) (Usage, bool) {
	var p struct {
		Usage *struct {
			InputTokens        int `json:"input_tokens"`
			OutputTokens       int `json:"output_tokens"`
			InputTokensDetails *struct {
				CachedTokens int `json:"cached_tokens"`
			} `json:"input_tokens_details"`
			OutputTokensDetails *struct {
				ReasoningTokens int `json:"reasoning_tokens"`
			} `json:"output_tokens_details"`
		} `json:"usage"`
	}
	if err := json.Unmarshal(b, &p); err != nil || p.Usage == nil {
		return Usage{}, false
	}
	u := p.Usage
	if u.InputTokens == 0 && u.OutputTokens == 0 {
		return Usage{}, false
	}
	out := Usage{PromptTokens: u.InputTokens, CompletionTokens: u.OutputTokens, TotalTokens: u.InputTokens + u.OutputTokens}
	if u.InputTokensDetails != nil {
		out.CachedTokens = u.InputTokensDetails.CachedTokens
	}
	if u.OutputTokensDetails != nil {
		out.ReasoningTokens = u.OutputTokensDetails.ReasoningTokens
	}
	return out, true
}
