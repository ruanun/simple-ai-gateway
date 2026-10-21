package server

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/ruanun/simple-ai-gateway/internal/config"
	"github.com/ruanun/simple-ai-gateway/internal/db"
	"github.com/ruanun/simple-ai-gateway/internal/dbtest"
	"github.com/ruanun/simple-ai-gateway/internal/model"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

// setupUsersTest 装配用户管理测试环境，返回 (engine, db, admin token)。
func setupUsersTest(t *testing.T) (*gin.Engine, *gorm.DB, string) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	cfg := config.Config{}
	cfg.Storage.Local.Root = t.TempDir() // 审计正文本地存储落临时目录，避免测试残留
	cfg.Security.APIKeyPrefix = "sk-saig"
	cfg.Security.ChannelKeyAESKey = "0123456789abcdef0123456789abcdef"
	cfg.JWT.Secret = "users-secret"
	cfg.JWT.ExpireHours = 1

	g := dbtest.OpenInMemory(t)
	require.NoError(t, db.Migrate(g, model.Migrations("admin", "sag12345")))
	// 兼容 -count=N 重跑：清掉种子 admin 之外的账号（shared-cache 内存库跨重跑保留数据）
	g.Where("username <> ?", "admin").Delete(&model.AdminUser{})
	g.Model(&model.AdminUser{}).Where("username = ?", "admin").Update("status", 1)

	r, shutdown, err := NewWithShutdown(cfg, g, nil)
	require.NoError(t, err)
	t.Cleanup(shutdown) // 先停审计 worker 等后台 goroutine，再让 TempDir 清理（Cleanup 为 LIFO）
	token := doJSON(t, r, http.MethodPost, "/admin/login", "",
		`{"username":"admin","password":"sag12345"}`, http.StatusOK)["token"].(string)
	return r, g, token
}

// TestAdminUserCRUDE2E 用户 CRUD：新建校验、列表不泄露密码哈希、重置密码、删除。
func TestAdminUserCRUDE2E(t *testing.T) {
	r, _, token := setupUsersTest(t)

	// 新建校验：空用户名 / 短密码 → 400
	doJSON(t, r, http.MethodPost, "/admin/users", token, `{"username":"","password":"longenough"}`, http.StatusBadRequest)
	doJSON(t, r, http.MethodPost, "/admin/users", token, `{"username":"u2","password":"short"}`, http.StatusBadRequest)

	// 正常新建 → 200；重名 → 409
	u2 := doJSON(t, r, http.MethodPost, "/admin/users", token, `{"username":"u2","password":"password2"}`, http.StatusOK)
	u2ID := itoa(u2["id"].(float64))
	doJSON(t, r, http.MethodPost, "/admin/users", token, `{"username":"u2","password":"password2"}`, http.StatusConflict)

	// 新用户可登录
	doJSON(t, r, http.MethodPost, "/admin/login", "", `{"username":"u2","password":"password2"}`, http.StatusOK)

	// 列表：2 个用户，响应不含密码哈希
	list := doJSONArray(t, r, http.MethodGet, "/admin/users", token, "", http.StatusOK)
	require.Len(t, list, 2)
	for _, row := range list {
		m := row.(map[string]any)
		_, has := m["password_hash"]
		require.False(t, has, "响应不应包含密码哈希")
	}

	// 管理员重置 u2 密码 → 旧密码登录失败，新密码登录成功
	doJSON(t, r, http.MethodPost, "/admin/users/"+u2ID, token, `{"password":"resetpass9"}`, http.StatusOK)
	doJSON(t, r, http.MethodPost, "/admin/login", "", `{"username":"u2","password":"password2"}`, http.StatusUnauthorized)
	doJSON(t, r, http.MethodPost, "/admin/login", "", `{"username":"u2","password":"resetpass9"}`, http.StatusOK)

	// 删除 u2 → 列表只剩 admin，u2 登录 401
	doJSON(t, r, http.MethodDelete, "/admin/users/"+u2ID, token, "", http.StatusOK)
	list = doJSONArray(t, r, http.MethodGet, "/admin/users", token, "", http.StatusOK)
	require.Len(t, list, 1)
	doJSON(t, r, http.MethodPost, "/admin/login", "", `{"username":"u2","password":"resetpass9"}`, http.StatusUnauthorized)
}

// TestAdminUserDisableE2E 启停：禁用后登录 403，已签发 token 立即失效，重新启用恢复。
// u2 给 super 角色（本测试验证禁用踢下线，需要 u2 能访问 /admin/users）。
func TestAdminUserDisableE2E(t *testing.T) {
	r, _, token := setupUsersTest(t)

	u2 := doJSON(t, r, http.MethodPost, "/admin/users", token, `{"username":"u2","password":"password2","role":"super"}`, http.StatusOK)
	u2ID := itoa(u2["id"].(float64))
	u2Token := doJSON(t, r, http.MethodPost, "/admin/login", "",
		`{"username":"u2","password":"password2"}`, http.StatusOK)["token"].(string)

	// u2 的 token 可用
	doJSONArray(t, r, http.MethodGet, "/admin/users", u2Token, "", http.StatusOK)

	// 禁用 u2 → 登录 403，已签发 token 立即 401
	doJSON(t, r, http.MethodPost, "/admin/users/"+u2ID, token, `{"status":0}`, http.StatusOK)
	doJSON(t, r, http.MethodPost, "/admin/login", "", `{"username":"u2","password":"password2"}`, http.StatusForbidden)
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/admin/users", nil)
	req.Header.Set("Authorization", "Bearer "+u2Token)
	r.ServeHTTP(w, req)
	require.Equal(t, http.StatusUnauthorized, w.Code, "禁用后已签发 token 应立即失效")

	// 重新启用 → 恢复登录
	doJSON(t, r, http.MethodPost, "/admin/users/"+u2ID, token, `{"status":1}`, http.StatusOK)
	doJSON(t, r, http.MethodPost, "/admin/login", "", `{"username":"u2","password":"password2"}`, http.StatusOK)
}

// TestAdminUserGuardsE2E 护栏：不能删除/禁用当前登录账号；非法 status 400；不存在 404。
func TestAdminUserGuardsE2E(t *testing.T) {
	r, g, token := setupUsersTest(t)

	var adminRow model.AdminUser
	require.NoError(t, g.Where("username = ?", "admin").First(&adminRow).Error)
	adminID := itoa(float64(adminRow.ID))

	doJSON(t, r, http.MethodDelete, "/admin/users/"+adminID, token, "", http.StatusBadRequest)
	doJSON(t, r, http.MethodPost, "/admin/users/"+adminID, token, `{"status":0}`, http.StatusBadRequest)
	doJSON(t, r, http.MethodPost, "/admin/users/"+adminID, token, `{"status":2}`, http.StatusBadRequest)
	doJSON(t, r, http.MethodPost, "/admin/users/"+adminID, token, `{}`, http.StatusBadRequest)
	doJSON(t, r, http.MethodPost, "/admin/users/999999", token, `{"status":0}`, http.StatusNotFound)
	doJSON(t, r, http.MethodDelete, "/admin/users/999999", token, "", http.StatusNotFound)
}

// TestChangeOwnPasswordE2E 修改自己密码：旧密码错误 400；成功后旧密码失效、新密码可登录。
func TestChangeOwnPasswordE2E(t *testing.T) {
	r, _, token := setupUsersTest(t)

	// 旧密码错误 / 新密码过短 → 400
	doJSON(t, r, http.MethodPost, "/admin/profile/password", token,
		`{"old_password":"wrongwrong","new_password":"newpass123"}`, http.StatusBadRequest)
	doJSON(t, r, http.MethodPost, "/admin/profile/password", token,
		`{"old_password":"sag12345","new_password":"short"}`, http.StatusBadRequest)

	// 正常修改 → 旧密码登录 401，新密码登录 200
	doJSON(t, r, http.MethodPost, "/admin/profile/password", token,
		`{"old_password":"sag12345","new_password":"newpass123"}`, http.StatusOK)
	doJSON(t, r, http.MethodPost, "/admin/login", "", `{"username":"admin","password":"sag12345"}`, http.StatusUnauthorized)
	doJSON(t, r, http.MethodPost, "/admin/login", "", `{"username":"admin","password":"newpass123"}`, http.StatusOK)

	// 改回种子密码，避免 -count=N 重跑受影响（shared-cache 内存库跨重跑保留数据）
	newToken := doJSON(t, r, http.MethodPost, "/admin/login", "",
		`{"username":"admin","password":"newpass123"}`, http.StatusOK)["token"].(string)
	doJSON(t, r, http.MethodPost, "/admin/profile/password", newToken,
		`{"old_password":"newpass123","new_password":"sag12345"}`, http.StatusOK)
}

// TestChangePasswordRevokesOldTokenE2E 改密后，改密前签发的 JWT 立即失效(#17)。
func TestChangePasswordRevokesOldTokenE2E(t *testing.T) {
	r, _, token := setupUsersTest(t)

	// 改密前 token 可用
	doJSONArray(t, r, http.MethodGet, "/admin/users", token, "", http.StatusOK)

	// 用该 token 改自己的密码（改密请求本身仍用旧 token 通过鉴权）
	doJSON(t, r, http.MethodPost, "/admin/profile/password", token,
		`{"old_password":"sag12345","new_password":"newpass123"}`, http.StatusOK)

	// 改密后旧 token 立即失效
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/admin/users", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	r.ServeHTTP(w, req)
	require.Equal(t, http.StatusUnauthorized, w.Code, "改密后旧 token 应立即失效")

	// 新密码登录的 token 可用；改回种子密码避免 -count 重跑污染
	newToken := doJSON(t, r, http.MethodPost, "/admin/login", "",
		`{"username":"admin","password":"newpass123"}`, http.StatusOK)["token"].(string)
	doJSONArray(t, r, http.MethodGet, "/admin/users", newToken, "", http.StatusOK)
	doJSON(t, r, http.MethodPost, "/admin/profile/password", newToken,
		`{"old_password":"newpass123","new_password":"sag12345"}`, http.StatusOK)
}
