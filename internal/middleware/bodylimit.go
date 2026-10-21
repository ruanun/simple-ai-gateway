package middleware

import (
	"net/http"

	"github.com/gin-gonic/gin"
)

// DefaultMaxRequestBody 入站请求体默认上限（32 MiB）：足够容纳长上下文与少量内联图片的
// base64，同时给未鉴权端点（登录/注册）兜底，防止超大 body 撑爆内存。
const DefaultMaxRequestBody int64 = 32 << 20

// BodyLimit 用 http.MaxBytesReader 包裹请求体，把单请求可读字节封顶到 max。
// 超限时后续 Read 返回错误、body 被截断，内存占用有界（DoS 兜底）。max<=0 时不限。
func BodyLimit(max int64) gin.HandlerFunc {
	return func(c *gin.Context) {
		if max > 0 && c.Request.Body != nil {
			c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, max)
		}
		c.Next()
	}
}
