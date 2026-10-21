package convert

import (
	"encoding/json"
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/ruanun/simple-ai-gateway/internal/convert/ir"
	"github.com/stretchr/testify/require"
)

// drainEvents 读完解码器全部事件（done 后应返回 io.EOF）。
func drainEvents(t *testing.T, d StreamDecoder) []*ir.StreamEvent {
	t.Helper()
	var evs []*ir.StreamEvent
	for {
		ev, err := d.Next()
		if err == io.EOF {
			return evs
		}
		require.NoError(t, err)
		evs = append(evs, ev)
	}
}

// anthropicSSE 真实形态的 anthropic 流样本：文本块 + 工具块 + usage。
const anthropicSSE = "event: message_start\n" +
	`data: {"type":"message_start","message":{"id":"msg_01","type":"message","role":"assistant","model":"claude-3-5-sonnet","content":[],"usage":{"input_tokens":25,"output_tokens":1}}}` + "\n\n" +
	"event: content_block_start\n" +
	`data: {"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}` + "\n\n" +
	"event: ping\n" + `data: {"type":"ping"}` + "\n\n" +
	"event: content_block_delta\n" +
	`data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"我查一下"}}` + "\n\n" +
	"event: content_block_stop\n" + `data: {"type":"content_block_stop","index":0}` + "\n\n" +
	"event: content_block_start\n" +
	`data: {"type":"content_block_start","index":1,"content_block":{"type":"tool_use","id":"toolu_1","name":"get_weather","input":{}}}` + "\n\n" +
	"event: content_block_delta\n" +
	`data: {"type":"content_block_delta","index":1,"delta":{"type":"input_json_delta","partial_json":"{\"city\":"}}` + "\n\n" +
	"event: content_block_delta\n" +
	`data: {"type":"content_block_delta","index":1,"delta":{"type":"input_json_delta","partial_json":"\"北京\"}"}}` + "\n\n" +
	"event: content_block_stop\n" + `data: {"type":"content_block_stop","index":1}` + "\n\n" +
	"event: message_delta\n" +
	`data: {"type":"message_delta","delta":{"stop_reason":"tool_use","stop_sequence":null},"usage":{"output_tokens":18}}` + "\n\n" +
	"event: message_stop\n" + `data: {"type":"message_stop"}` + "\n\n"

func TestAnthropicStreamDecoder(t *testing.T) {
	d := AnthropicCodec{}.NewStreamDecoder(strings.NewReader(anthropicSSE))
	evs := drainEvents(t, d)
	// start, text_delta, tool_start, tool_delta×2, finish, done（ping/block_start(text)/block_stop 无事件）
	require.Len(t, evs, 7)
	require.Equal(t, "start", evs[0].Type)
	require.Equal(t, "msg_01", evs[0].ID)
	require.Equal(t, "claude-3-5-sonnet", evs[0].Model)
	require.True(t, evs[0].HasUsage)
	require.Equal(t, 25, evs[0].Usage.InputTokens)
	require.Equal(t, "text_delta", evs[1].Type)
	require.Equal(t, "我查一下", evs[1].Text)
	require.Equal(t, "tool_start", evs[2].Type)
	require.Equal(t, 0, evs[2].Index, "工具序号从 0 起（与 anthropic 块索引 1 解耦）")
	require.Equal(t, "toolu_1", evs[2].ToolID)
	require.Equal(t, "get_weather", evs[2].ToolName)
	require.Equal(t, "tool_delta", evs[3].Type)
	require.Equal(t, `{"city":`, evs[3].ArgsDelta)
	require.Equal(t, "finish", evs[5].Type)
	require.Equal(t, "tool_use", evs[5].StopReason)
	require.True(t, evs[5].HasUsage)
	require.Equal(t, 18, evs[5].Usage.OutputTokens)
	require.Equal(t, "done", evs[6].Type)
}

// openaiSSE 真实形态的 openai chunk 流样本：role 块、文本、工具调用、finish、usage 末块。
const openaiSSE = `data: {"id":"chatcmpl-1","object":"chat.completion.chunk","model":"gpt-4o","choices":[{"index":0,"delta":{"role":"assistant","content":""},"finish_reason":null}]}` + "\n\n" +
	`data: {"id":"chatcmpl-1","object":"chat.completion.chunk","model":"gpt-4o","choices":[{"index":0,"delta":{"content":"我查一下"},"finish_reason":null}]}` + "\n\n" +
	`data: {"id":"chatcmpl-1","object":"chat.completion.chunk","model":"gpt-4o","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"call_1","type":"function","function":{"name":"get_weather","arguments":""}}]},"finish_reason":null}]}` + "\n\n" +
	`data: {"id":"chatcmpl-1","object":"chat.completion.chunk","model":"gpt-4o","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"function":{"arguments":"{\"city\":\"北京\"}"}}]},"finish_reason":null}]}` + "\n\n" +
	`data: {"id":"chatcmpl-1","object":"chat.completion.chunk","model":"gpt-4o","choices":[{"index":0,"delta":{},"finish_reason":"tool_calls"}]}` + "\n\n" +
	`data: {"id":"chatcmpl-1","object":"chat.completion.chunk","model":"gpt-4o","choices":[],"usage":{"prompt_tokens":25,"completion_tokens":18,"total_tokens":43}}` + "\n\n" +
	"data: [DONE]\n\n"

func TestOpenAIStreamDecoder(t *testing.T) {
	d := OpenAICodec{}.NewStreamDecoder(strings.NewReader(openaiSSE))
	evs := drainEvents(t, d)
	// start, text_delta, tool_start, tool_delta, finish, usage, done
	require.Len(t, evs, 7)
	require.Equal(t, "start", evs[0].Type)
	require.Equal(t, "chatcmpl-1", evs[0].ID)
	require.Equal(t, "gpt-4o", evs[0].Model)
	require.Equal(t, "text_delta", evs[1].Type)
	require.Equal(t, "我查一下", evs[1].Text)
	require.Equal(t, "tool_start", evs[2].Type)
	require.Equal(t, "call_1", evs[2].ToolID)
	require.Equal(t, "tool_delta", evs[3].Type)
	require.JSONEq(t, `{"city":"北京"}`, evs[3].ArgsDelta)
	require.Equal(t, "finish", evs[4].Type)
	require.Equal(t, "tool_use", evs[4].StopReason)
	require.Equal(t, "usage", evs[5].Type)
	require.True(t, evs[5].HasUsage)
	require.Equal(t, 25, evs[5].Usage.InputTokens)
	require.Equal(t, 18, evs[5].Usage.OutputTokens)
	require.Equal(t, "done", evs[6].Type)
}

// TestStreamDecoderGarbage 上游发非 JSON data → 解码报错（派发层按流失败处理）。
func TestStreamDecoderGarbage(t *testing.T) {
	d := OpenAICodec{}.NewStreamDecoder(strings.NewReader("data: {broken\n\n"))
	_, err := d.Next()
	require.Error(t, err)

	d2 := AnthropicCodec{}.NewStreamDecoder(strings.NewReader("event: message_start\ndata: {broken\n\n"))
	_, err = d2.Next()
	require.Error(t, err)
}

// TestAnthropicStreamDecoderUpstreamError 上游 error 事件 → 解码报错。
func TestAnthropicStreamDecoderUpstreamError(t *testing.T) {
	sse := "event: error\ndata: {\"type\":\"error\",\"error\":{\"type\":\"overloaded_error\",\"message\":\"overloaded\"}}\n\n"
	d := AnthropicCodec{}.NewStreamDecoder(strings.NewReader(sse))
	_, err := d.Next()
	require.Error(t, err)
	require.Contains(t, err.Error(), "overloaded")
}

// brokenReader 先交出预置字节，读尽后返回网络错误（模拟中途断流）。
type brokenReader struct {
	r   io.Reader
	err error
}

func (b *brokenReader) Read(p []byte) (int, error) {
	n, err := b.r.Read(p)
	if err == io.EOF {
		return n, b.err
	}
	return n, err
}

// TestStreamDecoderMidStreamDisconnect 网络中途断流 → 返回非 io.EOF 的错误，
// 不得伪装成正常收尾（否则截断响应被当成功计费）。
func TestStreamDecoderMidStreamDisconnect(t *testing.T) {
	partial := "event: message_start\n" +
		`data: {"type":"message_start","message":{"id":"msg_01","model":"claude-3-5-sonnet","usage":{"input_tokens":25}}}` + "\n\n" +
		"event: content_block_delta\n" +
		`data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"半截"}}` + "\n\n"
	r := &brokenReader{r: strings.NewReader(partial), err: errors.New("connection reset")}
	d := AnthropicCodec{}.NewStreamDecoder(r)

	ev, err := d.Next()
	require.NoError(t, err)
	require.Equal(t, "start", ev.Type)
	ev, err = d.Next()
	require.NoError(t, err)
	require.Equal(t, "text_delta", ev.Type)

	_, err = d.Next()
	require.Error(t, err)
	require.NotErrorIs(t, err, io.EOF, "断流不得折叠为 io.EOF")
	require.Contains(t, err.Error(), "读取上游流失败")
}

// TestAnthropicStreamDecoderOrphanToolDelta 未见 tool_use 起始的孤儿 input_json_delta → 丢弃，
// 不得错映射到第一个工具（Index=0）。
func TestAnthropicStreamDecoderOrphanToolDelta(t *testing.T) {
	sse := "event: content_block_delta\n" +
		`data: {"type":"content_block_delta","index":5,"delta":{"type":"input_json_delta","partial_json":"{\"x\":1}"}}` + "\n\n" +
		"event: message_stop\n" + `data: {"type":"message_stop"}` + "\n\n"
	d := AnthropicCodec{}.NewStreamDecoder(strings.NewReader(sse))
	evs := drainEvents(t, d)
	require.Len(t, evs, 1, "孤儿 tool_delta 应被跳过")
	require.Equal(t, "done", evs[0].Type)
}

// TestAnthropicStreamDecoderCacheTokens message_start 缓存字段进入 ir.Usage（保持 anthropic 原生语义：InputTokens 不含缓存，P12d）。
func TestAnthropicStreamDecoderCacheTokens(t *testing.T) {
	sse := "event: message_start\n" +
		`data: {"type":"message_start","message":{"id":"msg_1","model":"m","usage":{"input_tokens":100,"cache_creation_input_tokens":200,"cache_read_input_tokens":700}}}` + "\n\n" +
		"event: message_stop\n" + `data: {"type":"message_stop"}` + "\n\n"
	d := AnthropicCodec{}.NewStreamDecoder(strings.NewReader(sse))
	evs := drainEvents(t, d)
	require.Equal(t, "start", evs[0].Type)
	require.True(t, evs[0].HasUsage)
	require.Equal(t, 100, evs[0].Usage.InputTokens, "ir 保持原生语义,不做归一")
	require.Equal(t, 700, evs[0].Usage.CacheReadTokens)
	require.Equal(t, 200, evs[0].Usage.CacheCreationTokens)
}

// pipe 用 from 解码器读 sse，逐事件写入 to 编码器，返回输出字节（模拟 RelayConvert 主循环）。
func pipe(t *testing.T, sse, from, to string) string {
	t.Helper()
	cf, _ := Get(from)
	ct, _ := Get(to)
	var buf strings.Builder
	dec := cf.NewStreamDecoder(strings.NewReader(sse))
	enc := ct.NewStreamEncoder(&buf)
	for {
		ev, err := dec.Next()
		if err == io.EOF {
			break
		}
		require.NoError(t, err)
		require.NoError(t, enc.Write(ev))
	}
	return buf.String()
}

// parseSSE 把 SSE 文本拆成 (event,data) 序列（event 可空）。
func parseSSE(t *testing.T, s string) [][2]string {
	t.Helper()
	var out [][2]string
	var event string
	for _, line := range strings.Split(s, "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "event:") {
			event = strings.TrimSpace(strings.TrimPrefix(line, "event:"))
		} else if strings.HasPrefix(line, "data:") {
			out = append(out, [2]string{event, strings.TrimSpace(strings.TrimPrefix(line, "data:"))})
			event = ""
		}
	}
	return out
}

// TestStreamAnthropicToOpenAI anthropic 上游流 → openai 客户端流（黄金对拍）。
func TestStreamAnthropicToOpenAI(t *testing.T) {
	out := pipe(t, anthropicSSE, "anthropic", "openai")
	frames := parseSSE(t, out)
	// role 块, text, tool_start, tool_delta×2, finish, usage 块, [DONE]
	require.Len(t, frames, 8)
	require.Equal(t, "[DONE]", frames[7][1])

	var first map[string]any
	require.NoError(t, json.Unmarshal([]byte(frames[0][1]), &first))
	require.Equal(t, "chat.completion.chunk", first["object"])
	require.Equal(t, "msg_01", first["id"])
	require.Equal(t, "claude-3-5-sonnet", first["model"])
	delta0 := first["choices"].([]any)[0].(map[string]any)["delta"].(map[string]any)
	require.Equal(t, "assistant", delta0["role"])

	var textChunk map[string]any
	require.NoError(t, json.Unmarshal([]byte(frames[1][1]), &textChunk))
	require.Equal(t, "我查一下", textChunk["choices"].([]any)[0].(map[string]any)["delta"].(map[string]any)["content"])

	var toolStart map[string]any
	require.NoError(t, json.Unmarshal([]byte(frames[2][1]), &toolStart))
	tc := toolStart["choices"].([]any)[0].(map[string]any)["delta"].(map[string]any)["tool_calls"].([]any)[0].(map[string]any)
	require.Equal(t, "toolu_1", tc["id"])
	require.Equal(t, "get_weather", tc["function"].(map[string]any)["name"])
	require.EqualValues(t, 0, tc["index"])

	var finish map[string]any
	require.NoError(t, json.Unmarshal([]byte(frames[5][1]), &finish))
	require.Equal(t, "tool_calls", finish["choices"].([]any)[0].(map[string]any)["finish_reason"])

	// usage 附加块（include_usage 语义）：input 来自 message_start，output 来自 message_delta
	var usageChunk map[string]any
	require.NoError(t, json.Unmarshal([]byte(frames[6][1]), &usageChunk))
	u := usageChunk["usage"].(map[string]any)
	require.EqualValues(t, 25, u["prompt_tokens"])
	require.EqualValues(t, 18, u["completion_tokens"])
	require.EqualValues(t, 43, u["total_tokens"])
	require.Empty(t, usageChunk["choices"], "usage 块 choices 为空数组")
}

// TestStreamReasoningAndCacheUsage 验证 anthropic→openai 流式转换：thinking_delta 转成
// delta.reasoning_content（E），usage 末块 prompt_tokens 合并 cache 并补 cached_tokens（F）。
func TestStreamReasoningAndCacheUsage(t *testing.T) {
	sse := "event: message_start\n" +
		`data: {"type":"message_start","message":{"id":"msg_1","model":"claude","usage":{"input_tokens":10,"cache_read_input_tokens":100,"cache_creation_input_tokens":20,"output_tokens":1}}}` + "\n\n" +
		"event: content_block_start\n" +
		`data: {"type":"content_block_start","index":0,"content_block":{"type":"thinking"}}` + "\n\n" +
		"event: content_block_delta\n" +
		`data: {"type":"content_block_delta","index":0,"delta":{"type":"thinking_delta","thinking":"想一想"}}` + "\n\n" +
		"event: content_block_stop\n" + `data: {"type":"content_block_stop","index":0}` + "\n\n" +
		"event: content_block_start\n" +
		`data: {"type":"content_block_start","index":1,"content_block":{"type":"text","text":""}}` + "\n\n" +
		"event: content_block_delta\n" +
		`data: {"type":"content_block_delta","index":1,"delta":{"type":"text_delta","text":"答案"}}` + "\n\n" +
		"event: message_delta\n" +
		`data: {"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":5}}` + "\n\n" +
		"event: message_stop\n" + `data: {"type":"message_stop"}` + "\n\n"
	out := pipe(t, sse, "anthropic", "openai")
	frames := parseSSE(t, out)

	var reasoning string
	var usage map[string]any
	for _, f := range frames {
		if f[1] == "[DONE]" {
			continue
		}
		var m map[string]any
		require.NoError(t, json.Unmarshal([]byte(f[1]), &m))
		if chs, ok := m["choices"].([]any); ok && len(chs) > 0 {
			if d, ok := chs[0].(map[string]any)["delta"].(map[string]any); ok {
				if rc, ok := d["reasoning_content"].(string); ok {
					reasoning += rc
				}
			}
		}
		if u, ok := m["usage"].(map[string]any); ok {
			usage = u
		}
	}
	require.Equal(t, "想一想", reasoning, "thinking_delta 应转成 delta.reasoning_content")
	require.NotNil(t, usage, "应有 usage 末块")
	require.EqualValues(t, 130, usage["prompt_tokens"], "流式 usage prompt 合并 cache")
	details := usage["prompt_tokens_details"].(map[string]any)
	require.EqualValues(t, 100, details["cached_tokens"])
}

// TestStreamOpenAIToAnthropic openai 上游流 → anthropic 客户端流（黄金对拍）。
func TestStreamOpenAIToAnthropic(t *testing.T) {
	out := pipe(t, openaiSSE, "openai", "anthropic")
	frames := parseSSE(t, out)
	// message_start, content_block_start(text), content_block_delta(text),
	// content_block_stop(text), content_block_start(tool), content_block_delta(tool),
	// content_block_stop(tool), message_delta, message_stop
	require.Len(t, frames, 9)
	require.Equal(t, "message_start", frames[0][0])
	require.Equal(t, "content_block_start", frames[1][0])
	require.Equal(t, "content_block_delta", frames[2][0])
	require.Equal(t, "content_block_stop", frames[3][0])
	require.Equal(t, "content_block_start", frames[4][0])
	require.Equal(t, "content_block_delta", frames[5][0])
	require.Equal(t, "content_block_stop", frames[6][0])
	require.Equal(t, "message_delta", frames[7][0])
	require.Equal(t, "message_stop", frames[8][0])

	var ms map[string]any
	require.NoError(t, json.Unmarshal([]byte(frames[0][1]), &ms))
	msg := ms["message"].(map[string]any)
	require.Equal(t, "chatcmpl-1", msg["id"])
	require.Equal(t, "assistant", msg["role"])

	var textStart map[string]any
	require.NoError(t, json.Unmarshal([]byte(frames[1][1]), &textStart))
	require.EqualValues(t, 0, textStart["index"])
	require.Equal(t, "text", textStart["content_block"].(map[string]any)["type"])

	var toolStart map[string]any
	require.NoError(t, json.Unmarshal([]byte(frames[4][1]), &toolStart))
	require.EqualValues(t, 1, toolStart["index"], "工具块索引接在文本块之后")
	cb := toolStart["content_block"].(map[string]any)
	require.Equal(t, "tool_use", cb["type"])
	require.Equal(t, "call_1", cb["id"])
	require.Equal(t, "get_weather", cb["name"])

	var toolDelta map[string]any
	require.NoError(t, json.Unmarshal([]byte(frames[5][1]), &toolDelta))
	d := toolDelta["delta"].(map[string]any)
	require.Equal(t, "input_json_delta", d["type"])
	require.JSONEq(t, `{"city":"北京"}`, d["partial_json"].(string))

	// message_delta 带 stop_reason + usage（openai usage 末块缓冲到 done 一并写出）
	var md map[string]any
	require.NoError(t, json.Unmarshal([]byte(frames[7][1]), &md))
	require.Equal(t, "tool_use", md["delta"].(map[string]any)["stop_reason"])
	u := md["usage"].(map[string]any)
	require.EqualValues(t, 25, u["input_tokens"])
	require.EqualValues(t, 18, u["output_tokens"])
}

// TestStreamDoneOnlyToAnthropic openai 上游只有 [DONE] 的空流 → anthropic 客户端
// 必须先合成 message_start 再收尾（否则 anthropic SDK 收到孤儿终止帧会报错）。
func TestStreamDoneOnlyToAnthropic(t *testing.T) {
	out := pipe(t, "data: [DONE]\n\n", "openai", "anthropic")
	frames := parseSSE(t, out)
	require.Len(t, frames, 3)
	require.Equal(t, "message_start", frames[0][0])
	require.Equal(t, "message_delta", frames[1][0])
	require.Equal(t, "message_stop", frames[2][0])

	var ms map[string]any
	require.NoError(t, json.Unmarshal([]byte(frames[0][1]), &ms))
	msg := ms["message"].(map[string]any)
	require.Equal(t, "msg_convert", msg["id"], "合成 message_start 的 id 用兜底值")
	require.Contains(t, msg, "stop_sequence", "message 须含 stop_sequence 字段")
}

// TestStreamTextToolTextAlternation 直接驱动 anthropic 编码器验证块索引状态机的
// 文本→工具→文本交替路径：两个文本块(index 0/2)、工具块(index 1)、三次 stop。
func TestStreamTextToolTextAlternation(t *testing.T) {
	var buf strings.Builder
	enc := AnthropicCodec{}.NewStreamEncoder(&buf)
	evs := []*ir.StreamEvent{
		{Type: "start", ID: "msg_x", Model: "m"},
		{Type: "text_delta", Text: "a"},
		{Type: "tool_start", Index: 0, ToolID: "toolu_1", ToolName: "f"},
		{Type: "tool_delta", Index: 0, ArgsDelta: `{"x":1}`},
		{Type: "text_delta", Text: "b"},
		{Type: "done"},
	}
	for _, ev := range evs {
		require.NoError(t, enc.Write(ev))
	}
	frames := parseSSE(t, buf.String())
	// message_start, cbs(text0), cbd(text0), cb_stop(0), cbs(tool1), cbd(tool1),
	// cb_stop(1), cbs(text2), cbd(text2), cb_stop(2), message_delta, message_stop
	require.Len(t, frames, 12)

	type fr struct {
		event string
		body  map[string]any
	}
	parsed := make([]fr, 0, len(frames))
	for _, f := range frames {
		var m map[string]any
		require.NoError(t, json.Unmarshal([]byte(f[1]), &m))
		parsed = append(parsed, fr{event: f[0], body: m})
	}

	var textStarts, stops []float64
	var toolStartIdx float64 = -1
	for _, p := range parsed {
		switch p.event {
		case "content_block_start":
			cb := p.body["content_block"].(map[string]any)
			if cb["type"] == "text" {
				textStarts = append(textStarts, p.body["index"].(float64))
			} else if cb["type"] == "tool_use" {
				toolStartIdx = p.body["index"].(float64)
			}
		case "content_block_stop":
			stops = append(stops, p.body["index"].(float64))
		}
	}
	require.Equal(t, []float64{0, 2}, textStarts, "两个文本块索引 0 与 2")
	require.EqualValues(t, 1, toolStartIdx, "工具块索引 1")
	require.Equal(t, []float64{0, 1, 2}, stops, "三次 content_block_stop 依次关块")
	require.Equal(t, "message_delta", parsed[10].event)
	require.Equal(t, "message_stop", parsed[11].event)
}

// TestStreamEncoderWriteError 两侧编码器的错误帧格式。
func TestStreamEncoderWriteError(t *testing.T) {
	var oaBuf strings.Builder
	require.NoError(t, OpenAICodec{}.NewStreamEncoder(&oaBuf).WriteError("上游中断"))
	require.Contains(t, oaBuf.String(), `"message":"上游中断"`)
	require.Contains(t, oaBuf.String(), "data: [DONE]")

	var anBuf strings.Builder
	require.NoError(t, AnthropicCodec{}.NewStreamEncoder(&anBuf).WriteError("上游中断"))
	require.Contains(t, anBuf.String(), "event: error")
	require.Contains(t, anBuf.String(), `"type":"api_error"`)
}

// TestStreamOpenAIReasoningToAnthropic openai 推理模型流（reasoning_content 增量）→ anthropic
// 客户端流：应产出 thinking 块（content_block_start type=thinking + thinking_delta），
// 思考块先于正文块且索引交替正确（此前反方向缺口：解码器不识别 reasoning_content、
// 编码器丢弃 reasoning_delta，思考内容被静默丢光）。
func TestStreamOpenAIReasoningToAnthropic(t *testing.T) {
	sse := `data: {"id":"chatcmpl-r1","object":"chat.completion.chunk","model":"deepseek-r1","choices":[{"index":0,"delta":{"role":"assistant","reasoning_content":"想一"},"finish_reason":null}]}` + "\n\n" +
		`data: {"id":"chatcmpl-r1","object":"chat.completion.chunk","model":"deepseek-r1","choices":[{"index":0,"delta":{"reasoning_content":"想"},"finish_reason":null}]}` + "\n\n" +
		`data: {"id":"chatcmpl-r1","object":"chat.completion.chunk","model":"deepseek-r1","choices":[{"index":0,"delta":{"content":"答案"},"finish_reason":null}]}` + "\n\n" +
		`data: {"id":"chatcmpl-r1","object":"chat.completion.chunk","model":"deepseek-r1","choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}` + "\n\n" +
		"data: [DONE]\n\n"
	out := pipe(t, sse, "openai", "anthropic")
	frames := parseSSE(t, out)

	var thinkingStart, textStart map[string]any
	var thinking string
	for _, f := range frames {
		var m map[string]any
		require.NoError(t, json.Unmarshal([]byte(f[1]), &m))
		switch f[0] {
		case "content_block_start":
			cb := m["content_block"].(map[string]any)
			if cb["type"] == "thinking" {
				thinkingStart = m
			} else if cb["type"] == "text" {
				textStart = m
			}
		case "content_block_delta":
			if d := m["delta"].(map[string]any); d["type"] == "thinking_delta" {
				thinking += d["thinking"].(string)
			}
		}
	}
	require.NotNil(t, thinkingStart, "应有 thinking 块 start")
	require.EqualValues(t, 0, thinkingStart["index"], "thinking 块索引 0（先于正文）")
	require.Equal(t, "想一想", thinking, "reasoning_content 增量应转成 thinking_delta")
	require.NotNil(t, textStart, "应有正文块 start")
	require.EqualValues(t, 1, textStart["index"], "正文块索引接在 thinking 块之后")
}

// TestResponseOpenAIReasoningToAnthropic 非流式对拍：openai 响应的 message.reasoning_content
// 应进入 ir.Reasoning 并编码为 anthropic thinking 块（置于正文之前）。
func TestResponseOpenAIReasoningToAnthropic(t *testing.T) {
	body := []byte(`{"id":"chatcmpl-r1","model":"deepseek-r1","choices":[{"message":{"role":"assistant","content":"答案","reasoning_content":"想一想"},"finish_reason":"stop"}],"usage":{"prompt_tokens":10,"completion_tokens":5}}`)
	r, err := OpenAICodec{}.DecodeResponse(body)
	require.NoError(t, err)
	require.Equal(t, "想一想", r.Reasoning, "reasoning_content 应进入 ir.Reasoning")

	out, err := AnthropicCodec{}.EncodeResponse(r)
	require.NoError(t, err)
	var m map[string]any
	require.NoError(t, json.Unmarshal(out, &m))
	blocks := m["content"].([]any)
	require.GreaterOrEqual(t, len(blocks), 2)
	b0 := blocks[0].(map[string]any)
	require.Equal(t, "thinking", b0["type"])
	require.Equal(t, "想一想", b0["thinking"])
	b1 := blocks[1].(map[string]any)
	require.Equal(t, "text", b1["type"])
	require.Equal(t, "答案", b1["text"])
}

// TestOpenAIStreamDecoderCacheReasoning openai usage 末块的明细(cached/reasoning)进入 ir.Usage。
func TestOpenAIStreamDecoderCacheReasoning(t *testing.T) {
	sse := `data: {"id":"chatcmpl-1","object":"chat.completion.chunk","model":"gpt-4o","choices":[{"index":0,"delta":{"content":"hi"},"finish_reason":null}]}` + "\n\n" +
		`data: {"id":"chatcmpl-1","object":"chat.completion.chunk","model":"gpt-4o","choices":[],"usage":{"prompt_tokens":1000,"completion_tokens":200,"total_tokens":1200,"prompt_tokens_details":{"cached_tokens":700},"completion_tokens_details":{"reasoning_tokens":50}}}` + "\n\n" +
		"data: [DONE]\n\n"
	d := OpenAICodec{}.NewStreamDecoder(strings.NewReader(sse))
	evs := drainEvents(t, d)
	var usage *ir.StreamEvent
	for _, ev := range evs {
		if ev.Type == "usage" {
			usage = ev
		}
	}
	require.NotNil(t, usage, "应有 usage 事件")
	require.True(t, usage.HasUsage)
	require.Equal(t, 1000, usage.Usage.InputTokens)
	require.Equal(t, 200, usage.Usage.OutputTokens)
	require.Equal(t, 700, usage.Usage.CacheReadTokens)
	require.Equal(t, 50, usage.Usage.ReasoningTokens)
}
