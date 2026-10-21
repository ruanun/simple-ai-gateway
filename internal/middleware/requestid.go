package middleware

import (
	"github.com/gin-gonic/gin"
	"github.com/ruanun/simple-ai-gateway/internal/audit"
	"github.com/ruanun/simple-ai-gateway/internal/logger"
)

// CtxRequestID 是请求标识在 gin.Context 中的键。
const CtxRequestID = "request_id"

// RequestID 为每个入站请求生成一个 16 字节 hex 标识写入 Context，
// 供请求日志与审计记录共享同一标识（用于日志→审计正文关联）。
// 同时注入 request.Context()，slog.XxxContext 日志自动带出 request_id 字段；
// 并写入 X-SAIG-Request-Id 响应头，客户端可凭此 ID 到日志/审计中定位本次请求（P11）。
func RequestID() gin.HandlerFunc {
	return func(c *gin.Context) {
		id := audit.GenRequestID()
		c.Set(CtxRequestID, id)
		c.Request = c.Request.WithContext(logger.WithRequestID(c.Request.Context(), id))
		c.Header("X-SAIG-Request-Id", id)
		c.Next()
	}
}
