// 告警测试端点：同步发一条测试告警到配置的 webhook，供 admin 验证联通。
package admin

import (
	"log/slog"
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/ruanun/simple-ai-gateway/internal/notify"
)

// AlertHandler 告警测试。wn 为 nil 表示告警未启用。
type AlertHandler struct{ wn *notify.WebhookNotifier }

// NewAlertHandler wn 可为 nil（alert 未启用）。
func NewAlertHandler(wn *notify.WebhookNotifier) *AlertHandler { return &AlertHandler{wn: wn} }

// Test 同步发一条测试告警，返回联通结果。
func (h *AlertHandler) Test(c *gin.Context) {
	if h.wn == nil {
		c.JSON(http.StatusOK, gin.H{"ok": false, "error": "告警未启用（alert.enabled=false 或 webhook_url 为空）"})
		return
	}
	status, err := h.wn.TestSend()
	if err != nil {
		slog.Warn("测试告警发送失败", "err", err)
		c.JSON(http.StatusOK, gin.H{"ok": false, "status": status, "error": "发送测试告警失败，请检查 webhook 地址与网络连通性"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"ok": true, "status": status})
}
