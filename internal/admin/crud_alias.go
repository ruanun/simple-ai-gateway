// 模型别名与别名路由边管理接口（含批量建规范名别名）。
package admin

import (
	"net/http"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/ruanun/simple-ai-gateway/internal/httpx"
	"github.com/ruanun/simple-ai-gateway/internal/model"
	"gorm.io/gorm"
)

// ---- ModelAlias ----
func (h *CRUDHandler) ListAliases(c *gin.Context) {
	rows, err := h.aliases.ListAliases()
	if err != nil {
		httpx.Internal(c, "查询失败")
		return
	}
	// 附带每个别名的路由边数（一次 GROUP BY），供前端区分「重命名(1边)/智能路由(≥2边)/未配置(0边)」
	type cnt struct {
		AliasID uint64
		N       int
	}
	var cs []cnt
	h.g.Model(&model.AliasRoute{}).Select("alias_id, count(*) as n").Group("alias_id").Scan(&cs)
	counts := map[uint64]int{}
	for _, x := range cs {
		counts[x.AliasID] = x.N
	}
	type aliasWithCount struct {
		model.ModelAlias
		RouteCount int `json:"route_count"`
	}
	out := make([]aliasWithCount, 0, len(rows))
	for i := range rows {
		out = append(out, aliasWithCount{ModelAlias: rows[i], RouteCount: counts[rows[i].ID]})
	}
	c.JSON(http.StatusOK, out)
}
func (h *CRUDHandler) CreateAlias(c *gin.Context) {
	var body struct {
		Alias       string `json:"alias"`
		Description string `json:"description"`
	}
	if err := c.ShouldBindJSON(&body); err != nil || strings.TrimSpace(body.Alias) == "" {
		httpx.BadRequest(c, "请求参数不合法")
		return
	}
	body.Alias = strings.TrimSpace(body.Alias)
	// 重名预检：给出友好 409，而非撞唯一索引 500
	if exists, err := h.aliases.AliasExists(body.Alias); err == nil && exists {
		httpx.Conflict(c, "别名已存在")
		return
	}
	a, err := h.aliases.CreateAlias(body.Alias)
	if err != nil {
		httpx.Internal(c, "创建失败")
		return
	}
	if body.Description != "" {
		h.g.Model(&model.ModelAlias{}).Where("id = ?", a.ID).Update("description", body.Description)
		a.Description = body.Description
	}
	if err := h.notifyRouterChange(); err != nil {
		httpx.Internal(c, "路由表刷新失败")
		return
	}
	c.JSON(http.StatusOK, a)
}

// UpdateAlias 更新别名说明/启停（POST /admin/aliases/:id，指针=未传不改）。
// 别名名本身不可改（对外契约，改名请删除重建）；status 变化影响路由表，改后热加载。
func (h *CRUDHandler) UpdateAlias(c *gin.Context) {
	id, _ := strconv.ParseUint(c.Param("id"), 10, 64)
	var body struct {
		Description *string `json:"description"`
		Status      *int8   `json:"status"`
	}
	if err := c.ShouldBindJSON(&body); err != nil {
		httpx.BadRequest(c, "请求参数不合法")
		return
	}
	fields := map[string]any{}
	if body.Description != nil {
		fields["description"] = *body.Description
	}
	if body.Status != nil {
		fields["status"] = *body.Status
	}
	if len(fields) == 0 {
		c.JSON(http.StatusOK, gin.H{"ok": true})
		return
	}
	if err := h.aliases.UpdateAlias(id, fields); err != nil {
		httpx.Internal(c, "更新失败")
		return
	}
	if err := h.notifyRouterChange(); err != nil {
		httpx.Internal(c, "路由表刷新失败")
		return
	}
	c.JSON(http.StatusOK, gin.H{"ok": true})
}

func (h *CRUDHandler) DeleteAlias(c *gin.Context) {
	id, _ := strconv.ParseUint(c.Param("id"), 10, 64)
	if err := h.aliases.DeleteAlias(id); err != nil {
		httpx.Internal(c, "删除失败")
		return
	}
	if err := h.notifyRouterChange(); err != nil {
		httpx.Internal(c, "路由表刷新失败")
		return
	}
	c.JSON(http.StatusOK, gin.H{"ok": true})
}

// ---- AliasRoute ----
func (h *CRUDHandler) ListRoutes(c *gin.Context) {
	aliasID, _ := strconv.ParseUint(c.Param("id"), 10, 64)
	rows, err := h.aliases.ListRoutes(aliasID)
	if err != nil {
		httpx.Internal(c, "查询失败")
		return
	}
	c.JSON(http.StatusOK, rows)
}
func (h *CRUDHandler) AddRoute(c *gin.Context) {
	var body struct {
		AliasID   uint64 `json:"alias_id"`
		ChannelID uint64 `json:"channel_id"`
		Model     string `json:"model"`
		Weight    int    `json:"weight"`
		Priority  int    `json:"priority"`
	}
	if err := c.ShouldBindJSON(&body); err != nil || body.AliasID == 0 || body.ChannelID == 0 || body.Model == "" {
		httpx.BadRequest(c, "请求参数不合法")
		return
	}
	if body.Weight < 1 {
		body.Weight = 1
	}
	r, err := h.aliases.AddRoute(body.AliasID, body.ChannelID, body.Model, body.Weight, body.Priority)
	if err != nil {
		httpx.Internal(c, "创建失败")
		return
	}
	if err := h.notifyRouterChange(); err != nil {
		httpx.Internal(c, "路由表刷新失败")
		return
	}
	c.JSON(http.StatusOK, r)
}

// UpdateRoute 更新路由边权重/优先级（POST /admin/alias-routes/:id）。渠道/模型不可改。
func (h *CRUDHandler) UpdateRoute(c *gin.Context) {
	id, _ := strconv.ParseUint(c.Param("id"), 10, 64)
	var body struct {
		Weight   int `json:"weight"`
		Priority int `json:"priority"`
	}
	if err := c.ShouldBindJSON(&body); err != nil {
		httpx.BadRequest(c, "请求参数不合法")
		return
	}
	if body.Weight < 1 {
		body.Weight = 1
	}
	if err := h.aliases.UpdateRoute(id, body.Weight, body.Priority); err != nil {
		httpx.Internal(c, "更新失败")
		return
	}
	if err := h.notifyRouterChange(); err != nil {
		httpx.Internal(c, "路由表刷新失败")
		return
	}
	c.JSON(http.StatusOK, gin.H{"ok": true})
}

// BatchAddAliasRoutes 批量建规范名别名（POST /admin/alias-routes/batch）：
// 每项 upsert 别名（撞唯一索引则复用现有别名——第二个渠道的同名规范化自动变成加边）
// + 路由边查重后创建（weight=1、priority=0）。单事务保证原子性，事务后仅重建一次路由表。
func (h *CRUDHandler) BatchAddAliasRoutes(c *gin.Context) {
	var body struct {
		Items []struct {
			Alias     string `json:"alias"`
			ChannelID uint64 `json:"channel_id"`
			Model     string `json:"model"`
		} `json:"items"`
	}
	if err := c.ShouldBindJSON(&body); err != nil || len(body.Items) == 0 {
		httpx.BadRequest(c, "请求参数不合法")
		return
	}
	var created, reusedAlias, skipped int
	err := h.g.Transaction(func(tx *gorm.DB) error {
		for _, it := range body.Items {
			aliasName := strings.TrimSpace(it.Alias)
			modelName := strings.TrimSpace(it.Model)
			if aliasName == "" || modelName == "" || it.ChannelID == 0 {
				skipped++
				continue
			}
			var a model.ModelAlias
			res := tx.Where("alias = ?", aliasName).FirstOrCreate(&a, model.ModelAlias{Alias: aliasName, Status: 1})
			if res.Error != nil {
				return res.Error
			}
			if res.RowsAffected == 0 {
				reusedAlias++
			}
			var n int64
			tx.Model(&model.AliasRoute{}).
				Where("alias_id = ? AND channel_id = ? AND model = ?", a.ID, it.ChannelID, modelName).Count(&n)
			if n > 0 {
				skipped++
				continue
			}
			if err := tx.Create(&model.AliasRoute{AliasID: a.ID, ChannelID: it.ChannelID, Model: modelName, Weight: 1, Priority: 0}).Error; err != nil {
				return err
			}
			created++
		}
		return nil
	})
	if err != nil {
		httpx.Internal(c, "批量创建失败")
		return
	}
	if err := h.notifyRouterChange(); err != nil {
		httpx.Internal(c, "路由表刷新失败")
		return
	}
	c.JSON(http.StatusOK, gin.H{"created": created, "reused_alias": reusedAlias, "skipped": skipped})
}

func (h *CRUDHandler) DeleteRoute(c *gin.Context) {
	id, _ := strconv.ParseUint(c.Param("id"), 10, 64)
	if err := h.aliases.DeleteRoute(id); err != nil {
		httpx.Internal(c, "删除失败")
		return
	}
	if err := h.notifyRouterChange(); err != nil {
		httpx.Internal(c, "路由表刷新失败")
		return
	}
	c.JSON(http.StatusOK, gin.H{"ok": true})
}
