package gateway

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/ruanun/simple-ai-gateway/internal/breaker"
	"github.com/ruanun/simple-ai-gateway/internal/dbtest"
	"github.com/ruanun/simple-ai-gateway/internal/domain/price"
	"github.com/ruanun/simple-ai-gateway/internal/domain/requestlog"
	"github.com/ruanun/simple-ai-gateway/internal/middleware"
	"github.com/ruanun/simple-ai-gateway/internal/model"
	"github.com/stretchr/testify/require"
)

// TestRetryAfterSecondsFromBreaker Retry-After 取被熔断候选 OpenUntil 的最小剩余秒数，
// 向上取整、封顶 300s；无 open 项或已过期返回 0（调用方据此不发该头）。
func TestRetryAfterSecondsFromBreaker(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	brk := breaker.NewMemoryBreaker(breaker.Params{
		Window: 30 * time.Second, Cooldown: 45 * time.Second,
		FailThreshold: 1, ErrorRate: 0.5, MinSamples: 20, ProbeBackoffMax: 8,
	})
	// 打开渠道 1 的熔断（FailThreshold=1，一次失败即开）
	brk.RecordResult(1, now, false, false)

	skips := []skipReason{{ChannelID: 1, Model: "m", Kind: kindBreakerOpen}}
	got := retryAfterSeconds(brk, skips, now)
	require.Greater(t, got, 0, "熔断中的渠道应给出正的 Retry-After")
	require.LessOrEqual(t, got, 45, "不应超过 cooldown")

	// 非熔断类原因不查 breaker → 0
	require.Equal(t, 0, retryAfterSeconds(brk, []skipReason{{ChannelID: 1, Kind: kindDialFail}}, now))
	// nil breaker 安全
	require.Equal(t, 0, retryAfterSeconds(nil, skips, now))
	// 空 skips 安全
	require.Equal(t, 0, retryAfterSeconds(brk, nil, now))
}

// TestRespondDispatchFailure503WithRetryAfter 全候选都没打到上游 → 503 + Retry-After + 脱敏摘要 + request_id。
func TestRespondDispatchFailure503WithRetryAfter(t *testing.T) {
	gin.SetMode(gin.TestMode)
	now := time.Unix(1_700_000_000, 0)
	brk := breaker.NewMemoryBreaker(breaker.Params{
		Window: 30 * time.Second, Cooldown: 60 * time.Second,
		FailThreshold: 1, ErrorRate: 0.5, MinSamples: 20, ProbeBackoffMax: 8,
	})
	brk.RecordResult(7, now, false, false)

	b := &BillingCtx{Deps: Deps{Breaker: brk, Now: func() time.Time { return now }}, Now: func() time.Time { return now }}
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest("POST", "/v1/chat/completions", nil)
	c.Set(middleware.CtxRequestID, "req-503")

	skips := []skipReason{
		{ChannelID: 7, Model: "gpt-4o", Kind: kindBreakerOpen},
		{ChannelID: 8, Model: "gpt-4o", Kind: kindNoAccount},
	}
	b.respondDispatchFailure(c, "openai", skips, http.StatusBadGateway, "all candidates failed")

	require.Equal(t, http.StatusServiceUnavailable, w.Code, "全部没打到上游应为 503")
	require.NotEmpty(t, w.Header().Get("Retry-After"), "熔断中应给出 Retry-After")

	var got struct {
		Error struct {
			Message   string `json:"message"`
			Code      string `json:"code"`
			RequestID string `json:"request_id"`
		} `json:"error"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &got))
	require.Equal(t, "all_candidates_unavailable", got.Error.Code)
	require.Equal(t, "req-503", got.Error.RequestID)
	require.Contains(t, got.Error.Message, "熔断中")
	require.NotContains(t, got.Error.Message, "gpt-4o", "对外文案不得含模型名")
	require.NotContains(t, got.Error.Message, "ch7", "对外文案不得含渠道 ID")
}

// TestRespondDispatchFailure502OnUpstreamReached 混入真打到上游的失败 → 保持 502、不发 Retry-After。
func TestRespondDispatchFailure502OnUpstreamReached(t *testing.T) {
	gin.SetMode(gin.TestMode)
	b := &BillingCtx{Deps: Deps{Now: time.Now}, Now: time.Now}
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest("POST", "/v1/chat/completions", nil)
	c.Set(middleware.CtxRequestID, "req-502")

	skips := []skipReason{
		{ChannelID: 1, Model: "m", Kind: kindBreakerOpen},
		{ChannelID: 2, Model: "m", Kind: kindUpstreamStatus, Status: 503, Detail: "overloaded"},
	}
	b.respondDispatchFailure(c, "openai", skips, http.StatusBadGateway, "all candidates failed")

	require.Equal(t, http.StatusBadGateway, w.Code)
	require.Empty(t, w.Header().Get("Retry-After"), "非纯熔断场景不发 Retry-After")

	var got struct {
		Error struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &got))
	require.Equal(t, "all_candidates_failed", got.Error.Code)
	require.NotContains(t, got.Error.Message, "overloaded", "对外文案不得含上游错误原文")
}

// TestSettleDispatchWiresSkipsThrough 接线测试：settleDispatch 必须把 out.Skips 传进
// respondDispatchFailure，且把落库状态码改写成分流结果。
//
// 为什么单独测这个：respondDispatchFailure 自身的单测再全，也测不出「调用方忘了传 Skips」——
// 漏接时所有单测照样绿，线上却完全不生效（同 CaptureBody 那类静默漏接的坑）。
// 这里同时断言响应状态码与落库状态码，两者必须都是 503。
func TestSettleDispatchWiresSkipsThrough(t *testing.T) {
	gin.SetMode(gin.TestMode)
	now := time.Unix(1_700_000_000, 0)
	g := dbtest.OpenInMemory(t)
	require.NoError(t, g.AutoMigrate(&model.RequestLog{}))

	brk := breaker.NewMemoryBreaker(breaker.Params{
		Window: 30 * time.Second, Cooldown: 60 * time.Second,
		FailThreshold: 1, ErrorRate: 0.5, MinSamples: 20, ProbeBackoffMax: 8,
	})
	brk.RecordResult(7, now, false, false) // 打开渠道 7 熔断

	logs := requestlog.NewService(requestlog.NewRepo(g))
	b := &BillingCtx{
		Deps:   Deps{Breaker: brk, Now: func() time.Time { return now }},
		Logs:   logs,
		Prices: price.NewService(price.NewRepo(g)),
		Now:    func() time.Time { return now },
	}

	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest("POST", "/v1/chat/completions", nil)
	c.Set(middleware.CtxRequestID, "req-wire")

	// Dispatch 返回的原始状态码是 502；Skips 全为「没打到上游」→ 应被分流成 503。
	out := Outcome{
		Status: http.StatusBadGateway,
		Err:    "all candidates failed [ch7(gpt-4o):熔断中]",
		Skips:  []skipReason{{ChannelID: 7, Model: "gpt-4o", Kind: kindBreakerOpen}},
	}
	b.settleDispatch(c, out, prepared{Model: "gpt-4o"}, []byte(`{"model":"gpt-4o"}`), "", "openai", "chat", "", false)

	require.Equal(t, http.StatusServiceUnavailable, w.Code, "settleDispatch 未把 Skips 传下去（回写状态码仍是 502）")
	require.NotEmpty(t, w.Header().Get("Retry-After"), "熔断场景应带 Retry-After")

	var body struct {
		Error struct{ Code string } `json:"error"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &body))
	require.Equal(t, "all_candidates_unavailable", body.Error.Code)

	// 落库状态码必须与实际回给客户端的一致，否则请求日志与调用方所见对不上。
	var logged model.RequestLog
	require.NoError(t, g.Where("request_id = ?", "req-wire").First(&logged).Error)
	require.Equal(t, http.StatusServiceUnavailable, logged.Status, "落库状态码未随分流改写")
	require.Contains(t, logged.ErrorMsg, "ch7(gpt-4o):熔断中", "对内取证文本必须完整落库")
}

// TestRespondDispatchFailureFallsBackWhenNoSkips skips 为空（如渠道限流 429）时沿用旧路径：
// 用传入的 fallbackStatus 与 fallbackDetail，不改状态码、不发 Retry-After。
func TestRespondDispatchFailureFallsBackWhenNoSkips(t *testing.T) {
	gin.SetMode(gin.TestMode)
	b := &BillingCtx{Deps: Deps{Now: time.Now}, Now: time.Now}
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest("POST", "/v1/chat/completions", nil)

	b.respondDispatchFailure(c, "openai", nil, http.StatusTooManyRequests, "channel rate limited")

	require.Equal(t, http.StatusTooManyRequests, w.Code)
	var got struct {
		Error struct{ Message string } `json:"error"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &got))
	require.Equal(t, "channel rate limited", got.Error.Message, "非 5xx 短文案原样返回")
}

// TestRespondDispatchFailureErrorPassthrough 渠道开启 error_passthrough 时：
// 用最后一个上游错误的状态码 + 已脱敏详情回客户端，不再返回公共摘要。
func TestRespondDispatchFailureErrorPassthrough(t *testing.T) {
	gin.SetMode(gin.TestMode)
	b := &BillingCtx{Deps: Deps{Now: time.Now}, Now: time.Now}
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest("POST", "/v1/chat/completions", nil)

	skips := []skipReason{
		{ChannelID: 1, Model: "m", Kind: kindUpstreamStatus, Status: 503, Detail: "first"},
		{ChannelID: 2, Model: "m", Kind: kindUpstreamStatus, Status: 429, Detail: "upstream rate limited", ErrorPassthrough: true},
	}
	b.respondDispatchFailure(c, "openai", skips, http.StatusBadGateway, "all candidates failed")
	require.Equal(t, http.StatusTooManyRequests, w.Code)

	var got struct {
		Error struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &got))
	require.Equal(t, "upstream_error", got.Error.Code)
	require.Equal(t, "upstream rate limited", got.Error.Message)
}

func TestScrubDetailOnlyWhenPassthrough(t *testing.T) {
	info := ChannelInfo{UpstreamKey: "up-key-123", ErrorPassthrough: true}
	require.Equal(t, "bad [REDACTED]", scrubDetail(info, "bad up-key-123"))
	info.ErrorPassthrough = false
	require.Equal(t, "bad up-key-123", scrubDetail(info, "bad up-key-123"), "未开启透传的详情只进日志/审计，不改写")
}
