package gateway

import (
	"github.com/ruanun/simple-ai-gateway/internal/convert"
	"github.com/ruanun/simple-ai-gateway/internal/router"
)

// boostStickyChannel 把粘连渠道的候选提到列表最前面（保持其他顺序不变）。
// 若粘连渠道不在候选中则原样返回。
func boostStickyChannel(cands []router.Candidate, channelID uint64) []router.Candidate {
	if channelID == 0 {
		return cands
	}
	var sticky, rest []router.Candidate
	for _, c := range cands {
		if c.ChannelID == channelID {
			sticky = append(sticky, c)
		} else {
			rest = append(rest, c)
		}
	}
	if len(sticky) == 0 {
		return cands
	}
	return append(sticky, rest...)
}

// filterByProtocol 按入站协议 p 过滤候选：
// 渠道协议（ProviderType，一渠道一协议）= p → 保留并把本次出站协议 Outbound 置为 p（同协议透传）；
// 异协议候选剔除（跨协议转换见 filterWithConvert）。
func filterByProtocol(cands []router.Candidate, p string) []router.Candidate {
	var out []router.Candidate
	for _, c := range cands {
		if candidateSupports(c, p) {
			c.Outbound = p
			out = append(out, c)
		}
	}
	return out
}

// filterWithConvert 候选过滤 + 跨协议转换兜底（P12c，spec 规则 1+2）：
//  1. 透传候选：渠道协议 = p → 保留，出站=p，零转换；
//  2. 转换候选：渠道协议 ≠ p，但渠道 AllowConvert 且转换器支持 p↔渠道协议 → 保留，
//     出站=渠道协议(ProviderType)，标记 NeedsConvert。
//
// 排序：全部透传候选稳定分区在全部转换候选之前，两区内保持 Resolve 定序。
// 仅 chat/messages 两端点使用；responses/embeddings/images 维持 filterByProtocol（只透传）。
// 注意：渠道粘连 boostStickyChannel 在本函数之后执行，可把粘连的转换候选提前——
// 属会话亲和语义优先，接受（该渠道上次刚成功服务过本会话）。
func filterWithConvert(cands []router.Candidate, p string) []router.Candidate {
	var pass, conv []router.Candidate
	for _, c := range cands {
		if candidateSupports(c, p) {
			c.Outbound, c.Inbound = p, p
			pass = append(pass, c)
			continue
		}
		target := c.ProviderType
		if target == "" {
			target = "openai"
		}
		if c.AllowConvert && convert.Supported(p, target) {
			c.Outbound, c.Inbound, c.NeedsConvert = target, p, true
			conv = append(conv, c)
		}
	}
	return append(pass, conv...)
}

// hasConvertCandidate 候选中是否含需转换的候选（handler 预校验入站体用）。
func hasConvertCandidate(cands []router.Candidate) bool {
	for _, c := range cands {
		if c.NeedsConvert {
			return true
		}
	}
	return false
}

// candidateSupports 判断候选渠道是否支持协议 p：渠道协议（ProviderType，空视为 openai）= p。
func candidateSupports(c router.Candidate, p string) bool {
	t := c.ProviderType
	if t == "" {
		t = "openai"
	}
	return t == p
}

// protoOrDefault 返回出站协议，空(未做任何上游尝试)时回退默认值。
// P12c 起 Dispatch 成功与失败都会填实际尝试的出站协议；
// 仅「一次上游都没碰到」(全候选熔断/限流/无账号)时走这里的兜底。
func protoOrDefault(p, def string) string {
	if p == "" {
		return def
	}
	return p
}

// filterByAllowedChannels 按应用允许的渠道 ID 过滤候选；allowed 为空不过滤。
// ID 解析统一走 parseChannelIDs（models.go），与 /v1/models 的 App 渠道过滤共用同一实现。
func filterByAllowedChannels(cands []router.Candidate, allowed string) []router.Candidate {
	if allowed == "" {
		return cands
	}
	set := parseChannelIDs(allowed)
	if len(set) == 0 {
		// allowed 字段非空但全部 ID 解析失败：采用 fail-closed，拒绝所有候选，不回退放行。
		return nil
	}
	var out []router.Candidate
	for _, c := range cands {
		if set[c.ChannelID] {
			out = append(out, c)
		}
	}
	return out
}
