package gateway

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"time"

	"github.com/ruanun/simple-ai-gateway/internal/audit"
	"github.com/ruanun/simple-ai-gateway/internal/billing"
	"github.com/ruanun/simple-ai-gateway/internal/breaker"
	"github.com/ruanun/simple-ai-gateway/internal/convert"
	"github.com/ruanun/simple-ai-gateway/internal/proxydial"
	"github.com/ruanun/simple-ai-gateway/internal/ratelimit"
	"github.com/ruanun/simple-ai-gateway/internal/router"
)

// ChannelInfo 派发所需的渠道运行参数（由 handler 按 channelID 提供）。
// AccountID 标识本次选中的上游账号（密钥池）；0 表示未走账号选择（向后兼容）。
type ChannelInfo struct {
	BaseURL               string
	UpstreamKey           string
	Timeout               int
	MaxRetries            int
	RPMLimit              int
	AccountID             uint64
	AccountRPMLimit       int               // 账号级 RPM；<=0 不限
	AccountMaxConcurrency int               // 账号级在途并发；<=0 不限
	ProxyGroup            string            // 选中账号绑定的代理组名（空=直连出站）
	RateMultiplier        float64           // 渠道倍率(计费用);<=0 视为 1.0
	ProviderType          string            // "openai" 或 "anthropic"
	AuthStyle             string            // 出站鉴权风格：auto|x-api-key|bearer|both（仅 anthropic）
	CustomHeaders         map[string]string // 渠道级静态自定义请求头（已解析）
	ErrorPassthrough      bool              // 全候选失败时透传最后一个上游错误状态/脱敏文案
}

// Deps 派发依赖：熔断器、限流器、渠道参数查询、时钟。
// RecordAccountResult 可选：每次上游调用后回调账号级熔断器（nil 安全跳过）。
type Deps struct {
	Breaker             breaker.Breaker
	Limiter             ratelimit.Limiter
	AccountLimiter      ratelimit.Limiter                                                                          // 账号级 RPM（键 accrpm:<id>）；可为 nil
	AccountConcurrency  ratelimit.Concurrency                                                                      // 账号级在途并发（键 account:<id>）；可为 nil
	Lookup              func(channelID uint64, modelName string) (ChannelInfo, bool)                               // 取渠道运行参数（含选账号）
	LookupExcluding     func(channelID uint64, modelName string, exclude map[uint64]bool) (ChannelInfo, bool)      // 重试时排除已试账号重选（可为 nil，回退 Lookup）
	RecordAccountResult func(accountID uint64, modelName string, ok bool, quotaHit bool, retryAfter time.Duration) // 账号级结果回调（可为 nil）；quotaHit=429/403 配额类；retryAfter 见 parseRetryAfter
	Proxy               *ProxyResolver                                                                             // 出站代理解析（可为 nil，nil=全直连）
	Now                 func() time.Time
	ClientHeader        http.Header   // 客户端原始请求头，供出站白名单回填（每请求副本设置，可为 nil）
	MaxWall             time.Duration // 跨候选墙钟总超时(0=不限)，超时返回 504 止损（坑a）
	RecoverRoll         func() bool   // 渠道半开概率跳过：true=跳过本次半开渠道恢复期打流量；nil=不启用(不跳过)（坑c）
	ReqID               string        // 请求标识（每请求副本设置，可为空），502/504 日志按请求串联用
	CaptureBody         bool          // 本请求是否为审计捕获响应体（=App AuditEnabled，统一由 requestDeps 赋值；false 时流式透传不攒 captured）；raw 路径（DispatchRaw）不消费此字段，responses 流式在 handler 读取它门控 relaySSE。
}

// lookupChannel 取渠道运行参数：a>0 重试时优先用 LookupExcluding 跳过本轮已试账号（避免选回坏账号），
// LookupExcluding 为 nil 时回退 Lookup。
func lookupChannel(d Deps, channelID uint64, modelName string, tried map[uint64]bool) (ChannelInfo, bool) {
	if len(tried) > 0 && d.LookupExcluding != nil {
		return d.LookupExcluding(channelID, modelName, tried)
	}
	return d.Lookup(channelID, modelName)
}

// Outcome 一次派发的结果（落请求日志用）。
type Outcome struct {
	ChannelID        uint64
	AccountID        uint64 // 本次成功/最后尝试的账号 id（密钥池）
	Status           int
	Stream           bool
	LatencyMs        int64
	FirstTokenMs     int64 // 流式首 Token 延迟(ms)，非流式为 0
	Err              string
	Usage            billing.Usage   // 上游回传的 token 用量（成功 Relay 时填充）
	UsageOK          bool            // 是否解析到 usage（false 表示上游未给/无法解析）
	ResponseBody     []byte          // 已写出客户端的完整响应体（供审计回放；成功 Relay 时填充）
	ResponseOverflow *audit.Overflow // 流式捕获在途截断时的全文元数据（ResponseBody 仅为前缀）；未截断为 nil
	RateMultiplier   float64         // 命中渠道的倍率(计费用)
	Model            string          // 实际命中的上游模型(候选 c.Model);别名/映射后计费/日志按它
	Protocol         string          // 本次实际出站协议(openai|anthropic)，成功派发时填充
	Skips            []skipReason    // 全候选失败/墙钟超时时的结构化失败原因：对内渲染进 Err，对外派生脱敏摘要与状态码分流
}

func retryable(status int, err error) bool {
	return err != nil || status >= 500 || status == http.StatusTooManyRequests || status == http.StatusForbidden
}

// errFirstByteTimeout 首字节预算耗尽时作为 context 取消原因（cause）注入，
// 供 classifyFailure 把它与「客户端真取消」区分开。
var errFirstByteTimeout = errors.New("首字节预算超时")

// firstByteBudget 算本次上游尝试的「首字节预算」= min(渠道 Timeout, 墙钟剩余)。
//   - d.MaxWall==0（不限）→ 退化为渠道 Timeout，行为同硬墙钟改造前。
//   - sole=true（唯一候选豁免熔断跳过，见 dispatchcore.go 的 sole）→ 额外压到 10s：
//     单渠道部署在熔断期间仍会照打上游，若按渠道 Timeout(可能几百秒)等满，每个请求都会
//     被 CDN 边缘超时接管；压短预算让它快速失败，同时保住自愈探测路径。
//   - 墙钟剩余极小时预算随之极小，该次尝试几乎必然超时——这是期望行为（墙钟耗尽就该止损）。
func firstByteBudget(d Deps, info ChannelInfo, t0 time.Time, sole bool) time.Duration {
	budget := time.Duration(info.Timeout) * time.Second
	if d.MaxWall > 0 {
		budget = min(budget, d.MaxWall-d.Now().Sub(t0))
	}
	if sole {
		budget = min(budget, 10*time.Second)
	}
	return budget
}

// classifyFailure 判定一次上游尝试是否应计入渠道/账号/代理熔断，以及是否 429。
// 客户端取消(context.Canceled)属于"未真正打到上游"的本地失败——记入熔断会误伤健康渠道（坑b），
// 故不计。但出站请求现在绑了「首字节预算」ctx（见 firstByteBudget/DialWith），预算耗尽时
// 底层同样返回 context.Canceled——必须靠 context.Cause 把两者分开：
//
//	⚠️ 判定顺序是关键：预算砍掉时 errors.Is(err, context.Canceled) 也为 true，
//	所以必须先查 Cause==errFirstByteTimeout，再落到 Canceled 分支。顺序写反等于
//	改动完全失效（首字节超时不计熔断，挂死上游永不被隔离），且只验一向的测试发现不了。
//
// context.DeadlineExceeded 同样计入：来源是 http.Client.Timeout 打到的慢/挂上游
// （Go 1.23+ 起 client 超时错误 errors.Is 匹配 DeadlineExceeded）。
// 其余传输错误（连接失败等）视为真实失败计入。
// err==nil 时只看 status 是否 429（渠道/账号维度的配额判定）。
func classifyFailure(ctx context.Context, err error, status int) (recordBreaker bool, is429 bool) {
	if err != nil {
		if ctx != nil && errors.Is(context.Cause(ctx), errFirstByteTimeout) {
			return true, false // 首字节预算超时 = 上游慢/挂死，必须计入
		}
		if errors.Is(err, context.Canceled) {
			return false, false // 客户端取消，不计熔断
		}
		return true, false // 上游超时/连接/传输失败，计入
	}
	return true, status == http.StatusTooManyRequests
}

// parseRetryAfter 解析上游 Retry-After 响应头为等待时长：支持整数秒或 HTTP-date 两种格式，
// 解析失败/非正值/头为空一律返回 0（调用方按指数退避兜底）。
func parseRetryAfter(h string, now time.Time) time.Duration {
	if h == "" {
		return 0
	}
	if secs, err := strconv.Atoi(h); err == nil && secs > 0 {
		return time.Duration(secs) * time.Second
	}
	if tm, err := http.ParseTime(h); err == nil {
		if d := tm.Sub(now); d > 0 {
			return d
		}
	}
	return 0
}

// wallClockErr 拼出墙钟超时错误信息，附带已累积的 skipReasons 便于排障（MINOR#5）；
// reasons 为空时退化为固定文案。对内文案渲染与 coreFailDetail 同口径（joinInternal）。
func wallClockErr(reasons []skipReason) string {
	if len(reasons) == 0 {
		return "retry wall-clock exceeded"
	}
	return "retry wall-clock exceeded [" + joinInternal(reasons) + "]"
}

// Dispatch 按候选定序尝试，成功则 Relay 并返回 Outcome；全失败返回 status=502。
// 遍历/半开兜底语义统一在 dispatchCore（与 DispatchRaw 共用骨架，注释见 dispatchcore.go）。
func Dispatch(w http.ResponseWriter, cands []router.Candidate, body []byte, d Deps) Outcome {
	return dispatchInbound(w, cands, parseInbound(body), d)
}

// dispatchInbound 同 Dispatch，但复用入口已解析的请求体（handler 主路径，避免重复解析）。
func dispatchInbound(w http.ResponseWriter, cands []router.Candidate, in inbound, d Deps) Outcome {
	t0 := d.Now()
	var lastProto string // 最后一次协议决策（失败 Outcome 也要能回答 UpstreamProtocol），主/兜底遍历共享
	return dispatchCore(cands, d, t0, dispatchHooks[Outcome]{
		kind: "Dispatch",
		try: func(c router.Candidate, info ChannelInfo, reasons *[]skipReason, sole bool) (Outcome, bool, bool) {
			return tryCandidate(w, c, info, in, d, t0, reasons, &lastProto, sole)
		},
		wallTimeout: func(reasons []skipReason) Outcome {
			return Outcome{Status: http.StatusGatewayTimeout, Err: wallClockErr(reasons), Protocol: lastProto, Skips: reasons}
		},
		rateLimited: func(channelID uint64) Outcome {
			return Outcome{ChannelID: channelID, Status: http.StatusTooManyRequests, Err: "channel rate limited"}
		},
		allFailed: func(reasons []skipReason) Outcome {
			return Outcome{Status: http.StatusBadGateway, Err: coreFailDetail(reasons), Protocol: lastProto, Skips: reasons}
		},
		statusOf: func(o Outcome) int { return o.Status },
		errOf:    func(o Outcome) string { return o.Err },
		skipsOf:  func(o Outcome) []skipReason { return o.Skips },
	})
}

// tryCandidate 处理单个已经过（或按半开兜底豁免）熔断/限流门的候选：协议决策 + 出站体构造
// （改写 model / 协议转换 / thinking 剥离 / 流式 usage 注入剥离）+ 账号级重试循环（渠道内换号）。
// 供 corePass（主遍历）与 coreDeferred（半开兜底）共用，行为保持一致。
//
// 返回：
//   - out：仅当 terminal=true 时有意义（成功 Relay 结果 / 504 墙钟超时）；
//   - attemptedThis：本候选是否真正发起过账号选择尝试（哪怕因无可用代理而未打到上游，见 clientForInfo）；
//   - terminal：true 表示应把 out 作为最终结果立即返回给调用方。
func tryCandidate(w http.ResponseWriter, c router.Candidate, info ChannelInfo, in inbound, d Deps, t0 time.Time, reasons *[]skipReason, lastProto *string, sole bool) (Outcome, bool, bool) {
	streamReq, wantsUsage := in.stream, in.wantsUsage
	// skipOf 组装本次尝试的失败留痕。四个同源字段散在各留痕点手抄一遍，漏抄 BaseURL 就是
	// 502 日志静默少一条上游地址；闭包捕获 info 变量本身，渠道内重试换号后取到的是当次的值。
	skipOf := func(kind skipKind, detail string) skipReason {
		return skipReason{ChannelID: c.ChannelID, Model: c.Model, Kind: kind, Detail: scrubDetail(info, detail),
			BaseURL: info.BaseURL, AccountID: info.AccountID, ErrorPassthrough: info.ErrorPassthrough}
	}
	// 出站协议决策（P12b）：候选过滤阶段填的 Outbound 优先（透传=入站协议/转换=渠道原生协议），
	// 空则回退渠道原生协议 ProviderType（兼容未经过滤的旧路径/测试）。
	provType := c.Outbound
	if provType == "" {
		provType = info.ProviderType
	}
	if provType == "" {
		provType = "openai"
	}
	*lastProto = provType
	// 出站体单趟构造：透传路径 model 改写/thinking 剥离/include_usage 注入合并为一次解析序列化；
	// 转换路径 convert.Request 内部自带解析，仅在其前改 model、其后处置 include_usage（P12c）。
	// include_usage 决策（dispatch 是 openai 出站的唯一决策方）：见 streamUsageMode。
	suMode := streamUsageMode(provType, streamReq, c.DisableStreamUsage)
	var outBody []byte
	if c.NeedsConvert {
		cb, cerr := convert.Request(rewriteInbound(in, bodyRewrite{model: c.Model}), c.Inbound, c.Outbound)
		if cerr != nil {
			*reasons = append(*reasons, skipOf(kindReqConvertFail, cerr.Error()))
			return Outcome{}, false, false
		}
		// DisableStreamUsage 对转换路径一致生效：convert.Request 可能已强制加 include_usage，此处统一处置。
		outBody = rewriteBody(cb, bodyRewrite{streamUsage: suMode})
	} else {
		// 透传到不支持 thinking 的上游：剥离思考字段避免 400(B)；转换路径 IR 不带这些字段无需处理。
		outBody = rewriteInbound(in, bodyRewrite{model: c.Model, stripThinking: c.StripThinking, streamUsage: suMode})
	}
	// 剥离：客户端自身没要 usage 而我们注入了 → 透传路径回写时剥掉注入产生的用量块（保透传语义；
	// 转换路径客户端异协议、由编码器原生重发 usage，无需剥离）。
	stripInjected := provType == "openai" && !c.NeedsConvert && streamReq && !c.DisableStreamUsage && !wantsUsage
	attempts := 1 + info.MaxRetries
	start := d.Now()
	tried := map[uint64]bool{} // 本轮渠道内已试账号，重试时排除避免选回坏账号
	attemptedThis := false
	var accountRelease func()
	releaseAccount := func() {
		if accountRelease != nil {
			accountRelease()
			accountRelease = nil
		}
	}
	// 成功路径在 Relay 返回后由 defer 释放；失败路径在循环末尾显式释放，避免多账号重试叠占槽位。
	defer releaseAccount()
	for a := 0; a < attempts; a++ {
		// 墙钟总超时同样要在单渠道多次重试之间生效（同一候选高 MaxRetries 也不能拖过预算）。
		if d.MaxWall > 0 && d.Now().Sub(t0) > d.MaxWall {
			slog.Warn("Dispatch 504: 墙钟超时", "max_wall", d.MaxWall, "request_id", d.ReqID)
			return Outcome{Status: http.StatusGatewayTimeout, Err: wallClockErr(*reasons), Protocol: *lastProto, Skips: *reasons}, true, true
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
		stream := streamReq
		cli, proxyID, pok := clientForInfo(d, info, stream)
		if !pok {
			// 绑了代理组但组内无可用代理（R2）：本地配置问题，未真正打到上游——
			// 不计入渠道/账号熔断（坑b），只留痕跳过原因；账号已在 tried 中，换号仍生效。
			*reasons = append(*reasons, skipOf(kindNoProxy, ""))
			releaseAccount()
			continue
		}
		dialTime := d.Now()
		spec := specFor(provType, info, d.ClientHeader, stream)
		budget := firstByteBudget(d, info, t0, sole)
		if budget <= 0 {
			// 防御性兜底：正常时序下循环开头的墙钟检查已保证 budget>0；
			// 并发时序下预算耗尽就不再发起本次尝试，交由外层墙钟分支返回 504。
			*reasons = append(*reasons, skipOf(kindDialFail, "首字节预算耗尽"))
			releaseAccount()
			continue
		}
		dialCtx, dialCancel := context.WithCancelCause(context.Background())
		budgetTimer := time.AfterFunc(budget, func() { dialCancel(errFirstByteTimeout) })
		// 收尾：三个 Relay 都是同步调用，body 在本作用域内读完才返回，故函数退出时 cancel 安全。
		// 循环内累积的 defer 只是已 Stop 的 timer + 幂等 cancel（attempts 为个位数）。
		defer func() { budgetTimer.Stop(); dialCancel(nil) }()
		r, err := DialWith(dialCtx, cli, For(provType), spec, outBody)
		if err == nil {
			// ⚠️ 必须在 Relay 之前解除：预算只覆盖「发出请求到收到响应头」，
			// 否则 body 传输期间定时器触发会 cancel ctx，把流式 SSE 掐断在中途。
			budgetTimer.Stop()
		}
		status := 0
		var upMsg string
		if err == nil {
			status = r.StatusCode
			if !retryable(status, nil) {
				var res *Result
				var u billing.Usage
				var uok bool
				var respBody []byte
				if c.NeedsConvert {
					var cerr error
					res, u, uok, respBody, cerr = RelayConvert(w, r, stream, provType, c.Inbound, d.CaptureBody, dialTime)
					if cerr != nil {
						// 未写出任何字节的响应转换失败：协议不匹配属渠道级配置问题——
						// 换账号重试同渠道只会重复失败，直接 failover 下一候选（spec §7）。
						// 账号/代理都成功完成了本职（鉴权通过、连通拿到 200），按成功记账，
						// 只记渠道失败促成滑窗熔断隔离误配渠道。
						*reasons = append(*reasons, skipOf(kindRespConvertFail, cerr.Error()))
						d.Breaker.RecordResult(c.ChannelID, d.Now(), false, false)
						recordAcc(d, info.AccountID, c.Model, true, false, 0)
						recordProxy(d, proxyID, true, false)
						break // 跳出 attempts 循环 → 下一候选
					}
				} else if provType == "anthropic" {
					res, u, uok, respBody = RelayAnthropic(w, r, stream, d.CaptureBody, dialTime)
				} else {
					res, u, uok, respBody = Relay(w, r, stream, stripInjected, d.CaptureBody, dialTime)
				}
				d.Breaker.RecordResult(c.ChannelID, d.Now(), true, false)
				// 非重试型 4xx（400/401/404 等）也走这里清退避：鉴权/配额已通过（能拿到响应），
				// 只是请求本身有问题（如参数错误），不是账号/渠道健康问题，故按成功记账号结果。
				recordAcc(d, info.AccountID, c.Model, true, false, 0)
				recordProxy(d, proxyID, true, false)
				var errMsg string
				if res.Status >= 400 {
					errMsg = extractErrorMessage(respBody)
				}
				return Outcome{ChannelID: c.ChannelID, AccountID: info.AccountID, Status: res.Status, Stream: stream,
					LatencyMs: time.Since(start).Milliseconds(), FirstTokenMs: res.FirstTokenMs,
					Usage: u, UsageOK: uok, ResponseBody: respBody, ResponseOverflow: res.CaptureOverflow, Err: errMsg,
					RateMultiplier: info.RateMultiplier, Model: c.Model, Protocol: provType}, attemptedThis, true
			}
			// 错误体只取前 4KiB 提取错误信息：ContentLength=-1(chunked)无从预判大小，
			// 一律 LimitReader 封顶，防故障上游用超大错误体撑爆内存；顺带排空小错误体利于连接复用。
			if b, e := io.ReadAll(io.LimitReader(r.Body, 4096)); e == nil {
				upMsg = extractErrorMessage(b)
			}
			r.Body.Close()
		}
		is429 := status == http.StatusTooManyRequests
		is403 := status == http.StatusForbidden
		// 对内文案的「上游%d」与「上游%d(%s)」两种形态由 renderInternal 按 Detail 是否为空区分。
		if err != nil {
			*reasons = append(*reasons, skipOf(kindDialFail, err.Error()))
		} else {
			*reasons = append(*reasons, upstreamSkip(skipOf(kindUpstreamStatus, upMsg), status))
		}
		// 错误分类（坑b）：客户端取消属本地未达上游失败，不计入熔断，避免误伤健康渠道；
		// 首字节预算超时则相反，靠 dialCtx 的 cause 识别后必须计入（见 classifyFailure）。
		if rec, _ := classifyFailure(dialCtx, err, status); rec {
			// 渠道 breaker 一律走失败率滑窗：429/403 按账号×模型配额处理(quotaHit 交给账号层退避)，
			// 不再立即冷却整渠道，避免 A 模型额度耗尽误伤同渠道 B 模型（P12a）。
			// 注意 403 也可能是渠道级封禁或内容策略拒绝——此处按配额处理是权衡：
			// 渠道级封禁由滑窗兜底熔断，策略类 403 最多让该账号×模型冷却一个退避周期。
			var ra time.Duration
			if err == nil && is429 {
				ra = parseRetryAfter(r.Header.Get("Retry-After"), d.Now())
			}
			d.Breaker.RecordResult(c.ChannelID, d.Now(), false, false)
			recordAcc(d, info.AccountID, c.Model, false, is429 || is403, ra)
			// 代理层结果记入代理 breaker（err / 上游 5xx-429 均计失败；proxyID==0 安全跳过）。
			recordProxy(d, proxyID, false, is429)
		}
		releaseAccount()
	}
	return Outcome{}, attemptedThis, false
}

// extractErrorMessage 从上游 JSON 响应体提取错误信息（error.message 或 error 字符串）。
func extractErrorMessage(body []byte) string {
	var obj struct {
		Error any `json:"error"`
	}
	if json.Unmarshal(body, &obj) != nil || obj.Error == nil {
		return ""
	}
	switch e := obj.Error.(type) {
	case map[string]any:
		if msg, ok := e["message"].(string); ok {
			return msg
		}
	case string:
		return e
	}
	return ""
}

// recordAcc 安全回调账号级结果：RecordAccountResult 为 nil 或 accountID==0 时跳过。
// retryAfter 透传上游 Retry-After 解析结果（非 429 场景传 0），供账号×模型冷却参考（坑d）。
func recordAcc(d Deps, accountID uint64, modelName string, ok, quotaHit bool, retryAfter time.Duration) {
	if d.RecordAccountResult != nil && accountID != 0 {
		d.RecordAccountResult(accountID, modelName, ok, quotaHit, retryAfter)
	}
}

// clientForInfo 按 ChannelInfo 解析出站 http.Client：
//   - d.Proxy==nil（未装配代理池）：按 timeout/流式自建直连 client，proxyID=0，ok=true（行为同改造前）。
//   - d.Proxy!=nil：委托 ProxyResolver 按账号 ProxyGroup 选代理（空组=直连）。
//
// ok=false 表示绑了代理组但组内无可用代理（R2，本地配置问题、未真正打到上游）；
// 调用方只留痕跳过原因、换号重试，不计入渠道/账号熔断（坑b）。
func clientForInfo(d Deps, info ChannelInfo, stream bool) (*http.Client, uint64, bool) {
	if d.Proxy == nil {
		cli, _ := proxydial.BuildClient(nil, info.Timeout, stream)
		return cli, 0, true
	}
	return d.Proxy.ClientFor(info.ProxyGroup, info.Timeout, stream)
}

// recordProxy 安全回调代理级结果：d.Proxy 为 nil 或 proxyID==0（直连）时跳过。
func recordProxy(d Deps, proxyID uint64, ok, is429 bool) {
	if d.Proxy != nil && proxyID != 0 {
		d.Proxy.RecordResult(proxyID, ok, is429)
	}
}

func channelKey(id uint64) string {
	return "channel:" + strconv.FormatUint(id, 10)
}
