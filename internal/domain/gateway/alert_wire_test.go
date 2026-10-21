package gateway

import (
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/ruanun/simple-ai-gateway/internal/billing"
	"github.com/ruanun/simple-ai-gateway/internal/dbtest"
	"github.com/ruanun/simple-ai-gateway/internal/domain/apikey"
	"github.com/ruanun/simple-ai-gateway/internal/domain/app"
	"github.com/ruanun/simple-ai-gateway/internal/domain/requestlog"
	"github.com/ruanun/simple-ai-gateway/internal/model"
	"github.com/stretchr/testify/require"
)

type fakeNotifier struct {
	mu     sync.Mutex
	events []string
}

func (f *fakeNotifier) Alert(event, title, message string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.events = append(f.events, event)
}
func (f *fakeNotifier) has(prefix string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, e := range f.events {
		if len(e) >= len(prefix) && e[:len(prefix)] == prefix {
			return true
		}
	}
	return false
}

func TestAlertAllFailedFiresOn502(t *testing.T) {
	fn := &fakeNotifier{}
	b := &BillingCtx{Notifier: fn}
	b.alertAllFailed(502, "gpt-4o", "req1", "all candidates failed")
	require.True(t, fn.has("all_candidates_failed:gpt-4o"))
}

// TestAlertAllFailedFiresOn503 全候选失败改回 503（熔断/冷却类）后，告警不得静默失效。
// 这是状态码分流最容易漏的连带改动：alertAllFailed 原本只认 502/504。
func TestAlertAllFailedFiresOn503(t *testing.T) {
	fn := &fakeNotifier{}
	b := &BillingCtx{Notifier: fn}
	b.alertAllFailed(503, "gpt-4o", "req503", "all candidates failed")
	require.True(t, fn.has("all_candidates_failed:gpt-4o"), "503 全候选失败必须告警")
}

func TestAlertAllFailedFiresOn504(t *testing.T) {
	fn := &fakeNotifier{}
	b := &BillingCtx{Notifier: fn}
	b.alertAllFailed(504, "m", "r", "wall-clock")
	require.True(t, fn.has("all_candidates_failed:m"))
}

func TestAlertAllFailedSilentOnSuccess(t *testing.T) {
	fn := &fakeNotifier{}
	b := &BillingCtx{Notifier: fn}
	b.alertAllFailed(200, "m", "r", "")
	require.False(t, fn.has("all_candidates_failed"))
}

func TestAlertNilNotifierSafe(t *testing.T) {
	b := &BillingCtx{} // Notifier nil
	require.NotPanics(t, func() {
		b.alertAllFailed(502, "m", "r", "")
		b.alertDeductFail("app:1", "x")
	})
}

func TestAlertDeductFail(t *testing.T) {
	fn := &fakeNotifier{}
	b := &BillingCtx{Notifier: fn}
	b.alertDeductFail("app:7", "App 配额扣减失败")
	require.True(t, fn.has("deduct_failed:app:7"))
}

// finish()（responses/embeddings/images 走此路径）在 Apps.Deduct 失败时应触发扣费告警，
// 与 chat/messages 的 settleDispatch 路径对称。故意不迁移 apps 表使 Deduct 报错。
func TestFinishAlertsOnDeductFailure(t *testing.T) {
	gin.SetMode(gin.TestMode)
	g := dbtest.OpenInMemory(t)
	require.NoError(t, g.AutoMigrate(&model.RequestLog{})) // 迁移日志表使落库成功；apps 表故意缺失
	fn := &fakeNotifier{}
	b := &BillingCtx{
		Apps:     app.NewService(app.NewRepo(g)),
		Logs:     requestlog.NewService(requestlog.NewRepo(g)),
		Notifier: fn,
	}
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest("POST", "/v1/responses", nil)
	u := billing.Usage{PromptTokens: 10, CompletionTokens: 5, TotalTokens: 15}
	// appID=1、cost>0 且 apps 表缺失 → Deduct 失败应告警；status=200 使 alertAllFailed 不触发。
	b.finish(c, 1, 2, 3, "m", "responses", "", u, true, 100, 10, 0, false, 200)
	require.True(t, fn.has("deduct_failed:app:1"), "Apps.Deduct 失败应触发扣费告警")
}

// applyKeyDailyCost()（responses/embeddings/images 走此路径）在 ApplyDailyCost 失败时应触发扣费告警。
// 故意不迁移 api_keys 表使累加报错。
func TestApplyKeyDailyCostAlertsOnFailure(t *testing.T) {
	gin.SetMode(gin.TestMode)
	g := dbtest.OpenInMemory(t) // 空库：api_keys 表缺失 → ApplyDailyCost 报错
	fn := &fakeNotifier{}
	b := &BillingCtx{Keys: apikey.NewService(apikey.NewRepo(g), ""), Notifier: fn}
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest("POST", "/v1/embeddings", nil)
	// kid 改为显式传参（不再从 ctx 取），与 settleDispatch/raw 端点的新契约一致。
	b.applyKeyDailyCost(c, 7, 100)
	require.True(t, fn.has("deduct_failed:key:7"), "ApplyDailyCost 失败应触发扣费告警")
}
