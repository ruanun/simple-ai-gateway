package gateway

import (
	"io"

	"github.com/gin-gonic/gin"
	"github.com/ruanun/simple-ai-gateway/internal/session"
)

// ChatHandler 处理 /v1/chat/completions：Prepare 统一编排（治理/配额/路由/协议过滤/ACL/价格前置）
// 后按候选定序派发（重试/熔断/限流），成功后按上游 usage 计费扣减、落请求日志并投递审计。
type ChatHandler struct{ b BillingCtx }

func NewChatHandler(b BillingCtx) *ChatHandler { return &ChatHandler{b: b} }

func (h *ChatHandler) Completions(c *gin.Context) {
	raw, _ := io.ReadAll(c.Request.Body)
	p, ok := h.b.Prepare(c, raw, "openai", true)
	if !ok {
		return
	}

	// 自动从 clientIP+UA+apiKeyID+首条消息内容 生成 session hash，启用粘连路由。
	sessionID := session.ExtractChatSessionID(c.ClientIP(), c.Request.UserAgent(), p.Kid, raw)
	deps := h.b.requestDeps(c, p, sessionID)
	cands := h.b.stickyBoost(sessionID, p.Cands)

	res := dispatchInbound(c.Writer, cands, p.in, deps)
	// 请求日志 request_type 历史为空串（审计才是 "chat"），保持现状不改。
	h.b.settleDispatch(c, res, p, raw, sessionID, "openai", "chat", "", res.Stream)
}
