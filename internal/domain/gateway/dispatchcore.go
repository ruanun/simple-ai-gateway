package gateway

import (
	"log/slog"
	"net/http"
	"time"

	"github.com/ruanun/simple-ai-gateway/internal/router"
)

// dispatchHooks 把 chat 回写路径（Dispatch/Outcome）与 raw 路径（DispatchRaw/RawOutcome）
// 的结果构造差异参数化；dispatchCore 只负责两条路径完全一致的遍历定序语义。
// 历史教训：两套手写平行骨架曾漂移出行为不一致（见 git 历史），统一后物理上不可能再漂移。
type dispatchHooks[T any] struct {
	kind        string                                                                                       // 日志前缀："Dispatch" | "DispatchRaw"
	try         func(c router.Candidate, info ChannelInfo, reasons *[]skipReason, sole bool) (T, bool, bool) // 单候选尝试（tryCandidate/tryCandidateRaw 的闭包适配）；sole 见 corePass
	wallTimeout func(reasons []skipReason) T                                                                 // 504 墙钟超时结果
	rateLimited func(channelID uint64) T                                                                     // 末位候选被渠道限流的 429 结果
	allFailed   func(reasons []skipReason) T                                                                 // 502/503 全候选失败结果（状态码由调用方按 reasons 分流）
	statusOf    func(T) int                                                                                  // 结果状态码（502 日志判定）
	errOf       func(T) string                                                                               // 结果错误串（502 日志内容）
	skipsOf     func(T) []skipReason                                                                         // 结果失败留痕（502 日志渲染实际出站地址）
}

// dispatchCore 主遍历 + 半开兜底二次遍历的公共骨架。
// 主遍历（corePass）尊重渠道半开概率跳过（RecoverRoll），把被跳过的候选收进 deferred
// （这些候选已通过熔断 Allow + 限流 Allow 门，RPM 令牌已消费）；若主遍历全程没有任何候选
// 进入真正尝试且 deferred 非空，则用 coreDeferred 只重跑这些半开候选一次——直接 Lookup
// 打流量，跳过熔断/限流门（IMPORTANT#1：RPM 是硬约束，未被跳过的候选不能豁免限流；
// 但已消费令牌的半开候选也不该被二次扣费），避免恢复期无候选可用（坑c）。
func dispatchCore[T any](cands []router.Candidate, d Deps, t0 time.Time, h dispatchHooks[T]) T {
	out, attempted, deferred := corePass(cands, d, t0, h)
	if attempted || len(deferred) == 0 {
		coreLogIfAllFailed(h, len(cands), out, d.ReqID)
		return out
	}
	out2, _ := coreDeferred(deferred, d, t0, h)
	coreLogIfAllFailed(h, len(cands), out2, d.ReqID)
	return out2
}

// coreLogIfAllFailed 只在最终确定要返回给调用方的"全部候选失败"结果上打日志（502），
// 避免半开兜底二次遍历时把被丢弃的第一遍失败结果也误打成日志噪声。
func coreLogIfAllFailed[T any](h dispatchHooks[T], nCands int, out T, reqID string) {
	if h.statusOf(out) != http.StatusBadGateway {
		return
	}
	attrs := []any{"candidates", nCands, "err", h.errOf(out), "request_id", reqID}
	// 带上实际打到过的上游地址：对内取证串只有 ch{id}(model)，线上多渠道时答不了「连的哪台」。
	// 全部候选都没真出站（熔断/无账号）时为空，此时不带该字段。
	if up := renderUpstreams(h.skipsOf(out)); up != "" {
		attrs = append(attrs, "upstreams", up)
	}
	slog.Error(h.kind+" 502: 全候选失败", attrs...)
}

// corePass 跑主遍历：按候选定序尝试，遇到半开概率跳过的候选不放弃，而是收进 deferred
// 供 coreDeferred 兜底重跑（不重复扣限流，IMPORTANT#1）。
// 返回 (结果, 本轮是否有候选进入实际尝试, 被半开跳过的候选列表)。
func corePass[T any](cands []router.Candidate, d Deps, t0 time.Time, h dispatchHooks[T]) (T, bool, []router.Candidate) {
	now := d.Now()
	var skipReasons []skipReason
	attempted := false
	var deferred []router.Candidate
	for i, c := range cands {
		// 跨候选墙钟总超时：整体重试预算耗尽即止损，避免慢/坏上游拖满客户端连接（坑a）。
		// 这是终态返回，attempted 硬编码 true、deferred 置空，避免触发半开兜底二次遍历（MINOR#3）。
		// 这里刻意丢弃已收集的 deferred（放弃半开候选救援）：deferred 里的候选已经消费过限流令牌，
		// 若在此处仍尝试救援就必须再走一次 coreDeferred，但墙钟已耗尽，没有预算再打一轮；
		// 强行救援等于重新引入"二次遍历重扣限流"的旧 bug（IMPORTANT#1）。这是有意的取舍，勿改。
		if d.MaxWall > 0 && d.Now().Sub(t0) > d.MaxWall {
			slog.Warn(h.kind+" 504: 墙钟超时", "max_wall", d.MaxWall, "request_id", d.ReqID)
			return h.wallTimeout(skipReasons), true, nil
		}
		sole := len(cands) == 1
		// 熔断过滤（唯一候选豁免跳过，但 Allow 必须始终被调用）：open→halfOpen 的状态转换发生在
		// MemoryBreaker.Allow 内部（冷却到期时才置 halfOpen 并放行）。若唯一候选场景下因 sole 短路
		// 而完全不调用 Allow，该渠道就永远不会转半开、Recovering() 永远 true、RecordResult 在
		// stOpen 分支直接忽略结果——熔断永久卡死，无法自愈。故这里始终调用 Allow 驱动状态机，
		// 仅用其返回值在非 sole 时决定是否跳过。
		allowed := d.Breaker.Allow(c.ChannelID, now)
		if !sole && !allowed {
			skipReasons = append(skipReasons, skipReason{ChannelID: c.ChannelID, Model: c.Model, Kind: kindBreakerOpen})
			continue
		}
		info, ok := d.Lookup(c.ChannelID, c.Model)
		if !ok {
			skipReasons = append(skipReasons, skipReason{ChannelID: c.ChannelID, Model: c.Model, Kind: kindNoAccount})
			continue
		}
		// 渠道级限流（限流是硬约束，唯一候选不豁免）
		if !d.Limiter.Allow(channelKey(c.ChannelID), info.RPMLimit, now) {
			// 同上：终态返回，attempted 硬编码 true 避免触发半开兜底（MINOR#3）。这里同样刻意丢弃
			// deferred：救援它们需要重新过一遍限流门，而限流是硬约束不能豁免，救援即二次扣限流——
			// 这正是 IMPORTANT#1 要避免的 bug，故末位候选被限流时宁可直接止损也不救援，是有意的取舍。
			if i == len(cands)-1 {
				return h.rateLimited(c.ChannelID), true, nil
			}
			continue
		}
		// 渠道级半开概率跳过（坑c）：恢复期以 (1-recoverFactor) 概率少打流量；
		// RPM 令牌已在上面的 Limiter.Allow 消费，跳过不代表要退还——收进 deferred 供兜底遍历
		// 直接打流量（跳过熔断/限流门，不重复消费），而不是让整个候选序列重新走一遍限流门（IMPORTANT#1）。
		if d.Breaker != nil && d.RecoverRoll != nil && d.Breaker.Recovering(c.ChannelID, d.Now()) && d.RecoverRoll() {
			skipReasons = append(skipReasons, skipReason{ChannelID: c.ChannelID, Model: c.Model, Kind: kindHalfOpenSkip})
			deferred = append(deferred, c)
			continue
		}
		out, attemptedThis, terminal := h.try(c, info, &skipReasons, sole)
		if terminal {
			return out, true, nil
		}
		if attemptedThis {
			attempted = true
		}
	}
	// 日志留给 dispatchCore 在确定最终返回结果后统一打（避免半开兜底二次遍历时打出被丢弃的中间结果）。
	return h.allFailed(skipReasons), attempted, deferred
}

// coreDeferred 半开兜底：只重跑主遍历（corePass）中因半开概率跳过的候选。这些候选在主遍历里
// 已经通过熔断 Allow + 限流 Allow 门（RPM 令牌已消费），因此这里直接 Lookup 打流量，跳过熔断/限流门——
// 既不重复扣限流（IMPORTANT#1），也不需要再判 Recovering（跳过就是为了给它们一次探测机会）。
func coreDeferred[T any](deferred []router.Candidate, d Deps, t0 time.Time, h dispatchHooks[T]) (T, bool) {
	var skipReasons []skipReason
	attempted := false
	for _, c := range deferred {
		if d.MaxWall > 0 && d.Now().Sub(t0) > d.MaxWall {
			slog.Warn(h.kind+" 504: 墙钟超时", "max_wall", d.MaxWall, "request_id", d.ReqID)
			return h.wallTimeout(skipReasons), true
		}
		info, ok := d.Lookup(c.ChannelID, c.Model)
		if !ok {
			skipReasons = append(skipReasons, skipReason{ChannelID: c.ChannelID, Model: c.Model, Kind: kindNoAccount})
			continue
		}
		// sole=false：半开兜底的候选一定来自多候选场景（单候选不会被降权跳过），无短预算豁免。
		out, attemptedThis, terminal := h.try(c, info, &skipReasons, false)
		if terminal {
			return out, true
		}
		if attemptedThis {
			attempted = true
		}
	}
	return h.allFailed(skipReasons), attempted
}
