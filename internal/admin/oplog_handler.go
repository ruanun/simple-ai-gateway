package admin

import (
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/ruanun/simple-ai-gateway/internal/model"
	"github.com/ruanun/simple-ai-gateway/internal/paginate"
	"gorm.io/gorm"
)

// OpLogHandler 管理操作日志查询（P9-A 最小版，只读）。
type OpLogHandler struct {
	g *gorm.DB
}

func NewOpLogHandler(g *gorm.DB) *OpLogHandler {
	return &OpLogHandler{g: g}
}

// List 分页查询操作日志，支持按操作人/方法/时间范围过滤，形状与 ListRequestLogs 对齐。
func (h *OpLogHandler) List(c *gin.Context) {
	limit, offset := paginate.Params(c.DefaultQuery("limit", "20"), c.DefaultQuery("offset", "0"), 20, 200)
	q := h.g.Model(&model.AdminOpLog{}).Order("id desc")
	if v := c.Query("actor"); v != "" {
		q = q.Where("actor = ?", v)
	}
	if v := c.Query("method"); v != "" {
		q = q.Where("method = ?", v)
	}
	if v := c.Query("start_time"); v != "" {
		q = q.Where("created_at >= ?", v)
	}
	if v := c.Query("end_time"); v != "" {
		q = q.Where("created_at <= ?", v)
	}
	var total int64
	q.Count(&total)
	var rows []model.AdminOpLog
	q.Limit(limit).Offset(offset).Find(&rows)
	c.JSON(http.StatusOK, paginate.New(rows, total, limit, offset))
}
