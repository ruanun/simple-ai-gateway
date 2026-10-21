// API 密钥与令牌标签（KeyTag）管理接口。
package admin

import (
	"net/http"
	"strconv"

	"github.com/gin-gonic/gin"
	"github.com/ruanun/simple-ai-gateway/internal/httpx"
	"github.com/ruanun/simple-ai-gateway/internal/model"
)

// ---- APIKey ----
// ListAPIKeys 列出密钥；可选 ?tag_id= 按标签筛选。每项附带其绑定的标签列表。
func (h *CRUDHandler) ListAPIKeys(c *gin.Context) {
	var rows []model.APIKey
	q := h.g.Order("id desc")
	if tidStr := c.Query("tag_id"); tidStr != "" {
		tid, _ := strconv.ParseUint(tidStr, 10, 64)
		ids, err := h.tags.KeyIDsByTag(tid)
		if err != nil {
			httpx.Internal(c, "查询失败")
			return
		}
		if len(ids) == 0 {
			c.JSON(http.StatusOK, []any{})
			return
		}
		q = q.Where("id IN ?", ids)
	}
	if err := q.Find(&rows).Error; err != nil {
		httpx.Internal(c, "查询失败")
		return
	}
	// 批量取每把 key 的标签，一次查询按 key 分组，避免 N+1(E2)。
	keyIDs := make([]uint64, len(rows))
	for i := range rows {
		keyIDs[i] = rows[i].ID
	}
	tagsByKey, err := h.tags.TagsByKeys(keyIDs)
	if err != nil {
		httpx.Internal(c, "查询失败")
		return
	}
	// viewer(只读)不得读取可直接复用的明文令牌：剥离 Key 明文（前端据此显示「不可回查」）。
	// ops/super 保留明文以支持复制/取证；掩码在此处而非 DTO，避免影响其它复用同结构的路径。
	showPlain := canSeeSecrets(c)
	out := make([]gin.H, 0, len(rows))
	for i := range rows {
		tags := tagsByKey[rows[i].ID]
		if tags == nil {
			tags = []model.KeyTag{}
		}
		if !showPlain {
			rows[i].Key = ""
		}
		out = append(out, gin.H{"key": rows[i], "tags": tags})
	}
	c.JSON(http.StatusOK, out)
}

// UpdateAPIKey 更新密钥治理字段（POST /admin/api-keys/:id）：
// daily_cost_limit / model_whitelist / model_mappings / rpm / status，均用指针字段以区分“未传”与“清零”。
func (h *CRUDHandler) UpdateAPIKey(c *gin.Context) {
	id, _ := strconv.ParseUint(c.Param("id"), 10, 64)
	var body struct {
		DailyCostLimit *int64  `json:"daily_cost_limit"`
		ModelWhitelist *string `json:"model_whitelist"`
		ModelMappings  *string `json:"model_mappings"`
		RPM            *int    `json:"rpm"`
		Status         *int8   `json:"status"`
	}
	if err := c.ShouldBindJSON(&body); err != nil {
		httpx.BadRequest(c, "请求参数不合法")
		return
	}
	fields := map[string]any{}
	if body.DailyCostLimit != nil {
		fields["daily_cost_limit"] = *body.DailyCostLimit
	}
	if body.ModelWhitelist != nil {
		fields["model_whitelist"] = model.NormalizeList(*body.ModelWhitelist)
	}
	if body.ModelMappings != nil {
		fields["model_mappings"] = *body.ModelMappings
	}
	if body.RPM != nil {
		if !model.ValidLimit(*body.RPM) {
			httpx.BadRequest(c, "RPM "+model.LimitErrMsg)
			return
		}
		fields["rpm"] = *body.RPM
	}
	if body.Status != nil {
		fields["status"] = *body.Status
		// 管理员停用即打「管理员停用」标记（门户无法再启用），管理员启用则清除。
		fields["disabled_by_admin"] = *body.Status == 0
	}
	if len(fields) == 0 {
		c.JSON(http.StatusOK, gin.H{"ok": true})
		return
	}
	if err := h.keys.UpdateGovernance(id, fields); err != nil {
		httpx.Internal(c, "更新失败")
		return
	}
	h.keys.InvalidateAuthCacheByID(id)
	c.JSON(http.StatusOK, gin.H{"ok": true})
}

// ---- KeyTag（令牌标签）----
func (h *CRUDHandler) ListKeyTags(c *gin.Context) {
	rows, err := h.tags.List()
	if err != nil {
		httpx.Internal(c, "查询失败")
		return
	}
	if rows == nil {
		rows = []model.KeyTag{}
	}
	c.JSON(http.StatusOK, rows)
}
func (h *CRUDHandler) CreateKeyTag(c *gin.Context) {
	var body struct {
		Name string `json:"name"`
	}
	if err := c.ShouldBindJSON(&body); err != nil || body.Name == "" {
		httpx.BadRequest(c, "标签名称不能为空")
		return
	}
	t, err := h.tags.Create(body.Name)
	if err != nil {
		httpx.Internal(c, "创建失败")
		return
	}
	c.JSON(http.StatusOK, t)
}
func (h *CRUDHandler) DeleteKeyTag(c *gin.Context) {
	id, _ := strconv.ParseUint(c.Param("id"), 10, 64)
	if err := h.tags.Delete(id); err != nil {
		httpx.Internal(c, "删除失败")
		return
	}
	c.JSON(http.StatusOK, gin.H{"ok": true})
}

// BindKeyTag 给密钥绑定标签（POST /admin/api-keys/:id/tags {tag_id}）。
func (h *CRUDHandler) BindKeyTag(c *gin.Context) {
	keyID, _ := strconv.ParseUint(c.Param("id"), 10, 64)
	var body struct {
		TagID uint64 `json:"tag_id"`
	}
	if err := c.ShouldBindJSON(&body); err != nil || body.TagID == 0 {
		httpx.BadRequest(c, "tag_id 不能为空")
		return
	}
	if err := h.tags.Bind(keyID, body.TagID); err != nil {
		httpx.Internal(c, "绑定失败")
		return
	}
	c.JSON(http.StatusOK, gin.H{"ok": true})
}

// UnbindKeyTag 解绑密钥标签（DELETE /admin/api-keys/:id/tags/:tag_id）。
func (h *CRUDHandler) UnbindKeyTag(c *gin.Context) {
	keyID, _ := strconv.ParseUint(c.Param("id"), 10, 64)
	tagID, _ := strconv.ParseUint(c.Param("tag_id"), 10, 64)
	if err := h.tags.Unbind(keyID, tagID); err != nil {
		httpx.Internal(c, "解绑失败")
		return
	}
	c.JSON(http.StatusOK, gin.H{"ok": true})
}
func (h *CRUDHandler) CreateAPIKey(c *gin.Context) {
	var body struct {
		Name  string `json:"name"`
		RPM   int    `json:"rpm"`
		AppID uint64 `json:"app_id"`
	}
	if err := c.ShouldBindJSON(&body); err != nil {
		httpx.BadRequest(c, "请求参数不合法")
		return
	}
	if body.AppID == 0 {
		httpx.BadRequest(c, "app_id 不能为空")
		return
	}
	if !model.ValidLimit(body.RPM) {
		httpx.BadRequest(c, "RPM "+model.LimitErrMsg)
		return
	}
	// 与 portal 建 key 同走 GenerateForApp：单次 Create 原子建含归属（app_id/rpm）的 key。
	plain, rec, err := h.keys.GenerateForApp(body.Name, body.AppID, body.RPM, nil)
	if err != nil {
		httpx.Internal(c, "创建失败")
		return
	}
	c.JSON(http.StatusOK, gin.H{"key": plain, "record": rec}) // 明文已落库可回查
}
func (h *CRUDHandler) DeleteAPIKey(c *gin.Context) {
	// 先失效鉴权缓存再删行：钥匙失效必须立即生效，不能等缓存 TTL。
	if id, _ := strconv.ParseUint(c.Param("id"), 10, 64); id != 0 {
		h.keys.InvalidateAuthCacheByID(id)
	}
	h.deleteByID(c, &model.APIKey{})
}
