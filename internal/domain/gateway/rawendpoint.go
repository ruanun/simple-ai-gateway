package gateway

import (
	"io"

	"github.com/gin-gonic/gin"
	"github.com/ruanun/simple-ai-gateway/internal/billing"
)

// rawBilling 单次调用端点（embeddings/images）的计费策略：
// 仅在上游成功响应（<400）时被调用，从响应体计算 (usage, usageOK, cost)。
type rawBilling func(b *BillingCtx, channelID uint64, servedModel string, rateMultiplier float64, respBody []byte) (billing.Usage, bool, int64)

// handleRawEndpoint embeddings/images 共用骨架：读 body → Prepare（非转换模式）→
// DispatchRaw(path) → 回写（错误脱敏）→ 计费（bill 回调）→ finish 落日志 → auditSubmit。
// 两端点此前逐行复制，差异仅 path/reqType/计费策略三个参数（历史妥协收敛）。
func handleRawEndpoint(c *gin.Context, b *BillingCtx, path, reqType string, bill rawBilling) {
	raw, _ := io.ReadAll(c.Request.Body)
	p, ok := b.Prepare(c, raw, "openai", false)
	if !ok {
		return
	}
	appID := p.AppID
	// 单次调用端点，无需会话粘连（sessionID 传空）
	deps := b.requestDeps(c, p, "")
	out := dispatchRawInbound(p.Cands, path, p.in, deps, false)
	// 计费/日志/用量/审计按实际命中的上游模型(别名解析后的目标);空则回退请求模型名。
	servedModel := out.Model
	if servedModel == "" {
		servedModel = p.Model
	}
	if out.Resp == nil {
		// nil 响应仍落日志/审计，确保失败请求可追溯（公共收尾见 finishRawFailure）。
		b.finishRawFailure(c, out, p, servedModel, reqType, "", false, raw)
		return
	}
	defer out.Resp.Body.Close()
	respBody, _ := io.ReadAll(out.Resp.Body)
	// 错误状态回写前脱敏凭证片段（审计仍存原始 respBody）。
	c.Data(out.Resp.StatusCode, "application/json", scrubIfError(out.Resp, respBody))
	// 失败响应仍落日志/审计，cost=0；仅成功时按策略计费。
	var u billing.Usage
	var usageOK bool
	var cost int64
	if out.Resp.StatusCode < 400 {
		u, usageOK, cost = bill(b, out.ChannelID, servedModel, out.RateMultiplier, respBody)
		b.applyKeyDailyCost(c, p.Kid, cost) // key 日成本累加
		// 成功却无 usage → 零成本结算，暴露给运维（embeddings/images 非流式）。
		b.reportMissingUsage(reqIDOf(c), out.ChannelID, servedModel, out.Resp.StatusCode, false, usageOK)
	}
	// 传入真实状态码，避免日志始终记录 200
	b.finish(c, appID, out.ChannelID, out.AccountID, servedModel, reqType, "", u, usageOK, cost, out.LatencyMs, 0, false, out.Resp.StatusCode)
	// 审计：raw 即原始入站 body（本骨架端点无入站协议转换），respBody 为上游响应体。
	b.auditSubmit(c, p.AuditEnabled, appID, out.ChannelID, out.AccountID, servedModel, reqType, "", out.Resp.StatusCode, u, cost, out.LatencyMs, 0, false, raw, respBody, nil, 0)
}
