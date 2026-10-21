package gateway

import (
	"math"
	"net/http"
	"strconv"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/ruanun/simple-ai-gateway/internal/breaker"
	"github.com/ruanun/simple-ai-gateway/internal/v1proto"
)

// retryAfterMaxSeconds Retry-After 建议值封顶：再长的冷却也不给客户端一个离谱的等待建议。
const retryAfterMaxSeconds = 300

// respondError 按入站协议回写网关级错误信封（openai：{"error":{...}}；
// anthropic：{"type":"error","error":{...}}）。
// 实现与 status→type 映射见 v1proto.Respond，此处做薄包装。
func respondError(c *gin.Context, status int, proto, msg string) {
	v1proto.Respond(c, status, proto, msg)
}

// clientSafeDetail 把派发失败的内部详情收敛为对外泛化文案：
// 5xx 的 detail 含渠道 ID/上游 base_url/代理地址/真实模型名等内部拓扑（skipReasons），
// 不应回给终端调用方——只回泛化提示，完整 detail 仍进服务端日志与请求日志/审计（管理面可见）。
// 非 5xx（如 429 "channel rate limited"）本就是网关自造的安全短文案，原样返回。
//
// 有结构化 skips 时优先走 publicSummary（信息量更大且同样脱敏），此函数是无 skips 的兜底。
func clientSafeDetail(status int, detail string) string {
	if status >= 500 {
		return "all upstream candidates failed"
	}
	return detail
}

// retryAfterSeconds 从渠道熔断快照算出建议重试等待秒数：取所有 kindBreakerOpen 候选中
// OpenUntil 最近的那个，向上取整为秒，封顶 retryAfterMaxSeconds。
//
// 刻意只覆盖渠道级熔断：账号级配额冷却是「账号×模型」粒度、代理级熔断走独立 breaker，
// 从候选层拿不到确定的剩余时长——强行汇总会给出偏乐观或偏悲观的建议值。宁可不给建议，
// 也不给错的建议，故 kindNoAccount 等场景返回 0（调用方据此不发该头）。
func retryAfterSeconds(brk breaker.Breaker, skips []skipReason, now time.Time) int {
	if brk == nil || len(skips) == 0 {
		return 0
	}
	ids := make([]uint64, 0, len(skips))
	for _, s := range skips {
		if s.Kind == kindBreakerOpen && s.ChannelID != 0 {
			ids = append(ids, s.ChannelID)
		}
	}
	if len(ids) == 0 {
		return 0
	}
	best := int64(0)
	for _, st := range brk.Snapshot(ids, now) {
		if st.OpenUntil <= 0 {
			continue
		}
		if best == 0 || st.OpenUntil < best {
			best = st.OpenUntil
		}
	}
	if best == 0 {
		return 0
	}
	secs := int(math.Ceil(time.Unix(best, 0).Sub(now).Seconds()))
	if secs <= 0 {
		return 0
	}
	return min(secs, retryAfterMaxSeconds)
}

// respondDispatchFailure 回写派发失败错误：按失败类别分流状态码、附 Retry-After、
// 用脱敏摘要 + request_id 作正文。
//
// skips 为空时（如渠道限流 429，或防御性兜底路径）沿用旧行为：fallbackStatus +
// clientSafeDetail(fallbackDetail)，不改状态码、不发 Retry-After。
// lastPassthroughSkip 返回最后一个开启错误透传的失败原因；没有则 nil。
func lastPassthroughSkip(skips []skipReason) *skipReason {
	for i := len(skips) - 1; i >= 0; i-- {
		if skips[i].ErrorPassthrough {
			return &skips[i]
		}
	}
	return nil
}

func (b *BillingCtx) respondDispatchFailure(c *gin.Context, proto string, skips []skipReason, fallbackStatus int, fallbackDetail string) {
	if len(skips) == 0 {
		respondError(c, fallbackStatus, proto, clientSafeDetail(fallbackStatus, fallbackDetail))
		return
	}
	status := dispatchFailStatus(skips)
	code := "all_candidates_failed"
	if status == http.StatusServiceUnavailable {
		code = "all_candidates_unavailable"
	}
	// Retry-After 必须在 c.JSON 之前写：JSON 会 WriteHeader，之后加头无效。
	if secs := retryAfterSeconds(b.Deps.Breaker, skips, b.now()); secs > 0 {
		c.Header("Retry-After", strconv.Itoa(secs))
	}
	// 渠道显式开启错误透传时，用最后一个上游错误的状态码与已脱敏详情回客户端；
	// 未开启保持公共摘要（不含渠道/模型/上游原文等内部拓扑）。
	if ps := lastPassthroughSkip(skips); ps != nil {
		status = ps.Status
		if status < 400 || status > 599 {
			status = dispatchFailStatus(skips)
		}
		msg := ps.Detail
		if msg == "" {
			msg = clientSafeDetail(status, fallbackDetail)
		}
		v1proto.RespondDetail(c, status, proto, msg, "upstream_error", reqIDOf(c))
		return
	}
	v1proto.RespondDetail(c, status, proto, publicSummary(skips), code, reqIDOf(c))
}
