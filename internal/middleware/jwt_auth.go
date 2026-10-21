package middleware

import (
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/golang-jwt/jwt/v5"
	"github.com/ruanun/simple-ai-gateway/internal/httpx"
	"github.com/ruanun/simple-ai-gateway/internal/model"
	"gorm.io/gorm"
)

// gin context 键：JWTAuth 校验通过后写入的当前管理员用户名与角色。
const (
	CtxAdminUser = "admin_user"
	CtxAdminRole = "admin_role"
)

// IssueJWT 签发管理端/门户 token。typ 区分两面（admin|user），互不可换用。
// tokenVersion 写入 ver 声明，改密/重置递增后旧 token 在校验时因版本不符被拒（#17/#25）。
// uid 写入用户 ID：账号硬删除后再建同名账号，新行 ID 不同，旧 token 不会因用户名相同而复活。
func IssueJWT(secret string, uid uint64, username, typ string, tokenVersion, expireHours int) (string, error) {
	claims := jwt.MapClaims{
		"sub": username,
		"uid": uid,
		"typ": typ,
		"ver": tokenVersion,
		"exp": time.Now().Add(time.Duration(expireHours) * time.Hour).Unix(),
	}
	return jwt.NewWithClaims(jwt.SigningMethodHS256, claims).SignedString([]byte(secret))
}

// tokenClaims 校验通过的 token 声明。
type tokenClaims struct {
	sub string // 用户名
	uid uint64 // 用户 ID
	ver int    // token 版本
}

// verifyJWT 管理端/门户共用的 Bearer JWT 校验：取 token → HMAC 签名校验 → typ 精确等于 wantTyp
// （两面 token 不可换用）→ 必须带 uid。缺 typ/uid 的旧格式 token 一律拒绝，重新登录即可。
func verifyJWT(secret, authHeader, wantTyp string) (tokenClaims, bool) {
	tok := strings.TrimSpace(strings.TrimPrefix(authHeader, "Bearer "))
	parsed, err := jwt.Parse(tok, func(t *jwt.Token) (any, error) {
		if _, ok := t.Method.(*jwt.SigningMethodHMAC); !ok {
			return nil, jwt.ErrSignatureInvalid
		}
		return []byte(secret), nil
	})
	if err != nil || !parsed.Valid {
		return tokenClaims{}, false
	}
	claims, _ := parsed.Claims.(jwt.MapClaims)
	if typ, _ := claims["typ"].(string); typ != wantTyp {
		return tokenClaims{}, false
	}
	uid, _ := claims["uid"].(float64)
	if uid <= 0 {
		return tokenClaims{}, false
	}
	ver, _ := claims["ver"].(float64)
	sub, _ := parsed.Claims.GetSubject()
	return tokenClaims{sub: sub, uid: uint64(uid), ver: int(ver)}, true
}

// JWTAuth 校验管理端 Bearer JWT。lookup 非 nil 时每请求按 sub(用户名) 回查账号存在且启用，
// 比对用户 ID 与 token 版本并取实时角色——禁用/删除/改角色/改密对已签发 token 立即生效；
// nil 则跳过（单测用），角色按 super 处理。
func JWTAuth(secret string, lookup func(username string) (id uint64, role string, ver int, ok bool)) gin.HandlerFunc {
	return func(c *gin.Context) {
		tc, ok := verifyJWT(secret, c.GetHeader("Authorization"), "admin")
		if !ok {
			httpx.Unauthorized(c, "登录已过期，请重新登录")
			return
		}
		role := model.RoleSuper
		if lookup != nil {
			id, r, dbVer, ok := lookup(tc.sub)
			// 账号失效 / 同名重建（ID 不符）/ 改密后版本不符 → 拒绝(#17)
			if !ok || id != tc.uid || dbVer != tc.ver {
				httpx.Unauthorized(c, "登录已过期，请重新登录")
				return
			}
			role = r
		}
		c.Set(CtxAdminUser, tc.sub)
		c.Set(CtxAdminRole, role)
		c.Next()
	}
}

// gin context 键：PortalAuth 校验通过后写入的门户用户名与用户 ID。
const (
	CtxPortalUser   = "portal_user"
	CtxPortalUserID = "portal_user_id"
)

// PortalAuth 校验使用方门户 Bearer JWT（typ 必须为 user，管理端 token 不可换用）。
// lookup 每请求按用户名回查账号存在且启用并取 ID——禁用/删除立即生效。
func PortalAuth(secret string, lookup func(username string) (id uint64, ver int, ok bool)) gin.HandlerFunc {
	return func(c *gin.Context) {
		tc, ok := verifyJWT(secret, c.GetHeader("Authorization"), "user")
		if !ok {
			httpx.Unauthorized(c, "登录已过期，请重新登录")
			return
		}
		id, dbVer, ok := lookup(tc.sub)
		// 账号失效 / 同名重建（ID 不符）/ 改密后版本不符 → 拒绝(#25)
		if !ok || id != tc.uid || dbVer != tc.ver {
			httpx.Unauthorized(c, "登录已过期，请重新登录")
			return
		}
		c.Set(CtxPortalUser, tc.sub)
		c.Set(CtxPortalUserID, id)
		c.Next()
	}
}

// AdminRBAC 固定三角色路由级强制（super 全部 / ops 除用户管理与系统设置外全部 /
// viewer 仅 GET）。前端隐藏只是体验层，强制以此处为准。
func AdminRBAC() gin.HandlerFunc {
	return func(c *gin.Context) {
		role := c.GetString(CtxAdminRole)
		if role == model.RoleSuper {
			c.Next()
			return
		}
		m, p := c.Request.Method, c.Request.URL.Path
		// 改自己密码：全角色放行
		if p == "/admin/profile/password" {
			c.Next()
			return
		}
		// super 专属区：管理员账号管理、系统设置（含 OIDC secret 等敏感配置），读写全拦
		if strings.HasPrefix(p, "/admin/users") || strings.HasPrefix(p, "/admin/settings") {
			forbidRole(c)
			return
		}
		// 操作日志：ops 可读，viewer 不可见
		if strings.HasPrefix(p, "/admin/op-logs") {
			if role == model.RoleOps && m == http.MethodGet {
				c.Next()
				return
			}
			forbidRole(c)
			return
		}
		// 其余资源：GET 全角色可读；写仅 ops（未知角色按最小权限=viewer 处理）
		if m == http.MethodGet || role == model.RoleOps {
			c.Next()
			return
		}
		forbidRole(c)
	}
}

func forbidRole(c *gin.Context) {
	httpx.Forbidden(c, "当前角色无此权限")
}

// OpLog 管理操作日志（P9 最小版）：写方法完成后落一行操作记录，失败仅记日志不阻断。
func OpLog(g *gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		c.Next()
		m := c.Request.Method
		if m != http.MethodPost && m != http.MethodPut && m != http.MethodPatch && m != http.MethodDelete {
			return
		}
		actor, actorType, role := c.GetString(CtxAdminUser), "admin", c.GetString(CtxAdminRole)
		if actor == "" {
			if pu := c.GetString(CtxPortalUser); pu != "" {
				actor, actorType, role = pu, "user", ""
			}
		}
		row := model.AdminOpLog{
			ActorType: actorType,
			Actor:     actor,
			Role:      role,
			Method:    m,
			Path:      c.Request.URL.Path,
			Status:    c.Writer.Status(),
			ClientIP:  c.ClientIP(),
		}
		if err := g.Create(&row).Error; err != nil {
			slog.Error("oplog: 写入失败", "err", err)
		}
	}
}
