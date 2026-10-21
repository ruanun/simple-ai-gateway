package usage

import (
	"testing"
	"time"

	"github.com/ruanun/simple-ai-gateway/internal/dbtest"
	"github.com/ruanun/simple-ai-gateway/internal/model"
	"github.com/stretchr/testify/require"
)

func TestKPIByDate(t *testing.T) {
	g := dbtest.OpenInMemory(t)
	require.NoError(t, g.AutoMigrate(&model.UsageDaily{}, &model.UsageHourly{}))
	svc := NewService(NewRepo(g))

	require.NoError(t, g.Create(&model.UsageDaily{StatDate: "2026-06-20", AppID: 1, Model: "gpt-4o",
		Requests: 1, PromptTokens: 100, CompletionTokens: 50, CostMicros: 1000}).Error)
	require.NoError(t, g.Create(&model.UsageDaily{StatDate: "2026-06-19", AppID: 1, Model: "gpt-4o",
		Requests: 1, PromptTokens: 30, CompletionTokens: 10, CostMicros: 300}).Error)

	today, err := svc.KPIByDate("2026-06-20")
	require.NoError(t, err)
	require.Equal(t, int64(1), today.Requests) // 只算当天
	require.Equal(t, int64(100), today.PromptTokens)

	total, err := svc.KPI()
	require.NoError(t, err)
	require.Equal(t, int64(2), total.Requests) // 全时含两天
}

func TestTrendAndTop(t *testing.T) {
	g := dbtest.OpenInMemory(t)
	require.NoError(t, g.AutoMigrate(&model.UsageDaily{}, &model.UsageHourly{}))
	svc := NewService(NewRepo(g))

	// 日聚合：gpt-4o 两次并入（对应旧版两次 Aggregate 同键累加），gpt-4o-mini 一次。
	require.NoError(t, g.Create(&model.UsageDaily{StatDate: "2026-06-20", AppID: 1, Model: "gpt-4o",
		Requests: 2, PromptTokens: 120, CompletionTokens: 60, CostMicros: 1200}).Error)
	require.NoError(t, g.Create(&model.UsageDaily{StatDate: "2026-06-20", AppID: 1, Model: "gpt-4o-mini",
		Requests: 1, PromptTokens: 10, CompletionTokens: 5, CostMicros: 10}).Error)
	// 时聚合：两个不同小时。
	require.NoError(t, g.Create(&model.UsageHourly{StatHour: "2026-06-20 10", AppID: 1, Model: "gpt-4o",
		Requests: 1, PromptTokens: 100, CompletionTokens: 50, CostMicros: 1000}).Error)
	require.NoError(t, g.Create(&model.UsageHourly{StatHour: "2026-06-20 11", AppID: 1, Model: "gpt-4o-mini",
		Requests: 1, PromptTokens: 10, CompletionTokens: 5, CostMicros: 10}).Error)
	require.NoError(t, g.Create(&model.UsageHourly{StatHour: "2026-06-20 11", AppID: 1, Model: "gpt-4o",
		Requests: 1, PromptTokens: 20, CompletionTokens: 10, CostMicros: 200}).Error)

	trend, err := svc.TrendHourly(24)
	require.NoError(t, err)
	require.Len(t, trend, 2) // 两个不同小时

	top, err := svc.TopModels(10)
	require.NoError(t, err)
	require.Len(t, top, 2)
	require.Equal(t, "gpt-4o", top[0].Model) // 请求数最多在前（2 次）
	require.Equal(t, int64(2), top[0].Requests)
}

func TestKPIIncludesCacheDetail(t *testing.T) {
	g := dbtest.OpenInMemory(t)
	require.NoError(t, g.AutoMigrate(&model.UsageDaily{}))
	require.NoError(t, g.Create(&model.UsageDaily{StatDate: "2026-07-10", AppID: 1, Model: "m", Requests: 1,
		PromptTokens: 1000, CompletionTokens: 200, CachedTokens: 700, CacheCreationTokens: 100, ReasoningTokens: 50, CostMicros: 5}).Error)
	s := NewService(NewRepo(g))
	k, err := s.KPI()
	require.NoError(t, err)
	require.Equal(t, int64(700), k.CachedTokens)
	require.Equal(t, int64(100), k.CacheCreationTokens)
	require.Equal(t, int64(50), k.ReasoningTokens)
}

// TestUsageAnalysisQueries 覆盖 app/key 两个维度的模型聚合与日×模型明细查询。
func TestUsageAnalysisQueries(t *testing.T) {
	g := dbtest.OpenInMemory(t)
	require.NoError(t, g.AutoMigrate(&model.UsageDaily{}, &model.UsageDailyKey{}))
	svc := NewService(NewRepo(g))
	today := time.Now().Format("2006-01-02")
	yest := time.Now().AddDate(0, 0, -1).Format("2006-01-02")
	seed := []model.UsageDaily{
		{StatDate: yest, AppID: 1, Model: "gpt-4o", Requests: 2, PromptTokens: 100, CompletionTokens: 20, CostMicros: 3000},
		{StatDate: today, AppID: 1, Model: "gpt-4o", Requests: 1, PromptTokens: 50, CompletionTokens: 10, CostMicros: 1500},
		{StatDate: today, AppID: 1, Model: "claude-3", Requests: 4, PromptTokens: 400, CompletionTokens: 40, CostMicros: 9000},
		{StatDate: today, AppID: 2, Model: "gpt-4o", Requests: 9, PromptTokens: 900, CompletionTokens: 90, CostMicros: 99000},
	}
	for i := range seed {
		require.NoError(t, g.Create(&seed[i]).Error)
	}

	ms, err := svc.ModelsByApp(1, 7)
	require.NoError(t, err)
	require.Len(t, ms, 2)
	require.Equal(t, "claude-3", ms[0].Model)       // 费用降序
	require.Equal(t, int64(4500), ms[1].CostMicros) // gpt-4o 两日合并，且不含 app 2

	dm, err := svc.DailyModelsByApp(1, 7)
	require.NoError(t, err)
	require.Len(t, dm, 3)
	require.Equal(t, yest, dm[0].StatDate) // 日期升序

	kseed := []model.UsageDailyKey{
		{StatDate: today, APIKeyID: 7, Model: "gpt-4o", Requests: 1, CostMicros: 100},
		{StatDate: today, APIKeyID: 7, Model: "claude-3", Requests: 2, CostMicros: 900},
		{StatDate: today, APIKeyID: 8, Model: "gpt-4o", Requests: 5, CostMicros: 50},
	}
	for i := range kseed {
		require.NoError(t, g.Create(&kseed[i]).Error)
	}
	kt, err := svc.TrendByKey(7, 7)
	require.NoError(t, err)
	require.Len(t, kt, 1) // 按日分组后同日多模型只出一行
	require.Equal(t, int64(3), kt[0].Requests)

	km, err := svc.ModelsByKey(7, 7)
	require.NoError(t, err)
	require.Len(t, km, 2)
	require.Equal(t, "claude-3", km[0].Model)

	kd, err := svc.DailyModelsByKey(7, 7)
	require.NoError(t, err)
	require.Len(t, kd, 2)
}

// TestDashboardOpsQueries 覆盖今日运行指标、渠道今日聚合与应用排行。
func TestDashboardOpsQueries(t *testing.T) {
	g := dbtest.OpenInMemory(t)
	require.NoError(t, g.AutoMigrate(&model.RequestLog{}, &model.UsageDaily{}, &model.Channel{}, &model.App{}))
	svc := NewService(NewRepo(g))
	require.NoError(t, g.Create(&model.Channel{Name: "ch-a", Type: "openai"}).Error) // id=1
	require.NoError(t, g.Create(&model.App{Name: "app-a"}).Error)                    // id=1
	logs := []model.RequestLog{
		{AppID: 1, ChannelID: 1, Model: "gpt-4o", Status: 200, LatencyMs: 100, FirstTokenMs: 30},
		{AppID: 1, ChannelID: 1, Model: "gpt-4o", Status: 502, LatencyMs: 300},
	}
	for i := range logs {
		require.NoError(t, g.Create(&logs[i]).Error)
	}

	ops, err := svc.OpsToday()
	require.NoError(t, err)
	require.Equal(t, int64(2), ops.Requests)
	require.Equal(t, int64(1), ops.Errors)
	require.Equal(t, int64(200), ops.AvgLatencyMs)
	require.Equal(t, int64(30), ops.AvgFirstTokenMs) // 仅统计 first_token_ms > 0 的请求

	chs, err := svc.ChannelsToday()
	require.NoError(t, err)
	require.Len(t, chs, 1)
	require.Equal(t, "ch-a", chs[0].Name)
	require.Equal(t, int64(2), chs[0].Requests)
	require.Equal(t, int64(1), chs[0].Errors)

	require.NoError(t, g.Create(&model.UsageDaily{StatDate: time.Now().Format("2006-01-02"), AppID: 1, Model: "gpt-4o", Requests: 3, CostMicros: 100}).Error)
	apps, err := svc.TopApps(10)
	require.NoError(t, err)
	require.Len(t, apps, 1)
	require.Equal(t, "app-a", apps[0].Name)
	require.Equal(t, int64(3), apps[0].Requests)
}

// TestModelsByChannelWeightedAvg 跨天汇总时平均延迟必须按请求数加权，不能取 AVG(avg_latency_ms)。
// 构造 A 天 1 条 1000ms、B 天 999 条 10ms：加权应约 11ms，朴素算术平均是 505ms。
// 实现若写成 AVG(avg_latency_ms)，本用例必然失败。
func TestModelsByChannelWeightedAvg(t *testing.T) {
	g := dbtest.OpenInMemory(t)
	require.NoError(t, g.AutoMigrate(&model.UsageDailyChannel{}))
	today := time.Now().Format("2006-01-02")
	yesterday := time.Now().AddDate(0, 0, -1).Format("2006-01-02")

	require.NoError(t, g.Create(&model.UsageDailyChannel{StatDate: yesterday, ChannelID: 5, Model: "m", Requests: 1, AvgLatencyMs: 1000}).Error)
	require.NoError(t, g.Create(&model.UsageDailyChannel{StatDate: today, ChannelID: 5, Model: "m", Requests: 999, AvgLatencyMs: 10}).Error)

	s := NewService(NewRepo(g))
	rows, err := s.ModelsByChannel(5, 7)
	require.NoError(t, err)
	require.Len(t, rows, 1)
	require.Equal(t, int64(1000), rows[0].Requests)
	// (1*1000 + 999*10) / 1000 = 10.99 → 四舍五入 11。
	// ⚠️ 用 Equal 而非 InDelta：原先 InDelta(11, ±1) 会容忍 10，掩盖了 SQLite/PG
	// 整数除法静默截断的问题（sumColsChannel 靠 `* 1.0` 强制浮点才得到 10.99）。
	require.Equal(t, int64(11), rows[0].AvgLatencyMs, "必须按请求数加权且不得截断，朴素平均会得到 505")
}

// TestTrendByChannelZeroRequestsNoDivByZero 全部 requests=0 时加权平均输出 0，不得报除零错。
func TestTrendByChannelZeroRequestsNoDivByZero(t *testing.T) {
	g := dbtest.OpenInMemory(t)
	require.NoError(t, g.AutoMigrate(&model.UsageDailyChannel{}))
	today := time.Now().Format("2006-01-02")
	require.NoError(t, g.Create(&model.UsageDailyChannel{StatDate: today, ChannelID: 6, Model: "m", Requests: 0, Errors: 3, AvgLatencyMs: 0}).Error)

	s := NewService(NewRepo(g))
	rows, err := s.TrendByChannel(6, 7)
	require.NoError(t, err, "requests 全为 0 不得触发除零错误")
	require.Len(t, rows, 1)
	require.Equal(t, int64(0), rows[0].AvgLatencyMs)
	require.Equal(t, int64(3), rows[0].Errors, "errors 仍应正确汇总")
}

// TestAnalysisByChannelShape 三块形状 + days 白名单 + nil 归一为 []。
func TestAnalysisByChannelShape(t *testing.T) {
	g := dbtest.OpenInMemory(t)
	require.NoError(t, g.AutoMigrate(&model.UsageDailyChannel{}))
	s := NewService(NewRepo(g))

	// 空库：三块都应是非 nil 空切片（JSON 输出 [] 而非 null）
	a, err := s.AnalysisByChannel(1, 999) // 999 非法 → 回退 7
	require.NoError(t, err)
	require.NotNil(t, a.Trend)
	require.NotNil(t, a.Models)
	require.NotNil(t, a.DailyModels)
	require.Empty(t, a.Trend)
}

// TestModelsByChannelFirstTokenWeightedByStreamRequests 首字节均值必须用 stream_requests 加权，
// 不能用 requests——后者把非流式请求算进分母，会严重高估。
// 审查者给的场景：A 天 1 条流式(1000ms) + 999 条非流式；B 天 1000 条流式(10ms)。
// 真值 (1*1000 + 1000*10)/1001 ≈ 11ms；按 requests 加权得 (1000*1000+1000*10)/2000 = 505ms（45 倍）。
func TestModelsByChannelFirstTokenWeightedByStreamRequests(t *testing.T) {
	g := dbtest.OpenInMemory(t)
	require.NoError(t, g.AutoMigrate(&model.UsageDailyChannel{}))
	today := time.Now().Format("2006-01-02")
	yesterday := time.Now().AddDate(0, 0, -1).Format("2006-01-02")

	// A 天：1000 个成功请求里只有 1 个是流式，其首 Token 1000ms
	require.NoError(t, g.Create(&model.UsageDailyChannel{
		StatDate: yesterday, ChannelID: 8, Model: "m",
		Requests: 1000, StreamRequests: 1, AvgFirstTokenMs: 1000,
	}).Error)
	// B 天：1000 个成功请求全是流式，首 Token 10ms
	require.NoError(t, g.Create(&model.UsageDailyChannel{
		StatDate: today, ChannelID: 8, Model: "m",
		Requests: 1000, StreamRequests: 1000, AvgFirstTokenMs: 10,
	}).Error)

	s := NewService(NewRepo(g))
	rows, err := s.ModelsByChannel(8, 7)
	require.NoError(t, err)
	require.Len(t, rows, 1)
	require.Equal(t, int64(1001), rows[0].StreamRequests)
	// (1*1000 + 1000*10) / 1001 = 10.99 → 四舍五入 11
	require.Equal(t, int64(11), rows[0].AvgFirstTokenMs, "必须用 stream_requests 加权；用 requests 会得到 505")
}

// TestTrendByChannelZeroStreamRequestsNoDivByZero 无任何流式请求时首字节均值输出 0，不报除零错。
func TestTrendByChannelZeroStreamRequestsNoDivByZero(t *testing.T) {
	g := dbtest.OpenInMemory(t)
	require.NoError(t, g.AutoMigrate(&model.UsageDailyChannel{}))
	today := time.Now().Format("2006-01-02")
	require.NoError(t, g.Create(&model.UsageDailyChannel{
		StatDate: today, ChannelID: 10, Model: "m",
		Requests: 5, StreamRequests: 0, AvgLatencyMs: 200, AvgFirstTokenMs: 0,
	}).Error)

	s := NewService(NewRepo(g))
	rows, err := s.TrendByChannel(10, 7)
	require.NoError(t, err, "stream_requests 全为 0 不得触发除零错误")
	require.Len(t, rows, 1)
	require.Equal(t, int64(0), rows[0].AvgFirstTokenMs)
	require.Equal(t, int64(200), rows[0].AvgLatencyMs, "总延迟均值不受影响")
}
