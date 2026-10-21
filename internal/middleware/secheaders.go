package middleware

import "github.com/gin-gonic/gin"

// contentSecurityPolicy 站点 CSP。管理端与门户同源、JWT 存 localStorage，任何一处 XSS 都能同时读走
// 两边的 token，CSP 是最后一道兜底：
//   - script-src 只放行同源与 Cloudflare Turnstile（门户注册人机验证），不放行内联脚本
//     （入口 HTML 的主题防闪脚本已外置为 /theme-init.js）；
//   - style-src 放行 'unsafe-inline'：React style 属性与 Radix 定位依赖内联样式，样式注入危害远低于脚本；
//     另放行 Google Fonts（web/src/index.css 引入 Fira 字体：样式表 fonts.googleapis.com、字体文件 fonts.gstatic.com）；
//   - img-src 放行 data:（图形验证码为 data URI）；
//   - frame-ancestors 'none' 防点击劫持（等价且优先于 X-Frame-Options）。
const contentSecurityPolicy = "default-src 'self'; " +
	"script-src 'self' https://challenges.cloudflare.com; " +
	"style-src 'self' 'unsafe-inline' https://fonts.googleapis.com; " +
	"img-src 'self' data:; " +
	"font-src 'self' data: https://fonts.gstatic.com; " +
	"connect-src 'self'; " +
	"frame-src https://challenges.cloudflare.com; " +
	"frame-ancestors 'none'; " +
	"object-src 'none'; " +
	"base-uri 'self'; " +
	"form-action 'self'"

// SecurityHeaders 为所有响应设置浏览器安全头。对 /v1、/admin 等 JSON 接口无副作用。
// HSTS 不在此设置：TLS 通常在反代终结，由反代按实际域名配置（见部署指南）。
func SecurityHeaders() gin.HandlerFunc {
	return func(c *gin.Context) {
		h := c.Writer.Header()
		h.Set("Content-Security-Policy", contentSecurityPolicy)
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("X-Frame-Options", "DENY")
		h.Set("Referrer-Policy", "no-referrer")
		c.Next()
	}
}
