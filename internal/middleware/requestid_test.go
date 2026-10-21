package middleware

import (
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/ruanun/simple-ai-gateway/internal/logger"
)

func TestRequestIDSetsContextValue(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(RequestID())
	var got string
	r.GET("/x", func(c *gin.Context) {
		got = c.GetString(CtxRequestID)
		c.Status(200)
	})
	r.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("GET", "/x", nil))
	if len(got) != 32 { // audit.GenRequestID = 16 字节 hex = 32 字符
		t.Fatalf("request id not set or wrong length: %q", got)
	}
}

// request_id 同时注入 request.Context()，供 slog.XxxContext 日志自动带出（日志↔审计↔请求日志三方贯通）。
func TestRequestIDInjectsRequestContext(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(RequestID())
	var ginID, ctxID string
	r.GET("/x", func(c *gin.Context) {
		ginID = c.GetString(CtxRequestID)
		ctxID = logger.RequestIDFrom(c.Request.Context())
		c.Status(200)
	})
	r.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("GET", "/x", nil))
	if ctxID == "" || ctxID != ginID {
		t.Fatalf("request.Context 中的 request_id 应与 gin.Context 一致: ctx=%q gin=%q", ctxID, ginID)
	}
}

// 响应头 X-SAIG-Request-Id 与 Context 中的 request_id 一致（P11 诊断贯通）。
func TestRequestIDSetsResponseHeader(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(RequestID())
	var got string
	r.GET("/x", func(c *gin.Context) {
		got = c.GetString(CtxRequestID)
		c.Status(200)
	})
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest("GET", "/x", nil))
	if h := w.Header().Get("X-SAIG-Request-Id"); h == "" || h != got {
		t.Fatalf("response header mismatch: header=%q ctx=%q", h, got)
	}
}
