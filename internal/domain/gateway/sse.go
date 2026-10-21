package gateway

import (
	"bufio"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"hash"
	"io"
	"net/http"
	"time"

	"github.com/ruanun/simple-ai-gateway/internal/audit"
)

// captureLimit 审计捕获在途上限，测试可改小。
var captureLimit = audit.MaxCaptureBytes

// sseKeepaliveInterval 首行到达前的 SSE 注释心跳间隔；测试可调小。
var sseKeepaliveInterval = 15 * time.Second

// captureBuf 有界审计捕获缓冲：前 captureLimit 字节原样保留，超出部分丢弃但继续计长并算全文摘要，
// 使截断如实入审计元数据。摘要在首次溢出时才启动（补算已存前缀），未溢出的正常响应零额外开销——
// 其摘要仍由 audit.Submit 统一计算。
type captureBuf struct {
	buf   bytes.Buffer
	limit int
	total int
	h     hash.Hash // 首次溢出后非空
}

func newCaptureBuf() *captureBuf { return &captureBuf{limit: captureLimit} }

// Write 实现 io.Writer，恒成功（捕获是旁路，绝不影响客户端写出）。
func (b *captureBuf) Write(p []byte) (int, error) {
	b.total += len(p)
	room := b.limit - b.buf.Len()
	if b.h == nil && len(p) <= room {
		b.buf.Write(p)
		return len(p), nil
	}
	if b.h == nil {
		b.h = sha256.New()
		b.h.Write(b.buf.Bytes())
	}
	if room > 0 {
		b.buf.Write(p[:room])
	}
	b.h.Write(p)
	return len(p), nil
}

// result 返回捕获前缀与（若溢出）全文元数据。
func (b *captureBuf) result() ([]byte, *audit.Overflow) {
	if b.h == nil {
		return b.buf.Bytes(), nil
	}
	return b.buf.Bytes(), &audit.Overflow{OrigSize: b.total, SHA256: hex.EncodeToString(b.h.Sum(nil))}
}

// relaySSE 流转发原语：逐行把上游 SSE 流透传给客户端，onLine 回调供调用方旁路处理每行
// （提取 usage、维护 event 状态机等；可为 nil）；回调返回 true 表示该行应剥离
// （不写客户端、不入 captured），如注入产生的用量块。读错误（含 io.EOF）即停止；
// 响应头/状态码由调用方先写。
// capture=false 时不累积捕获缓冲、恒返回 nil——审计关闭的请求避免整条流驻留内存。
// capture=true 时捕获以 captureLimit 封顶，超限返回非空 Overflow。
func relaySSE(w http.ResponseWriter, r io.Reader, capture bool, onLine func(trimmed []byte) bool) ([]byte, *audit.Overflow) {
	fl, _ := w.(http.Flusher)
	var captured *captureBuf
	if capture {
		captured = newCaptureBuf()
	}
	reader := bufio.NewReader(r)
	// 读上游放到独立 goroutine：主循环才能在上游思考/排队、迟迟不吐首个 token 时
	// 周期性向客户端写 SSE 注释（": keepalive"），避免反代/客户端读超时。
	type sseRead struct {
		line []byte
		err  error
	}
	lines := make(chan sseRead, 1)
	go func() {
		for {
			line, err := reader.ReadBytes('\n')
			lines <- sseRead{line: line, err: err}
			if err != nil {
				return
			}
		}
	}()
	ticker := time.NewTicker(sseKeepaliveInterval)
	defer ticker.Stop()
	firstLine := true
readLoop:
	for {
		select {
		case msg := <-lines:
			if len(msg.line) > 0 {
				firstLine = false
				drop := false
				if onLine != nil {
					drop = onLine(bytes.TrimSpace(msg.line))
				}
				if !drop {
					_, _ = w.Write(msg.line)
					if captured != nil {
						_, _ = captured.Write(msg.line)
					}
					if fl != nil {
						fl.Flush()
					}
				}
			}
			if msg.err != nil {
				break readLoop
			}
		case <-ticker.C:
			if firstLine {
				_, _ = w.Write([]byte(": keepalive\n\n"))
				if fl != nil {
					fl.Flush()
				}
			}
		}
	}
	if captured == nil {
		return nil, nil
	}
	return captured.result()
}
