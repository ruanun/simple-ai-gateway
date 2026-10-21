package gateway

import (
	"io"

	"github.com/gin-gonic/gin"
	"github.com/ruanun/simple-ai-gateway/internal/billing"
)

// CountTokensHandler 处理两个免费的 token 计数端点：
//   - POST /v1/messages/count_tokens   → Anthropic 上游 /v1/messages/count_tokens
//   - POST /v1/responses/input_tokens  → OpenAI 上游 /responses/input_tokens
//
// 语义：不产生 usage、不计费、不占 App 配额（但仍走模型映射/白名单/渠道 ACL/并发/熔断/请求日志/审计）。
// 上游不支持该端点时原样回写 4xx，由客户端决定是否本地估算。
type CountTokensHandler struct{ b BillingCtx }

func NewCountTokensHandler(b BillingCtx) *CountTokensHandler { return &CountTokensHandler{b: b} }

func (h *CountTokensHandler) Messages(c *gin.Context) {
	handleCountTokens(c, &h.b, "anthropic", "/v1/messages/count_tokens", "messages_count_tokens")
}

func (h *CountTokensHandler) Responses(c *gin.Context) {
	handleCountTokens(c, &h.b, "openai", "/responses/input_tokens", "responses_input_tokens")
}

func handleCountTokens(c *gin.Context, b *BillingCtx, proto, path, reqType string) {
	raw, _ := io.ReadAll(c.Request.Body)
	p, ok := b.PrepareNoPrice(c, raw, proto)
	if !ok {
		return
	}
	deps := b.requestDeps(c, p, "")
	out := dispatchRawInbound(p.Cands, path, p.in, deps, false)
	servedModel := out.Model
	if servedModel == "" {
		servedModel = p.Model
	}
	if out.Resp == nil {
		b.finishRawFailureProto(c, proto, out, p, servedModel, reqType, "", false, raw)
		return
	}
	defer out.Resp.Body.Close()

	respBody, _ := io.ReadAll(out.Resp.Body)
	ct := out.Resp.Header.Get("Content-Type")
	if ct == "" {
		ct = "application/json"
	}
	c.Data(out.Resp.StatusCode, ct, scrubIfError(out.Resp, respBody))

	// 免费端点：usageOK=false、cost=0，但不触发 missing-usage 告警（本端点本来就没有 usage）。
	b.finishProto(c, proto, p.AppID, out.ChannelID, out.AccountID, servedModel, reqType, "",
		billing.Usage{}, false, 0, out.LatencyMs, 0, false, out.Resp.StatusCode)
	b.auditSubmit(c, p.AuditEnabled, p.AppID, out.ChannelID, out.AccountID, servedModel, reqType, "",
		out.Resp.StatusCode, billing.Usage{}, 0, out.LatencyMs, 0, false, raw, respBody, nil, 0)
}
