package admin

import (
	"log/slog"
	"net/http"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/ruanun/simple-ai-gateway/internal/httpx"
	"github.com/ruanun/simple-ai-gateway/internal/model"
	"github.com/ruanun/simple-ai-gateway/internal/portal"
	"github.com/ruanun/simple-ai-gateway/internal/pwpolicy"
	"golang.org/x/crypto/bcrypt"
	"gorm.io/gorm"
)

// PortalUserHandler 管理端的使用方用户管理（P9-B）：建号/重置密码/启停/删除 + App 归属分配。
type PortalUserHandler struct {
	g *gorm.DB
}

func NewPortalUserHandler(g *gorm.DB) *PortalUserHandler {
	return &PortalUserHandler{g: g}
}

// List 用户列表，附带各自名下 App 数。
func (h *PortalUserHandler) List(c *gin.Context) {
	var rows []model.PortalUser
	h.g.Order("id desc").Find(&rows) // 新增门户成员置顶
	type item struct {
		model.PortalUser
		AppCount int64 `json:"app_count"`
	}
	out := make([]item, 0, len(rows))
	for _, u := range rows {
		var n int64
		h.g.Model(&model.AppMember{}).Where("portal_user_id = ?", u.ID).Count(&n)
		out = append(out, item{PortalUser: u, AppCount: n})
	}
	c.JSON(http.StatusOK, out)
}

// Create 管理员建号。with_app=true 时同时建同名默认 App（配额取 quota，micros）。
func (h *PortalUserHandler) Create(c *gin.Context) {
	var body struct {
		Username string `json:"username"`
		Password string `json:"password"`
		WithApp  bool   `json:"with_app"`
		Quota    int64  `json:"quota"`
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
	if body.WithApp {
		u, err := portal.CreateUserWithDefaultApp(h.g, body.Username, body.Password, body.Quota)
		if err != nil {
			httpx.Conflict(c, "用户名已存在")
			return
		}
		c.JSON(http.StatusOK, u)
		return
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(body.Password), bcrypt.DefaultCost)
	if err != nil {
		slog.Error("admin: 生成密码哈希失败", "err", err)
		httpx.Internal(c, "密码加密失败")
		return
	}
	u := model.PortalUser{Username: body.Username, PasswordHash: string(hash), Status: 1, Source: "local"}
	if err := h.g.Create(&u).Error; err != nil {
		httpx.Conflict(c, "用户名已存在")
		return
	}
	c.JSON(http.StatusOK, u)
}

// Update 重置密码 / 启停。
func (h *PortalUserHandler) Update(c *gin.Context) {
	var body struct {
		Password *string `json:"password"`
		Status   *int8   `json:"status"`
	}
	if err := c.ShouldBindJSON(&body); err != nil {
		httpx.BadRequest(c, "请求参数不合法")
		return
	}
	var u model.PortalUser
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
		updates["token_version"] = gorm.Expr("token_version + 1") // 重置密码即踢下线(#25)
	}
	if body.Status != nil {
		if *body.Status != 0 && *body.Status != 1 {
			httpx.BadRequest(c, "status 仅支持 0/1")
			return
		}
		updates["status"] = *body.Status
	}
	if len(updates) == 0 {
		httpx.BadRequest(c, "未提供需要更新的字段")
		return
	}
	if err := h.g.Model(&u).Updates(updates).Error; err != nil {
		slog.Error("admin: 更新门户用户失败", "id", u.ID, "err", err)
		httpx.Internal(c, "更新失败")
		return
	}
	c.JSON(http.StatusOK, u)
}

// Delete 删除用户；其名下 App 归属重置为 0（App/令牌/日志保留）。
func (h *PortalUserHandler) Delete(c *gin.Context) {
	var u model.PortalUser
	if err := h.g.First(&u, "id = ?", c.Param("id")).Error; err != nil {
		httpx.NotFound(c, "用户不存在")
		return
	}
	err := h.g.Transaction(func(tx *gorm.DB) error {
		if err := tx.Model(&model.App{}).Where("owner_user_id = ?", u.ID).Update("owner_user_id", 0).Error; err != nil {
			return err
		}
		if err := tx.Where("portal_user_id = ?", u.ID).Delete(&model.AppMember{}).Error; err != nil {
			return err
		}
		return tx.Delete(&model.PortalUser{}, u.ID).Error
	})
	if err != nil {
		slog.Error("admin: 删除门户用户失败", "id", u.ID, "err", err)
		httpx.Internal(c, "删除失败")
		return
	}
	c.JSON(http.StatusOK, gin.H{"ok": true})
}

// SetAppOwner 把 App 分配给门户用户（user_id=0 收回为无主）。
func (h *PortalUserHandler) SetAppOwner(c *gin.Context) {
	var body struct {
		UserID uint64 `json:"user_id"`
	}
	if err := c.ShouldBindJSON(&body); err != nil {
		httpx.BadRequest(c, "请求参数不合法")
		return
	}
	var app model.App
	if err := h.g.First(&app, "id = ?", c.Param("id")).Error; err != nil {
		httpx.NotFound(c, "App 不存在")
		return
	}
	if body.UserID != 0 {
		var n int64
		h.g.Model(&model.PortalUser{}).Where("id = ?", body.UserID).Count(&n)
		if n == 0 {
			httpx.BadRequest(c, "目标用户不存在")
			return
		}
	}
	oldOwner := app.OwnerUserID
	if err := h.g.Model(&app).Update("owner_user_id", body.UserID).Error; err != nil {
		slog.Error("admin: 变更 App 归属失败", "app_id", app.ID, "err", err)
		httpx.Internal(c, "更新失败")
		return
	}
	app.OwnerUserID = body.UserID
	// 改派/收回时吊销旧 owner 的成员行：AppMember 是门户访问权唯一真相源，漏清会让
	// 被收回的用户仍能管理该 App 的 key/日志并烧其配额（确需保留为普通成员可在成员管理加回）。
	if oldOwner != 0 && oldOwner != body.UserID {
		h.g.Where("app_id = ? AND portal_user_id = ?", app.ID, oldOwner).Delete(&model.AppMember{})
	}
	// owner 始终是成员：分配后确保存在对应成员行（幂等）。
	if body.UserID != 0 {
		var ex int64
		h.g.Model(&model.AppMember{}).Where("app_id = ? AND portal_user_id = ?", app.ID, body.UserID).Count(&ex)
		if ex == 0 {
			h.g.Create(&model.AppMember{AppID: app.ID, PortalUserID: body.UserID})
		}
	}
	c.JSON(http.StatusOK, app)
}

// ListAppMembers 列出某 App 的成员（含用户名与是否 owner）。
func (h *PortalUserHandler) ListAppMembers(c *gin.Context) {
	var app model.App
	if err := h.g.First(&app, "id = ?", c.Param("id")).Error; err != nil {
		httpx.NotFound(c, "App 不存在")
		return
	}
	var members []model.AppMember
	h.g.Where("app_id = ?", app.ID).Order("id desc").Find(&members) // 新增成员置顶(owner 靠 is_owner 字段区分,不依赖位置)
	type item struct {
		UserID   uint64 `json:"user_id"`
		Username string `json:"username"`
		IsOwner  bool   `json:"is_owner"`
	}
	out := make([]item, 0, len(members))
	for _, m := range members {
		var u model.PortalUser
		h.g.First(&u, m.PortalUserID)
		out = append(out, item{UserID: m.PortalUserID, Username: u.Username, IsOwner: m.PortalUserID == app.OwnerUserID})
	}
	c.JSON(http.StatusOK, out)
}

// AddAppMember 给 App 加成员（幂等）。
func (h *PortalUserHandler) AddAppMember(c *gin.Context) {
	var body struct {
		UserID uint64 `json:"user_id"`
	}
	if err := c.ShouldBindJSON(&body); err != nil || body.UserID == 0 {
		httpx.BadRequest(c, "请求参数不合法")
		return
	}
	var app model.App
	if err := h.g.First(&app, "id = ?", c.Param("id")).Error; err != nil {
		httpx.NotFound(c, "App 不存在")
		return
	}
	var n int64
	h.g.Model(&model.PortalUser{}).Where("id = ?", body.UserID).Count(&n)
	if n == 0 {
		httpx.BadRequest(c, "目标用户不存在")
		return
	}
	// 幂等：已存在则跳过
	var existing int64
	h.g.Model(&model.AppMember{}).Where("app_id = ? AND portal_user_id = ?", app.ID, body.UserID).Count(&existing)
	if existing == 0 {
		if err := h.g.Create(&model.AppMember{AppID: app.ID, PortalUserID: body.UserID}).Error; err != nil {
			slog.Error("admin: 添加 App 成员失败", "app_id", app.ID, "user_id", body.UserID, "err", err)
			httpx.Internal(c, "创建失败")
			return
		}
	}
	c.JSON(http.StatusOK, gin.H{"ok": true})
}

// RemoveAppMember 移除成员；拒绝移除 owner（须先改 owner）。
func (h *PortalUserHandler) RemoveAppMember(c *gin.Context) {
	var app model.App
	if err := h.g.First(&app, "id = ?", c.Param("id")).Error; err != nil {
		httpx.NotFound(c, "App 不存在")
		return
	}
	userID, _ := strconv.ParseUint(c.Param("userId"), 10, 64)
	if userID == app.OwnerUserID {
		httpx.BadRequest(c, "不能移除所有者，请先变更所有者")
		return
	}
	if err := h.g.Where("app_id = ? AND portal_user_id = ?", app.ID, userID).Delete(&model.AppMember{}).Error; err != nil {
		slog.Error("admin: 移除 App 成员失败", "app_id", app.ID, "user_id", userID, "err", err)
		httpx.Internal(c, "删除失败")
		return
	}
	c.JSON(http.StatusOK, gin.H{"ok": true})
}
