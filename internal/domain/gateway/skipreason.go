package gateway

import (
	"fmt"
	"net/http"
	"strings"
)

// skipKind 派发过程中单个候选被跳过/失败的原因分类。
// 取值与「对内渲染文案」一一对应（见 renderInternal）——结构化的唯一目的是在保住对内
// 取证文本逐字不变的前提下，额外派生出一份不含内部拓扑的对外摘要（见 publicSummary）。
type skipKind uint8

const (
	kindBreakerOpen     skipKind = iota // 渠道熔断中，未打到上游
	kindNoAccount                       // 渠道禁用/账号全熔断或配额冷却，取不到可用账号
	kindHalfOpenSkip                    // 半开恢复期按概率降权跳过
	kindNoProxy                         // 账号绑了代理组但组内无可用代理（本地配置问题）
	kindReqConvertFail                  // 请求协议转换失败
	kindRespConvertFail                 // 响应协议转换失败（已拿到 200 但转换不了）
	kindDialFail                        // 连接/传输失败，含首字节预算超时
	kindUpstreamStatus                  // 真打到上游但返回可重试的错误状态
	kindAccountRPM                      // 账号级 RPM 已达上限，未打到上游
	kindAccountBusy                     // 账号级在途并发已满，未打到上游
)

// skipReason 一条候选失败留痕。Detail 只进对内渲染（含上游错误原文/错误串），
// 对外摘要恒不消费它。
type skipReason struct {
	ChannelID uint64
	Model     string
	Kind      skipKind
	Status    int    // 仅 kindUpstreamStatus 有意义
	Detail    string // 上游错误消息 / error 字符串；仅对内

	// 以下两项**只供进程日志**（renderUpstreams），renderInternal 与 publicSummary 都不消费：
	// 前者是进 RequestLog/审计的取证证据，格式已冻结；后者刻意不含内部拓扑。
	// 未真正打到上游的原因（熔断/无账号/降权跳过）留空。
	BaseURL   string // 本次尝试对应的上游地址（不含凭证，凭证在请求头；未必已真正发出，如无代理/转换失败）
	AccountID uint64 // 实际选中的账号，定位是哪把 key
	// ErrorPassthrough 渠道开启了错误透传：respondDispatchFailure 可用本条 Status/Detail 回给客户端。
	ErrorPassthrough bool
}

// upstreamSkip 给「真打到上游但状态码不对」的留痕补上状态码（Status 仅此 Kind 有意义）。
func upstreamSkip(r skipReason, status int) skipReason {
	r.Status = status
	return r
}

// renderInternal 渲染单条对内文案。
// ⚠️ 这些字符串进 RequestLog.ErrorMsg 与审计正文，是取证证据——
// 与结构化改造前的 fmt.Sprintf 输出必须逐字一致，改动等于损坏历史证据的可比性。
func renderInternal(r skipReason) string {
	switch r.Kind {
	case kindBreakerOpen:
		return fmt.Sprintf("ch%d(%s):熔断中", r.ChannelID, r.Model)
	case kindNoAccount:
		return fmt.Sprintf("ch%d(%s):无可用账号(渠道禁用/全部熔断或配额冷却)", r.ChannelID, r.Model)
	case kindHalfOpenSkip:
		return fmt.Sprintf("ch%d(%s):半开降权跳过", r.ChannelID, r.Model)
	case kindNoProxy:
		return fmt.Sprintf("ch%d(%s):账号无可用代理", r.ChannelID, r.Model)
	case kindReqConvertFail:
		return fmt.Sprintf("ch%d(%s):请求转换失败(%s)", r.ChannelID, r.Model, r.Detail)
	case kindRespConvertFail:
		return fmt.Sprintf("ch%d(%s):响应转换失败(%s)", r.ChannelID, r.Model, r.Detail)
	case kindDialFail:
		return fmt.Sprintf("ch%d(%s):连接失败(%s)", r.ChannelID, r.Model, r.Detail)
	case kindUpstreamStatus:
		if r.Detail != "" {
			return fmt.Sprintf("ch%d(%s):上游%d(%s)", r.ChannelID, r.Model, r.Status, r.Detail)
		}
		return fmt.Sprintf("ch%d(%s):上游%d", r.ChannelID, r.Model, r.Status)
	case kindAccountRPM:
		return fmt.Sprintf("ch%d(%s):账号RPM限流", r.ChannelID, r.Model)
	case kindAccountBusy:
		return fmt.Sprintf("ch%d(%s):账号并发已满", r.ChannelID, r.Model)
	}
	return fmt.Sprintf("ch%d(%s):未知失败", r.ChannelID, r.Model)
}

// coreFailDetail 拼全候选失败的对内详情（带累计跳过原因）。
// 输出格式与结构化改造前一致：空列表退化为固定文案，非空时 "; " 连接包在方括号内。
func coreFailDetail(reasons []skipReason) string {
	if len(reasons) == 0 {
		return "all candidates failed"
	}
	return fmt.Sprintf("all candidates failed [%s]", joinInternal(reasons))
}

// joinInternal 把多条原因按对内文案用 "; " 连接（coreFailDetail 与 wallClockErr 共用）。
func joinInternal(reasons []skipReason) string {
	parts := make([]string, 0, len(reasons))
	for _, r := range reasons {
		parts = append(parts, renderInternal(r))
	}
	return strings.Join(parts, "; ")
}

// renderUpstreams 渲染「本次实际打到过哪些上游」，仅供 502 全候选失败的进程日志。
// 对内取证串（renderInternal）只有 ch{id}(model)，线上多渠道时无法回答「到底连的哪台」；
// 而对外摘要按设计不含地址。故单开这条：ch{id}(acc{aid})={base_url}，按 "; " 连接。
// 同渠道账号多次重试会产生多条同址记录，去重后只留一条。
// 没有任何候选真打到上游时返回空串，调用方据此不带该字段。
func renderUpstreams(reasons []skipReason) string {
	seen := map[string]bool{}
	parts := make([]string, 0, len(reasons))
	for _, r := range reasons {
		if r.BaseURL == "" {
			continue // 熔断/无账号等未出站的原因没有地址可言
		}
		s := fmt.Sprintf("ch%d(acc%d)=%s", r.ChannelID, r.AccountID, r.BaseURL)
		if seen[s] {
			continue
		}
		seen[s] = true
		parts = append(parts, s)
	}
	return strings.Join(parts, "; ")
}

// publicKindWord Kind→对外措辞。多个 Kind 可共享同一措辞（如请求/响应转换失败），
// publicSummary 按措辞而非按 Kind 聚合，避免同措辞被拆成两条计数。
func publicKindWord(k skipKind) string {
	switch k {
	case kindBreakerOpen:
		return "熔断中"
	case kindNoAccount:
		return "限流冷却"
	case kindHalfOpenSkip:
		return "恢复期降权"
	case kindNoProxy:
		return "无可用代理"
	case kindReqConvertFail, kindRespConvertFail:
		return "协议转换失败"
	case kindDialFail:
		return "连接失败"
	case kindUpstreamStatus:
		return "上游错误"
	case kindAccountRPM, kindAccountBusy:
		return "账号容量"
	}
	return "未知原因"
}

// publicSummary 生成回给终端调用方的失败摘要：按对外措辞聚合计数，顺序按首次出现（输出稳定）。
// 刻意不含渠道 ID/渠道名/模型名/base_url/代理地址/上游错误原文——这些只进对内渲染。
func publicSummary(reasons []skipReason) string {
	if len(reasons) == 0 {
		return "所有上游候选均不可用"
	}
	order := make([]string, 0, len(reasons))
	cnt := map[string]int{}
	for _, r := range reasons {
		w := publicKindWord(r.Kind)
		if _, seen := cnt[w]; !seen {
			order = append(order, w)
		}
		cnt[w]++
	}
	parts := make([]string, 0, len(order))
	for _, w := range order {
		parts = append(parts, fmt.Sprintf("%d 个%s", cnt[w], w))
	}
	return "所有上游候选均不可用：" + strings.Join(parts, "、")
}

// dispatchFailStatus 按失败类别决定对外状态码：
//   - 全部候选都没真打到上游（熔断/无账号/降权跳过/无代理）→ 503 服务暂不可用（配 Retry-After）
//   - 存在任一真打到上游的失败（上游错误状态/连接失败/协议转换失败）→ 502 保持原语义
//
// 502 的语义是"上游给了无效响应"，用它表达"网关暂时没有可用容量"会让 CDN/SDK/客户端
// 一律判成源站故障，故做此分流。
func dispatchFailStatus(reasons []skipReason) int {
	if len(reasons) == 0 {
		return http.StatusBadGateway
	}
	for _, r := range reasons {
		switch r.Kind {
		case kindUpstreamStatus, kindDialFail, kindReqConvertFail, kindRespConvertFail:
			return http.StatusBadGateway
		}
	}
	return http.StatusServiceUnavailable
}
