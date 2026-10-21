// 管理面访问日志中间件测试。作者: ruan
package middleware

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/ruanun/simple-ai-gateway/internal/httpx"
	"github.com/ruanun/simple-ai-gateway/internal/logtest"
	"github.com/stretchr/testify/require"
)

// runWithLog 跑一次请求并返回捕获到的日志行。
func runWithLog(t *testing.T, build func(*gin.Engine), method, path string) []map[string]any {
	t.Helper()
	gin.SetMode(gin.TestMode)
	log := logtest.Capture(t)
	r := gin.New()
	build(r)
	r.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(method, path, nil))
	return log.Lines()
}

// 成功请求也留一行：没有访问日志时，管理面操作在进程日志里完全无痕。
func TestAccessLogRecordsSuccessfulRequest(t *testing.T) {
	lines := runWithLog(t, func(r *gin.Engine) {
		r.Use(AccessLog())
		r.GET("/admin/channels", func(c *gin.Context) { c.JSON(http.StatusOK, gin.H{"ok": true}) })
	}, http.MethodGet, "/admin/channels")

	require.Len(t, lines, 1)
	m := lines[0]
	require.Equal(t, "INFO", m["level"])
	require.Equal(t, "GET", m["method"])
	require.Equal(t, "/admin/channels", m["path"])
	require.Equal(t, float64(200), m["status"])
	require.Contains(t, m, "duration_ms")
	require.NotEmpty(t, m["client_ip"])
	// 无查询串时不带空字段，避免每行都拖一个 query=""
	require.NotContains(t, m, "query")
}

// 查询串单列一个字段：排查分页/筛选类问题得知道实际参数，
// 但不并进 path——否则日志系统按 path 聚合会被 query 打散。
func TestAccessLogRecordsQuery(t *testing.T) {
	lines := runWithLog(t, func(r *gin.Engine) {
		r.Use(AccessLog())
		r.GET("/admin/request-logs", func(c *gin.Context) { c.JSON(http.StatusOK, gin.H{}) })
	}, http.MethodGet, "/admin/request-logs?page=2&channel=3")

	require.Len(t, lines, 1)
	require.Equal(t, "/admin/request-logs", lines[0]["path"])
	require.Equal(t, "page=2&channel=3", lines[0]["query"])
}

// 失败请求产生两行：访问日志（发生了什么请求）+ 错误日志（为什么失败），
// 两行靠同一个 request_id 串联——这是本次改造的核心目标。
func TestAccessLogAndErrLogShareRequestID(t *testing.T) {
	lines := runWithLog(t, func(r *gin.Engine) {
		r.Use(RequestID(), AccessLog())
		r.POST("/admin/channels/:id/fetch-models", func(c *gin.Context) {
			httpx.Err(c, http.StatusBadGateway, "bad_gateway", "上游返回状态码 404")
		})
	}, http.MethodPost, "/admin/channels/3/fetch-models")

	require.Len(t, lines, 2, "应有访问日志与错误日志各一行")
	reqID, _ := lines[0]["request_id"].(string)
	require.NotEmpty(t, reqID, "request_id 必须落在日志里，否则无法串联")
	require.Equal(t, reqID, lines[1]["request_id"])

	// 错误详情在前（handler 内记），访问日志在后（c.Next 之后记）
	require.Equal(t, "ERROR", lines[0]["level"])
	require.Equal(t, "上游返回状态码 404", lines[0]["message"])
	require.Equal(t, float64(502), lines[1]["status"], "访问日志要如实反映最终状态码")
}

// 高频轮询端点跳过访问日志：前端渠道页每 10s 拉一次熔断状态，全记会淹没真正的操作。
func TestAccessLogSkipsConfiguredPaths(t *testing.T) {
	lines := runWithLog(t, func(r *gin.Engine) {
		r.Use(AccessLog("/admin/breaker/status"))
		r.GET("/admin/breaker/status", func(c *gin.Context) { c.JSON(http.StatusOK, gin.H{}) })
	}, http.MethodGet, "/admin/breaker/status")

	require.Empty(t, lines, "轮询端点不应产生访问日志")
}

// 跳过按**路由模式**匹配而非原始 URL，故带路径参数的端点也能表达。
func TestAccessLogSkipsParameterizedRoute(t *testing.T) {
	lines := runWithLog(t, func(r *gin.Engine) {
		r.Use(AccessLog("/admin/channels/:id/status"))
		r.GET("/admin/channels/:id/status", func(c *gin.Context) { c.JSON(http.StatusOK, gin.H{}) })
	}, http.MethodGet, "/admin/channels/42/status")

	require.Empty(t, lines)
}

// 跳过的是「访问」记录而非「故障」记录：被跳过的端点失败时错误日志照常留下。
func TestSkippedPathStillLogsFailure(t *testing.T) {
	lines := runWithLog(t, func(r *gin.Engine) {
		r.Use(AccessLog("/admin/breaker/status"))
		r.GET("/admin/breaker/status", func(c *gin.Context) { httpx.Internal(c, "内部错误") })
	}, http.MethodGet, "/admin/breaker/status")

	require.Len(t, lines, 1)
	require.Equal(t, "请求失败", lines[0]["msg"])
	require.Equal(t, "ERROR", lines[0]["level"])
}

// panic 被 Recover 兜住后仍要留下访问日志，否则崩溃请求在日志里同样无痕。
func TestAccessLogRecordsAfterPanic(t *testing.T) {
	lines := runWithLog(t, func(r *gin.Engine) {
		r.Use(AccessLog(), Recover())
		r.GET("/admin/boom", func(_ *gin.Context) { panic("boom") })
	}, http.MethodGet, "/admin/boom")

	var access map[string]any
	for _, m := range lines {
		if m["msg"] == "请求完成" {
			access = m
		}
	}
	require.NotNil(t, access, "panic 后仍应有访问日志")
	require.Equal(t, float64(500), access["status"])
}
