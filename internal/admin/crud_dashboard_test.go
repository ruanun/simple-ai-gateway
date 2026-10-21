package admin

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/ruanun/simple-ai-gateway/internal/dbtest"
	"github.com/ruanun/simple-ai-gateway/internal/domain/usage"
	"github.com/ruanun/simple-ai-gateway/internal/model"
	"github.com/stretchr/testify/require"
)

// TestChannelUsageAnalysisEndpoint 端点返回 trend/models/daily_models 三块，
// 且带上渠道特有的 errors 与平均延迟。
func TestChannelUsageAnalysisEndpoint(t *testing.T) {
	gin.SetMode(gin.TestMode)
	g := dbtest.OpenInMemory(t)
	require.NoError(t, g.AutoMigrate(&model.UsageDailyChannel{}))
	today := time.Now().Format("2006-01-02")
	require.NoError(t, g.Create(&model.UsageDailyChannel{
		StatDate: today, ChannelID: 7, Model: "gpt-4o",
		Requests: 5, Errors: 1, CostMicros: 2500, AvgLatencyMs: 900,
	}).Error)

	h := &CRUDHandler{usages: usage.NewService(usage.NewRepo(g))}
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest("GET", "/admin/channels/7/usage-analysis?days=7", nil)
	c.Params = gin.Params{{Key: "id", Value: "7"}}

	h.ChannelUsageAnalysis(c)

	require.Equal(t, http.StatusOK, w.Code)
	var got struct {
		Trend []struct {
			StatDate     string `json:"stat_date"`
			Requests     int64  `json:"requests"`
			Errors       int64  `json:"errors"`
			AvgLatencyMs int64  `json:"avg_latency_ms"`
		} `json:"trend"`
		Models      []map[string]any `json:"models"`
		DailyModels []map[string]any `json:"daily_models"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &got))
	require.Len(t, got.Trend, 1)
	require.Equal(t, int64(5), got.Trend[0].Requests)
	require.Equal(t, int64(1), got.Trend[0].Errors)
	require.Equal(t, int64(900), got.Trend[0].AvgLatencyMs)
	require.NotNil(t, got.Models)
	require.NotNil(t, got.DailyModels)
}

// TestChannelUsageAnalysisEmptyIsArray 无数据时三块必须是 []，不能是 null
// （前端直接 .map，null 会抛异常）。
func TestChannelUsageAnalysisEmptyIsArray(t *testing.T) {
	gin.SetMode(gin.TestMode)
	g := dbtest.OpenInMemory(t)
	require.NoError(t, g.AutoMigrate(&model.UsageDailyChannel{}))

	h := &CRUDHandler{usages: usage.NewService(usage.NewRepo(g))}
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest("GET", "/admin/channels/99/usage-analysis", nil)
	c.Params = gin.Params{{Key: "id", Value: "99"}}

	h.ChannelUsageAnalysis(c)

	require.Equal(t, http.StatusOK, w.Code)
	require.Contains(t, w.Body.String(), `"trend":[]`)
	require.Contains(t, w.Body.String(), `"models":[]`)
	require.Contains(t, w.Body.String(), `"daily_models":[]`)
}
