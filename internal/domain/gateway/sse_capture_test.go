package gateway

import (
	"crypto/sha256"
	"encoding/hex"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// 审计捕获在传输过程中必须有硬上限：超限后停止累积、客户端照常收全量，
// 并给出全文原长与全文 SHA-256，供审计元数据如实记录截断。
func TestRelaySSECaptureBoundedInFlight(t *testing.T) {
	defer func(old int) { captureLimit = old }(captureLimit)
	captureLimit = 100

	var sb strings.Builder
	for i := 0; i < 50; i++ {
		sb.WriteString("data: {\"delta\":\"0123456789\"}\n\n")
	}
	full := sb.String()

	rec := httptest.NewRecorder()
	captured, overflow := relaySSE(rec, strings.NewReader(full), true, nil)
	require.Equal(t, full, rec.Body.String(), "客户端必须收到全量，不受审计上限影响")
	require.Equal(t, 100, len(captured))
	require.Equal(t, full[:100], string(captured))
	require.NotNil(t, overflow)
	require.Equal(t, len(full), overflow.OrigSize)
	sum := sha256.Sum256([]byte(full))
	require.Equal(t, hex.EncodeToString(sum[:]), overflow.SHA256, "摘要须覆盖全文而非截断前缀")
}

// 未超限时行为不变：全量捕获、无溢出元数据。
func TestRelaySSECaptureUnderLimit(t *testing.T) {
	body := "data: {\"a\":1}\n\ndata: [DONE]\n\n"
	rec := httptest.NewRecorder()
	captured, overflow := relaySSE(rec, strings.NewReader(body), true, nil)
	require.Equal(t, body, string(captured))
	require.Nil(t, overflow)
}

// 关闭捕获时不攒、不产生溢出元数据。
func TestRelaySSECaptureDisabled(t *testing.T) {
	defer func(old int) { captureLimit = old }(captureLimit)
	captureLimit = 1
	rec := httptest.NewRecorder()
	captured, overflow := relaySSE(rec, strings.NewReader("data: x\n\n"), false, nil)
	require.Nil(t, captured)
	require.Nil(t, overflow)
}

// Relay 流式路径须把在途截断元数据挂到 Result 上，供 Outcome→audit.Entry 透传。
func TestRelayStreamReportsCaptureOverflow(t *testing.T) {
	defer func(old int) { captureLimit = old }(captureLimit)
	captureLimit = 10
	body := "data: {\"delta\":\"hello world\"}\n\ndata: [DONE]\n\n"
	resp := &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {"text/event-stream"}},
		Body: io.NopCloser(strings.NewReader(body))}
	rec := httptest.NewRecorder()
	res, _, _, captured := Relay(rec, resp, true, false, true)
	require.Equal(t, body, rec.Body.String())
	require.Equal(t, 10, len(captured))
	require.NotNil(t, res.CaptureOverflow)
	require.Equal(t, len(body), res.CaptureOverflow.OrigSize)
}
