// 可信代理配置的端到端验证：client_ip 取证字段是否可被 X-Forwarded-For 伪造。作者: ruan
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
	"github.com/ruanun/simple-ai-gateway/internal/model"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

// setupTrustedProxyTest 按给定 trusted_proxies 装配服务并登录，返回 (engine, db, token)。
func setupTrustedProxyTest(t *testing.T, trusted []string) (*gin.Engine, *gorm.DB, string) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	cfg := config.Config{}
	cfg.Storage.Local.Root = t.TempDir()
	cfg.Server.TrustedProxies = trusted
	cfg.Security.APIKeyPrefix = "sk-saig"
	cfg.Security.ChannelKeyAESKey = "0123456789abcdef0123456789abcdef"
	cfg.JWT.Secret = "tp-secret"
	cfg.JWT.ExpireHours = 1
	g := dbtest.OpenInMemory(t)
	require.NoError(t, db.Migrate(g, model.Migrations("admin", "sag12345")))
	r, shutdown, err := NewWithShutdown(cfg, g, nil)
	require.NoError(t, err)
	t.Cleanup(shutdown)
	token := doJSON(t, r, http.MethodPost, "/admin/login", "",
		`{"username":"admin","password":"sag12345"}`, http.StatusOK)["token"].(string)
	return r, g, token
}

// opLogClientIP 发一次带 X-Forwarded-For 的管理面写请求，返回操作日志里记下的 client_ip。
// httptest.NewRequest 的对端地址固定为 192.0.2.1:1234。
func opLogClientIP(t *testing.T, r *gin.Engine, g *gorm.DB, token, xff string) string {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/admin/key-tags", strings.NewReader(`{"name":"tp-probe"}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("X-Forwarded-For", xff)
	r.ServeHTTP(httptest.NewRecorder(), req)
	var row model.AdminOpLog
	require.NoError(t, g.Where("path = ?", "/admin/key-tags").Order("id desc").First(&row).Error)
	return row.ClientIP
}

// 未配置可信代理：X-Forwarded-For 必须被忽略，取证字段只信 TCP 对端地址。
func TestClientIPIgnoresForwardedForByDefault(t *testing.T) {
	r, g, token := setupTrustedProxyTest(t, nil)
	require.Equal(t, "192.0.2.1", opLogClientIP(t, r, g, token, "203.0.113.9"))
}

// 配置了可信代理且请求确实来自该代理：取 X-Forwarded-For 里的真实客户端地址。
func TestClientIPHonorsForwardedForFromTrustedProxy(t *testing.T) {
	r, g, token := setupTrustedProxyTest(t, []string{"192.0.2.1"})
	require.Equal(t, "203.0.113.9", opLogClientIP(t, r, g, token, "203.0.113.9"))
}

// trusted_proxies 写错（非 IP/CIDR）应在装配期拒启，而不是静默退化为信任全部或不信任。
func TestInvalidTrustedProxiesRejected(t *testing.T) {
	gin.SetMode(gin.TestMode)
	cfg := config.Config{}
	cfg.Storage.Local.Root = t.TempDir()
	cfg.Server.TrustedProxies = []string{"not-an-ip"}
	cfg.Security.ChannelKeyAESKey = "0123456789abcdef0123456789abcdef"
	g := dbtest.OpenInMemory(t)
	require.NoError(t, db.Migrate(g, model.Migrations("admin", "sag12345")))
	_, _, err := NewWithShutdown(cfg, g, nil)
	require.Error(t, err)
	require.Contains(t, err.Error(), "trusted_proxies")
}
