// Package convert 提供 chat↔messages 跨协议转换（P12c，枢纽式）：
// 入站 body → DecodeRequest → ir → EncodeRequest → 出站；响应/流式反向。
// 无法映射的字段丢弃并记一条日志，不阻断请求。
package convert

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"

	"github.com/ruanun/simple-ai-gateway/internal/convert/ir"
)

// StreamDecoder 把上游 SSE 字节流解码为 ir.StreamEvent 序列；流正常结束返回 io.EOF。
type StreamDecoder interface {
	Next() (*ir.StreamEvent, error)
}

// StreamEncoder 把 ir.StreamEvent 编码为客户端协议的 SSE 帧写出。
// Write 收到 done 事件时写出协议收尾帧；WriteError 写协议对应的错误事件（流中途失败用）。
type StreamEncoder interface {
	Write(ev *ir.StreamEvent) error
	WriteError(msg string) error
}

// Codec 单协议编解码器（请求/响应/流式六件套）。
type Codec interface {
	DecodeRequest(body []byte) (*ir.Request, error)
	EncodeRequest(req *ir.Request) ([]byte, error)
	DecodeResponse(body []byte) (*ir.Response, error)
	EncodeResponse(resp *ir.Response) ([]byte, error)
	NewStreamDecoder(r io.Reader) StreamDecoder
	NewStreamEncoder(w io.Writer) StreamEncoder
}

// codecs 协议 → codec 注册表。v2 增加 responses codec 即可打通 messages↔responses。
var codecs = map[string]Codec{
	"openai":    OpenAICodec{},
	"anthropic": AnthropicCodec{},
}

// Get 返回协议对应的 codec。
func Get(protocol string) (Codec, bool) {
	c, ok := codecs[protocol]
	return c, ok
}

// Supported 判断 from→to 是否可转换（两侧均有 codec 且协议不同）。
func Supported(from, to string) bool {
	if from == to {
		return false
	}
	_, okF := codecs[from]
	_, okT := codecs[to]
	return okF && okT
}

// Request 把 from 协议请求体转为 to 协议请求体。
func Request(body []byte, from, to string) ([]byte, error) {
	cf, ok := Get(from)
	if !ok {
		return nil, fmt.Errorf("convert: 未知入站协议 %q", from)
	}
	ct, ok := Get(to)
	if !ok {
		return nil, fmt.Errorf("convert: 未知出站协议 %q", to)
	}
	r, err := cf.DecodeRequest(body)
	if err != nil {
		return nil, err
	}
	return ct.EncodeRequest(r)
}

// Response 把 from 协议响应体转为 to 协议响应体（非流式成功响应）。
func Response(body []byte, from, to string) ([]byte, error) {
	cf, ok := Get(from)
	if !ok {
		return nil, fmt.Errorf("convert: 未知上游协议 %q", from)
	}
	ct, ok := Get(to)
	if !ok {
		return nil, fmt.Errorf("convert: 未知客户端协议 %q", to)
	}
	r, err := cf.DecodeResponse(body)
	if err != nil {
		return nil, err
	}
	return ct.EncodeResponse(r)
}

// ValidateRequest 校验 body 能否按 from 协议解析（handler 预检，失败对应 400）。
func ValidateRequest(body []byte, from string) error {
	cf, ok := Get(from)
	if !ok {
		return fmt.Errorf("convert: 未知协议 %q", from)
	}
	_, err := cf.DecodeRequest(body)
	return err
}

// Error 把上游错误体转为 to 协议的错误信封（只搬 message，不走 ir）。
// 两种协议错误体顶层都有 error 对象；解析不出 message 时原样返回。
func Error(body []byte, to string) []byte {
	var probe struct {
		Error *struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	if json.Unmarshal(body, &probe) != nil || probe.Error == nil || probe.Error.Message == "" {
		return body
	}
	msg := probe.Error.Message
	if to == "anthropic" {
		b, _ := json.Marshal(map[string]any{"type": "error", "error": map[string]any{"type": "api_error", "message": msg}})
		return b
	}
	b, _ := json.Marshal(map[string]any{"error": map[string]any{"message": msg, "type": "upstream_error"}})
	return b
}

// rawOrEmptyObj 把空/null/非法 JSON/非对象的 RawMessage 归一为 {}
// （anthropic tool input / openai arguments 都要求合法 JSON 对象；
// 模型偶发吐出畸形 arguments 时丢弃并记日志，不让本地 Marshal 失败）。
func rawOrEmptyObj(r json.RawMessage) json.RawMessage {
	t := bytes.TrimSpace(r)
	if len(t) == 0 || bytes.Equal(t, []byte("null")) {
		return json.RawMessage("{}")
	}
	if !json.Valid(t) || t[0] != '{' {
		slog.Warn("convert: 丢弃非法工具入参(非 JSON 对象)", "input", fmt.Sprintf("%.60s", t))
		return json.RawMessage("{}")
	}
	return r
}

// idOr 空 id 回退默认值（转换生成的响应/事件必须带 id）。
func idOr(id, def string) string {
	if id == "" {
		return def
	}
	return id
}
