// 管理面访问日志中间件。作者: ruan
package middleware

import (
	"log/slog"
	"slices"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/ruanun/simple-ai-gateway/internal/httpx"
)

// AccessLog 每个请求完成后记一行访问日志（方法/路径/状态码/耗时/客户端 IP，查询串见 httpx.ReqAttrs）。
//
// 只挂管理面与门户面：业务面 /v1 每次调用已落 RequestLog 表并支持详情回放，再记一行是重复放大；
// 这两个面此前没有任何请求级记录，操作在进程日志里完全无痕。
//
// 与 httpx.Err 的错误日志是互补的两条：这条回答「发生了什么请求」，那条回答「为什么失败」，
// 靠 RequestID 中间件注入的 request_id 串联（故本中间件须排在 RequestID 之后）。
// 字段口径由 httpx.ReqAttrs 单点提供，保证两条日志能按同名字段互相检索。
//
// skipPaths 排除高频轮询端点（前端有 10s 一次的熔断状态查询，全记会淹没真正的操作）。
// 跳过的只是访问记录：这些端点若失败，httpx.Err 的错误日志照常留下，故障不会被隐藏。
// 匹配的是路由模式（c.FullPath()）而非原始 URL，故参数化路由（/channels/:id/...）也能表达；
// 路径知识由装配层（server.go）传入，中间件本身不认识具体业务路由。
func AccessLog(skipPaths ...string) gin.HandlerFunc {
	return func(c *gin.Context) {
		// 级别被过滤时（如 log.level=warn）整条会被丢弃，提前退出连计时都省掉。
		if !slog.Default().Enabled(c.Request.Context(), slog.LevelInfo) {
			c.Next()
			return
		}
		start := time.Now()
		c.Next()
		// 路由匹配后才有 FullPath，故在 c.Next 之后判断；skipPaths 至多几条，线性比较即可。
		if slices.Contains(skipPaths, c.FullPath()) {
			return
		}
		attrs := append(httpx.ReqAttrs(c),
			slog.Int("status", c.Writer.Status()),
			slog.Int64("duration_ms", time.Since(start).Milliseconds()))
		// request_id 由 logger 的 ctxHandler 从请求 ctx 自动附加。
		slog.LogAttrs(c.Request.Context(), slog.LevelInfo, "请求完成", attrs...)
	}
}
