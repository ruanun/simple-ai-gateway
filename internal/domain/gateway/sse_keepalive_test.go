package gateway

import (
	"io"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestRelaySSEKeepaliveBeforeFirstLine(t *testing.T) {
	old := sseKeepaliveInterval
	sseKeepaliveInterval = 10 * time.Millisecond
	t.Cleanup(func() { sseKeepaliveInterval = old })

	pr, pw := io.Pipe()
	defer pw.Close()
	rec := httptest.NewRecorder()
	done := make(chan struct{})
	go func() {
		_, _ = relaySSE(rec, pr, false, nil)
		close(done)
	}()

	time.Sleep(35 * time.Millisecond)
	require.Contains(t, rec.Body.String(), ": keepalive", "首行到达前应发 SSE 注释心跳")

	_, err := pw.Write([]byte("data: {}\n\n"))
	require.NoError(t, err)
	require.NoError(t, pw.Close())
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("relaySSE 未在流关闭后返回")
	}
	require.Contains(t, rec.Body.String(), "data: {}")
}
