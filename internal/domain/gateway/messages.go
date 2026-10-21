package gateway

import (
	"io"

	"github.com/gin-gonic/gin"
	"github.com/ruanun/simple-ai-gateway/internal/session"
)

// MessagesHandler 处理 POST /v1/messages（Anthropic Messages 入站）：
// 同协议透传优先——anthropic 型渠道原样代理；
// 无透传候选覆盖时,允许转换的 openai 渠道经 chat↔messages 转换兜底(P12c)。
type MessagesHandler struct{ b BillingCtx }

func NewMessagesHandler(b BillingCtx) *MessagesHandler { return &MessagesHandler{b: b} }

func (h *MessagesHandler) Handle(c *gin.Context) {
	raw, _ := io.ReadAll(c.Request.Body)
	p, ok := h.b.Prepare(c, raw, "anthropic", true)
	if !ok {
		return
	}
	// 判断流式（计费/日志口径按请求体 stream 字段）
	stream := p.in.stream

	// 透传派发（带会话粘连）：自动从请求体 metadata.user_id 提取 session，无需客户端传 header
	sessionID := session.ExtractAnthropicSessionID(raw)
	deps := h.b.requestDeps(c, p, sessionID)
	cands := h.b.stickyBoost(sessionID, p.Cands)

	out := dispatchInbound(c.Writer, cands, p.in, deps)
	h.b.settleDispatch(c, out, p, raw, sessionID, "anthropic", "messages", "messages", stream)
}
