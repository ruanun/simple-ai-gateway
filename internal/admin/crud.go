// 后台管理 CRUD 入口：CRUDHandler 定义与构造器、各资源文件共用的小工具函数。
// 各资源接口按文件拆分：crud_channel.go / crud_apikey.go / crud_log.go / crud_alias.go /
// crud_app.go / crud_price.go / crud_account.go / crud_proxy.go / crud_dashboard.go。
package admin

import (
	"fmt"
	"net/http"
	"strconv"

	"github.com/gin-gonic/gin"
	"github.com/ruanun/simple-ai-gateway/internal/audit"
	"github.com/ruanun/simple-ai-gateway/internal/domain/account"
	"github.com/ruanun/simple-ai-gateway/internal/domain/alias"
	"github.com/ruanun/simple-ai-gateway/internal/domain/apikey"
	"github.com/ruanun/simple-ai-gateway/internal/domain/app"
	"github.com/ruanun/simple-ai-gateway/internal/domain/channel"
	"github.com/ruanun/simple-ai-gateway/internal/domain/keytag"
	"github.com/ruanun/simple-ai-gateway/internal/domain/price"
	"github.com/ruanun/simple-ai-gateway/internal/domain/proxy"
	"github.com/ruanun/simple-ai-gateway/internal/domain/requestlog"
	"github.com/ruanun/simple-ai-gateway/internal/domain/usage"
	"github.com/ruanun/simple-ai-gateway/internal/httpx"
	"github.com/ruanun/simple-ai-gateway/internal/middleware"
	"github.com/ruanun/simple-ai-gateway/internal/model"
	"github.com/ruanun/simple-ai-gateway/internal/router"
	"gorm.io/gorm"
)

// CRUDHandler 提供后台管理资源的列表/创建/删除。
type CRUDHandler struct {
	g            *gorm.DB
	keys         *apikey.Service
	logs         *requestlog.Service
	aliases      *alias.Service
	table        *router.Table
	aesKey       string
	apps         *app.Service
	prices       *price.Service
	usages       *usage.Service
	accounts     *account.Service
	proxies      *proxy.Service
	tags         *keytag.Service
	channels     *channel.Service
	routerNotify func() error   // 路由表变更后通知其他实例(Pub/Sub)
	audit        *audit.Service // 审计正文读路径（存储层）
}

func NewCRUDHandler(g *gorm.DB, keys *apikey.Service, logs *requestlog.Service, aliases *alias.Service, table *router.Table, aesKey string,
	apps *app.Service, prices *price.Service, usages *usage.Service, routerNotify func() error, auditSvc *audit.Service) *CRUDHandler {
	return &CRUDHandler{g: g, keys: keys, logs: logs, aliases: aliases, table: table, aesKey: aesKey,
		apps: apps, prices: prices, usages: usages, accounts: account.NewService(account.NewRepo(g)),
		proxies: proxy.NewService(proxy.NewRepo(g)), tags: keytag.NewService(keytag.NewRepo(g)),
		channels:     channel.NewService(channel.NewRepo(g), aesKey),
		routerNotify: routerNotify, audit: auditSvc}
}

func (h *CRUDHandler) notifyRouterChange() error {
	buildErr := h.table.Build(h.g)
	if buildErr != nil {
		h.table.Invalidate()
	}
	// DB 已变更，无论本地 Build 成败都通知其他实例 reload
	if h.routerNotify != nil {
		if err := h.routerNotify(); err != nil {
			return fmt.Errorf("publish: %w", err)
		}
	}
	return buildErr
}

func (h *CRUDHandler) deleteByID(c *gin.Context, m any) {
	id, _ := strconv.ParseUint(c.Param("id"), 10, 64)
	if err := h.g.Delete(m, id).Error; err != nil {
		httpx.Internal(c, "删除失败")
		return
	}
	c.JSON(http.StatusOK, gin.H{"ok": true})
}

// canSeeSecrets 判定当前管理员角色能否读取明文凭证/审计正文/渠道秘密头。
// viewer(只读)与未知角色一律不可见；ops(可写运维)与 super 可见——白名单式放行，
// 与 AdminRBAC「未知角色按最小权限=viewer 处理」的取向一致。
// 中间件链外直接调用 handler 的单测无此 ctx 键（返回 false，看不到明文）。
func canSeeSecrets(c *gin.Context) bool {
	role := c.GetString(middleware.CtxAdminRole)
	return role == model.RoleSuper || role == model.RoleOps
}
