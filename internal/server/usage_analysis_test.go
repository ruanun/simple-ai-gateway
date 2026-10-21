package server

import (
	"net/http"
	"testing"
	"time"

	"github.com/ruanun/simple-ai-gateway/internal/model"
	"github.com/stretchr/testify/require"
)

// TestAdminUsageAnalysis 管理端应用/令牌用量分析端点：三块数组齐全，非法 days 回退 7。
func TestAdminUsageAnalysis(t *testing.T) {
	r, g, admin := setupPortalTest(t)
	// 兼容 -count=N 重跑：清空聚合表
	g.Where("1 = 1").Delete(&model.UsageDaily{})
	g.Where("1 = 1").Delete(&model.UsageDailyKey{})
	today := time.Now().Format("2006-01-02")
	require.NoError(t, g.Create(&model.UsageDaily{StatDate: today, AppID: 42, Model: "gpt-4o", Requests: 3, PromptTokens: 300, CompletionTokens: 30, CostMicros: 7000}).Error)
	require.NoError(t, g.Create(&model.UsageDailyKey{StatDate: today, APIKeyID: 9, Model: "gpt-4o", Requests: 2, CostMicros: 500}).Error)

	resp := doJSON(t, r, http.MethodGet, "/admin/apps/42/usage-analysis?days=7", admin, "", http.StatusOK)
	require.Len(t, resp["trend"], 1)
	require.Len(t, resp["models"], 1)
	require.Len(t, resp["daily_models"], 1)

	// 非法 days 回退 7，不报错
	resp = doJSON(t, r, http.MethodGet, "/admin/api-keys/9/usage-analysis?days=999", admin, "", http.StatusOK)
	require.Len(t, resp["models"], 1)

	// 无数据 id：三块均为空数组而非 null
	resp = doJSON(t, r, http.MethodGet, "/admin/apps/424242/usage-analysis", admin, "", http.StatusOK)
	require.NotNil(t, resp["trend"])
	require.Len(t, resp["trend"], 0)
}

// TestDashboardExtendedFields 仪表盘扩展字段存在性。
func TestDashboardExtendedFields(t *testing.T) {
	r, _, admin := setupPortalTest(t)
	resp := doJSON(t, r, http.MethodGet, "/admin/dashboard", admin, "", http.StatusOK)
	require.Contains(t, resp, "ops")
	require.Contains(t, resp, "top_apps")
	require.Contains(t, resp, "channels")
}
