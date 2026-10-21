package gateway

import (
	"net/http"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestRenderInternalMatchesLegacyFormat 对内渲染必须与结构化改造前的 fmt.Sprintf 文案逐字一致。
// 期望值逐条抄自改造前的源码（dispatchcore.go:77,82,99 / dispatch.go:182,221,244,279,281,283 /
// dispatchraw.go:86,113,115），任何一处漂移都会损坏 RequestLog.ErrorMsg 与审计取证证据。
func TestRenderInternalMatchesLegacyFormat(t *testing.T) {
	cases := []struct {
		name string
		in   skipReason
		want string
	}{
		{"熔断中", skipReason{ChannelID: 1, Model: "gpt-4o", Kind: kindBreakerOpen},
			"ch1(gpt-4o):熔断中"},
		{"无可用账号", skipReason{ChannelID: 2, Model: "gpt-4o", Kind: kindNoAccount},
			"ch2(gpt-4o):无可用账号(渠道禁用/全部熔断或配额冷却)"},
		{"半开降权跳过", skipReason{ChannelID: 3, Model: "m", Kind: kindHalfOpenSkip},
			"ch3(m):半开降权跳过"},
		{"账号无可用代理", skipReason{ChannelID: 4, Model: "m", Kind: kindNoProxy},
			"ch4(m):账号无可用代理"},
		{"请求转换失败", skipReason{ChannelID: 5, Model: "m", Kind: kindReqConvertFail, Detail: "bad ir"},
			"ch5(m):请求转换失败(bad ir)"},
		{"响应转换失败", skipReason{ChannelID: 6, Model: "m", Kind: kindRespConvertFail, Detail: "bad sse"},
			"ch6(m):响应转换失败(bad sse)"},
		{"连接失败", skipReason{ChannelID: 7, Model: "m", Kind: kindDialFail, Detail: "connection reset"},
			"ch7(m):连接失败(connection reset)"},
		{"上游状态带消息", skipReason{ChannelID: 8, Model: "m", Kind: kindUpstreamStatus, Status: 503, Detail: "overloaded"},
			"ch8(m):上游503(overloaded)"},
		{"上游状态无消息", skipReason{ChannelID: 9, Model: "m", Kind: kindUpstreamStatus, Status: 500},
			"ch9(m):上游500"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			require.Equal(t, tc.want, renderInternal(tc.in))
		})
	}
}

// TestCoreFailDetail 空列表退化为固定文案；非空时用 "; " 连接并包在方括号里（与改造前一致）。
func TestCoreFailDetail(t *testing.T) {
	require.Equal(t, "all candidates failed", coreFailDetail(nil))

	got := coreFailDetail([]skipReason{
		{ChannelID: 1, Model: "gpt-4o", Kind: kindBreakerOpen},
		{ChannelID: 2, Model: "gpt-4o", Kind: kindUpstreamStatus, Status: 503, Detail: "overloaded"},
	})
	require.Equal(t, "all candidates failed [ch1(gpt-4o):熔断中; ch2(gpt-4o):上游503(overloaded)]", got)
}

// TestPublicSummaryAggregates 对外摘要按「对外措辞」聚合计数，顺序稳定（按首次出现），
// 且请求/响应转换失败共享同一措辞时必须合并成一条计数而不是重复两条。
func TestPublicSummaryAggregates(t *testing.T) {
	require.Equal(t, "所有上游候选均不可用", publicSummary(nil))

	got := publicSummary([]skipReason{
		{ChannelID: 1, Model: "gpt-4o", Kind: kindBreakerOpen},
		{ChannelID: 2, Model: "gpt-4o", Kind: kindBreakerOpen},
		{ChannelID: 3, Model: "gpt-4o", Kind: kindDialFail, Detail: "connection reset"},
	})
	require.Equal(t, "所有上游候选均不可用：2 个熔断中、1 个连接失败", got)

	merged := publicSummary([]skipReason{
		{ChannelID: 1, Model: "m", Kind: kindReqConvertFail, Detail: "a"},
		{ChannelID: 2, Model: "m", Kind: kindRespConvertFail, Detail: "b"},
	})
	require.Equal(t, "所有上游候选均不可用：2 个协议转换失败", merged, "同措辞的两种 Kind 必须合并计数")
}

// TestPublicSummaryLeaksNothing 对外摘要禁止泄露内部拓扑：渠道 ID、模型名、上游原文一律不得出现。
func TestPublicSummaryLeaksNothing(t *testing.T) {
	got := publicSummary([]skipReason{
		{ChannelID: 42, Model: "secret-model", Kind: kindUpstreamStatus, Status: 503, Detail: "http://10.0.0.5:8080 refused"},
		{ChannelID: 43, Model: "secret-model", Kind: kindDialFail, Detail: "proxy 192.168.1.1:1080 unreachable"},
	})
	for _, leak := range []string{"42", "43", "secret-model", "10.0.0.5", "192.168.1.1", "refused", "unreachable"} {
		require.NotContains(t, got, leak, "对外摘要泄露了内部信息: "+leak)
	}
}

// TestDispatchFailStatus 全部「没真打到上游」→503；混入任一真打到上游的失败→502；空列表兜底 502。
func TestDispatchFailStatus(t *testing.T) {
	require.Equal(t, http.StatusBadGateway, dispatchFailStatus(nil), "空列表兜底 502")

	notReached := []skipReason{
		{Kind: kindBreakerOpen}, {Kind: kindNoAccount}, {Kind: kindHalfOpenSkip}, {Kind: kindNoProxy},
	}
	require.Equal(t, http.StatusServiceUnavailable, dispatchFailStatus(notReached))

	for _, k := range []skipKind{kindUpstreamStatus, kindDialFail, kindReqConvertFail, kindRespConvertFail} {
		mixed := append([]skipReason{{Kind: kindBreakerOpen}}, skipReason{Kind: k})
		require.Equal(t, http.StatusBadGateway, dispatchFailStatus(mixed), "混入真打到上游的失败应为 502")
	}
}
