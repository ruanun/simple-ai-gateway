package middleware

import (
	"github.com/gin-gonic/gin"
	"github.com/ruanun/simple-ai-gateway/internal/v1proto"
)

// abortV1Error 中间件专用：按路径推导协议回写 /v1 信封并中止后续 handler（实现本体在 v1proto）。
func abortV1Error(c *gin.Context, status int, msg string) {
	v1proto.Respond(c, status, v1proto.ProtoForPath(c.Request.URL.Path), msg)
	c.Abort()
}
