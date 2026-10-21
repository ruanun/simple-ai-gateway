package middleware

import (
	"net/http"

	"github.com/gin-gonic/gin"
)

// Recover 全局 panic 兜底：r.Use 注册于路由匹配之前，同时覆盖 /v1、/admin、/portal/api
// 三个面及静态资源，此时无法可靠判定协议/面以选择 httpx 或 v1 信封，故保持与业务面无关的
// 最简响应，不迁 httpx（全局 recover，不迁 httpx）。
func Recover() gin.HandlerFunc {
	return func(c *gin.Context) {
		defer func() {
			if r := recover(); r != nil {
				c.AbortWithStatusJSON(http.StatusInternalServerError, gin.H{"error": "internal error"})
			}
		}()
		c.Next()
	}
}
