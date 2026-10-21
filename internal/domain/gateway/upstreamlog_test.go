// 全候选失败时把实际出站上游地址带进日志的测试。作者: ruan
package gateway

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/ruanun/simple-ai-gateway/internal/breaker"
	"github.com/ruanun/simple-ai-gateway/internal/logtest"
	"github.com/ruanun/simple-ai-gateway/internal/ratelimit"
	"github.com/ruanun/simple-ai-gateway/internal/router"
	"github.com/stretchr/testify/require"
)

// 真打到上游的失败要渲染出地址，便于线上多渠道时定位到底连的哪台。
func TestRenderUpstreamsListsAttemptedAddresses(t *testing.T) {
	got := renderUpstreams([]skipReason{
		{ChannelID: 1, Model: "m", Kind: kindUpstreamStatus, Status: 500, BaseURL: "https://a.example.com/v1", AccountID: 7},
		{ChannelID: 2, Model: "m", Kind: kindDialFail, BaseURL: "https://b.example.com", AccountID: 9},
	})
	require.Equal(t, "ch1(acc7)=https://a.example.com/v1; ch2(acc9)=https://b.example.com", got)
}

// 同一渠道账号重试多次会产生多条同址记录，日志里去重，否则三次重试刷三遍同一行。
func TestRenderUpstreamsDeduplicates(t *testing.T) {
	got := renderUpstreams([]skipReason{
		{ChannelID: 1, Kind: kindDialFail, BaseURL: "https://a.example.com", AccountID: 7},
		{ChannelID: 1, Kind: kindDialFail, BaseURL: "https://a.example.com", AccountID: 7},
		{ChannelID: 1, Kind: kindDialFail, BaseURL: "https://a.example.com", AccountID: 8},
	})
	require.Equal(t, "ch1(acc7)=https://a.example.com; ch1(acc8)=https://a.example.com", got)
}

// 压根没打到上游的原因（熔断/无账号）没有地址可言，不该渲染出空条目。
func TestRenderUpstreamsSkipsUnattempted(t *testing.T) {
	require.Equal(t, "", renderUpstreams([]skipReason{
		{ChannelID: 1, Kind: kindBreakerOpen},
		{ChannelID: 2, Kind: kindNoAccount},
	}))
	require.Equal(t, "", renderUpstreams(nil))
}

// 端到端：两个候选各自打到不同的坏上游 → 502 日志里能看到两个真实地址。
func TestDispatchAllFailLogsUpstreamURLs(t *testing.T) {
	badA := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(500) }))
	defer badA.Close()
	badB := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(500) }))
	defer badB.Close()

	log := logtest.Capture(t)
	d := Deps{
		Breaker: breaker.NewMemoryBreaker(breaker.Params{
			Window: time.Minute, Cooldown: time.Minute,
			FailThreshold: 3, ErrorRate: 0.5, MinSamples: 20, ProbeBackoffMax: 8,
		}),
		Limiter: ratelimit.NewMemoryLimiter(),
		Lookup: func(id uint64, _ string) (ChannelInfo, bool) {
			if id == 1 {
				return ChannelInfo{BaseURL: badA.URL, Timeout: 5, AccountID: 11}, true
			}
			return ChannelInfo{BaseURL: badB.URL, Timeout: 5, AccountID: 22}, true
		},
		Now: func() time.Time { return time.Unix(1_000_000, 0) },
	}
	out := Dispatch(httptest.NewRecorder(),
		[]router.Candidate{{ChannelID: 1, Model: "m"}, {ChannelID: 2, Model: "m"}},
		[]byte(`{"model":"m"}`), d)
	require.Equal(t, 502, out.Status)

	line := log.Line("Dispatch 502: 全候选失败")
	require.NotNil(t, line, "全候选失败应有 502 日志")
	up, _ := line["upstreams"].(string)
	require.Contains(t, up, badA.URL, "日志应含第一个候选实际打到的地址")
	require.Contains(t, up, badB.URL, "日志应含第二个候选实际打到的地址")
	require.Contains(t, up, "acc11")
}

// 对外摘要与对内取证渲染都不得被新字段污染：前者刻意不含内部拓扑，
// 后者是冻结格式的取证证据。
func TestUpstreamFieldsStayOutOfExistingRenderings(t *testing.T) {
	r := []skipReason{{ChannelID: 1, Model: "m", Kind: kindDialFail, Detail: "conn refused",
		BaseURL: "https://secret-internal.example.com", AccountID: 7}}
	require.NotContains(t, publicSummary(r), "secret-internal", "对外摘要不得泄漏内部地址")
	require.Equal(t, "ch1(m):连接失败(conn refused)", renderInternal(r[0]), "对内取证渲染格式必须逐字不变")
}
