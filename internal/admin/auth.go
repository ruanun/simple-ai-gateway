package admin

import (
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/ruanun/simple-ai-gateway/internal/httpx"
	"github.com/ruanun/simple-ai-gateway/internal/middleware"
	"github.com/ruanun/simple-ai-gateway/internal/model"
	"github.com/ruanun/simple-ai-gateway/internal/pwpolicy"
	"golang.org/x/crypto/bcrypt"
	"gorm.io/gorm"
)

// AuthHandler 处理后台登录，校验 AdminUser 后签发 JWT。
type AuthHandler struct {
	g           *gorm.DB
	jwtSecret   string
	expireHours int
	guard       *middleware.LoginGuard // 按用户名的登录失败锁定，nil=关闭
}

func NewAuthHandler(g *gorm.DB, secret string, expireHours int, guard *middleware.LoginGuard) *AuthHandler {
	return &AuthHandler{g: g, jwtSecret: secret, expireHours: expireHours, guard: guard}
}

func (h *AuthHandler) Login(c *gin.Context) {
	var body struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}
	if err := c.ShouldBindJSON(&body); err != nil {
		httpx.BadRequest(c, "请求参数不合法")
		return
	}
	succeed, ok := h.guard.Acquire("admin", body.Username)
	if !ok {
		httpx.Err(c, http.StatusTooManyRequests, "too_many_requests", "该账号登录失败次数过多，请稍后再试")
		return
	}
	var u model.AdminUser
	if err := h.g.Where("username = ?", body.Username).First(&u).Error; err != nil {
		pwpolicy.DummyCompare(body.Password) // 与密码错误分支耗时对齐，防按时间差枚举用户名
		httpx.Unauthorized(c, "用户名或密码错误")
		return
	}
	if bcrypt.CompareHashAndPassword([]byte(u.PasswordHash), []byte(body.Password)) != nil {
		httpx.Unauthorized(c, "用户名或密码错误")
		return
	}
	succeed() // 密码正确即退还名额（含账号已禁用的情形：那不是猜错密码）
	if u.Status != 1 {
		httpx.Forbidden(c, "账号已禁用")
		return
	}
	tok, err := middleware.IssueJWT(h.jwtSecret, u.ID, u.Username, "admin", u.TokenVersion, h.expireHours)
	if err != nil {
		httpx.Internal(c, "登录失败，请重试")
		return
	}
	role := u.Role
	if role == "" {
		role = model.RoleSuper // 存量行未回填时兜底
	}
	c.JSON(http.StatusOK, gin.H{"token": tok, "role": role, "username": u.Username})
}
