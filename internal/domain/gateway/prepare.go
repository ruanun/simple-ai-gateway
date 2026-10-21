package gateway

import (
	"net/http"
	"strconv"

	"github.com/gin-gonic/gin"
	"github.com/ruanun/simple-ai-gateway/internal/audit"
	"github.com/ruanun/simple-ai-gateway/internal/billing"
	"github.com/ruanun/simple-ai-gateway/internal/convert"
	"github.com/ruanun/simple-ai-gateway/internal/domain/app"
	"github.com/ruanun/simple-ai-gateway/internal/middleware"
	"github.com/ruanun/simple-ai-gateway/internal/model"
	"github.com/ruanun/simple-ai-gateway/internal/router"
)

// prepared 编排前半段（Prepare）的产物：治理/配额/路由/过滤/价格前置校验全部通过后的派发输入。
type prepared struct {
	Kid           uint64              // API key id（取自鉴权已载入的 key 行，复用给计费/日志/审计）
	AppID         uint64              // key 所属 App id（0 表示未绑定）
	AuditEnabled  bool                // App 审计开关（Prepare 时从 App 行带出，审计投递不再回表；无归属恒 false）
	KeyDailyLimit int64               // key 日成本上限（0=不限）；预留时判断是否需要占 key 日额度
	Model         string              // govern 映射后的模型，贯穿后续路由/计费/日志/审计
	Cands         []router.Candidate  // 协议过滤 + ACL 过滤后的派发候选
	in            inbound             // 入站请求体单次解析结果，派发/改写复用
	Reservation   *BillingReservation // 在途额度预留（计费端点）；由 settleCommon 结算/失败时释放
}

type prepareOptions struct {
	convertMode  bool // chat/messages 允许跨协议转换兜底
	requirePrice bool // 计费端点要求每个候选都配价；count_tokens 等免费端点跳过
	reserve      bool // 是否做在途额度预留（计费端点 true；免费端点 false）
}

// Prepare 封装五端点（chat/messages/responses/embeddings/images）共用的编排前半段：
// model 必填 → 令牌治理（模型映射/白名单403/日成本402）→ App 配额 402 → 路由解析 →
// 协议过滤（convertMode=true 时 filterWithConvert 含转换兜底候选，false 时 filterByProtocol 只透传）→
// App 渠道 ACL → 转换预校验 → 价格前置校验（未配价 400，不消耗上游）。
// 任一环节失败在函数内回写错误 JSON 并返回 ok=false，调用方直接 return。
func (b *BillingCtx) Prepare(c *gin.Context, raw []byte, proto string, convertMode bool) (prepared, bool) {
	return b.prepare(c, raw, proto, prepareOptions{convertMode: convertMode, requirePrice: true, reserve: true})
}

// PrepareNoPrice 与 Prepare 同源，但跳过「候选必须配价」校验，供 count_tokens 等不计费端点使用。
// 其余治理/配额/ACL/并发语义保持一致，避免免费端点成为绕过模型白名单或渠道 ACL 的后门。
func (b *BillingCtx) PrepareNoPrice(c *gin.Context, raw []byte, proto string) (prepared, bool) {
	return b.prepare(c, raw, proto, prepareOptions{requirePrice: false})
}

func (b *BillingCtx) prepare(c *gin.Context, raw []byte, proto string, opts prepareOptions) (prepared, bool) {
	in := parseInbound(raw)
	reqModel := in.model
	if reqModel == "" {
		respondError(c, http.StatusBadRequest, proto, "model required")
		return prepared{}, false
	}
	if !in.streamOK {
		respondError(c, http.StatusBadRequest, proto, "stream must be a boolean")
		return prepared{}, false
	}
	// 令牌治理（P4）：模型映射→白名单403→日成本402。
	// key 行复用 APIKeyAuth 已载入的 ctx 记录（CtxAPIKeyRec），消除每请求回表重查同一行；
	// gv.Model 为映射后模型，贯穿后续路由/计费/日志/审计；gv.AppID 复用避免重复查 key。
	var keyRow model.APIKey
	if rec := middleware.APIKeyFromCtx(c); rec != nil {
		keyRow = *rec
	}
	kid := keyRow.ID
	gv := govern(b.Keys, keyRow, reqModel, b.now())
	if gv.Status != 0 {
		respondError(c, gv.Status, proto, gv.Msg)
		return prepared{}, false
	}
	appID := gv.AppID
	// App 行只查一次：配额前置校验（402）与渠道 ACL 复用同一份，消除 HasQuota+Get 对同一行的双查。
	// App 无法确认（DB 错误或记录缺失）时 fail-closed：不能跳过配额/渠道 ACL 直接放行，
	// 否则库抖动/脏 appID 会让请求绕过应用级白名单（工程 Review E1）。
	var appRow *model.App
	if appID != 0 {
		a, err := b.Apps.Get(appID)
		if err != nil || a == nil {
			respondError(c, http.StatusInternalServerError, proto, "app lookup failed")
			return prepared{}, false
		}
		if !app.QuotaOK(a) {
			respondError(c, http.StatusPaymentRequired, proto, "app quota exhausted")
			return prepared{}, false
		}
		// App 模型白名单：管理员对租户的硬约束（门户不可改），与令牌白名单同口径、按映射后模型名匹配。
		// 租户自建的令牌没有白名单也绕不过这一层。
		if !model.WhitelistAllows(a.ModelWhitelist, gv.Model) {
			respondError(c, http.StatusForbidden, proto, "model not allowed for this app")
			return prepared{}, false
		}
		appRow = a
	}
	// 路由解析 + 协议过滤：convertMode（chat/messages）同协议透传候选优先、
	// 允许转换的异协议渠道兜底（P12c）；非 convertMode（responses/embeddings/images）只保留同协议透传候选。
	cands := b.Table.Resolve(gv.Model)
	if opts.convertMode {
		cands = filterWithConvert(cands, proto)
	} else {
		cands = filterByProtocol(cands, proto)
	}
	beforeACL := len(cands)
	if appRow != nil {
		cands = filterByAllowedChannels(cands, appRow.AllowedChannels)
	}
	if len(cands) == 0 {
		if beforeACL > 0 {
			respondError(c, http.StatusForbidden, proto, "app not allowed to use channels for this model")
		} else {
			respondError(c, http.StatusNotFound, proto, "no channel for model")
		}
		return prepared{}, false
	}
	// 含转换候选时预校验入站体可按本协议解析（请求转换失败→400，不浪费上游尝试；spec §7）。
	if opts.convertMode && hasConvertCandidate(cands) {
		if err := convert.ValidateRequest(raw, proto); err != nil {
			respondError(c, http.StatusBadRequest, proto, "请求体无法转换: "+err.Error())
			return prepared{}, false
		}
	}
	// 价格前置校验：与计费同口径（渠道覆盖价→全局价）逐个候选检查，剔除未配价的候选；
	// 全部未配价才 400（不派发、不消耗上游）。曾只查首个候选：别名下的后备渠道未配价时，
	// 主渠道一故障，故障期间的请求就全按 0 计费。
	// count_tokens 等免费端点跳过（opts.requirePrice=false），但不能因此绕过白名单/ACL。
	if opts.requirePrice {
		cands = pricedCandidates(cands, b.Prices.Resolve)
		if len(cands) == 0 {
			respondError(c, http.StatusBadRequest, proto, "no price configured for model")
			return prepared{}, false
		}
	}
	// App 并发占位放在所有拒绝分支之后：只有真正要派发的请求才占槽位。
	// 配额是先查后扣的软限额，并发越高临界点超额越大；封顶在途数即把超额限制在「上限 × 单次成本」内。
	// 槽位经 middleware.AddCleanup 在请求结束（含流式写完、panic）时归还。
	if appRow != nil && b.Concurrency != nil {
		limit := model.EffectiveLimit(appRow.MaxConcurrency, b.DefaultAppConcurrency)
		release, ok := b.Concurrency.Acquire("app:"+strconv.FormatUint(appRow.ID, 10), limit)
		if !ok {
			respondError(c, http.StatusTooManyRequests, proto, "too many concurrent requests for this app")
			return prepared{}, false
		}
		middleware.AddCleanup(c, release)
	}
	p := prepared{Kid: kid, AppID: appID, KeyDailyLimit: keyRow.DailyCostLimit, Model: gv.Model, Cands: cands, in: in}
	// 审计开关随 App 行带出（appRow==nil 即无归属，恒不审计），审计投递不再回表查同一行。
	if appRow != nil {
		p.AuditEnabled = appRow.AuditEnabled
	}
	if opts.reserve {
		res, status, msg := b.reserve(p, raw)
		if status != 0 {
			respondError(c, status, proto, msg)
			return prepared{}, false
		}
		p.Reservation = res
		if res != nil {
			c.Set(CtxBillingReservation, res)
			// 任何提前返回（含 panic/流式中断）都由请求收尾清理；Settle 幂等，成功后为 no-op。
			middleware.AddCleanup(c, res.Release)
		}
	}
	return p, true
}

// stickyBoost 渠道级粘连：会话上次成功的渠道存在时把其候选提到最前（Dispatch 前调用）。
func (b *BillingCtx) stickyBoost(sessionID string, cands []router.Candidate) []router.Candidate {
	if b.ChSticky != nil && sessionID != "" {
		if stickyChID, ok := b.ChSticky.Get(sessionID); ok {
			return boostStickyChannel(cands, stickyChID)
		}
	}
	return cands
}

// settleDispatch 封装 chat/messages 派发（Dispatch）后的公共收尾：
// 渠道粘连记录/清除 → 计费扣减（App 配额 + key 日成本）→ 审计投递 → 请求日志 → 失败兜底回写错误 JSON。
// stream 口径由调用方决定（chat 传 out.Stream，messages 传请求体 stream 字段，保持现状）；
// logType 为请求日志的 request_type（chat 历史为空串、审计才是 "chat"，保持现状不改）。
func (b *BillingCtx) settleDispatch(c *gin.Context, out Outcome, p prepared, raw []byte, sessionID, proto, reqType, logType string, stream bool) {
	// 全候选失败按类别分流为 502/503（见 dispatchFailStatus）。out 是值副本，就地改写即可让
	// 后续粘连判定/计费判定/审计/请求日志/告警/兜底回写全部用同一个状态码——落库与调用方所见必须一致。
	if len(out.Skips) > 0 {
		out.Status = dispatchFailStatus(out.Skips)
	}
	// 渠道粘连：成功时记录，失败时清除（避免粘到故障渠道）
	if b.ChSticky != nil && sessionID != "" {
		if out.ChannelID != 0 && out.Status < 400 {
			b.ChSticky.Set(sessionID, out.ChannelID)
		} else {
			b.ChSticky.Delete(sessionID)
		}
	}

	// 计费/日志/用量/审计按实际命中的上游模型(别名解析后的目标);空则回退请求模型名。
	servedModel := out.Model
	if servedModel == "" {
		servedModel = p.Model
	}

	reqID := c.GetString(middleware.CtxRequestID)
	// 计费：仅成功派发（命中渠道且非错误状态）才计费；App 扣减在 settleCommon 统一做。
	var cost int64
	if out.ChannelID != 0 && out.Status < 400 {
		if pr, ok := b.Prices.Resolve(out.ChannelID, servedModel); ok && out.UsageOK {
			cost = billing.ApplyMultiplier(billing.Compute(out.Usage, pr), out.RateMultiplier)
		}
		// key 日成本累加（与 App.Deduct 并行，独立校验/扣减；失败日志+告警统一在 applyKeyDailyCost）。
		b.applyKeyDailyCost(c, p.Kid, cost)
		// 成功却无 usage → 零成本结算，暴露给运维排查（仅非流式，见 reportMissingUsage）。
		b.reportMissingUsage(reqID, out.ChannelID, servedModel, out.Status, stream, out.UsageOK)
	}
	// 取证级审计（若该 App 启用）：成功和失败都记录，投递原始请求体 + 上游响应体（异步落库，非阻塞）。
	auditSubmit(b.Audit, p.AuditEnabled, audit.Entry{
		RequestID: reqID,
		APIKeyID:  p.Kid, AppID: p.AppID, ChannelID: out.ChannelID, AccountID: out.AccountID,
		Model: servedModel, RequestType: reqType, Status: out.Status, LatencyMs: out.LatencyMs, FirstTokenMs: out.FirstTokenMs, SessionID: sessionID, Stream: stream,
		PromptTokens: out.Usage.PromptTokens, CompletionTokens: out.Usage.CompletionTokens,
		CachedTokens: out.Usage.CachedTokens, CacheCreationTokens: out.Usage.CacheCreationTokens, ReasoningTokens: out.Usage.ReasoningTokens, CostMicros: cost,
		ClientIP: c.ClientIP(), UserAgent: c.Request.UserAgent(),
		RequestBody: raw, ResponseBody: out.ResponseBody, ResponseOverflow: out.ResponseOverflow,
	})

	// 公共尾段：App 扣减 + 请求日志 + 全候选失败告警（chat 的 request_type 历史口径为 logType，保持）。
	b.settleCommon(c, settleArgs{
		Kid: p.Kid, AppID: p.AppID, ChannelID: out.ChannelID, AccountID: out.AccountID,
		Model: servedModel, ReqType: logType, SessionID: sessionID,
		Status: out.Status, LatencyMs: out.LatencyMs, FirstTokenMs: out.FirstTokenMs, Stream: stream,
		Usage: out.Usage, UsageOK: out.UsageOK, Cost: cost,
		ErrMsg: out.Err,
		Proto:  proto, UpstreamProto: protoOrDefault(out.Protocol, proto),
	})

	// Dispatch 成功时已 Relay（响应已回写），不再重复写响应；
	// 未发生 Relay 的失败分支（全候选失败 status=502/503，或渠道限流 status=429
	// 且 Err 非空但未回写）由这里补回错误 JSON。
	if !c.Writer.Written() && out.Err != "" {
		b.respondDispatchFailure(c, proto, out.Skips, out.Status, out.Err)
	}
}

// pricedCandidates 保序筛出已配价的候选（resolve 与计费的 Prices.Resolve 同签名）。
func pricedCandidates[P any](cands []router.Candidate, resolve func(channelID uint64, model string) (P, bool)) []router.Candidate {
	out := cands[:0:0]
	for _, c := range cands {
		if _, ok := resolve(c.ChannelID, c.Model); ok {
			out = append(out, c)
		}
	}
	return out
}
