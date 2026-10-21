package usage

import (
	"testing"
	"time"

	"github.com/ruanun/simple-ai-gateway/internal/dbtest"
	"github.com/ruanun/simple-ai-gateway/internal/model"
	"github.com/stretchr/testify/require"
)

func TestAggregateDailySumsCacheDetail(t *testing.T) {
	g := dbtest.OpenInMemory(t)
	require.NoError(t, g.AutoMigrate(&model.RequestLog{}, &model.UsageDaily{}, &model.UsageHourly{}, &model.UsageDailyKey{}))
	today := time.Now().Format("2006-01-02")
	for i := 0; i < 2; i++ {
		require.NoError(t, g.Create(&model.RequestLog{
			AppID: 1, APIKeyID: 1, Model: "claude-3", Status: 200,
			PromptTokens: 1000, CompletionTokens: 200, CachedTokens: 700, CacheCreationTokens: 100, ReasoningTokens: 50,
		}).Error)
	}
	a := NewAggregatorStatic(g)
	a.Aggregate()

	var d model.UsageDaily
	require.NoError(t, g.Where("stat_date = ? AND app_id = ? AND model = ?", today, 1, "claude-3").First(&d).Error)
	require.Equal(t, int64(1400), d.CachedTokens)
	require.Equal(t, int64(200), d.CacheCreationTokens)
	require.Equal(t, int64(100), d.ReasoningTokens)
}

// TestAggregateDailyKeyByModel 同一令牌多模型请求应各落一行（key×model 维度）。
func TestAggregateDailyKeyByModel(t *testing.T) {
	g := dbtest.OpenInMemory(t)
	require.NoError(t, g.AutoMigrate(&model.RequestLog{}, &model.UsageDaily{}, &model.UsageHourly{}, &model.UsageDailyKey{}))
	today := time.Now().Format("2006-01-02")
	for _, m := range []string{"gpt-4o", "claude-3", "claude-3"} {
		require.NoError(t, g.Create(&model.RequestLog{
			AppID: 1, APIKeyID: 7, Model: m, Status: 200,
			PromptTokens: 100, CompletionTokens: 10, CostMicros: 500,
		}).Error)
	}
	NewAggregatorStatic(g).Aggregate()

	var rows []model.UsageDailyKey
	require.NoError(t, g.Where("stat_date = ? AND api_key_id = ?", today, 7).Order("model asc").Find(&rows).Error)
	require.Len(t, rows, 2)
	require.Equal(t, "claude-3", rows[0].Model)
	require.Equal(t, int64(2), rows[0].Requests)
	require.Equal(t, "gpt-4o", rows[1].Model)
	require.Equal(t, int64(1), rows[1].Requests)
}

// TestAggregateDailyDayBoundary 验证范围过滤的日边界：昨天 23:30 的日志不计入今天，今天 00:30 的计入。
func TestAggregateDailyDayBoundary(t *testing.T) {
	g := dbtest.OpenInMemory(t)
	require.NoError(t, g.AutoMigrate(&model.RequestLog{}, &model.UsageDaily{}, &model.UsageHourly{}, &model.UsageDailyKey{}))
	now := time.Now()
	todayStart := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.Local)
	mk := func(at time.Time) model.RequestLog {
		return model.RequestLog{
			BaseModel: model.BaseModel{CreatedAt: at, UpdatedAt: at},
			AppID:     7, Model: "m1", Status: 200, PromptTokens: 1, CostMicros: 10,
		}
	}
	older := mk(todayStart.Add(-30 * time.Minute)) // 昨天 23:30
	newer := mk(todayStart.Add(30 * time.Minute))  // 今天 00:30
	require.NoError(t, g.Create(&older).Error)
	require.NoError(t, g.Create(&newer).Error)

	NewAggregatorStatic(g).Aggregate()

	var rows []model.UsageDaily
	require.NoError(t, g.Where("app_id = ?", 7).Find(&rows).Error)
	require.Len(t, rows, 1)
	require.Equal(t, time.Now().Format("2006-01-02"), rows[0].StatDate)
	require.Equal(t, int64(1), rows[0].Requests) // 只有今天 00:30 这条
}

// TestAggregateDailyChannel 渠道聚合的核心口径：
// requests/errors 按 status 分开统计；token 与 cost 只累计成功请求；
// channel_id=0（全候选失败，不归属任何渠道）的行不入表。
func TestAggregateDailyChannel(t *testing.T) {
	g := dbtest.OpenInMemory(t)
	require.NoError(t, g.AutoMigrate(&model.RequestLog{}, &model.UsageDailyChannel{}))
	today := time.Now().Format("2006-01-02")

	rows := []model.RequestLog{
		// 渠道 7 / gpt-4o：两条成功 + 一条失败
		{ChannelID: 7, Model: "gpt-4o", Status: 200, PromptTokens: 100, CompletionTokens: 50, CostMicros: 1000, LatencyMs: 800, FirstTokenMs: 100, Stream: true},
		// ⚠️ 1201 而非 1200 是刻意的：AVG=(800+1201)/2=1000.5 为非整数。
		// 原先用 1200 让均值恰好是整数 1000.0，掩盖了「AVG 返回 float 扫不进 int64」这个
		// 致命 bug（错误发生在写入循环前，当天所有渠道一行都写不进去）。别改回整数。
		{ChannelID: 7, Model: "gpt-4o", Status: 200, PromptTokens: 200, CompletionTokens: 60, CostMicros: 2000, LatencyMs: 1201, FirstTokenMs: 200, Stream: true},
		{ChannelID: 7, Model: "gpt-4o", Status: 500, PromptTokens: 0, CompletionTokens: 0, CostMicros: 0, LatencyMs: 50},
		// channel_id=0：全候选失败，不归属渠道，必须被排除
		{ChannelID: 0, Model: "gpt-4o", Status: 503, CostMicros: 0},
	}
	for i := range rows {
		require.NoError(t, g.Create(&rows[i]).Error)
	}

	a := NewAggregatorStatic(g)
	require.NoError(t, a.aggregateDailyChannel(today))

	var got model.UsageDailyChannel
	require.NoError(t, g.Where("stat_date = ? AND channel_id = ? AND model = ?", today, 7, "gpt-4o").First(&got).Error)
	require.Equal(t, int64(2), got.Requests, "只数成功请求")
	require.Equal(t, int64(1), got.Errors, "失败请求单独计数")
	require.Equal(t, int64(300), got.PromptTokens, "token 只累计成功请求")
	require.Equal(t, int64(3000), got.CostMicros, "cost 只累计成功请求")
	require.Equal(t, int64(1001), got.AvgLatencyMs, "(800+1201)/2=1000.5 四舍五入为 1001；失败请求的 50ms 不参与")

	// channel_id=0 不入表
	var zero int64
	require.NoError(t, g.Model(&model.UsageDailyChannel{}).Where("channel_id = ?", 0).Count(&zero).Error)
	require.Equal(t, int64(0), zero)
}

// TestAggregateDailyChannelFirstTokenSkipsNonStream 首字节均值只计入 first_token_ms > 0 的请求。
// 非流式请求该字段为 0，若聚合 SQL 写成 AVG(CASE WHEN ... ELSE 0 END) 会把它们当 0 拉低均值——
// 本用例正是守这个：必须用无 ELSE 的 AVG(CASE WHEN ... END) 让其为 NULL 被跳过。
func TestAggregateDailyChannelFirstTokenSkipsNonStream(t *testing.T) {
	g := dbtest.OpenInMemory(t)
	require.NoError(t, g.AutoMigrate(&model.RequestLog{}, &model.UsageDailyChannel{}))
	today := time.Now().Format("2006-01-02")

	rows := []model.RequestLog{
		{ChannelID: 9, Model: "m", Status: 200, FirstTokenMs: 300, LatencyMs: 1000, Stream: true},
		{ChannelID: 9, Model: "m", Status: 200, FirstTokenMs: 0, LatencyMs: 1000}, // 非流式
	}
	for i := range rows {
		require.NoError(t, g.Create(&rows[i]).Error)
	}

	a := NewAggregatorStatic(g)
	require.NoError(t, a.aggregateDailyChannel(today))

	var got model.UsageDailyChannel
	require.NoError(t, g.Where("stat_date = ? AND channel_id = ?", today, 9).First(&got).Error)
	require.Equal(t, int64(300), got.AvgFirstTokenMs, "非流式的 0 不得参与首字节均值")
	require.Equal(t, int64(1000), got.AvgLatencyMs, "总延迟均值两条都算")
}

// TestAggregateDailyChannelIdempotent 连续跑两次结果不翻倍（覆盖写而非累加）。
func TestAggregateDailyChannelIdempotent(t *testing.T) {
	g := dbtest.OpenInMemory(t)
	require.NoError(t, g.AutoMigrate(&model.RequestLog{}, &model.UsageDailyChannel{}))
	today := time.Now().Format("2006-01-02")
	require.NoError(t, g.Create(&model.RequestLog{ChannelID: 3, Model: "m", Status: 200, PromptTokens: 10, CostMicros: 500, LatencyMs: 100}).Error)

	a := NewAggregatorStatic(g)
	require.NoError(t, a.aggregateDailyChannel(today))
	require.NoError(t, a.aggregateDailyChannel(today))

	var got model.UsageDailyChannel
	require.NoError(t, g.Where("stat_date = ? AND channel_id = ?", today, 3).First(&got).Error)
	require.Equal(t, int64(1), got.Requests, "重复聚合不得翻倍")
	require.Equal(t, int64(500), got.CostMicros)

	var cnt int64
	require.NoError(t, g.Model(&model.UsageDailyChannel{}).Count(&cnt).Error)
	require.Equal(t, int64(1), cnt, "不得产生重复行")
}

// TestAggregateDailyChannelCountsStreamRequests stream_requests 的过滤条件必须与
// avg_first_token_ms 严格一致（成功 且 first_token_ms > 0），否则读侧加权分母就是错的。
func TestAggregateDailyChannelCountsStreamRequests(t *testing.T) {
	g := dbtest.OpenInMemory(t)
	require.NoError(t, g.AutoMigrate(&model.RequestLog{}, &model.UsageDailyChannel{}))
	today := time.Now().Format("2006-01-02")

	rows := []model.RequestLog{
		{ChannelID: 12, Model: "m", Status: 200, FirstTokenMs: 100, LatencyMs: 500, Stream: true}, // 计入
		{ChannelID: 12, Model: "m", Status: 200, FirstTokenMs: 300, LatencyMs: 700, Stream: true}, // 计入
		{ChannelID: 12, Model: "m", Status: 200, FirstTokenMs: 0, LatencyMs: 600},                 // 非流式，不计入
		{ChannelID: 12, Model: "m", Status: 500, FirstTokenMs: 50, LatencyMs: 80, Stream: true},   // 失败，不计入
	}
	for i := range rows {
		require.NoError(t, g.Create(&rows[i]).Error)
	}

	a := NewAggregatorStatic(g)
	require.NoError(t, a.aggregateDailyChannel(today))

	var got model.UsageDailyChannel
	require.NoError(t, g.Where("stat_date = ? AND channel_id = ?", today, 12).First(&got).Error)
	require.Equal(t, int64(3), got.Requests, "三条成功")
	require.Equal(t, int64(1), got.Errors)
	require.Equal(t, int64(2), got.StreamRequests, "只有成功且 first_token_ms>0 的两条")
	require.Equal(t, int64(200), got.AvgFirstTokenMs, "(100+300)/2，与 stream_requests 同口径")
}
