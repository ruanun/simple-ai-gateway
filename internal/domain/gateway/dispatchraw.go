package gateway

import (
	"context"
	"log/slog"
	"net/http"
	"time"

	"github.com/ruanun/simple-ai-gateway/internal/router"
)

// RawOutcome 原始派发结果（响应未回写客户端，由调用方读取/转换/关闭 Resp.Body）。
type RawOutcome struct {
	ChannelID      uint64
	AccountID      uint64 // 本次成功/最后尝试的账号 id（密钥池）
	Resp           *http.Response
	Err            string
	Status         int // HTTP 状态码（成功时从 Resp 取，限流等错误时预填，默认 502）
	LatencyMs      int64
	RateMultiplier float64      // 命中渠道的倍率(计费用)
	Model          string       // 实际命中的上游模型(候选 c.Model)
	Skips          []skipReason // 全候选失败/墙钟超时时的结构化失败原因（与 Outcome.Skips 同义）
}

// DispatchRaw 按候选定序请求上游指定 path（相对渠道 base_url），返回首个非 5xx 响应。
// 不做客户端回写；供 embeddings/images/协议转换 handler 使用。
// stream=true 时构造不带整体超时的 HTTP client，避免流式响应被截断。
// 遍历/半开兜底语义统一在 dispatchCore（与 Dispatch 共用骨架，注释见 dispatchcore.go）。
func DispatchRaw(cands []router.Candidate, path string, body []byte, d Deps, stream bool) RawOutcome {
	return dispatchRawInbound(cands, path, parseInbound(body), d, stream)
}

// dispatchRawInbound 同 DispatchRaw，但复用入口已解析的请求体（handler 主路径，避免重复解析）。
func dispatchRawInbound(cands []router.Candidate, path string, in inbound, d Deps, stream bool) RawOutcome {
	t0 := d.Now()
	return dispatchCore(cands, d, t0, dispatchHooks[RawOutcome]{
		kind: "DispatchRaw",
		try: func(c router.Candidate, info ChannelInfo, reasons *[]skipReason, sole bool) (RawOutcome, bool, bool) {
			return tryCandidateRaw(c, info, path, in, d, stream, t0, reasons, sole)
		},
		wallTimeout: func(reasons []skipReason) RawOutcome {
			return RawOutcome{Status: http.StatusGatewayTimeout, Err: wallClockErr(reasons), Skips: reasons}
		},
		rateLimited: func(channelID uint64) RawOutcome {
			return RawOutcome{ChannelID: channelID, Err: "channel rate limited", Status: http.StatusTooManyRequests}
		},
		allFailed: func(reasons []skipReason) RawOutcome {
			return RawOutcome{Err: coreFailDetail(reasons), Status: http.StatusBadGateway, Skips: reasons}
		},
		statusOf: func(o RawOutcome) int { return o.Status },
		errOf:    func(o RawOutcome) string { return o.Err },
		skipsOf:  func(o RawOutcome) []skipReason { return o.Skips },
	})
}

// tryCandidateRaw 处理单个已经过（或按半开兜底豁免）熔断/限流门的候选：账号级重试循环
// （渠道内换号）+ 上游请求。经 dispatchHooks.try 供 dispatchCore 的主遍历/半开兜底共用。
//
// 返回：
//   - out：仅当 terminal=true 时有意义（成功结果 / 504 墙钟超时）；
//   - attemptedThis：本候选是否真正发起过账号选择尝试（哪怕因无可用代理而未打到上游）；
//   - terminal：true 表示应把 out 作为最终结果立即返回给调用方。
func tryCandidateRaw(c router.Candidate, info ChannelInfo, path string, in inbound, d Deps, stream bool, t0 time.Time, reasons *[]skipReason, sole bool) (RawOutcome, bool, bool) {
	// 见 tryCandidate 同名闭包：集中组装失败留痕的四个同源字段，避免各留痕点手抄漏项。
	skipOf := func(kind skipKind, detail string) skipReason {
		return skipReason{ChannelID: c.ChannelID, Model: c.Model, Kind: kind, Detail: scrubDetail(info, detail),
			BaseURL: info.BaseURL, AccountID: info.AccountID, ErrorPassthrough: info.ErrorPassthrough}
	}
	attempts := 1 + info.MaxRetries
	start := d.Now()
	// 出站体一次构造：c.Model 跨重试不变，改写后的 body 循环内复用（对齐 tryCandidate 的循环外构造）。
	outBody := rewriteInbound(in, bodyRewrite{model: c.Model})
	tried := map[uint64]bool{} // 本轮渠道内已试账号，重试时排除避免选回坏账号
	attemptedThis := false
	var accountRelease func()
	releaseAccount := func() {
		if accountRelease != nil {
			accountRelease()
			accountRelease = nil
		}
	}
	defer releaseAccount()
	for a := 0; a < attempts; a++ {
		// 墙钟总超时同样要在单渠道多次重试之间生效。
		if d.MaxWall > 0 && d.Now().Sub(t0) > d.MaxWall {
			slog.Warn("DispatchRaw 504: 墙钟超时", "max_wall", d.MaxWall, "request_id", d.ReqID)
			return RawOutcome{Status: http.StatusGatewayTimeout, Err: wallClockErr(*reasons), Skips: *reasons}, true, true
		}
		if a > 0 {
			// 渠道内重试时重选账号（跳过已熔断/失败/已试账号）。
			var ok bool
			info, ok = lookupChannel(d, c.ChannelID, c.Model, tried)
			if !ok {
				break // 渠道内账号已全部不可用 → 下一渠道候选
			}
		}
		if info.AccountID != 0 {
			tried[info.AccountID] = true
		}
		attemptedThis = true
		if accountRPMExceeded(d, info) {
			*reasons = append(*reasons, skipOf(kindAccountRPM, ""))
			continue
		}
		rel, ok := acquireAccountSlot(d, info)
		if !ok {
			*reasons = append(*reasons, skipOf(kindAccountBusy, ""))
			continue
		}
		accountRelease = rel
		// 按调用方传入的 stream 参数构造 client：流式时禁用整体超时，避免响应被截断。
		cli, proxyID, pok := clientForInfo(d, info, stream)
		if !pok {
			// 绑了代理组但组内无可用代理（R2）：本地配置问题，未真正打到上游——
			// 不计入渠道/账号熔断（坑b），只留痕跳过原因；账号已在 tried 中，换号仍生效。
			*reasons = append(*reasons, skipOf(kindNoProxy, ""))
			releaseAccount()
			continue
		}
		provType := info.ProviderType
		if provType == "" {
			provType = "openai"
		}
		spec := specFor(provType, info, d.ClientHeader, stream)
		budget := firstByteBudget(d, info, t0, sole)
		if budget <= 0 {
			// 防御性兜底：正常时序下循环开头的墙钟检查已保证 budget>0。
			*reasons = append(*reasons, skipOf(kindDialFail, "首字节预算耗尽"))
			releaseAccount()
			continue
		}
		dialCtx, dialCancel := context.WithCancelCause(context.Background())
		budgetTimer := time.AfterFunc(budget, func() { dialCancel(errFirstByteTimeout) })
		resp, err := dialPathWith(dialCtx, cli, spec, path, outBody)
		if err == nil {
			// 拿到响应头即解除预算：raw 路径的 resp 由调用方读取，body 不受预算约束。
			budgetTimer.Stop()
		}
		// ⚠️ 这里刻意不 defer dialCancel：成功分支把未读的 resp 交给调用方（handler 负责
		// 读取/转换/关闭），函数返回时 cancel 会直接掐断响应体。cancel 只在失败分支执行。
		// 失败判定与 Dispatch 对齐：err / 5xx / 429 都换号重试（429 之前会被直接透传客户端、不换号、不记账号熔断）。
		if err != nil || retryable(resp.StatusCode, nil) {
			status := 0
			if resp != nil {
				status = resp.StatusCode
			}
			is429 := status == http.StatusTooManyRequests
			is403 := status == http.StatusForbidden
			var ra time.Duration
			if err == nil && is429 && resp != nil {
				ra = parseRetryAfter(resp.Header.Get("Retry-After"), d.Now())
			}
			if resp != nil {
				resp.Body.Close()
			}
			// 与 dispatch.go 对齐：上游真实失败也留痕，让全候选失败时详情带上失败原因。
			// raw 路径不读错误体，故 kindUpstreamStatus 的 Detail 恒空 → 渲染为「上游%d」（与改造前一致）。
			if err != nil {
				*reasons = append(*reasons, skipOf(kindDialFail, err.Error()))
			} else {
				*reasons = append(*reasons, upstreamSkip(skipOf(kindUpstreamStatus, ""), status))
			}
			// 错误分类（坑b）：客户端取消属本地未达上游失败，不计入熔断；
			// 首字节预算超时靠 dialCtx 的 cause 识别后必须计入（见 classifyFailure）。
			if rec, _ := classifyFailure(dialCtx, err, status); rec {
				// 渠道 breaker 一律走滑窗；429/403 记账号×模型配额冷却（P12a），不再立即冷却整渠道。
				// 注意 403 也可能是渠道级封禁或内容策略拒绝——此处按配额处理是权衡：
				// 渠道级封禁由滑窗兜底熔断，策略类 403 最多让该账号×模型冷却一个退避周期。
				d.Breaker.RecordResult(c.ChannelID, d.Now(), false, false)
				recordAcc(d, info.AccountID, c.Model, false, is429 || is403, ra)
				recordProxy(d, proxyID, false, is429)
			}
			// 本次尝试已失败、resp 已关闭 → 释放预算 ctx（成功分支刻意不做，见上方说明）。
			budgetTimer.Stop()
			dialCancel(nil)
			releaseAccount()
			continue
		}
		d.Breaker.RecordResult(c.ChannelID, d.Now(), true, false)
		recordAcc(d, info.AccountID, c.Model, true, false, 0)
		recordProxy(d, proxyID, true, false)
		if accountRelease != nil && resp != nil {
			resp.Body = &releaseOnClose{ReadCloser: resp.Body, release: accountRelease}
			accountRelease = nil
		}
		return RawOutcome{ChannelID: c.ChannelID, AccountID: info.AccountID, Resp: resp,
			Status:    resp.StatusCode,
			LatencyMs: time.Since(start).Milliseconds(), RateMultiplier: info.RateMultiplier, Model: c.Model}, attemptedThis, true
	}
	return RawOutcome{}, attemptedThis, false
}

// dialPathWith 用传入 client 请求上游指定 path（相对 base_url）。ctx 承载首字节预算，语义同 DialWith。
func dialPathWith(ctx context.Context, client *http.Client, spec HeaderSpec, path string, body []byte) (*http.Response, error) {
	req, err := buildPathRequest(spec, path, body)
	if err != nil {
		return nil, err
	}
	return client.Do(req.WithContext(withSpecSecrets(ctx, spec)))
}
