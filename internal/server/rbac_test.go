package server

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/golang-jwt/jwt/v5"
	"github.com/ruanun/simple-ai-gateway/internal/config"
	"github.com/ruanun/simple-ai-gateway/internal/db"
	"github.com/ruanun/simple-ai-gateway/internal/dbtest"
	"github.com/ruanun/simple-ai-gateway/internal/model"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

const rbacTestSecret = "rbac-secret"

// setupRBACTest 装配 RBAC 测试环境：种子 admin(super) + 新建 ops/viewer 各一，返回三角色 token。
func setupRBACTest(t *testing.T) (*gin.Engine, *gorm.DB, map[string]string) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	cfg := config.Config{}
	cfg.Storage.Local.Root = t.TempDir() // 审计正文本地存储落临时目录，避免测试残留
	cfg.Security.APIKeyPrefix = "sk-saig"
	cfg.Security.ChannelKeyAESKey = "0123456789abcdef0123456789abcdef"
	cfg.JWT.Secret = rbacTestSecret
	cfg.JWT.ExpireHours = 1

	g := dbtest.OpenInMemory(t)
	require.NoError(t, db.Migrate(g, model.Migrations("admin", "sag12345")))
	// 兼容 -count=N 重跑：清掉种子 admin 之外的账号并复位 admin
	g.Where("username <> ?", "admin").Delete(&model.AdminUser{})
	g.Model(&model.AdminUser{}).Where("username = ?", "admin").Updates(map[string]any{"status": 1, "role": model.RoleSuper})
	g.Where("1 = 1").Delete(&model.AdminOpLog{})

	r, shutdown, err := NewWithShutdown(cfg, g, nil)
	require.NoError(t, err)
	t.Cleanup(shutdown) // 先停审计 worker 等后台 goroutine，再让 TempDir 清理（Cleanup 为 LIFO）

	login := func(user, pass string) string {
		return doJSON(t, r, http.MethodPost, "/admin/login", "",
			`{"username":"`+user+`","password":"`+pass+`"}`, http.StatusOK)["token"].(string)
	}
	super := login("admin", "sag12345")
	doJSON(t, r, http.MethodPost, "/admin/users", super, `{"username":"op1","password":"password1","role":"ops"}`, http.StatusOK)
	doJSON(t, r, http.MethodPost, "/admin/users", super, `{"username":"vw1","password":"password1","role":"viewer"}`, http.StatusOK)
	return r, g, map[string]string{
		model.RoleSuper:  super,
		model.RoleOps:    login("op1", "password1"),
		model.RoleViewer: login("vw1", "password1"),
	}
}

func doReq(t *testing.T, r *gin.Engine, method, path, token, body string) int {
	t.Helper()
	var rd io.Reader
	if body != "" {
		rd = strings.NewReader(body)
	}
	req := httptest.NewRequest(method, path, rd)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+token)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	return w.Code
}

// TestRBACMatrixE2E 三角色 × 代表性端点矩阵。
func TestRBACMatrixE2E(t *testing.T) {
	r, _, toks := setupRBACTest(t)

	cases := []struct {
		role, method, path, body string
		want                     int
	}{
		// viewer：其余 GET 可读，写/专属区/操作日志全拦
		{model.RoleViewer, http.MethodGet, "/admin/channels", "", http.StatusOK},
		{model.RoleViewer, http.MethodGet, "/admin/request-logs", "", http.StatusOK},
		{model.RoleViewer, http.MethodPost, "/admin/channels", `{"type":"openai","name":"x","base_url":"http://u","api_key":"k"}`, http.StatusForbidden},
		{model.RoleViewer, http.MethodDelete, "/admin/channels/1", "", http.StatusForbidden},
		{model.RoleViewer, http.MethodGet, "/admin/users", "", http.StatusForbidden},
		{model.RoleViewer, http.MethodGet, "/admin/settings", "", http.StatusForbidden},
		{model.RoleViewer, http.MethodGet, "/admin/op-logs", "", http.StatusForbidden},
		// ops：业务读写可，专属区拦，操作日志可读
		{model.RoleOps, http.MethodGet, "/admin/channels", "", http.StatusOK},
		{model.RoleOps, http.MethodPost, "/admin/channels", `{"type":"openai","name":"ops-ch","base_url":"http://u","api_key":"k"}`, http.StatusOK},
		{model.RoleOps, http.MethodGet, "/admin/users", "", http.StatusForbidden},
		{model.RoleOps, http.MethodPost, "/admin/users", `{"username":"z","password":"password9"}`, http.StatusForbidden},
		{model.RoleOps, http.MethodGet, "/admin/settings", "", http.StatusForbidden},
		{model.RoleOps, http.MethodPost, "/admin/settings", `{}`, http.StatusForbidden},
		{model.RoleOps, http.MethodGet, "/admin/op-logs", "", http.StatusOK},
		// super：全部可
		{model.RoleSuper, http.MethodGet, "/admin/users", "", http.StatusOK},
		{model.RoleSuper, http.MethodGet, "/admin/settings", "", http.StatusOK},
		{model.RoleSuper, http.MethodGet, "/admin/op-logs", "", http.StatusOK},
	}
	for _, tc := range cases {
		got := doReq(t, r, tc.method, tc.path, toks[tc.role], tc.body)
		require.Equal(t, tc.want, got, "%s %s %s", tc.role, tc.method, tc.path)
	}

	// 改自己密码：viewer 也放行（旧密码错 → 400 说明已过 RBAC 而非 403）
	require.Equal(t, http.StatusBadRequest,
		doReq(t, r, http.MethodPost, "/admin/profile/password", toks[model.RoleViewer],
			`{"old_password":"wrongwrong","new_password":"newpass123"}`))
}

// TestRBACRoleGuardsE2E 角色护栏：不能改自己角色；登录响应带角色；角色变更即时生效。
func TestRBACRoleGuardsE2E(t *testing.T) {
	r, g, toks := setupRBACTest(t)

	var adminRow model.AdminUser
	require.NoError(t, g.Where("username = ?", "admin").First(&adminRow).Error)
	adminID := itoa(float64(adminRow.ID))

	// 不能修改当前登录账号的角色
	doJSON(t, r, http.MethodPost, "/admin/users/"+adminID, toks[model.RoleSuper], `{"role":"ops"}`, http.StatusBadRequest)
	// 非法 role → 400
	var opRow model.AdminUser
	require.NoError(t, g.Where("username = ?", "op1").First(&opRow).Error)
	doJSON(t, r, http.MethodPost, "/admin/users/"+itoa(float64(opRow.ID)), toks[model.RoleSuper], `{"role":"boss"}`, http.StatusBadRequest)

	// 登录响应带 role
	resp := doJSON(t, r, http.MethodPost, "/admin/login", "", `{"username":"op1","password":"password1"}`, http.StatusOK)
	require.Equal(t, model.RoleOps, resp["role"])

	// 角色变更即时生效：op1 升 super 后，旧 token 立刻能访问 /admin/users（角色实时查库）
	doJSON(t, r, http.MethodPost, "/admin/users/"+itoa(float64(opRow.ID)), toks[model.RoleSuper], `{"role":"super"}`, http.StatusOK)
	require.Equal(t, http.StatusOK, doReq(t, r, http.MethodGet, "/admin/users", toks[model.RoleOps], ""))
	// 降回 viewer 后，旧 token 立刻失去写权限
	doJSON(t, r, http.MethodPost, "/admin/users/"+itoa(float64(opRow.ID)), toks[model.RoleSuper], `{"role":"viewer"}`, http.StatusOK)
	require.Equal(t, http.StatusForbidden,
		doReq(t, r, http.MethodPost, "/admin/channels", toks[model.RoleOps], `{"type":"openai","name":"y","base_url":"http://u","api_key":"k"}`))
}

// TestOpLogE2E 写操作（含 403 被拒）落操作日志；GET 不记；actor/method 过滤可用。
func TestOpLogE2E(t *testing.T) {
	r, g, toks := setupRBACTest(t)
	g.Where("1 = 1").Delete(&model.AdminOpLog{}) // 清掉 setup 期间产生的记录

	// super 一次成功写 + viewer 一次被 RBAC 拒绝的写 + 若干 GET
	doJSON(t, r, http.MethodPost, "/admin/channels", toks[model.RoleSuper],
		`{"type":"openai","name":"log-ch","base_url":"http://u","api_key":"k"}`, http.StatusOK)
	require.Equal(t, http.StatusForbidden,
		doReq(t, r, http.MethodPost, "/admin/channels", toks[model.RoleViewer], `{"type":"openai","name":"n","base_url":"http://u","api_key":"k"}`))
	doReq(t, r, http.MethodGet, "/admin/channels", toks[model.RoleSuper], "")

	var rows []model.AdminOpLog
	require.NoError(t, g.Order("id asc").Find(&rows).Error)
	require.Len(t, rows, 2, "仅两次写操作落日志，GET 不记")
	require.Equal(t, "admin", rows[0].Actor)
	require.Equal(t, model.RoleSuper, rows[0].Role)
	require.Equal(t, http.MethodPost, rows[0].Method)
	require.Equal(t, "/admin/channels", rows[0].Path)
	require.Equal(t, http.StatusOK, rows[0].Status)
	require.Equal(t, "vw1", rows[1].Actor)
	require.Equal(t, http.StatusForbidden, rows[1].Status, "被 RBAC 拒绝的写也留痕")

	// 查询接口 + 过滤
	list := doJSON(t, r, http.MethodGet, "/admin/op-logs?actor=vw1", toks[model.RoleSuper], "", http.StatusOK)
	require.Equal(t, float64(1), list["total"])
}

// TestJWTTypCompatE2E 旧 token（无 typ）兼容视为 admin；typ=user 的门户 token 不可用于管理面。
func TestJWTTypCompatE2E(t *testing.T) {
	r, _, _ := setupRBACTest(t)

	sign := func(claims jwt.MapClaims) string {
		tok, err := jwt.NewWithClaims(jwt.SigningMethodHS256, claims).SignedString([]byte(rbacTestSecret))
		require.NoError(t, err)
		return tok
	}
	exp := time.Now().Add(time.Hour).Unix()

	// 旧格式（无 typ / 无 uid）：一律 401，重新登录即可
	require.Equal(t, http.StatusUnauthorized, doReq(t, r, http.MethodGet, "/admin/channels",
		sign(jwt.MapClaims{"sub": "admin", "exp": exp}), ""))
	require.Equal(t, http.StatusUnauthorized, doReq(t, r, http.MethodGet, "/admin/channels",
		sign(jwt.MapClaims{"sub": "admin", "typ": "admin", "ver": 0, "exp": exp}), ""), "缺 uid 须拒")

	// 门户 token（typ=user）→ 管理面一律 401
	portalTok := sign(jwt.MapClaims{"sub": "admin", "uid": 1, "typ": "user", "exp": exp})
	require.Equal(t, http.StatusUnauthorized, doReq(t, r, http.MethodGet, "/admin/channels", portalTok, ""))
}

// 回归：token 曾只按用户名回查。账号硬删除后再建同名账号（新行 token_version 也从 0 起），
// 旧 token 会以新账号身份通过。现比对用户 ID，管理端与门户都验。
func TestRecreatedSameNameRejectsOldTokenE2E(t *testing.T) {
	r, g, toks := setupRBACTest(t)
	super := toks[model.RoleSuper]

	// 管理端：ops 账号 bob 登录 → 删除 → 同名重建（给了更高的 super 角色）
	doJSON(t, r, http.MethodPost, "/admin/users", super, `{"username":"bob","password":"password1","role":"ops"}`, http.StatusOK)
	old := doJSON(t, r, http.MethodPost, "/admin/login", "", `{"username":"bob","password":"password1"}`, http.StatusOK)["token"].(string)
	require.Equal(t, http.StatusOK, doReq(t, r, http.MethodGet, "/admin/channels", old, ""))
	var bob model.AdminUser
	require.NoError(t, g.Where("username = ?", "bob").First(&bob).Error)
	doJSON(t, r, http.MethodDelete, "/admin/users/"+itoa(float64(bob.ID)), super, "", http.StatusOK)
	doJSON(t, r, http.MethodPost, "/admin/users", super, `{"username":"bob","password":"password2","role":"super"}`, http.StatusOK)
	require.Equal(t, http.StatusUnauthorized, doReq(t, r, http.MethodGet, "/admin/users", old, ""), "旧 token 不得以同名新账号身份通过")
	fresh := doJSON(t, r, http.MethodPost, "/admin/login", "", `{"username":"bob","password":"password2"}`, http.StatusOK)["token"].(string)
	require.Equal(t, http.StatusOK, doReq(t, r, http.MethodGet, "/admin/users", fresh, ""), "新账号重新登录正常")

	// 门户：同理
	doJSON(t, r, http.MethodPost, "/admin/portal-users", super, `{"username":"carol","password":"password1"}`, http.StatusOK)
	pOld := doJSON(t, r, http.MethodPost, "/portal/api/login", "", `{"username":"carol","password":"password1"}`, http.StatusOK)["token"].(string)
	require.Equal(t, http.StatusOK, doReq(t, r, http.MethodGet, "/portal/api/me", pOld, ""))
	var carol model.PortalUser
	require.NoError(t, g.Where("username = ?", "carol").First(&carol).Error)
	doJSON(t, r, http.MethodDelete, "/admin/portal-users/"+itoa(float64(carol.ID)), super, "", http.StatusOK)
	doJSON(t, r, http.MethodPost, "/admin/portal-users", super, `{"username":"carol","password":"password2"}`, http.StatusOK)
	require.Equal(t, http.StatusUnauthorized, doReq(t, r, http.MethodGet, "/portal/api/me", pOld, ""), "门户旧 token 不得以同名新账号身份通过")
}

// ops 不能改已有渠道的 base_url（否则可把账号上游密钥引到自己服务器）；回传未改动的 base_url、改其他字段照常；
// super 可以改；ops 新建渠道（自带密钥）不受影响。
func TestOpsCannotChangeChannelBaseURLE2E(t *testing.T) {
	r, g, toks := setupRBACTest(t)
	ops, super := toks[model.RoleOps], toks[model.RoleSuper]

	ch := doJSON(t, r, http.MethodPost, "/admin/channels", ops,
		`{"type":"openai","name":"ops-own","base_url":"https://up.example.com","api_key":"sk-ops"}`, http.StatusOK)
	path := "/admin/channels/" + itoa(ch["id"].(float64))
	baseOf := func() string {
		var c model.Channel
		require.NoError(t, g.First(&c, uint64(ch["id"].(float64))).Error)
		return c.BaseURL
	}

	require.Equal(t, http.StatusForbidden, doReq(t, r, http.MethodPost, path, ops, `{"base_url":"https://attacker.example"}`))
	require.Equal(t, "https://up.example.com", baseOf(), "被拒时 base_url 不得改动")

	require.Equal(t, http.StatusOK, doReq(t, r, http.MethodPost, path, ops,
		`{"base_url":"https://up.example.com","name":"renamed","timeout":30}`), "整表单回传未改动的 base_url 应放行")

	require.Equal(t, http.StatusOK, doReq(t, r, http.MethodPost, path, super, `{"base_url":"https://up2.example.com"}`))
	require.Equal(t, "https://up2.example.com", baseOf(), "super 可改")
}

// 安全头：SPA 页面、静态资源与 JSON 接口都带 CSP/防点击劫持/nosniff；入口 HTML 不得含内联脚本（会被 CSP 拦）。
func TestSecurityHeadersE2E(t *testing.T) {
	r, _, toks := setupRBACTest(t)
	check := func(path, token string) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, path, nil)
		if token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}
		r.ServeHTTP(w, req)
		csp := w.Header().Get("Content-Security-Policy")
		require.Contains(t, csp, "script-src 'self' https://challenges.cloudflare.com", path)
		require.Contains(t, csp, "frame-ancestors 'none'", path)
		require.NotContains(t, csp, "'unsafe-eval'", path)
		require.Equal(t, "DENY", w.Header().Get("X-Frame-Options"), path)
		require.Equal(t, "nosniff", w.Header().Get("X-Content-Type-Options"), path)
		require.Equal(t, "no-referrer", w.Header().Get("Referrer-Policy"), path)
		return w
	}
	page := check("/", "")
	require.Equal(t, http.StatusOK, page.Code)
	require.NotRegexp(t, `<script>`, page.Body.String(), "入口 HTML 不得含内联脚本")
	require.Contains(t, page.Body.String(), `src="/theme-init.js"`)
	check("/theme-init.js", "")
	check("/admin/channels", toks[model.RoleSuper])
	check("/v1/models", "")
}
