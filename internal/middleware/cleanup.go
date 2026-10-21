package middleware

import "github.com/gin-gonic/gin"

const ctxCleanups = "saig_cleanups"

// AddCleanup 登记一个在本次请求结束（含 panic）时执行的清理函数，如归还并发槽位。
// 业务 handler 可能在多处提前 return，集中在中间件里统一执行，避免漏还。
func AddCleanup(c *gin.Context, f func()) {
	var fs []func()
	if v, ok := c.Get(ctxCleanups); ok {
		fs, _ = v.([]func())
	}
	c.Set(ctxCleanups, append(fs, f))
}

// RunCleanups 在后续处理链结束后按登记的逆序执行清理函数；panic 时同样执行（defer），
// 由外层 Recover 继续处理 panic。
func RunCleanups() gin.HandlerFunc {
	return func(c *gin.Context) {
		defer func() {
			if v, ok := c.Get(ctxCleanups); ok {
				fs, _ := v.([]func())
				for i := len(fs) - 1; i >= 0; i-- {
					fs[i]()
				}
			}
		}()
		c.Next()
	}
}
