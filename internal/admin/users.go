package admin

import (
	"log/slog"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/ruanun/simple-ai-gateway/internal/httpx"
	"github.com/ruanun/simple-ai-gateway/internal/middleware"
	"github.com/ruanun/simple-ai-gateway/internal/model"
	"github.com/ruanun/simple-ai-gateway/internal/pwpolicy"
	"golang.org/x/crypto/bcrypt"
	"gorm.io/gorm"
)

// UserHandler 管理员账号管理（P9 用户管理最小集）：
// 列表/新建/重置密码/启停/删除 + 修改自己密码。
// 所有登录者同权（超级管理员），仅靠"最后一个启用账号"与"自身"两类护栏防锁死。
type UserHandler struct {
	g *gorm.DB
}

func NewUserHandler(g *gorm.DB) *UserHandler {
	return &UserHandler{g: g}
}

// currentUsername 取 JWTAuth 注入的当前登录用户名。
func currentUsername(c *gin.Context) string {
	v, _ := c.Get(middleware.CtxAdminUser)
	s, _ := v.(string)
	return s
}

func (h *UserHandler) List(c *gin.Context) {
	var rows []model.AdminUser
	h.g.Order("id desc").Find(&rows) // 新增管理员置顶
	c.JSON(http.StatusOK, rows)
}

// validRole 角色白名单。
func validRole(r string) bool {
	return r == model.RoleSuper || r == model.RoleOps || r == model.RoleViewer
}

func (h *UserHandler) Create(c *gin.Context) {
	var body struct {
		Username string `json:"username"`
		Password string `json:"password"`
		Role     string `json:"role"`
	}
	if err := c.ShouldBindJSON(&body); err != nil {
		httpx.BadRequest(c, "请求参数不合法")
		return
	}
	body.Username = strings.TrimSpace(body.Username)
	if body.Username == "" || len(body.Username) > 64 {
		httpx.BadRequest(c, "用户名不能为空且不超过 64 字符")
		return
	}
	if !pwpolicy.Valid(body.Password) {
		httpx.BadRequest(c, pwpolicy.ErrMsg)
		return
	}
	if body.Role == "" {
		body.Role = model.RoleViewer // 未指定时按最小权限
	}
	if !validRole(body.Role) {
		httpx.BadRequest(c, "role 仅支持 super/ops/viewer")
		return
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(body.Password), bcrypt.DefaultCost)
	if err != nil {
		slog.Error("admin: 生成密码哈希失败", "err", err)
		httpx.Internal(c, "密码加密失败")
		return
	}
	u := model.AdminUser{Username: body.Username, PasswordHash: string(hash), Status: 1, Role: body.Role}
	if err := h.g.Create(&u).Error; err != nil {
		httpx.Conflict(c, "用户名已存在")
		return
	}
	c.JSON(http.StatusOK, u)
}

// Update 重置密码 / 启停 / 改角色。三字段均可选，至少传其一。
func (h *UserHandler) Update(c *gin.Context) {
	var body struct {
		Password *string `json:"password"`
		Status   *int8   `json:"status"`
		Role     *string `json:"role"`
	}
	if err := c.ShouldBindJSON(&body); err != nil {
		httpx.BadRequest(c, "请求参数不合法")
		return
	}
	var u model.AdminUser
	if err := h.g.First(&u, "id = ?", c.Param("id")).Error; err != nil {
		httpx.NotFound(c, "用户不存在")
		return
	}
	updates := map[string]any{}
	if body.Password != nil {
		if !pwpolicy.Valid(*body.Password) {
			httpx.BadRequest(c, pwpolicy.ErrMsg)
			return
		}
		hash, err := bcrypt.GenerateFromPassword([]byte(*body.Password), bcrypt.DefaultCost)
		if err != nil {
			slog.Error("admin: 生成密码哈希失败", "err", err)
			httpx.Internal(c, "密码加密失败")
			return
		}
		updates["password_hash"] = string(hash)
		updates["token_version"] = gorm.Expr("token_version + 1") // 重置密码即踢下线(#17)
	}
	if body.Status != nil {
		if *body.Status != 0 && *body.Status != 1 {
			httpx.BadRequest(c, "status 仅支持 0/1")
			return
		}
		if *body.Status == 0 {
			if u.Username == currentUsername(c) {
				httpx.BadRequest(c, "不能禁用当前登录账号")
				return
			}
			if u.Status == 1 && h.enabledCountExcept(u.ID) == 0 {
				httpx.BadRequest(c, "至少保留一个启用的管理员")
				return
			}
			if u.Status == 1 && u.Role == model.RoleSuper && h.enabledSuperCountExcept(u.ID) == 0 {
				httpx.BadRequest(c, "至少保留一个启用的超管")
				return
			}
		}
		updates["status"] = *body.Status
	}
	if body.Role != nil {
		if !validRole(*body.Role) {
			httpx.BadRequest(c, "role 仅支持 super/ops/viewer")
			return
		}
		if u.Username == currentUsername(c) {
			httpx.BadRequest(c, "不能修改当前登录账号的角色")
			return
		}
		if u.Role == model.RoleSuper && *body.Role != model.RoleSuper &&
			u.Status == 1 && h.enabledSuperCountExcept(u.ID) == 0 {
			httpx.BadRequest(c, "至少保留一个启用的超管")
			return
		}
		updates["role"] = *body.Role
	}
	if len(updates) == 0 {
		httpx.BadRequest(c, "未提供需要更新的字段")
		return
	}
	if err := h.g.Model(&u).Updates(updates).Error; err != nil {
		slog.Error("admin: 更新管理员用户失败", "id", u.ID, "err", err)
		httpx.Internal(c, "更新失败")
		return
	}
	c.JSON(http.StatusOK, u)
}

func (h *UserHandler) Delete(c *gin.Context) {
	var u model.AdminUser
	if err := h.g.First(&u, "id = ?", c.Param("id")).Error; err != nil {
		httpx.NotFound(c, "用户不存在")
		return
	}
	if u.Username == currentUsername(c) {
		httpx.BadRequest(c, "不能删除当前登录账号")
		return
	}
	if u.Status == 1 && h.enabledCountExcept(u.ID) == 0 {
		httpx.BadRequest(c, "至少保留一个启用的管理员")
		return
	}
	if u.Status == 1 && u.Role == model.RoleSuper && h.enabledSuperCountExcept(u.ID) == 0 {
		httpx.BadRequest(c, "至少保留一个启用的超管")
		return
	}
	if err := h.g.Delete(&model.AdminUser{}, u.ID).Error; err != nil {
		slog.Error("admin: 删除管理员用户失败", "id", u.ID, "err", err)
		httpx.Internal(c, "删除失败")
		return
	}
	c.JSON(http.StatusOK, gin.H{"ok": true})
}

// ChangePassword 当前登录账号修改自己的密码（须验证旧密码）。
func (h *UserHandler) ChangePassword(c *gin.Context) {
	var body struct {
		OldPassword string `json:"old_password"`
		NewPassword string `json:"new_password"`
	}
	if err := c.ShouldBindJSON(&body); err != nil {
		httpx.BadRequest(c, "请求参数不合法")
		return
	}
	if !pwpolicy.Valid(body.NewPassword) {
		httpx.BadRequest(c, pwpolicy.ErrMsg)
		return
	}
	var u model.AdminUser
	if err := h.g.Where("username = ?", currentUsername(c)).First(&u).Error; err != nil {
		httpx.Unauthorized(c, "登录已过期，请重新登录")
		return
	}
	if bcrypt.CompareHashAndPassword([]byte(u.PasswordHash), []byte(body.OldPassword)) != nil {
		httpx.BadRequest(c, "旧密码不正确")
		return
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(body.NewPassword), bcrypt.DefaultCost)
	if err != nil {
		slog.Error("admin: 生成密码哈希失败", "err", err)
		httpx.Internal(c, "密码加密失败")
		return
	}
	// 改密同时递增 token_version，使改密前签发的 JWT 立即失效(#17)。
	if err := h.g.Model(&u).Updates(map[string]any{
		"password_hash": string(hash),
		"token_version": gorm.Expr("token_version + 1"),
	}).Error; err != nil {
		slog.Error("admin: 修改密码失败", "id", u.ID, "err", err)
		httpx.Internal(c, "更新失败")
		return
	}
	c.JSON(http.StatusOK, gin.H{"ok": true})
}

// enabledCountExcept 排除指定 id 后仍处于启用状态的管理员数量。
func (h *UserHandler) enabledCountExcept(id uint64) int64 {
	var n int64
	h.g.Model(&model.AdminUser{}).Where("status = 1 AND id <> ?", id).Count(&n)
	return n
}

// enabledSuperCountExcept 排除指定 id 后仍处于启用状态的超管数量（防降级/删光锁死）。
func (h *UserHandler) enabledSuperCountExcept(id uint64) int64 {
	var n int64
	h.g.Model(&model.AdminUser{}).Where("status = 1 AND role = ? AND id <> ?", model.RoleSuper, id).Count(&n)
	return n
}
