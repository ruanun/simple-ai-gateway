// Package v1proto /v1 业务面双协议（openai/anthropic）错误信封与协议推导。
// 叶子包：不 import 任何业务包，middleware 与 gateway 均可安全依赖（历史上信封实现
// 因 gateway→middleware 单向依赖被迫住在 middleware，本包解除该妥协）。
package v1proto

import (
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
)

// ErrorType 网关级错误的 status→协议错误 type 映射（OpenAI/Anthropic 共用一张表，仅 402 分流）。
func ErrorType(status int, proto string) string {
	switch status {
	case http.StatusBadRequest:
		return "invalid_request_error"
	case http.StatusUnauthorized:
		return "authentication_error"
	case http.StatusPaymentRequired:
		// OpenAI 有 insufficient_quota；Anthropic 无对应类型，用 permission_error 兜底。
		if proto == "anthropic" {
			return "permission_error"
		}
		return "insufficient_quota"
	case http.StatusForbidden:
		return "permission_error"
	case http.StatusNotFound:
		return "not_found_error"
	case http.StatusTooManyRequests:
		return "rate_limit_error"
	}
	return "api_error" // 5xx 及其余未列状态兜底
}

// Respond 按入站协议回写 /v1 业务面的网关级错误信封，对齐官方 SDK 契约：
//   - openai：{"error":{"message":msg,"type":<t>}}（error 恒为对象）
//   - anthropic：{"type":"error","error":{"type":<t>,"message":msg}}
//
// 不迁 httpx（httpx 只统一 /admin、/portal/api 面的响应形状）。
func Respond(c *gin.Context, status int, proto, msg string) {
	RespondDetail(c, status, proto, msg, "", "")
}

// RespondDetail 同 Respond，额外带上可选的 code 与 request_id 便于调用方自助排查：
// code 是网关自定义的稳定错误码（如 all_candidates_unavailable），request_id 与
// X-SAIG-Request-Id 响应头同值、可直接拿到管理面的请求日志/审计里定位。
// 两者为空时不输出对应字段——保证 Respond 的既有输出形状逐字不变。
// 扩展字段对官方 SDK 安全：未知字段被忽略。
func RespondDetail(c *gin.Context, status int, proto, msg, code, requestID string) {
	inner := gin.H{"type": ErrorType(status, proto), "message": msg}
	if code != "" {
		inner["code"] = code
	}
	if requestID != "" {
		inner["request_id"] = requestID
	}
	if proto == "anthropic" {
		c.JSON(status, gin.H{"type": "error", "error": inner})
		return
	}
	c.JSON(status, gin.H{"error": inner})
}

// ProtoForPath 按请求路径推导入站协议（调用方不知端点协议时用）：
// /v1/messages 前缀→anthropic，其余→openai。
func ProtoForPath(path string) string {
	if strings.HasPrefix(path, "/v1/messages") {
		return "anthropic"
	}
	return "openai"
}
