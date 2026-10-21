package admin

import (
	"log/slog"
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/ruanun/simple-ai-gateway/internal/httpx"
	"github.com/ruanun/simple-ai-gateway/internal/ipgeo"
)

// IPHandler 处理 IP 归属查询（管理面）。
type IPHandler struct {
	geo *ipgeo.Client
}

// NewIPHandler 构造 IP 归属查询 handler。
func NewIPHandler(geo *ipgeo.Client) *IPHandler {
	return &IPHandler{geo: geo}
}

// Lookup GET /admin/ip-info/:ip：查询单个 IP 的归属信息；私有/内网 IP 返回 private 标记，
// 公网 IP 经外部服务查询（带缓存）。
func (h *IPHandler) Lookup(c *gin.Context) {
	ip := c.Param("ip")
	res, err := h.geo.Lookup(c.Request.Context(), ip)
	if err != nil {
		slog.Warn("IP 归属查询失败", "ip", ip, "err", err)
		httpx.Err(c, http.StatusBadGateway, "bad_gateway", "IP 归属查询失败")
		return
	}
	c.JSON(http.StatusOK, res)
}
