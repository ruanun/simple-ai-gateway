// IP 代理池（Proxy）管理接口（列表/新增/删除/启停）。
package admin

import (
	"net/http"
	"strconv"

	"github.com/gin-gonic/gin"
	"github.com/ruanun/simple-ai-gateway/internal/httpx"
	"github.com/ruanun/simple-ai-gateway/internal/model"
)

// ---- Proxy（IP 代理池）----
// ListProxies 列出代理；可选 ?group= 过滤某代理组（仅启用）。无 group 时返回全部（含停用）。
func (h *CRUDHandler) ListProxies(c *gin.Context) {
	if group := c.Query("group"); group != "" {
		rows, err := h.proxies.ListEnabledByGroup(group)
		if err != nil {
			httpx.Internal(c, "查询失败")
			return
		}
		c.JSON(http.StatusOK, rows)
		return
	}
	rows, err := h.proxies.List()
	if err != nil {
		httpx.Internal(c, "查询失败")
		return
	}
	c.JSON(http.StatusOK, rows)
}

// CreateProxy 新增代理（明文 username/password 本期直存，加密留后续）。host 必填。
func (h *CRUDHandler) CreateProxy(c *gin.Context) {
	var body struct {
		Group    string `json:"group"`
		Name     string `json:"name"`
		Protocol string `json:"protocol"`
		Host     string `json:"host"`
		Port     int    `json:"port"`
		Username string `json:"username"`
		Password string `json:"password"`
		Weight   int    `json:"weight"`
		Priority int    `json:"priority"`
	}
	if err := c.ShouldBindJSON(&body); err != nil || body.Host == "" {
		httpx.BadRequest(c, "请求参数不合法")
		return
	}
	p, err := h.proxies.Create(model.Proxy{
		Group: body.Group, Name: body.Name, Protocol: body.Protocol, Host: body.Host, Port: body.Port,
		Username: body.Username, Password: body.Password, Weight: body.Weight, Priority: body.Priority, Status: 1,
	})
	if err != nil {
		httpx.Internal(c, "创建失败")
		return
	}
	c.JSON(http.StatusOK, p)
}

// DeleteProxy 删除代理。
func (h *CRUDHandler) DeleteProxy(c *gin.Context) {
	id, _ := strconv.ParseUint(c.Param("id"), 10, 64)
	if err := h.proxies.Delete(id); err != nil {
		httpx.Internal(c, "删除失败")
		return
	}
	c.JSON(http.StatusOK, gin.H{"ok": true})
}

// SetProxyStatus 启用(1)/停用(0)代理。
func (h *CRUDHandler) SetProxyStatus(c *gin.Context) {
	id, _ := strconv.ParseUint(c.Param("id"), 10, 64)
	var body struct {
		Status int8 `json:"status"`
	}
	if err := c.ShouldBindJSON(&body); err != nil {
		httpx.BadRequest(c, "请求参数不合法")
		return
	}
	if err := h.proxies.SetStatus(id, body.Status); err != nil {
		httpx.Internal(c, "更新失败")
		return
	}
	c.JSON(http.StatusOK, gin.H{"ok": true})
}
