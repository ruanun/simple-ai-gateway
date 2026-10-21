package server

import (
	"net/http"
	"strconv"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/ruanun/simple-ai-gateway/internal/config"
	"github.com/ruanun/simple-ai-gateway/internal/db"
	"github.com/ruanun/simple-ai-gateway/internal/dbtest"
	"github.com/ruanun/simple-ai-gateway/internal/model"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

// setupPortalTest 装配门户测试环境，返回 (engine, db, admin token)。
func setupPortalTest(t *testing.T) (*gin.Engine, *gorm.DB, string) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	cfg := config.Config{}
	cfg.Storage.Local.Root = t.TempDir() // 审计正文本地存储落临时目录，避免测试残留
	cfg.Security.APIKeyPrefix = "sk-saig"
	cfg.Security.ChannelKeyAESKey = "0123456789abcdef0123456789abcdef"
	cfg.JWT.Secret = "portal-secret"
	cfg.JWT.ExpireHours = 1

	g := dbtest.OpenInMemory(t)
	require.NoError(t, db.Migrate(g, model.Migrations("admin", "sag12345")))
	// 兼容 -count=N 重跑：清掉门户用户及其名下 App/令牌，复位注册开关
	var ownedApps []uint64
	g.Model(&model.App{}).Where("owner_user_id <> 0").Pluck("id", &ownedApps)
	if len(ownedApps) > 0 {
		g.Where("app_id IN ?", ownedApps).Delete(&model.APIKey{})
		g.Where("id IN ?", ownedApps).Delete(&model.App{})
	}
	g.Where("1 = 1").Delete(&model.PortalUser{})
	g.Model(&model.SystemSetting{}).Where("id = 1").Update("portal_register_enabled", false)
	g.Model(&model.AdminUser{}).Where("username = ?", "admin").Updates(map[string]any{"status": 1, "role": model.RoleSuper})

	r, shutdown, err := NewWithShutdown(cfg, g, nil)
	require.NoError(t, err)
	t.Cleanup(shutdown) // 先停审计 worker 等后台 goroutine，再让 TempDir 清理（Cleanup 为 LIFO）
	token := doJSON(t, r, http.MethodPost, "/admin/login", "",
		`{"username":"admin","password":"sag12345"}`, http.StatusOK)["token"].(string)
	return r, g, token
}

// enableRegister 打开注册开关并设默认配额。
func enableRegister(t *testing.T, r *gin.Engine, adminToken string, quota int64) {
	doJSON(t, r, http.MethodPost, "/admin/settings", adminToken,
		`{"audit_retention_days":30,"request_log_retention_days":90,"portal_register_enabled":true,"portal_register_quota":`+strconv.FormatInt(quota, 10)+`}`, http.StatusOK)
}

// TestPortalRegisterE2E 注册开关：默认关 403；开后注册成功并自动建默认 App（配额取设置值）。
func TestPortalRegisterE2E(t *testing.T) {
	r, g, admin := setupPortalTest(t)

	// 默认关闭 → 403
	doJSON(t, r, http.MethodPost, "/portal/api/register", "", `{"username":"pu1","password":"password1"}`, http.StatusForbidden)

	enableRegister(t, r, admin, 2_000_000)
	// 密码过短 → 400
	doJSON(t, r, http.MethodPost, "/portal/api/register", "", `{"username":"pu1","password":"short"}`, http.StatusBadRequest)
	// 正常注册 → 返回 token；重名 → 409
	resp := doJSON(t, r, http.MethodPost, "/portal/api/register", "", `{"username":"pu1","password":"password1"}`, http.StatusOK)
	require.NotEmpty(t, resp["token"])
	doJSON(t, r, http.MethodPost, "/portal/api/register", "", `{"username":"pu1","password":"password1"}`, http.StatusConflict)

	// 自动建默认 App：归属正确、配额=设置值
	var u model.PortalUser
	require.NoError(t, g.Where("username = ?", "pu1").First(&u).Error)
	var app model.App
	require.NoError(t, g.Where("owner_user_id = ?", u.ID).First(&app).Error)
	require.Equal(t, int64(2_000_000), app.Quota)

	// me 能看到 1 个 App
	tok := resp["token"].(string)
	me := doJSON(t, r, http.MethodGet, "/portal/api/me", tok, "", http.StatusOK)
	require.Equal(t, float64(1), me["app_count"])
}

// TestPortalIsolationE2E 越权矩阵：用户 A 不可见/不可操作用户 B 的 App/令牌/日志。
func TestPortalIsolationE2E(t *testing.T) {
	r, g, admin := setupPortalTest(t)
	enableRegister(t, r, admin, 0)

	tokA := doJSON(t, r, http.MethodPost, "/portal/api/register", "", `{"username":"userA","password":"password1"}`, http.StatusOK)["token"].(string)
	tokB := doJSON(t, r, http.MethodPost, "/portal/api/register", "", `{"username":"userB","password":"password1"}`, http.StatusOK)["token"].(string)

	appsA := doJSONArray(t, r, http.MethodGet, "/portal/api/apps", tokA, "", http.StatusOK)
	require.Len(t, appsA, 1, "A 只见自己的 App")
	appAID := itoa(appsA[0].(map[string]any)["id"].(float64))

	// A 在自己 App 下建令牌 → 200；B 用 A 的 app_id 建令牌 → 404
	keyResp := doJSON(t, r, http.MethodPost, "/portal/api/api-keys", tokA, `{"name":"kA","app_id":`+appAID+`}`, http.StatusOK)
	require.Contains(t, keyResp["key"].(string), "sk-saig-")
	keyAID := itoa(keyResp["id"].(float64))
	doJSON(t, r, http.MethodPost, "/portal/api/api-keys", tokB, `{"name":"kB","app_id":`+appAID+`}`, http.StatusNotFound)

	// B 改/删 A 的令牌 → 404；B 看 A 的 App 趋势 → 404
	doJSON(t, r, http.MethodPost, "/portal/api/api-keys/"+keyAID, tokB, `{"name":"hack"}`, http.StatusNotFound)
	doJSON(t, r, http.MethodDelete, "/portal/api/api-keys/"+keyAID, tokB, "", http.StatusNotFound)
	doJSON(t, r, http.MethodGet, "/portal/api/apps/"+appAID+"/usage-trend", tokB, "", http.StatusNotFound)

	// 用量分析端点同样 404 隔离；owner 自查 200 且三块字段齐全
	doJSON(t, r, http.MethodGet, "/portal/api/apps/"+appAID+"/usage-analysis", tokB, "", http.StatusNotFound)
	ua := doJSON(t, r, http.MethodGet, "/portal/api/apps/"+appAID+"/usage-analysis?days=14", tokA, "", http.StatusOK)
	require.Contains(t, ua, "trend")
	require.Contains(t, ua, "models")
	require.Contains(t, ua, "daily_models")

	// B 的令牌列表看不到 A 的令牌（含 app_id 过滤越权查空）
	keysB := doJSONArray(t, r, http.MethodGet, "/portal/api/api-keys", tokB, "", http.StatusOK)
	require.Len(t, keysB, 0)
	keysB2 := doJSONArray(t, r, http.MethodGet, "/portal/api/api-keys?app_id="+appAID, tokB, "", http.StatusOK)
	require.Len(t, keysB2, 0)

	// 日志强制归属过滤：造一条 A 的 App 日志，B 查不到
	var uA model.PortalUser
	require.NoError(t, g.Where("username = ?", "userA").First(&uA).Error)
	var appA model.App
	require.NoError(t, g.Where("owner_user_id = ?", uA.ID).First(&appA).Error)
	require.NoError(t, g.Create(&model.RequestLog{Model: "m1", AppID: appA.ID}).Error)
	logsA := doJSON(t, r, http.MethodGet, "/portal/api/request-logs", tokA, "", http.StatusOK)
	require.Equal(t, float64(1), logsA["total"])
	logsB := doJSON(t, r, http.MethodGet, "/portal/api/request-logs", tokB, "", http.StatusOK)
	require.Equal(t, float64(0), logsB["total"])
	logsB2 := doJSON(t, r, http.MethodGet, "/portal/api/request-logs?app_id="+appAID, tokB, "", http.StatusOK)
	require.Equal(t, float64(0), logsB2["total"], "带越权 app_id 过滤也查空")

	// 管理端 token 不可用于门户 API（typ=admin）
	require.Equal(t, http.StatusUnauthorized, doReq(t, r, http.MethodGet, "/portal/api/apps", admin, ""))
}

// TestPortalKeyLimitAndGovernE2E 令牌上限 20 + 治理字段更新 + 删除。
func TestPortalKeyLimitAndGovernE2E(t *testing.T) {
	r, _, admin := setupPortalTest(t)
	enableRegister(t, r, admin, 0)
	tok := doJSON(t, r, http.MethodPost, "/portal/api/register", "", `{"username":"limitu","password":"password1"}`, http.StatusOK)["token"].(string)
	apps := doJSONArray(t, r, http.MethodGet, "/portal/api/apps", tok, "", http.StatusOK)
	appID := itoa(apps[0].(map[string]any)["id"].(float64))

	var firstKey string
	for i := 0; i < 20; i++ {
		resp := doJSON(t, r, http.MethodPost, "/portal/api/api-keys", tok, `{"name":"k`+strconv.Itoa(i)+`","app_id":`+appID+`}`, http.StatusOK)
		if i == 0 {
			firstKey = itoa(resp["id"].(float64))
		}
	}
	doJSON(t, r, http.MethodPost, "/portal/api/api-keys", tok, `{"name":"k20","app_id":`+appID+`}`, http.StatusBadRequest)

	// 治理更新：改名+日成本上限+禁用
	doJSON(t, r, http.MethodPost, "/portal/api/api-keys/"+firstKey, tok, `{"name":"renamed","daily_cost_limit":500000,"status":0}`, http.StatusOK)
	// 删除一把后可再建
	doJSON(t, r, http.MethodDelete, "/portal/api/api-keys/"+firstKey, tok, "", http.StatusOK)
	doJSON(t, r, http.MethodPost, "/portal/api/api-keys", tok, `{"name":"k-again","app_id":`+appID+`}`, http.StatusOK)
}

// TestPortalAccountLifecycleE2E 管理端建号/分配归属/禁用踢下线/删除释放归属。
func TestPortalAccountLifecycleE2E(t *testing.T) {
	r, g, admin := setupPortalTest(t)

	// 管理员建号（带默认 App，配额 3 USD）
	pu := doJSON(t, r, http.MethodPost, "/admin/portal-users", admin, `{"username":"mgr-u","password":"password1","with_app":true,"quota":3000000}`, http.StatusOK)
	puID := itoa(pu["id"].(float64))
	tok := doJSON(t, r, http.MethodPost, "/portal/api/login", "", `{"username":"mgr-u","password":"password1"}`, http.StatusOK)["token"].(string)
	require.Equal(t, float64(1), doJSON(t, r, http.MethodGet, "/portal/api/me", tok, "", http.StatusOK)["app_count"])

	// 再建一个无主 App 并分配给 mgr-u → app_count=2
	app2 := doJSON(t, r, http.MethodPost, "/admin/apps", admin, `{"name":"assign-me","quota":0}`, http.StatusOK)
	app2ID := itoa(app2["id"].(float64))
	doJSON(t, r, http.MethodPost, "/admin/apps/"+app2ID+"/owner", admin, `{"user_id":`+puID+`}`, http.StatusOK)
	require.Equal(t, float64(2), doJSON(t, r, http.MethodGet, "/portal/api/me", tok, "", http.StatusOK)["app_count"])
	// 分配给不存在的用户 → 400；收回为无主 → 200
	doJSON(t, r, http.MethodPost, "/admin/apps/"+app2ID+"/owner", admin, `{"user_id":999999}`, http.StatusBadRequest)
	doJSON(t, r, http.MethodPost, "/admin/apps/"+app2ID+"/owner", admin, `{"user_id":0}`, http.StatusOK)

	// 禁用 → 登录 403 + 已签发 token 立即失效；重新启用恢复
	doJSON(t, r, http.MethodPost, "/admin/portal-users/"+puID, admin, `{"status":0}`, http.StatusOK)
	doJSON(t, r, http.MethodPost, "/portal/api/login", "", `{"username":"mgr-u","password":"password1"}`, http.StatusForbidden)
	require.Equal(t, http.StatusUnauthorized, doReq(t, r, http.MethodGet, "/portal/api/me", tok, ""))
	doJSON(t, r, http.MethodPost, "/admin/portal-users/"+puID, admin, `{"status":1}`, http.StatusOK)

	// 门户改自己密码：旧密码错 400 → 正常改 → 新密码可登录
	doJSON(t, r, http.MethodPost, "/portal/api/password", tok, `{"old_password":"wrongwrong","new_password":"newpass123"}`, http.StatusBadRequest)
	doJSON(t, r, http.MethodPost, "/portal/api/password", tok, `{"old_password":"password1","new_password":"newpass123"}`, http.StatusOK)
	doJSON(t, r, http.MethodPost, "/portal/api/login", "", `{"username":"mgr-u","password":"newpass123"}`, http.StatusOK)

	// 删除用户 → 名下 App 归属重置为 0，App 保留
	var urow model.PortalUser
	require.NoError(t, g.Where("username = ?", "mgr-u").First(&urow).Error)
	uid := urow.ID
	doJSON(t, r, http.MethodDelete, "/admin/portal-users/"+puID, admin, "", http.StatusOK)
	var orphan int64
	g.Model(&model.App{}).Where("owner_user_id = ?", uid).Count(&orphan)
	require.Equal(t, int64(0), orphan, "删除用户后 App 归属应重置为 0")
	var appCnt int64
	g.Model(&model.App{}).Where("name = ?", "mgr-u").Count(&appCnt)
	require.Equal(t, int64(1), appCnt, "App 本身保留")
}

// TestPortalChangePasswordRevokesOldTokenE2E 门户用户改密后，改密前签发的 JWT 立即失效(#25)。
func TestPortalChangePasswordRevokesOldTokenE2E(t *testing.T) {
	r, _, admin := setupPortalTest(t)
	enableRegister(t, r, admin, 0)

	token := doJSON(t, r, http.MethodPost, "/portal/api/register", "",
		`{"username":"pu","password":"password1"}`, http.StatusOK)["token"].(string)
	doJSON(t, r, http.MethodGet, "/portal/api/me", token, "", http.StatusOK) // 改密前可用

	// 改密（改密请求本身仍用旧 token 通过鉴权）
	doJSON(t, r, http.MethodPost, "/portal/api/password", token,
		`{"old_password":"password1","new_password":"password2"}`, http.StatusOK)

	// 改密后旧 token 立即失效
	doJSON(t, r, http.MethodGet, "/portal/api/me", token, "", http.StatusUnauthorized)

	// 新密码登录的 token 可用
	newToken := doJSON(t, r, http.MethodPost, "/portal/api/login", "",
		`{"username":"pu","password":"password2"}`, http.StatusOK)["token"].(string)
	doJSON(t, r, http.MethodGet, "/portal/api/me", newToken, "", http.StatusOK)
}
