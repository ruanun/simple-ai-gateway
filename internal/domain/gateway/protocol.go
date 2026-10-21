package gateway

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/ruanun/simple-ai-gateway/internal/audit"
	"github.com/ruanun/simple-ai-gateway/internal/billing"
	"github.com/ruanun/simple-ai-gateway/internal/domain/apikey"
	"github.com/ruanun/simple-ai-gateway/internal/domain/app"
	"github.com/ruanun/simple-ai-gateway/internal/domain/price"
	"github.com/ruanun/simple-ai-gateway/internal/domain/requestlog"
	"github.com/ruanun/simple-ai-gateway/internal/middleware"
	"github.com/ruanun/simple-ai-gateway/internal/model"
	"github.com/ruanun/simple-ai-gateway/internal/notify"
	"github.com/ruanun/simple-ai-gateway/internal/ratelimit"
	"github.com/ruanun/simple-ai-gateway/internal/router"
	"github.com/ruanun/simple-ai-gateway/internal/session"
)

// BillingCtx 协议端点（embeddings/images/messages/responses）共用的计费&落库依赖。
// 故意持有具体 service 类型（而非接口），避免未导出类型泄漏与签名不匹配。
type BillingCtx struct {
	Table    *router.Table
	Deps     Deps
	Apps     *app.Service
	Prices   *price.Service
	Logs     *requestlog.Service
	Selector *AccountSelector
	Now      func() time.Time
	Audit    *audit.AsyncWriter
	Keys     *apikey.Service
	ChSticky session.ChannelStickyStore
	Notifier notify.Notifier // 告警投递（nil 时告警为 no-op）
	// App 并发上限：Concurrency 为 nil 时不限；App.MaxConcurrency=0 时套用 DefaultAppConcurrency。
	Concurrency           ratelimit.Concurrency
	DefaultAppConcurrency int
}

// alertAllFailed 全候选失败(502/503)/墙钟超时(504)时告警；去重 key 按模型。nil Notifier / 非失败状态 → no-op。
// ⚠️ 503 是「全候选都没真打到上游（熔断/冷却/无代理）」的分流状态码（见 dispatchFailStatus）——
// 漏掉它会让这类最需要运维知晓的失败静默无声。
func (b *BillingCtx) alertAllFailed(status int, modelName, reqID, errMsg string) {
	if b.Notifier == nil {
		return
	}
	switch status {
	case http.StatusBadGateway, http.StatusServiceUnavailable, http.StatusGatewayTimeout:
	default:
		return
	}
	b.Notifier.Alert("all_candidates_failed:"+modelName, "全候选失败", "model="+modelName+" req="+reqID+" "+errMsg)
}

// alertDeductFail 计费扣减失败告警；dedupKey 如 "app:7"/"key:3"。nil Notifier → no-op。
func (b *BillingCtx) alertDeductFail(dedupKey, message string) {
	if b.Notifier == nil {
		return
	}
	b.Notifier.Alert("deduct_failed:"+dedupKey, "扣费失败", message)
}

// reportMissingUsage 上游成功(<400)却拿不到 usage 时记 error + 告警：这类请求会按零成本
// 结算，长期未察觉即被白嫖，故主动暴露给运维排查（上游是否漏发 usage / 解析口径是否漂移）。
// 仅对非流式生效：流式无 usage 是常见合法场景（渠道 DisableStreamUsage、上游不支持
// include_usage），一并告警会刷屏，故豁免。去重 key 按模型，避免同模型刷屏。
func (b *BillingCtx) reportMissingUsage(reqID string, channelID uint64, modelName string, status int, stream, usageOK bool) {
	if usageOK || stream || status >= 400 {
		return
	}
	slog.Error("上游成功响应缺少 usage，按零成本结算", "channel", channelID, "model", modelName, "status", status, "request_id", reqID)
	if b.Notifier != nil {
		b.Notifier.Alert("missing_usage:"+modelName, "计费缺少 usage",
			fmt.Sprintf("上游成功却无 usage，按零成本结算 model=%s channel=%d req=%s", modelName, channelID, reqID))
	}
}

// reqIDOf 从 gin.Context 取请求标识（RequestID 中间件写入），供端点给 Deps.ReqID 赋值。
func reqIDOf(c *gin.Context) string {
	return c.GetString(middleware.CtxRequestID)
}

// applyKeyDailyCost 成功计费后累加 key 的当日成本（与 App.Deduct 并行）；
// kid 由调用方提供（settleDispatch 用 prepared.Kid，raw 端点自取 ctx），失败日志+告警统一在此。
func (b *BillingCtx) applyKeyDailyCost(c *gin.Context, kid uint64, cost int64) {
	if reservationFromCtx(c) != nil {
		return // 预留路径由 BillingReservation.Settle 统一结算 key 日成本
	}
	if kid != 0 && cost > 0 && b.Keys != nil {
		if err := b.Keys.ApplyDailyCost(kid, cost, b.now()); err != nil {
			reqID := c.GetString(middleware.CtxRequestID)
			slog.Error("累加 key 日成本失败", "key", kid, "cost", cost, "request_id", reqID, "err", err)
			b.alertDeductFail("key:"+strconv.FormatUint(kid, 10), fmt.Sprintf("key 日成本累加失败 key=%d cost=%d req=%s: %v", kid, cost, reqID, err))
		}
	}
}

// requestDeps 构造 per-request Deps 的唯一入口：注入 Lookup（sessionID 非空启用会话粘连）
// + 客户端头 + 请求 ID + 审计捕获门控。新端点一律经此构造，勿在 handler 手抄字段赋值——
// CaptureBody 是其中唯一「漏接不报错」的字段（零值 false 表现为开审计的 App 静默存空正文）。
func (b *BillingCtx) requestDeps(c *gin.Context, p prepared, sessionID string) Deps {
	d := b.injectLookupWithSession(sessionID)
	d.ClientHeader = c.Request.Header
	d.ReqID = reqIDOf(c)
	d.CaptureBody = p.AuditEnabled // 审计关闭的请求流式透传不攒捕获体
	return d
}

// injectLookupWithSession 给 Deps 注入按 channelID 选账号→渠道运行参数(解密 key)的闭包；
// sessionID 非空时启用会话粘连路由。返回注入后的 Deps 副本，避免并发写共享 b.Deps。
func (b *BillingCtx) injectLookupWithSession(sessionID string) Deps {
	d := b.Deps
	if sessionID != "" {
		d.Lookup = func(channelID uint64, modelName string) (ChannelInfo, bool) {
			return b.Selector.LookupWithSession(sessionID, channelID, modelName)
		}
		d.LookupExcluding = func(channelID uint64, modelName string, exclude map[uint64]bool) (ChannelInfo, bool) {
			return b.Selector.LookupExcludingWithSession(sessionID, channelID, modelName, exclude)
		}
	} else {
		d.Lookup = b.Selector.Lookup
		d.LookupExcluding = b.Selector.LookupExcluding
	}
	d.RecordAccountResult = b.Selector.RecordResult
	return d
}

// settleArgs 请求收尾公共参数（settleDispatch 与 finish 两条路径共用）。
type settleArgs struct {
	Kid, AppID, ChannelID, AccountID uint64
	Model, ReqType, SessionID        string
	Status                           int
	LatencyMs, FirstTokenMs          int64
	Stream                           bool
	Usage                            billing.Usage
	UsageOK                          bool
	Cost                             int64
	ErrMsg                           string
	Proto, UpstreamProto             string
}

// settleCommon 两条收尾路径（chat/messages 的 settleDispatch、raw 端点的 finish）的公共尾段：
// App 配额扣减（失败记日志+告警，响应已回写无法拒绝）→ 请求日志落库 → 全候选失败告警。
// key 日成本、审计投递、粘连维护等差异部分留在各自调用方。
func (b *BillingCtx) settleCommon(c *gin.Context, a settleArgs) {
	reqID := c.GetString(middleware.CtxRequestID)
	if res := reservationFromCtx(c); res != nil {
		// 预留路径：按实际成本多退少补（App 配额 + key 日成本一次结算）。
		if err := res.Settle(a.Cost); err != nil {
			slog.Error("计费预留结算失败", "cost", a.Cost, "request_id", reqID, "err", err)
			b.alertDeductFail("reservation:"+strconv.FormatUint(a.AppID, 10), fmt.Sprintf("计费预留结算失败 cost=%d req=%s: %v", a.Cost, reqID, err))
		}
	} else if a.AppID != 0 && a.Cost > 0 {
		if err := b.Apps.Deduct(a.AppID, a.Cost); err != nil {
			slog.Error("计费扣减 App 配额失败", "app", a.AppID, "cost", a.Cost, "request_id", reqID, "err", err)
			b.alertDeductFail("app:"+strconv.FormatUint(a.AppID, 10), fmt.Sprintf("App 配额扣减失败 app=%d cost=%d req=%s: %v", a.AppID, a.Cost, reqID, err))
		}
	}
	b.Logs.Record(&model.RequestLog{
		APIKeyID:            a.Kid,
		AppID:               a.AppID,
		ChannelID:           a.ChannelID,
		AccountID:           a.AccountID,
		Model:               a.Model,
		Status:              a.Status,
		LatencyMs:           a.LatencyMs,
		FirstTokenMs:        a.FirstTokenMs,
		SessionID:           a.SessionID,
		Stream:              a.Stream,
		RequestType:         a.ReqType,
		ErrorMsg:            a.ErrMsg,
		PromptTokens:        a.Usage.PromptTokens,
		CompletionTokens:    a.Usage.CompletionTokens,
		TotalTokens:         a.Usage.TotalTokens,
		CachedTokens:        a.Usage.CachedTokens,
		CacheCreationTokens: a.Usage.CacheCreationTokens,
		ReasoningTokens:     a.Usage.ReasoningTokens,
		CostMicros:          a.Cost,
		UsageMissing:        !a.UsageOK,
		ClientIP:            c.ClientIP(),
		Headers:             sanitizeHeaders(c.Request.Header),
		RequestID:           reqID,
		Protocol:            a.Proto,
		UpstreamProtocol:    a.UpstreamProto,
	})
	b.alertAllFailed(a.Status, a.Model, reqID, a.ErrMsg)
}

// finish 统一计费扣减 + 落请求日志（raw 端点路径；key 日成本由各 handler 自行 applyKeyDailyCost）。
// status 由调用方传入实际 HTTP 状态码（失败时为 4xx/5xx，避免硬编码 200）。
// Protocol/UpstreamProtocol 恒 openai：embeddings/images/responses 均为 openai-only 端点。
func (b *BillingCtx) finish(c *gin.Context, appID, channelID, accountID uint64, modelName, reqType, sessionID string, u billing.Usage, usageOK bool, cost, latencyMs, firstTokenMs int64, stream bool, status int, errMsg ...string) {
	b.finishProto(c, "openai", appID, channelID, accountID, modelName, reqType, sessionID, u, usageOK, cost, latencyMs, firstTokenMs, stream, status, errMsg...)
}

// finishProto 同 finish，但允许指定入站/出站协议（count_tokens 等 Anthropic 端点用）。
func (b *BillingCtx) finishProto(c *gin.Context, proto string, appID, channelID, accountID uint64, modelName, reqType, sessionID string, u billing.Usage, usageOK bool, cost, latencyMs, firstTokenMs int64, stream bool, status int, errMsg ...string) {
	idv, _ := c.Get(middleware.CtxAPIKeyID)
	kid, _ := idv.(uint64)
	b.settleCommon(c, settleArgs{
		Kid: kid, AppID: appID, ChannelID: channelID, AccountID: accountID,
		Model: modelName, ReqType: reqType, SessionID: sessionID,
		Status: status, LatencyMs: latencyMs, FirstTokenMs: firstTokenMs, Stream: stream,
		Usage: u, UsageOK: usageOK, Cost: cost,
		ErrMsg: strings.Join(errMsg, ""),
		Proto:  proto, UpstreamProto: proto,
	})
}

// finishRawFailure raw 端点（embeddings/images/responses）DispatchRaw 未拿到响应（out.Resp==nil）
// 时的公共收尾：回写派发错误 → 落请求日志 → 投递审计，确保失败请求可追溯。
func (b *BillingCtx) finishRawFailure(c *gin.Context, out RawOutcome, p prepared, servedModel, reqType, sessionID string, stream bool, raw []byte) {
	b.finishRawFailureProto(c, "openai", out, p, servedModel, reqType, sessionID, stream, raw)
}

// finishRawFailureProto 同 finishRawFailure，但允许指定协议。
func (b *BillingCtx) finishRawFailureProto(c *gin.Context, proto string, out RawOutcome, p prepared, servedModel, reqType, sessionID string, stream bool, raw []byte) {
	// 使用 out.Status：限流时为 429，全候选失败时为 502/503（按失败类别分流）。
	st := out.Status
	if st == 0 {
		// 防御性兜底：DispatchRaw 现有出口均已填 Status。
		st = http.StatusBadGateway
	}
	if len(out.Skips) > 0 {
		// 与 respondDispatchFailure 的分流保持一致：落库/审计的状态码必须等于实际回给客户端的，
		// 否则请求日志与调用方所见对不上。
		st = dispatchFailStatus(out.Skips)
	}
	b.respondDispatchFailure(c, proto, out.Skips, st, out.Err)
	b.finishProto(c, proto, p.AppID, out.ChannelID, out.AccountID, servedModel, reqType, sessionID, billing.Usage{}, false, 0, out.LatencyMs, 0, stream, st, out.Err)
	b.auditSubmit(c, p.AuditEnabled, p.AppID, out.ChannelID, out.AccountID, servedModel, reqType, sessionID, st, billing.Usage{}, 0, out.LatencyMs, 0, stream, raw, []byte(out.Err), nil, 0)
}

// auditSubmit 协议端点统一审计投递：从 ctx 取 APIKeyID，组装 Entry 旁路落库。
// auditEnabled 由调用方传 prepared.AuditEnabled（Prepare 时从 App 行带出，不再回表查开关）。
// reqBody 须为转换前的原始入站 body（raw），respBody 为 handler 已读的（转换前/转换后均可）上游响应体；
// overflow 为流式捕获在途截断的全文元数据（非流式/未截断传 nil）。
func (b *BillingCtx) auditSubmit(c *gin.Context, auditEnabled bool, appID, channelID, accountID uint64, modelName, reqType, sessionID string,
	status int, u billing.Usage, cost, latencyMs, firstTokenMs int64, stream bool, reqBody, respBody []byte, overflow *audit.Overflow, eventCount int) {
	idv, _ := c.Get(middleware.CtxAPIKeyID)
	kid, _ := idv.(uint64)
	auditSubmit(b.Audit, auditEnabled, audit.Entry{
		RequestID: c.GetString(middleware.CtxRequestID),
		APIKeyID:  kid, AppID: appID, ChannelID: channelID, AccountID: accountID,
		Model: modelName, RequestType: reqType, Status: status, LatencyMs: latencyMs, FirstTokenMs: firstTokenMs, SessionID: sessionID, Stream: stream,
		PromptTokens: u.PromptTokens, CompletionTokens: u.CompletionTokens,
		CachedTokens: u.CachedTokens, CacheCreationTokens: u.CacheCreationTokens, ReasoningTokens: u.ReasoningTokens, CostMicros: cost,
		ClientIP: c.ClientIP(), UserAgent: c.Request.UserAgent(),
		RequestBody: reqBody, ResponseBody: respBody, ResponseOverflow: overflow, EventCount: eventCount,
	})
}

// now 返回时钟（未注入则用 time.Now）。
func (b *BillingCtx) now() time.Time {
	if b.Now != nil {
		return b.Now()
	}
	return time.Now()
}

// inbound 入站请求体的单次解析结果：入口解析一次，治理/计费读到的 model、stream、include_usage
// 与出站改写基于同一个对象，由类型保证两边对请求体的理解一致，候选内也不再重复解析。
//
// 曾经各处分别用定向 struct 解码取字段、出站另行整体解析：struct 解码跳过未知字段不校验其中的数字、
// 键名大小写不敏感且后者覆盖前者，客户端带 1e999 或 {"stream":true,"Stream":false} 即可让入口与
// 出站（及上游）结论分歧，绕过模型映射/白名单或让本次请求按 0 计费。
type inbound struct {
	raw        []byte
	obj        map[string]any // 解析失败（非对象/非法 JSON）为 nil
	model      string
	stream     bool
	streamOK   bool // stream 缺失、null 或布尔；字符串 "true" 等会被部分上游当作 true，入口须拒绝
	wantsUsage bool // 客户端自己要了 stream_options.include_usage
}

// parseInbound 解析入站请求体；失败时只保留 raw（model 为空，入口据此 400）。
func parseInbound(raw []byte) inbound {
	in := inbound{raw: raw}
	m, err := decodeBodyObject(raw)
	if err != nil {
		return in
	}
	in.obj = m
	in.model, _ = m["model"].(string)
	switch v := m["stream"].(type) {
	case nil:
		in.streamOK = true
	case bool:
		in.stream, in.streamOK = v, true
	}
	if so, ok := m["stream_options"].(map[string]any); ok {
		in.wantsUsage, _ = so["include_usage"].(bool)
	}
	return in
}

// decodeBodyObject 把请求体解析为顶层 JSON 对象。UseNumber 让数字按原文保留（json.Number），
// 超出 float64 的数字不会解析失败，重新序列化时也原样写回，不改变客户端请求语义。
// 顶层之后还有多余内容（如 {"a":1}{"b":2}）视为非法，避免与上游解析器理解不一致。
func decodeBodyObject(raw []byte) (map[string]any, error) {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	var m map[string]any
	if err := dec.Decode(&m); err != nil {
		return nil, err
	}
	if m == nil {
		return nil, errors.New("请求体须为 JSON 对象")
	}
	if _, err := dec.Token(); err != io.EOF {
		return nil, errors.New("请求体在 JSON 对象之后还有多余内容")
	}
	return m, nil
}

// sanitizeHeaders 提取请求头并脱敏敏感字段，返回 JSON 字符串。
// 敏感头（authorization/x-api-key/cookie）替换为 "***"，多值头序列化为 JSON 数组字符串。
// sensitiveHeaderParts 头名含这些片段即视为凭证类头（不区分大小写）：覆盖 Authorization、
// Proxy-Authorization、x-api-key、api-key（Azure）、x-goog-api-key（Gemini）、Cookie/Set-Cookie 及各类
// *-token/*-secret 自定义鉴权头。请求日志对所有管理角色与门户成员可见，客户端误带的第三方凭证不能明文落库。
var sensitiveHeaderParts = []string{"authorization", "api-key", "apikey", "cookie", "token", "secret", "password"}

func sensitiveHeader(name string) bool {
	n := strings.ToLower(name)
	for _, p := range sensitiveHeaderParts {
		if strings.Contains(n, p) {
			return true
		}
	}
	return false
}

func sanitizeHeaders(h http.Header) string {
	safe := make(map[string]string, len(h))
	for k, vs := range h {
		if sensitiveHeader(k) {
			safe[k] = "***"
		} else if len(vs) == 1 {
			safe[k] = vs[0]
		} else {
			b, _ := json.Marshal(vs)
			safe[k] = string(b)
		}
	}
	out, _ := json.Marshal(safe)
	return string(out)
}
