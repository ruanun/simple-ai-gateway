// Package httpx 统一管理面/门户面（/admin/*、/portal/api/*）的 HTTP 错误响应形状：
// {"error": {"code": "...", "message": "..."}}。message 为面向用户的简体中文文案，
// code 为固定通用词表（bad_request/unauthorized/forbidden/not_found/conflict/internal，
// 以及上游依赖失败的 bad_gateway、限流触发的 too_many_requests——分别对应 502/429，
// 无快捷方式，经 Err 显式传入），
// 仅当前端需要程序化区分某个错误时才新增专用 code——不为无人消费的错误发明 code。
// 业务面 /v1/* 的双协议错误信封不经此包（见 middleware/v1error.go）。
// 作者: ruan
package httpx

import (
	"context"
	"log/slog"
	"net/http"
	"net/url"
	"strings"

	"github.com/gin-gonic/gin"
)

// ErrBody 统一错误响应体（嵌在 {"error": ...} 里）。
type ErrBody struct {
	Code    string `json:"code"`    // 机器可判定的通用错误码
	Message string `json:"message"` // 面向用户的简体中文文案，可直接展示
}

// Err 统一错误出口。用 AbortWithStatusJSON 兼容两种调用场景：
// handler 处于链尾时 Abort 无副作用；中间件里则依赖 Abort 终止后续 handler。
// 这里也是全站错误的唯一收敛点，故一并记日志：调用方多数只调本函数就 return，
// 不在此处记就意味着绝大多数失败请求（含 5xx）不留任何痕迹，线上无从排查。
func Err(c *gin.Context, status int, code, msg string) {
	logErr(c, status, code, msg)
	c.AbortWithStatusJSON(status, gin.H{"error": ErrBody{Code: code, Message: msg}})
}

// logErr 按状态码分级记录失败详情：5xx 是内部故障记 Error；401/403 在管理面属高频常态
// （令牌过期、越权探测），记 Info 免得噪音淹没真正的故障；其余 4xx 记 Warn。
// request_id 由 logger 的 ctxHandler 从请求 ctx 自动附加，可与访问日志串联同一次操作。
// message 是面向用户的中文文案，本身不含凭证，可直接入日志。
func logErr(c *gin.Context, status int, code, msg string) {
	level := slog.LevelWarn
	switch {
	case status >= http.StatusInternalServerError:
		level = slog.LevelError
	case status == http.StatusUnauthorized || status == http.StatusForbidden:
		level = slog.LevelInfo
	}
	ctx := ReqContext(c)
	// 级别被过滤时直接退出：401 在管理面是最高频的错误（令牌过期 + 前端轮询），
	// 而 log.level=warn 下它整条都会被丢弃——不短路就白付 ClientIP 解析与属性构造的代价。
	if !slog.Default().Enabled(ctx, level) {
		return
	}
	attrs := append(ReqAttrs(c),
		slog.Int("status", status), slog.String("code", code), slog.String("message", msg))
	slog.LogAttrs(ctx, level, "请求失败", attrs...)
}

// ReqContext 取请求 ctx，供 logger 的 ctxHandler 带出 request_id。
// 裸 gin.Context（部分单测直接构造，无 Request）回退 Background——日志不该比它记录的错误更危险。
func ReqContext(c *gin.Context) context.Context {
	if c.Request == nil {
		return context.Background()
	}
	return c.Request.Context()
}

// ReqAttrs 请求侧公共日志字段（method/path/client_ip，查询串非空时附 query）。
// 错误日志与访问日志共用同一份实现，两条日志才能按同名字段互相检索——
// 各写一份的话，加字段或改口径时漏改一处，就变成「一条筛得出、另一条筛不出」。
// 用 slog.Attr 而非 ...any：int 值不装箱，且省掉 slog 内部再扫一遍构造 []Attr。
func ReqAttrs(c *gin.Context) []slog.Attr {
	if c.Request == nil {
		return nil
	}
	attrs := make([]slog.Attr, 0, 7) // 4 个公共字段 + 调用方通常再追加 2~3 个，一次分配到位
	attrs = append(attrs,
		slog.String("method", c.Request.Method),
		slog.String("path", c.Request.URL.Path),
		slog.String("client_ip", c.ClientIP()))
	// 查询串非空才带：分页/筛选参数常常正是触发失败的输入；单列字段而不并进 path，
	// 否则日志系统按 path 聚合会被 query 打散。
	if q := c.Request.URL.RawQuery; q != "" {
		attrs = append(attrs, slog.String("query", redactQuery(q)))
	}
	return attrs
}

// sensitiveQueryKeys 查询串中值须脱敏的参数名（小写比对）。典型如 OIDC 回调的 code/state：
// 授权码虽一次性且短时有效，也不该明文躺在日志里。
var sensitiveQueryKeys = map[string]bool{
	"code": true, "state": true, "token": true, "access_token": true, "id_token": true,
	"key": true, "api_key": true, "password": true, "secret": true,
}

// redactQuery 把敏感参数的值替换为 ***，其余参数保持原样（不重排、不重新编码）。
func redactQuery(q string) string {
	parts := strings.Split(q, "&")
	changed := false
	for i, p := range parts {
		k, _, found := strings.Cut(p, "=")
		if !found {
			continue
		}
		if name, err := url.QueryUnescape(k); err == nil && sensitiveQueryKeys[strings.ToLower(name)] {
			parts[i] = k + "=***"
			changed = true
		}
	}
	if !changed {
		return q
	}
	return strings.Join(parts, "&")
}

// 常用快捷方式：收敛绝大多数调用形态，状态码与 code 一一对应。
func BadRequest(c *gin.Context, msg string)   { Err(c, http.StatusBadRequest, "bad_request", msg) }
func Unauthorized(c *gin.Context, msg string) { Err(c, http.StatusUnauthorized, "unauthorized", msg) }
func Forbidden(c *gin.Context, msg string)    { Err(c, http.StatusForbidden, "forbidden", msg) }
func NotFound(c *gin.Context, msg string)     { Err(c, http.StatusNotFound, "not_found", msg) }
func Conflict(c *gin.Context, msg string)     { Err(c, http.StatusConflict, "conflict", msg) }
func Internal(c *gin.Context, msg string)     { Err(c, http.StatusInternalServerError, "internal", msg) }
