// 仪表盘与用量趋势接口。归属说明：AppUsageTrend/KeyUsageTrend 分别挂在 App/APIKey 路由下，
// 但本质是用量趋势查询（usage-trend），按主要资源归入本文件。
package admin

import (
	"net/http"
	"strconv"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/ruanun/simple-ai-gateway/internal/domain/usage"
	"github.com/ruanun/simple-ai-gateway/internal/httpx"
	"github.com/ruanun/simple-ai-gateway/internal/model"
)

// ---- Dashboard（仪表盘）----
// ChannelHealth 渠道健康聚合（最近 N 天）：成功率/错误数/加权延迟，供渠道健康页。
func (h *CRUDHandler) ChannelHealth(c *gin.Context) {
	days, _ := strconv.Atoi(c.DefaultQuery("days", "7"))
	rows, err := h.usages.ChannelHealth(days)
	if err != nil {
		httpx.Internal(c, "查询渠道健康失败")
		return
	}
	if rows == nil {
		rows = []usage.ChannelHealthRow{}
	}
	c.JSON(http.StatusOK, rows)
}

func (h *CRUDHandler) Dashboard(c *gin.Context) {
	total, err := h.usages.KPI()
	if err != nil {
		httpx.Internal(c, "仪表盘数据查询失败")
		return
	}
	today, _ := h.usages.KPIByDate(time.Now().Format("2006-01-02"))
	hours := 24
	if v, _ := strconv.Atoi(c.DefaultQuery("hours", "24")); v > 0 {
		hours = v
	}
	trend, _ := h.usages.TrendHourly(hours)
	top, _ := h.usages.TopModels(10)
	apps, _ := h.apps.List()
	ops, _ := h.usages.OpsToday()
	topApps, _ := h.usages.TopApps(10)
	chansToday, _ := h.usages.ChannelsToday()
	if trend == nil {
		trend = []model.UsageHourly{}
	}
	if top == nil {
		top = []model.UsageDaily{}
	}
	if topApps == nil {
		topApps = []usage.AppUsageRow{}
	}
	if chansToday == nil {
		chansToday = []usage.ChannelTodayRow{}
	}
	c.JSON(http.StatusOK, gin.H{
		"kpi":       today, // 今日 KPI（对齐 spec §2.7）
		"kpi_total": total, // 全时累计
		"trend":     trend, // 始终为数组，避免前端 null 迭代
		"top":       top,
		"app_count": len(apps),
		"ops":       ops,        // 今日运行指标（含错误请求）
		"top_apps":  topApps,    // 应用用量排行
		"channels":  chansToday, // 渠道今日请求/错误
	})
}

// AppUsageAnalysis 应用用量分析：总量趋势 + 期间模型聚合 + 日×模型明细，单端点一次拉全。
// 天数白名单（7/14/30，非法回退 7）与 nil→[] 归一收敛在 usage.Service.AnalysisByApp。
func (h *CRUDHandler) AppUsageAnalysis(c *gin.Context) {
	id, _ := strconv.ParseUint(c.Param("id"), 10, 64)
	days, _ := strconv.Atoi(c.DefaultQuery("days", "7"))
	a, err := h.usages.AnalysisByApp(id, days)
	if err != nil {
		httpx.Internal(c, "查询失败")
		return
	}
	c.JSON(http.StatusOK, gin.H{"trend": a.Trend, "models": a.Models, "daily_models": a.DailyModels})
}

// KeyUsageAnalysis 令牌用量分析：形状同 AppUsageAnalysis，数据源 usage_daily_key。
func (h *CRUDHandler) KeyUsageAnalysis(c *gin.Context) {
	id, _ := strconv.ParseUint(c.Param("id"), 10, 64)
	days, _ := strconv.Atoi(c.DefaultQuery("days", "7"))
	a, err := h.usages.AnalysisByKey(id, days)
	if err != nil {
		httpx.Internal(c, "查询失败")
		return
	}
	c.JSON(http.StatusOK, gin.H{"trend": a.Trend, "models": a.Models, "daily_models": a.DailyModels})
}

// ChannelUsageAnalysis 渠道用量分析：形状同 AppUsageAnalysis，数据源 usage_daily_channel。
// 比应用/令牌多返回 errors 与两个平均延迟（渠道维度关注可用性与快慢）。
// 刻意不提供 portal 对应端点：渠道属内部拓扑，不对使用方暴露。
func (h *CRUDHandler) ChannelUsageAnalysis(c *gin.Context) {
	id, _ := strconv.ParseUint(c.Param("id"), 10, 64)
	days, _ := strconv.Atoi(c.DefaultQuery("days", "7"))
	a, err := h.usages.AnalysisByChannel(id, days)
	if err != nil {
		httpx.Internal(c, "查询失败")
		return
	}
	c.JSON(http.StatusOK, gin.H{"trend": a.Trend, "models": a.Models, "daily_models": a.DailyModels})
}

// AppUsageTrend 返回应用最近 N 天的日用量趋势（按日期升序）。
func (h *CRUDHandler) AppUsageTrend(c *gin.Context) {
	id, _ := strconv.ParseUint(c.Param("id"), 10, 64)
	days, _ := strconv.Atoi(c.DefaultQuery("days", "7"))
	if days <= 0 {
		days = 7
	}
	rows, err := h.usages.TrendByApp(id, days)
	if err != nil {
		httpx.Internal(c, "查询失败")
		return
	}
	if rows == nil {
		rows = []model.UsageDaily{}
	}
	c.JSON(http.StatusOK, rows)
}

// KeyUsageTrend 返回令牌最近 N 天的日用量趋势（按日期升序）。
func (h *CRUDHandler) KeyUsageTrend(c *gin.Context) {
	id, _ := strconv.ParseUint(c.Param("id"), 10, 64)
	days, _ := strconv.Atoi(c.DefaultQuery("days", "7"))
	if days <= 0 {
		days = 7
	}
	rows, err := h.usages.TrendByKey(id, days)
	if err != nil {
		httpx.Internal(c, "查询失败")
		return
	}
	if rows == nil {
		rows = []model.UsageDailyKey{}
	}
	c.JSON(http.StatusOK, rows)
}
