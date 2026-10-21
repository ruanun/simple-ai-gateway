// 管理面请求可观测性的端到端验证：访问日志 + 错误日志 + request_id 串联。作者: ruan
package server

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/ruanun/simple-ai-gateway/internal/config"
	"github.com/ruanun/simple-ai-gateway/internal/db"
	"github.com/ruanun/simple-ai-gateway/internal/dbtest"
	"github.com/ruanun/simple-ai-gateway/internal/logtest"
	"github.com/ruanun/simple-ai-gateway/internal/model"
	"github.com/stretchr/testify/require"
)

// serveWithLog 起一套最小服务、发一次请求，返回捕获到的日志会话。
func serveWithLog(t *testing.T, method, path, body string) *logtest.Log {
	t.Helper()
	gin.SetMode(gin.TestMode)
	cfg := config.Config{}
	cfg.Storage.Local.Root = t.TempDir() // 审计正文本地存储落临时目录，避免测试残留
	cfg.Security.APIKeyPrefix = "sk-saig"
	cfg.Security.ChannelKeyAESKey = "0123456789abcdef0123456789abcdef"
	cfg.JWT.Secret = "s"
	cfg.JWT.ExpireHours = 1
	g := dbtest.OpenInMemory(t)
	require.NoError(t, db.Migrate(g, model.Migrations("admin", "sag12345")))
	r, shutdown, err := NewWithShutdown(cfg, g, nil)
	require.NoError(t, err)
	t.Cleanup(shutdown) // 先停审计 worker 等后台 goroutine，再让 TempDir 清理（Cleanup 为 LIFO）

	// 捕获放在建库/装配之后：迁移与装配期的日志与本用例无关，混进来只会干扰断言。
	log := logtest.Capture(t)
	r.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(method, path, strings.NewReader(body)))
	return log
}

// 管理面失败请求必须同时留下访问日志与错误日志，且靠同一 request_id 串联。
func TestAdminRequestLeavesTraceableLogs(t *testing.T) {
	log := serveWithLog(t, http.MethodGet, "/admin/channels", "")

	access, errLine := log.Line("请求完成"), log.Line("请求失败")
	require.NotNil(t, access, "管理面请求应有访问日志")
	require.NotNil(t, errLine, "失败原因应有错误日志")
	require.Equal(t, "/admin/channels", access["path"])
	require.Equal(t, float64(401), access["status"])
	require.NotEmpty(t, access["request_id"])
	require.Equal(t, access["request_id"], errLine["request_id"], "两行日志必须能串联到同一次操作")
}

// /admin/login 是单独注册的路由（不在受 JWT 保护的组里），同样要被覆盖——
// 登录失败恰恰是最需要留痕的场景。
func TestAdminLoginIsLogged(t *testing.T) {
	log := serveWithLog(t, http.MethodPost, "/admin/login", `{"username":"admin","password":"wrong"}`)

	access := log.Line("请求完成")
	require.NotNil(t, access, "登录端点也应有访问日志")
	require.Equal(t, "/admin/login", access["path"])
	require.NotEmpty(t, access["request_id"])
}

// 门户面与管理面共用 httpx 错误出口，可观测性底座必须同构——否则 portal 的错误日志
// 没有 request_id、也没有配套的访问日志行，串不起同一次操作。
func TestPortalRequestLeavesTraceableLogs(t *testing.T) {
	log := serveWithLog(t, http.MethodGet, "/portal/api/me", "")

	access, errLine := log.Line("请求完成"), log.Line("请求失败")
	require.NotNil(t, access, "门户面请求应有访问日志")
	require.NotNil(t, errLine, "失败原因应有错误日志")
	require.Equal(t, "/portal/api/me", access["path"])
	require.NotEmpty(t, access["request_id"])
	require.Equal(t, access["request_id"], errLine["request_id"])
}

// 业务面 /v1 不挂访问日志：每次调用已落 RequestLog 表并可回放，再记一行是重复放大。
func TestV1HasNoAccessLog(t *testing.T) {
	log := serveWithLog(t, http.MethodPost, "/v1/chat/completions", `{"model":"gpt-4o"}`)
	require.Nil(t, log.Line("请求完成"), "业务面不应产生访问日志")
}
