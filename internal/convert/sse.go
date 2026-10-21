package convert

import (
	"bufio"
	"bytes"
	"fmt"
	"io"
)

// sseScanner 逐条读取 SSE 消息：返回 (event 名, data 载荷)。
// openai 流没有 event 行（event 返回空串）；anthropic 每条带 event 行。
// 跳过空行/注释行；流正常读尽返回 io.EOF，读取失败（断流等）返回包装错误。
// 仅支持单行 data 载荷（openai/anthropic 的 data 都是单行 JSON，多行 data 拼接不支持）。
type sseScanner struct {
	r *bufio.Reader
}

func newSSEScanner(r io.Reader) *sseScanner {
	return &sseScanner{r: bufio.NewReader(r)}
}

// next 返回下一条含 data 的 SSE 消息。event 行必须先于同组 data 行出现（两种协议均如此）。
func (s *sseScanner) next() (event string, data []byte, err error) {
	for {
		line, rerr := s.r.ReadBytes('\n')
		t := bytes.TrimSpace(line)
		if bytes.HasPrefix(t, []byte("event:")) {
			event = string(bytes.TrimSpace(t[len("event:"):]))
		} else if bytes.HasPrefix(t, []byte("data:")) {
			return event, bytes.TrimSpace(t[len("data:"):]), nil
		}
		if rerr != nil {
			if rerr == io.EOF {
				return "", nil, io.EOF
			}
			// 网络断流等真实错误不得折叠为 EOF，否则截断响应会被当成功处理
			return "", nil, fmt.Errorf("convert: 读取上游流失败: %w", rerr)
		}
	}
}
