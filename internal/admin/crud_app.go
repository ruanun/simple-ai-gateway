// 应用（App，配额主体）管理接口（含审计开关）。应用用量趋势 AppUsageTrend 见 crud_dashboard.go。
package admin

import (
	"net/http"
	"strconv"

	"github.com/gin-gonic/gin"
	"github.com/ruanun/simple-ai-gateway/internal/httpx"
	"github.com/ruanun/simple-ai-gateway/internal/model"
)

// ---- App（配额主体）----
func (h *CRUDHandler) ListApps(c *gin.Context) {
	rows, err := h.apps.List()
	if err != nil {
		httpx.Internal(c, "查询失败")
		return
	}
	c.JSON(http.StatusOK, rows)
}
func (h *CRUDHandler) CreateApp(c *gin.Context) {
	var body struct {
		Name            string `json:"name"`
		Quota           int64  `json:"quota"`
		AllowedChannels string `json:"allowed_channels"`
		ModelWhitelist  string `json:"model_whitelist"`
		MaxConcurrency  int    `json:"max_concurrency"`
	}
	if err := c.ShouldBindJSON(&body); err != nil || body.Name == "" {
		httpx.BadRequest(c, "请求参数不合法")
		return
	}
	if !model.ValidLimit(body.MaxConcurrency) {
		httpx.BadRequest(c, "并发上限"+model.LimitErrMsg)
		return
	}
	a := &model.App{Name: body.Name, Quota: body.Quota, Status: 1, AllowedChannels: body.AllowedChannels,
		ModelWhitelist: model.NormalizeList(body.ModelWhitelist), MaxConcurrency: body.MaxConcurrency}
	if err := h.g.Create(a).Error; err != nil {
		httpx.Internal(c, "创建失败")
		return
	}
	c.JSON(http.StatusOK, a)
}
func (h *CRUDHandler) DeleteApp(c *gin.Context) {
	id, _ := strconv.ParseUint(c.Param("id"), 10, 64)
	if err := h.apps.Delete(id); err != nil {
		httpx.Internal(c, "删除失败")
		return
	}
	c.JSON(http.StatusOK, gin.H{"ok": true})
}

// UpdateApp 更新应用 name/quota/status/allowed_channels/model_whitelist/max_concurrency
// (指针=未传则不改;不动 used_quota)。
func (h *CRUDHandler) UpdateApp(c *gin.Context) {
	id, _ := strconv.ParseUint(c.Param("id"), 10, 64)
	var body struct {
		Name            *string `json:"name"`
		Quota           *int64  `json:"quota"`
		Status          *int8   `json:"status"`
		AllowedChannels *string `json:"allowed_channels"`
		ModelWhitelist  *string `json:"model_whitelist"`
		MaxConcurrency  *int    `json:"max_concurrency"`
	}
	if err := c.ShouldBindJSON(&body); err != nil {
		httpx.BadRequest(c, "请求参数不合法")
		return
	}
	fields := map[string]any{}
	if body.ModelWhitelist != nil {
		fields["model_whitelist"] = model.NormalizeList(*body.ModelWhitelist)
	}
	if body.MaxConcurrency != nil {
		if !model.ValidLimit(*body.MaxConcurrency) {
			httpx.BadRequest(c, "并发上限"+model.LimitErrMsg)
			return
		}
		fields["max_concurrency"] = *body.MaxConcurrency
	}
	if body.Name != nil {
		fields["name"] = *body.Name
	}
	if body.Quota != nil {
		fields["quota"] = *body.Quota
	}
	if body.Status != nil {
		fields["status"] = *body.Status
	}
	if body.AllowedChannels != nil {
		fields["allowed_channels"] = *body.AllowedChannels
	}
	if len(fields) == 0 {
		c.JSON(http.StatusOK, gin.H{"ok": true})
		return
	}
	if err := h.g.Model(&model.App{}).Where("id = ?", id).Updates(fields).Error; err != nil {
		httpx.Internal(c, "更新失败")
		return
	}
	c.JSON(http.StatusOK, gin.H{"ok": true})
}

// SetAppAudit 切换 App 的取证级审计开关（audit_enabled）。
func (h *CRUDHandler) SetAppAudit(c *gin.Context) {
	id, _ := strconv.ParseUint(c.Param("id"), 10, 64)
	var body struct {
		AuditEnabled bool `json:"audit_enabled"`
	}
	if err := c.ShouldBindJSON(&body); err != nil {
		httpx.BadRequest(c, "请求参数不合法")
		return
	}
	if err := h.g.Model(&model.App{}).Where("id = ?", id).Update("audit_enabled", body.AuditEnabled).Error; err != nil {
		httpx.Internal(c, "更新失败")
		return
	}
	c.JSON(http.StatusOK, gin.H{"ok": true})
}
