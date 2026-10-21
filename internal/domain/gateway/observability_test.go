package gateway

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/ruanun/simple-ai-gateway/internal/billing"
	"github.com/ruanun/simple-ai-gateway/internal/dbtest"
	"github.com/ruanun/simple-ai-gateway/internal/domain/requestlog"
	"github.com/ruanun/simple-ai-gateway/internal/logger"
	"github.com/ruanun/simple-ai-gateway/internal/model"
	"github.com/stretchr/testify/require"
)

// TestLogIfAllFailedStructured 全候选失败(502)日志走 slog error 级，
// 带 candidates/request_id 结构化字段（与 ctx 注入的字段名一致，便于采集端按同一字段检索）。
// 该逻辑已收敛进 dispatchCore 骨架（coreLogIfAllFailed，dispatchcore.go），此处改调用新入口，断言不变。
func TestLogIfAllFailedStructured(t *testing.T) {
	prev := slog.Default()
	t.Cleanup(func() { slog.SetDefault(prev) })
	var buf bytes.Buffer
	logger.InitWriter(&buf, "info", "json")

	h := dispatchHooks[Outcome]{
		kind:     "Dispatch",
		statusOf: func(o Outcome) int { return o.Status },
		errOf:    func(o Outcome) string { return o.Err },
		skipsOf:  func(o Outcome) []skipReason { return o.Skips },
	}
	coreLogIfAllFailed(h, 3, Outcome{Status: 502, Err: "boom"}, "req-1")

	var m map[string]any
	require.NoError(t, json.Unmarshal(buf.Bytes(), &m))
	require.Equal(t, "ERROR", m["level"])
	require.Equal(t, float64(3), m["candidates"])
	require.Equal(t, "req-1", m["request_id"])
	require.Equal(t, "boom", m["err"])
}

// TestFinishRecordsCacheDetail 验证 BillingCtx.finish 落库时带上缓存读/写与推理 token 明细（P12e Task2）。
func TestFinishRecordsCacheDetail(t *testing.T) {
	gin.SetMode(gin.TestMode)
	g := dbtest.OpenInMemory(t)
	require.NoError(t, g.AutoMigrate(&model.RequestLog{}))
	b := &BillingCtx{Logs: requestlog.NewService(requestlog.NewRepo(g))}

	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest("POST", "/v1/messages", nil)
	u := billing.Usage{PromptTokens: 1000, CompletionTokens: 200, CachedTokens: 700, CacheCreationTokens: 100, ReasoningTokens: 50, TotalTokens: 1200}
	// appID=0、cost=0：跳过 finish 内的 Apps.Deduct（此处 BillingCtx 未装配 Apps），只验证落库三列。
	b.finish(c, 0, 2, 3, "claude-3", "messages", "", u, true, 0, 10, 0, false, 200)

	var rl model.RequestLog
	require.NoError(t, g.First(&rl).Error)
	require.Equal(t, 700, rl.CachedTokens)
	require.Equal(t, 100, rl.CacheCreationTokens)
	require.Equal(t, 50, rl.ReasoningTokens)
}
