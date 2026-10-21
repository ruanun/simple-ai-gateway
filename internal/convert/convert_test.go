package convert

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestRegistryGetSupported(t *testing.T) {
	_, ok := Get("openai")
	require.True(t, ok)
	_, ok = Get("anthropic")
	require.True(t, ok)
	_, ok = Get("responses")
	require.False(t, ok)

	require.True(t, Supported("openai", "anthropic"))
	require.True(t, Supported("anthropic", "openai"))
	require.False(t, Supported("openai", "openai"), "同协议不需要转换")
	require.False(t, Supported("openai", "responses"), "无 codec 的协议不可转换")
}

// TestRequestOpenAIToAnthropic 覆盖：system 合流、多轮、assistant tool_calls、
// role:tool 归一为 tool_result、tools/tool_choice 映射、max_tokens 缺省补 4096、stop、stream。
func TestRequestOpenAIToAnthropic(t *testing.T) {
	in := []byte(`{
		"model": "claude-3-5-sonnet",
		"messages": [
			{"role": "system", "content": "你是助手"},
			{"role": "user", "content": "北京天气如何"},
			{"role": "assistant", "content": "我查一下", "tool_calls": [
				{"id": "call_1", "type": "function", "function": {"name": "get_weather", "arguments": "{\"city\":\"北京\"}"}}
			]},
			{"role": "tool", "tool_call_id": "call_1", "content": "晴 25 度"},
			{"role": "user", "content": [{"type": "text", "text": "谢谢，总结一下"}]}
		],
		"tools": [{"type": "function", "function": {"name": "get_weather", "description": "查天气", "parameters": {"type": "object", "properties": {"city": {"type": "string"}}}}}],
		"tool_choice": "auto",
		"temperature": 0.7,
		"stop": ["END"],
		"stream": true
	}`)
	out, err := Request(in, "openai", "anthropic")
	require.NoError(t, err)

	var an map[string]any
	require.NoError(t, json.Unmarshal(out, &an))
	require.Equal(t, "claude-3-5-sonnet", an["model"])
	require.Equal(t, "你是助手", an["system"])
	require.EqualValues(t, 4096, an["max_tokens"], "openai 未带 max_tokens 时补默认")
	require.Equal(t, true, an["stream"])
	require.EqualValues(t, 0.7, an["temperature"])
	require.Equal(t, []any{"END"}, an["stop_sequences"])

	msgs := an["messages"].([]any)
	// system 抽走后剩 user/assistant/user；tool_result-user 与后随 user 文本
	// 因 anthropic 角色交替要求被合并为一条 user 消息（tool_result 块 + text 块）
	require.Len(t, msgs, 3)
	// assistant 消息含 text + tool_use 块
	asst := msgs[1].(map[string]any)
	require.Equal(t, "assistant", asst["role"])
	asstBlocks := asst["content"].([]any)
	require.Len(t, asstBlocks, 2)
	require.Equal(t, "text", asstBlocks[0].(map[string]any)["type"])
	tu := asstBlocks[1].(map[string]any)
	require.Equal(t, "tool_use", tu["type"])
	require.Equal(t, "call_1", tu["id"])
	require.Equal(t, "get_weather", tu["name"])
	require.Equal(t, map[string]any{"city": "北京"}, tu["input"])
	// role:tool → user 消息内 tool_result 块，且与后随 user 文本合并
	trMsg := msgs[2].(map[string]any)
	require.Equal(t, "user", trMsg["role"])
	trBlocks := trMsg["content"].([]any)
	require.Len(t, trBlocks, 2)
	tr := trBlocks[0].(map[string]any)
	require.Equal(t, "tool_result", tr["type"])
	require.Equal(t, "call_1", tr["tool_use_id"])
	require.Equal(t, "晴 25 度", tr["content"])
	require.Equal(t, map[string]any{"type": "text", "text": "谢谢，总结一下"}, trBlocks[1])
	// tools/tool_choice
	tool := an["tools"].([]any)[0].(map[string]any)
	require.Equal(t, "get_weather", tool["name"])
	require.NotNil(t, tool["input_schema"])
	require.Equal(t, map[string]any{"type": "auto"}, an["tool_choice"])
}

// TestRequestAnthropicToOpenAI 覆盖：system 字段回填 system 消息、tool_result 展开为
// role:tool 消息、tool_use → tool_calls、tool_choice any→required、max_tokens 透传、
// 流式出站自动带 stream_options.include_usage（计费依赖）。
func TestRequestAnthropicToOpenAI(t *testing.T) {
	in := []byte(`{
		"model": "gpt-4o",
		"max_tokens": 1024,
		"system": "你是助手",
		"messages": [
			{"role": "user", "content": "北京天气如何"},
			{"role": "assistant", "content": [
				{"type": "text", "text": "我查一下"},
				{"type": "tool_use", "id": "toolu_1", "name": "get_weather", "input": {"city": "北京"}}
			]},
			{"role": "user", "content": [
				{"type": "tool_result", "tool_use_id": "toolu_1", "content": "晴 25 度"},
				{"type": "text", "text": "总结一下"}
			]}
		],
		"tools": [{"name": "get_weather", "description": "查天气", "input_schema": {"type": "object"}}],
		"tool_choice": {"type": "any"},
		"stop_sequences": ["END"],
		"stream": true
	}`)
	out, err := Request(in, "anthropic", "openai")
	require.NoError(t, err)

	var oa map[string]any
	require.NoError(t, json.Unmarshal(out, &oa))
	require.Equal(t, "gpt-4o", oa["model"])
	require.EqualValues(t, 1024, oa["max_tokens"])
	require.Equal(t, true, oa["stream"])
	require.Equal(t, map[string]any{"include_usage": true}, oa["stream_options"], "openai 流式出站必须带 include_usage")
	require.Equal(t, []any{"END"}, oa["stop"])
	require.Equal(t, "required", oa["tool_choice"])

	msgs := oa["messages"].([]any)
	// system + user + assistant(tool_calls) + tool + user(text)
	require.Len(t, msgs, 5)
	require.Equal(t, map[string]any{"role": "system", "content": "你是助手"}, msgs[0])
	asst := msgs[2].(map[string]any)
	require.Equal(t, "我查一下", asst["content"])
	tc := asst["tool_calls"].([]any)[0].(map[string]any)
	require.Equal(t, "toolu_1", tc["id"])
	require.Equal(t, "function", tc["type"])
	fn := tc["function"].(map[string]any)
	require.Equal(t, "get_weather", fn["name"])
	require.JSONEq(t, `{"city":"北京"}`, fn["arguments"].(string), "arguments 是 JSON 字符串")
	toolMsg := msgs[3].(map[string]any)
	require.Equal(t, "tool", toolMsg["role"])
	require.Equal(t, "toolu_1", toolMsg["tool_call_id"])
	require.Equal(t, "晴 25 度", toolMsg["content"])
	require.Equal(t, map[string]any{"role": "user", "content": "总结一下"}, msgs[4])
	// tools
	tool := oa["tools"].([]any)[0].(map[string]any)
	require.Equal(t, "function", tool["type"])
	require.Equal(t, "get_weather", tool["function"].(map[string]any)["name"])
}

// TestRequestAnthropicMessageSystemKept 验证 anthropic messages 数组里的 system 消息
// （Claude Code 会在对话流中插入 system-reminder）不再被丢弃，而是转成 user 消息保留在
// 对话流原位置、内容包裹 <system-reminder> 标签。参照 CLIProxyAPI 的处理。
func TestRequestAnthropicMessageSystemKept(t *testing.T) {
	in := []byte(`{
		"model": "gpt-4o",
		"max_tokens": 1024,
		"system": "你是助手",
		"messages": [
			{"role": "user", "content": "第一句"},
			{"role": "system", "content": "可用子代理：claude"},
			{"role": "assistant", "content": "好的"},
			{"role": "user", "content": "第二句"}
		]
	}`)
	out, err := Request(in, "anthropic", "openai")
	require.NoError(t, err)

	var oa map[string]any
	require.NoError(t, json.Unmarshal(out, &oa))
	msgs := oa["messages"].([]any)
	// system(顶层) + user + user(reminder) + assistant + user
	require.Len(t, msgs, 5)
	require.Equal(t, map[string]any{"role": "system", "content": "你是助手"}, msgs[0])
	require.Equal(t, map[string]any{"role": "user", "content": "第一句"}, msgs[1])
	reminder := msgs[2].(map[string]any)
	require.Equal(t, "user", reminder["role"], "messages 里的 system 应降级为 user 保留在原位置")
	content := reminder["content"].(string)
	require.Contains(t, content, "可用子代理：claude", "system-reminder 内容不能丢")
	require.Contains(t, content, "<system-reminder>")
	require.Contains(t, content, "</system-reminder>")
	require.Equal(t, map[string]any{"role": "assistant", "content": "好的"}, msgs[3])
	require.Equal(t, map[string]any{"role": "user", "content": "第二句"}, msgs[4])
}

// TestRequestAnthropicFiltersBillingHeader 验证顶层 system 里 Claude Code 的计费/指纹头
// （x-anthropic-billing-header: 开头）被过滤，不随 system 提示传给上游，其余内容保留。
func TestRequestAnthropicFiltersBillingHeader(t *testing.T) {
	in := []byte(`{
		"model": "gpt-4o",
		"max_tokens": 1024,
		"system": [
			{"type": "text", "text": "x-anthropic-billing-header: cc_version=2.1; cc_entrypoint=cli"},
			{"type": "text", "text": "You are Claude Code"}
		],
		"messages": [{"role": "user", "content": "hi"}]
	}`)
	out, err := Request(in, "anthropic", "openai")
	require.NoError(t, err)

	var oa map[string]any
	require.NoError(t, json.Unmarshal(out, &oa))
	msgs := oa["messages"].([]any)
	require.Len(t, msgs, 2) // system + user
	sys := msgs[0].(map[string]any)
	require.Equal(t, "system", sys["role"])
	content := sys["content"].(string)
	require.Contains(t, content, "You are Claude Code")
	require.NotContains(t, content, "x-anthropic-billing-header", "计费/指纹头应被过滤，不传给上游")
}

// TestRequestNoParamToolGetsInputSchema 验证 openai→anthropic 转换时，无 parameters 的
// 工具也补上 input_schema（anthropic 必填），避免出站 anthropic 上游确定性 400。
func TestRequestNoParamToolGetsInputSchema(t *testing.T) {
	in := []byte(`{
		"model": "claude-3-5-sonnet",
		"max_tokens": 100,
		"messages": [{"role": "user", "content": "现在几点"}],
		"tools": [{"type": "function", "function": {"name": "get_time", "description": "取当前时间"}}]
	}`)
	out, err := Request(in, "openai", "anthropic")
	require.NoError(t, err)

	var an map[string]any
	require.NoError(t, json.Unmarshal(out, &an))
	tools := an["tools"].([]any)
	require.Len(t, tools, 1)
	tool := tools[0].(map[string]any)
	require.Equal(t, "get_time", tool["name"])
	schema, ok := tool["input_schema"].(map[string]any)
	require.True(t, ok, "无参工具也必须有 input_schema(anthropic 必填)")
	require.Equal(t, "object", schema["type"])
	require.NotNil(t, schema["properties"], "input_schema 需含 properties")
}

// TestResponseToolIDSanitized 验证 openai→anthropic 响应转换时，上游 tool_call id 里的
// 非法字符（:、. 等）被清洗为 _，避免严格 Claude Code 客户端拒收、工具回合断裂。
func TestResponseToolIDSanitized(t *testing.T) {
	in := []byte(`{
		"id": "chatcmpl-1",
		"choices": [{"message": {"role":"assistant","content":null,"tool_calls":[
			{"id":"call:abc.123","type":"function","function":{"name":"f","arguments":"{}"}}
		]}, "finish_reason":"tool_calls"}],
		"usage": {"prompt_tokens":1,"completion_tokens":1}
	}`)
	out, err := Response(in, "openai", "anthropic")
	require.NoError(t, err)

	var an map[string]any
	require.NoError(t, json.Unmarshal(out, &an))
	var toolUse map[string]any
	for _, b := range an["content"].([]any) {
		bb := b.(map[string]any)
		if bb["type"] == "tool_use" {
			toolUse = bb
			break
		}
	}
	require.NotNil(t, toolUse, "应有 tool_use 块")
	require.Equal(t, "call_abc_123", toolUse["id"], "非法字符 :/. 应清洗为 _")
}

// TestRequestToolResultStructuredContentKept 验证 anthropic→openai 转换时，tool_result 的
// 结构化 JSON 内容（MCP 工具常见）不再丢成空串，而是退化为原始 JSON 字符串保留。
func TestRequestToolResultStructuredContentKept(t *testing.T) {
	in := []byte(`{
		"model": "gpt-4o",
		"max_tokens": 100,
		"messages": [
			{"role": "assistant", "content": [{"type":"tool_use","id":"toolu_1","name":"q","input":{}}]},
			{"role": "user", "content": [
				{"type": "tool_result", "tool_use_id": "toolu_1", "content": {"temperature": 25, "unit": "C"}}
			]}
		]
	}`)
	out, err := Request(in, "anthropic", "openai")
	require.NoError(t, err)

	var oa map[string]any
	require.NoError(t, json.Unmarshal(out, &oa))
	var toolMsg map[string]any
	for _, m := range oa["messages"].([]any) {
		mm := m.(map[string]any)
		if mm["role"] == "tool" {
			toolMsg = mm
			break
		}
	}
	require.NotNil(t, toolMsg, "应有 role:tool 消息")
	content := toolMsg["content"].(string)
	require.Contains(t, content, "temperature", "结构化 tool_result 内容不能丢")
	require.Contains(t, content, "25")
}

// TestResponseReasoningPreserved 验证 anthropic→openai 响应转换时，thinking 块内容
// 保留到 openai message.reasoning_content，而非被丢弃。
func TestResponseReasoningPreserved(t *testing.T) {
	in := []byte(`{
		"id": "msg_1", "model": "claude", "role": "assistant",
		"content": [
			{"type": "thinking", "thinking": "让我想想"},
			{"type": "text", "text": "答案是42"}
		],
		"stop_reason": "end_turn",
		"usage": {"input_tokens": 5, "output_tokens": 10}
	}`)
	out, err := Response(in, "anthropic", "openai")
	require.NoError(t, err)

	var oa map[string]any
	require.NoError(t, json.Unmarshal(out, &oa))
	msg := oa["choices"].([]any)[0].(map[string]any)["message"].(map[string]any)
	require.Equal(t, "答案是42", msg["content"])
	require.Equal(t, "让我想想", msg["reasoning_content"], "thinking 内容应保留到 reasoning_content")
}

// TestResponseUsageCacheTokens 验证 anthropic→openai 响应转换时，客户端可见 usage 的
// prompt_tokens 合并了 cache token，并补 prompt_tokens_details.cached_tokens。
func TestResponseUsageCacheTokens(t *testing.T) {
	in := []byte(`{
		"id": "msg_1", "model": "claude", "role": "assistant",
		"content": [{"type":"text","text":"hi"}],
		"stop_reason": "end_turn",
		"usage": {"input_tokens": 10, "output_tokens": 5, "cache_read_input_tokens": 100, "cache_creation_input_tokens": 20}
	}`)
	out, err := Response(in, "anthropic", "openai")
	require.NoError(t, err)

	var oa map[string]any
	require.NoError(t, json.Unmarshal(out, &oa))
	usage := oa["usage"].(map[string]any)
	require.EqualValues(t, 130, usage["prompt_tokens"], "prompt=input+cache_read+cache_creation")
	require.EqualValues(t, 5, usage["completion_tokens"])
	details := usage["prompt_tokens_details"].(map[string]any)
	require.EqualValues(t, 100, details["cached_tokens"], "cached=cache_read")
}

// TestRequestDecodeInvalid 非法入站体：解析报错（对应 handler 预校验 400）。
func TestRequestDecodeInvalid(t *testing.T) {
	_, err := Request([]byte(`{not json`), "openai", "anthropic")
	require.Error(t, err)
	_, err = Request([]byte(`{"model":"m"}`), "openai", "anthropic")
	require.Error(t, err, "缺 messages 视为不合法")
	_, err = Request([]byte(`{"model":"m"}`), "anthropic", "openai")
	require.Error(t, err)
}

// TestRequestInvalidToolArguments 模型偶发吐出非法 arguments（非 JSON / 非对象）：
// 转换不报错，出站 input 归一为 {}（丢弃并记日志，不让本地 Marshal 失败）。
func TestRequestInvalidToolArguments(t *testing.T) {
	in := []byte(`{
		"model": "claude-3-5-sonnet",
		"messages": [
			{"role": "user", "content": "hi"},
			{"role": "assistant", "content": "", "tool_calls": [
				{"id": "call_1", "type": "function", "function": {"name": "f1", "arguments": "not json"}},
				{"id": "call_2", "type": "function", "function": {"name": "f2", "arguments": "123"}}
			]}
		]
	}`)
	out, err := Request(in, "openai", "anthropic")
	require.NoError(t, err)

	var an map[string]any
	require.NoError(t, json.Unmarshal(out, &an))
	msgs := an["messages"].([]any)
	require.Len(t, msgs, 2)
	blocks := msgs[1].(map[string]any)["content"].([]any)
	require.Len(t, blocks, 2)
	for _, b := range blocks {
		tu := b.(map[string]any)
		require.Equal(t, "tool_use", tu["type"])
		require.Equal(t, map[string]any{}, tu["input"], "非法 arguments 应归一为空对象")
	}
}

// TestRequestDegenerateMessages 退化输入（仅 system / content:null）：
// 转换后无有效对话消息 → 报错，handler 预校验（ValidateRequest）同样拦截对应 400。
func TestRequestDegenerateMessages(t *testing.T) {
	// 仅 system：解码阶段即报错，ValidateRequest 可拦截
	onlySystem := []byte(`{"model":"m","messages":[{"role":"system","content":"你是助手"}]}`)
	_, err := Request(onlySystem, "openai", "anthropic")
	require.Error(t, err)
	require.Contains(t, err.Error(), "无有效对话消息")
	require.Error(t, ValidateRequest(onlySystem, "openai"), "预校验须拦下仅 system 的退化输入")

	// content:null 的 user 消息：编码阶段防御报错（空 text 块被丢弃后 messages 为空）
	nullContent := []byte(`{"model":"m","messages":[{"role":"user","content":null}]}`)
	_, err = Request(nullContent, "openai", "anthropic")
	require.Error(t, err)
	require.Contains(t, err.Error(), "无有效对话消息")
}

// TestRequestConsecutiveSameRole openai 允许连续同角色消息，anthropic 要求 user/assistant
// 严格交替：编码时相邻同角色消息须合并进前一条。
func TestRequestConsecutiveSameRole(t *testing.T) {
	in := []byte(`{
		"model": "claude-3-5-sonnet",
		"messages": [
			{"role": "user", "content": "第一句"},
			{"role": "user", "content": "第二句"}
		]
	}`)
	out, err := Request(in, "openai", "anthropic")
	require.NoError(t, err)

	var an map[string]any
	require.NoError(t, json.Unmarshal(out, &an))
	msgs := an["messages"].([]any)
	require.Len(t, msgs, 1, "连续 user 消息应合并为一条")
	m := msgs[0].(map[string]any)
	require.Equal(t, "user", m["role"])
	blocks := m["content"].([]any)
	require.Len(t, blocks, 2)
	require.Equal(t, "第一句", blocks[0].(map[string]any)["text"])
	require.Equal(t, "第二句", blocks[1].(map[string]any)["text"])
}

// TestResponseAnthropicToOpenAI text+tool_use 响应 → openai chat.completion。
func TestResponseAnthropicToOpenAI(t *testing.T) {
	in := []byte(`{
		"id": "msg_01", "type": "message", "role": "assistant", "model": "claude-3-5-sonnet",
		"content": [
			{"type": "text", "text": "我来查"},
			{"type": "tool_use", "id": "toolu_1", "name": "get_weather", "input": {"city": "北京"}}
		],
		"stop_reason": "tool_use",
		"usage": {"input_tokens": 30, "output_tokens": 12}
	}`)
	out, err := Response(in, "anthropic", "openai")
	require.NoError(t, err)

	var oa map[string]any
	require.NoError(t, json.Unmarshal(out, &oa))
	require.Equal(t, "msg_01", oa["id"])
	require.Equal(t, "chat.completion", oa["object"])
	require.Equal(t, "claude-3-5-sonnet", oa["model"])
	ch := oa["choices"].([]any)[0].(map[string]any)
	require.Equal(t, "tool_calls", ch["finish_reason"])
	msg := ch["message"].(map[string]any)
	require.Equal(t, "assistant", msg["role"])
	require.Equal(t, "我来查", msg["content"])
	tc := msg["tool_calls"].([]any)[0].(map[string]any)
	require.Equal(t, "toolu_1", tc["id"])
	require.JSONEq(t, `{"city":"北京"}`, tc["function"].(map[string]any)["arguments"].(string))
	u := oa["usage"].(map[string]any)
	require.EqualValues(t, 30, u["prompt_tokens"])
	require.EqualValues(t, 12, u["completion_tokens"])
	require.EqualValues(t, 42, u["total_tokens"])
}

// TestResponseOpenAIToAnthropic openai chat.completion → anthropic message。
func TestResponseOpenAIToAnthropic(t *testing.T) {
	in := []byte(`{
		"id": "chatcmpl-1", "object": "chat.completion", "model": "gpt-4o",
		"choices": [{"index": 0, "finish_reason": "stop",
			"message": {"role": "assistant", "content": "你好"}}],
		"usage": {"prompt_tokens": 10, "completion_tokens": 5, "total_tokens": 15}
	}`)
	out, err := Response(in, "openai", "anthropic")
	require.NoError(t, err)

	var an map[string]any
	require.NoError(t, json.Unmarshal(out, &an))
	require.Equal(t, "chatcmpl-1", an["id"])
	require.Equal(t, "message", an["type"])
	require.Equal(t, "assistant", an["role"])
	require.Equal(t, "end_turn", an["stop_reason"])
	blk := an["content"].([]any)[0].(map[string]any)
	require.Equal(t, "text", blk["type"])
	require.Equal(t, "你好", blk["text"])
	u := an["usage"].(map[string]any)
	require.EqualValues(t, 10, u["input_tokens"])
	require.EqualValues(t, 5, u["output_tokens"])
}

// TestResponseDecodeInvalid 上游返回非本协议结构 → 报错（触发派发层 failover）。
func TestResponseDecodeInvalid(t *testing.T) {
	_, err := Response([]byte(`{"foo":"bar"}`), "openai", "anthropic")
	require.Error(t, err, "openai 响应缺 choices 视为不合法")
	_, err = Response([]byte(`{"foo":"bar"}`), "anthropic", "openai")
	require.Error(t, err, "anthropic 响应缺 content/stop_reason 视为不合法")
}

// TestResponseRefusal openai 拒答响应（content:null + refusal 字段）→ 转 anthropic 后
// 拒答文本须落到 text 块，不能产出空 content 消息。
func TestResponseRefusal(t *testing.T) {
	in := []byte(`{"id":"chatcmpl-r","model":"gpt-4o","choices":[{"index":0,"finish_reason":"stop","message":{"role":"assistant","content":null,"refusal":"我不能协助该请求"}}]}`)
	out, err := Response(in, "openai", "anthropic")
	require.NoError(t, err)

	var an map[string]any
	require.NoError(t, json.Unmarshal(out, &an))
	blocks := an["content"].([]any)
	require.Len(t, blocks, 1)
	blk := blocks[0].(map[string]any)
	require.Equal(t, "text", blk["type"])
	require.Equal(t, "我不能协助该请求", blk["text"])
}

// TestResponseNoUsage 无 usage 的 openai 响应 → 转 anthropic 后输出体不含 usage 字段。
func TestResponseNoUsage(t *testing.T) {
	in := []byte(`{"id":"chatcmpl-2","model":"gpt-4o","choices":[{"index":0,"finish_reason":"stop","message":{"role":"assistant","content":"好的"}}]}`)
	out, err := Response(in, "openai", "anthropic")
	require.NoError(t, err)

	var an map[string]any
	require.NoError(t, json.Unmarshal(out, &an))
	_, ok := an["usage"]
	require.False(t, ok, "无 usage 输入不应产出 usage 字段")
}

// TestErrorEnvelope 错误体只转信封；解析不出 message 时原样返回。
func TestErrorEnvelope(t *testing.T) {
	an := Error([]byte(`{"error":{"message":"invalid key","type":"authentication_error"},"type":"error"}`), "anthropic")
	require.JSONEq(t, `{"type":"error","error":{"type":"api_error","message":"invalid key"}}`, string(an))
	oa := Error([]byte(`{"type":"error","error":{"type":"overloaded_error","message":"overloaded"}}`), "openai")
	require.JSONEq(t, `{"error":{"message":"overloaded","type":"upstream_error"}}`, string(oa))
	raw := Error([]byte(`plain text`), "openai")
	require.Equal(t, `plain text`, string(raw))
}

// TestAnthropicCodecImageRoundTrip anthropic 图片块 decode→ir→encode 往返保真（base64 与 url 两形式）。
func TestAnthropicCodecImageRoundTrip(t *testing.T) {
	in := []byte(`{
		"model": "claude-3-5-sonnet",
		"max_tokens": 1024,
		"messages": [
			{"role": "user", "content": [
				{"type": "text", "text": "看图"},
				{"type": "image", "source": {"type": "base64", "media_type": "image/png", "data": "AAAA"}},
				{"type": "image", "source": {"type": "url", "url": "https://x/y.png"}}
			]}
		]
	}`)
	req, err := AnthropicCodec{}.DecodeRequest(in)
	require.NoError(t, err)
	require.Len(t, req.Messages, 1)
	blocks := req.Messages[0].Content
	require.Len(t, blocks, 3)
	require.Equal(t, "image", blocks[1].Type)
	require.Equal(t, "image/png", blocks[1].ImageMediaType)
	require.Equal(t, "AAAA", blocks[1].ImageData)
	require.Equal(t, "image", blocks[2].Type)
	require.Equal(t, "https://x/y.png", blocks[2].ImageURL)

	out, err := AnthropicCodec{}.EncodeRequest(req)
	require.NoError(t, err)
	var an map[string]any
	require.NoError(t, json.Unmarshal(out, &an))
	content := an["messages"].([]any)[0].(map[string]any)["content"].([]any)
	require.Len(t, content, 3)
	img0 := content[1].(map[string]any)
	require.Equal(t, "image", img0["type"])
	src0 := img0["source"].(map[string]any)
	require.Equal(t, "base64", src0["type"])
	require.Equal(t, "image/png", src0["media_type"])
	require.Equal(t, "AAAA", src0["data"])
	img1 := content[2].(map[string]any)
	src1 := img1["source"].(map[string]any)
	require.Equal(t, "url", src1["type"])
	require.Equal(t, "https://x/y.png", src1["url"])
}

// TestOpenAICodecImageRoundTrip openai image_url 块 decode→ir→encode 往返保真（data URI 与 http url）。
func TestOpenAICodecImageRoundTrip(t *testing.T) {
	in := []byte(`{
		"model": "gpt-4o",
		"messages": [
			{"role": "user", "content": [
				{"type": "text", "text": "看图"},
				{"type": "image_url", "image_url": {"url": "data:image/png;base64,AAAA"}},
				{"type": "image_url", "image_url": {"url": "https://x/y.png"}}
			]}
		]
	}`)
	req, err := OpenAICodec{}.DecodeRequest(in)
	require.NoError(t, err)
	blocks := req.Messages[0].Content
	require.Len(t, blocks, 3)
	require.Equal(t, "image", blocks[1].Type)
	require.Equal(t, "image/png", blocks[1].ImageMediaType)
	require.Equal(t, "AAAA", blocks[1].ImageData)
	require.Equal(t, "https://x/y.png", blocks[2].ImageURL)

	out, err := OpenAICodec{}.EncodeRequest(req)
	require.NoError(t, err)
	var oa map[string]any
	require.NoError(t, json.Unmarshal(out, &oa))
	var userMsg map[string]any
	for _, m := range oa["messages"].([]any) {
		mm := m.(map[string]any)
		if mm["role"] == "user" {
			userMsg = mm
		}
	}
	require.NotNil(t, userMsg)
	parts := userMsg["content"].([]any)
	require.Len(t, parts, 3)
	require.Equal(t, "text", parts[0].(map[string]any)["type"])
	p1 := parts[1].(map[string]any)
	require.Equal(t, "image_url", p1["type"])
	require.Equal(t, "data:image/png;base64,AAAA", p1["image_url"].(map[string]any)["url"])
	p2 := parts[2].(map[string]any)
	require.Equal(t, "https://x/y.png", p2["image_url"].(map[string]any)["url"])
}

// TestRequestImageAnthropicToOpenAI anthropic 图片请求 → openai：user content 块数组含 image_url（base64→data URI）。
func TestRequestImageAnthropicToOpenAI(t *testing.T) {
	in := []byte(`{"model":"m","max_tokens":100,"messages":[{"role":"user","content":[{"type":"text","text":"看图"},{"type":"image","source":{"type":"base64","media_type":"image/jpeg","data":"BBBB"}}]}]}`)
	out, err := Request(in, "anthropic", "openai")
	require.NoError(t, err)
	var oa map[string]any
	require.NoError(t, json.Unmarshal(out, &oa))
	msgs := oa["messages"].([]any)
	user := msgs[len(msgs)-1].(map[string]any)
	parts := user["content"].([]any)
	require.Len(t, parts, 2)
	require.Equal(t, "text", parts[0].(map[string]any)["type"])
	img := parts[1].(map[string]any)
	require.Equal(t, "image_url", img["type"])
	require.Equal(t, "data:image/jpeg;base64,BBBB", img["image_url"].(map[string]any)["url"])
}

// TestRequestImageOpenAIToAnthropic openai 图片请求 → anthropic：user content image 块 source=base64。
func TestRequestImageOpenAIToAnthropic(t *testing.T) {
	in := []byte(`{"model":"m","messages":[{"role":"user","content":[{"type":"text","text":"看图"},{"type":"image_url","image_url":{"url":"data:image/jpeg;base64,BBBB"}}]}]}`)
	out, err := Request(in, "openai", "anthropic")
	require.NoError(t, err)
	var an map[string]any
	require.NoError(t, json.Unmarshal(out, &an))
	content := an["messages"].([]any)[0].(map[string]any)["content"].([]any)
	require.Len(t, content, 2)
	img := content[1].(map[string]any)
	require.Equal(t, "image", img["type"])
	src := img["source"].(map[string]any)
	require.Equal(t, "base64", src["type"])
	require.Equal(t, "image/jpeg", src["media_type"])
	require.Equal(t, "BBBB", src["data"])
}
