package usage

import (
	"errors"
	"math"
	"time"

	"github.com/ruanun/simple-ai-gateway/internal/model"
)

type Service struct{ repo *Repo }

func NewService(repo *Repo) *Service { return &Service{repo: repo} }

// sinceDate 返回 N 天前的日期串（YYYY-MM-DD），各「最近 N 天」用量查询的统一起点。
func sinceDate(days int) string {
	return time.Now().AddDate(0, 0, -days).Format("2006-01-02")
}

// 聚合列串（供各查询 Select 拼接复用）：全量版含七项指标；
// 精简版供日×模型明细（堆叠图只需四项，省 cached/cache_creation/reasoning）。
const (
	sumCols     = "SUM(requests) requests, SUM(prompt_tokens) prompt_tokens, SUM(completion_tokens) completion_tokens, SUM(cached_tokens) cached_tokens, SUM(cache_creation_tokens) cache_creation_tokens, SUM(reasoning_tokens) reasoning_tokens, SUM(cost_micros) cost_micros"
	sumColsLite = "SUM(requests) requests, SUM(prompt_tokens) prompt_tokens, SUM(completion_tokens) completion_tokens, SUM(cost_micros) cost_micros"
)

// 渠道维度专用列拼接：比 sumCols 多 errors，且两个平均值必须按请求数加权——
// 表里存的是每天/每模型的均值，直接 AVG 会变成「平均的平均」，各行请求数悬殊时严重失真
// （如 1 条 1000ms 与 999 条 10ms，朴素平均 505ms，加权后约 11ms）。
// NULLIF(SUM(requests),0) 防除零：分母为 0 时整体为 NULL，外层 COALESCE 兜成 0。
//
// ⚠️ `* 1.0` 不是冗余，两个作用缺一不可：
//  1. 强制浮点除法。否则 SQLite/PG 走整数除法静默截断（10.99 → 10）；
//  2. 统一结果类型。MySQL 对整数操作数的 `/` 恒返回 DECIMAL（驱动给 []byte），
//     直接 Scan 进 int64 会报 converting driver.Value 错误——即 MySQL 部署下
//     本查询无条件 500。乘 1.0 后三方言都给浮点，由 channelAggRow 以 float64
//     接收再四舍五入成 int64。
//
// ⚠️ 两个平均值的加权分母不同，不能混用：
// avg_latency_ms 覆盖全部成功请求 → 用 requests；
// avg_first_token_ms 只覆盖成功且有首 Token 计时的流式请求 → 必须用 stream_requests，
// 用 requests 会把非流式请求算进分母而严重高估（见 model.UsageDailyChannel.StreamRequests）。
const sumColsChannel = "SUM(requests) requests, SUM(errors) errors, SUM(stream_requests) stream_requests, " +
	"SUM(prompt_tokens) prompt_tokens, SUM(completion_tokens) completion_tokens, " +
	"SUM(cached_tokens) cached_tokens, SUM(cache_creation_tokens) cache_creation_tokens, " +
	"SUM(reasoning_tokens) reasoning_tokens, SUM(cost_micros) cost_micros, " +
	"COALESCE(SUM(avg_latency_ms * requests) * 1.0 / NULLIF(SUM(requests), 0), 0) avg_latency_ms, " +
	"COALESCE(SUM(avg_first_token_ms * stream_requests) * 1.0 / NULLIF(SUM(stream_requests), 0), 0) avg_first_token_ms"

// channelAggRow 渠道聚合查询的暂存行：两个加权平均在 SQL 层是浮点/DECIMAL，
// 必须先落 float64 再转 int64（见 sumColsChannel 的说明，同包 OpsToday 同一模式）。
type channelAggRow struct {
	StatDate            string
	Model               string
	Requests            int64
	Errors              int64
	StreamRequests      int64
	PromptTokens        int64
	CompletionTokens    int64
	CachedTokens        int64
	CacheCreationTokens int64
	ReasoningTokens     int64
	CostMicros          int64
	AvgLatencyMs        float64
	AvgFirstTokenMs     float64
}

// toChannelModels 把暂存行转为对外模型，两个平均值四舍五入落回 int64。
func toChannelModels(rows []channelAggRow) []model.UsageDailyChannel {
	out := make([]model.UsageDailyChannel, 0, len(rows))
	for _, r := range rows {
		out = append(out, model.UsageDailyChannel{
			StatDate: r.StatDate, Model: r.Model,
			Requests: r.Requests, Errors: r.Errors, StreamRequests: r.StreamRequests,
			PromptTokens: r.PromptTokens, CompletionTokens: r.CompletionTokens,
			CachedTokens: r.CachedTokens, CacheCreationTokens: r.CacheCreationTokens,
			ReasoningTokens: r.ReasoningTokens, CostMicros: r.CostMicros,
			AvgLatencyMs:    int64(math.Round(r.AvgLatencyMs)),
			AvgFirstTokenMs: int64(math.Round(r.AvgFirstTokenMs)),
		})
	}
	return out
}

// KPIData 全量汇总指标。
type KPIData struct {
	Requests            int64 `json:"requests"`
	PromptTokens        int64 `json:"prompt_tokens"`
	CompletionTokens    int64 `json:"completion_tokens"`
	CachedTokens        int64 `json:"cached_tokens"`
	CacheCreationTokens int64 `json:"cache_creation_tokens"`
	ReasoningTokens     int64 `json:"reasoning_tokens"`
	CostMicros          int64 `json:"cost_micros"`
}

// KPI 返回全量汇总。
func (s *Service) KPI() (KPIData, error) {
	return s.kpiWhere("")
}

// KPIByDate 返回指定日期(YYYY-MM-DD)的汇总；statDate 为空等同全量。
func (s *Service) KPIByDate(statDate string) (KPIData, error) {
	return s.kpiWhere(statDate)
}

func (s *Service) kpiWhere(statDate string) (KPIData, error) {
	var k KPIData
	q := s.repo.g.Model(&model.UsageDaily{}).
		Select("COALESCE(SUM(requests),0) requests, COALESCE(SUM(prompt_tokens),0) prompt_tokens, COALESCE(SUM(completion_tokens),0) completion_tokens, COALESCE(SUM(cached_tokens),0) cached_tokens, COALESCE(SUM(cache_creation_tokens),0) cache_creation_tokens, COALESCE(SUM(reasoning_tokens),0) reasoning_tokens, COALESCE(SUM(cost_micros),0) cost_micros")
	if statDate != "" {
		q = q.Where("stat_date = ?", statDate)
	}
	err := q.Scan(&k).Error
	return k, err
}

// TrendHourly 返回最近 N 小时趋势（按 stat_hour 降序，取最近 limit 条）。
func (s *Service) TrendHourly(limit int) ([]model.UsageHourly, error) {
	var rows []model.UsageHourly
	err := s.repo.g.Model(&model.UsageHourly{}).
		Select("stat_hour, " + sumCols).
		Group("stat_hour").Order("stat_hour desc").Limit(limit).Scan(&rows).Error
	return rows, err
}

// TopModels 模型用量排行（按请求数降序）。
func (s *Service) TopModels(limit int) ([]model.UsageDaily, error) {
	var rows []model.UsageDaily
	err := s.repo.g.Model(&model.UsageDaily{}).
		Select("model, " + sumCols).
		Group("model").Order("requests desc").Limit(limit).Scan(&rows).Error
	return rows, err
}

// TrendByApp 返回某应用最近 N 天的日聚合趋势（按日期升序）。
func (s *Service) TrendByApp(appID uint64, days int) ([]model.UsageDaily, error) {
	var rows []model.UsageDaily
	err := s.repo.g.Model(&model.UsageDaily{}).
		Select("stat_date, "+sumCols).
		Where("app_id = ? AND stat_date >= ?", appID, sinceDate(days)).
		Group("stat_date").Order("stat_date asc").
		Scan(&rows).Error
	return rows, err
}

// TrendByKey 返回某令牌最近 N 天的日聚合趋势（按日期升序；表为 key×model 维度，此处按日汇总）。
func (s *Service) TrendByKey(apiKeyID uint64, days int) ([]model.UsageDailyKey, error) {
	var rows []model.UsageDailyKey
	err := s.repo.g.Model(&model.UsageDailyKey{}).
		Select("stat_date, "+sumCols).
		Where("api_key_id = ? AND stat_date >= ?", apiKeyID, sinceDate(days)).
		Group("stat_date").Order("stat_date asc").Scan(&rows).Error
	return rows, err
}

// ModelsByApp 某应用最近 N 天按模型聚合（费用降序），供占比环形图与明细表。
func (s *Service) ModelsByApp(appID uint64, days int) ([]model.UsageDaily, error) {
	var rows []model.UsageDaily
	err := s.repo.g.Model(&model.UsageDaily{}).
		Select("model, "+sumCols).
		Where("app_id = ? AND stat_date >= ?", appID, sinceDate(days)).
		Group("model").Order("cost_micros desc").Scan(&rows).Error
	return rows, err
}

// DailyModelsByApp 某应用最近 N 天的日×模型明细（日期升序），供堆叠柱状图。
func (s *Service) DailyModelsByApp(appID uint64, days int) ([]model.UsageDaily, error) {
	var rows []model.UsageDaily
	err := s.repo.g.Model(&model.UsageDaily{}).
		Select("stat_date, model, "+sumColsLite).
		Where("app_id = ? AND stat_date >= ?", appID, sinceDate(days)).
		Group("stat_date, model").Order("stat_date asc").Scan(&rows).Error
	return rows, err
}

// ModelsByKey 某令牌最近 N 天按模型聚合（费用降序）。model 为空串的行是加维度前的历史存量。
func (s *Service) ModelsByKey(apiKeyID uint64, days int) ([]model.UsageDailyKey, error) {
	var rows []model.UsageDailyKey
	err := s.repo.g.Model(&model.UsageDailyKey{}).
		Select("model, "+sumCols).
		Where("api_key_id = ? AND stat_date >= ?", apiKeyID, sinceDate(days)).
		Group("model").Order("cost_micros desc").Scan(&rows).Error
	return rows, err
}

// DailyModelsByKey 某令牌最近 N 天的日×模型明细（日期升序）。
func (s *Service) DailyModelsByKey(apiKeyID uint64, days int) ([]model.UsageDailyKey, error) {
	var rows []model.UsageDailyKey
	err := s.repo.g.Model(&model.UsageDailyKey{}).
		Select("stat_date, model, "+sumColsLite).
		Where("api_key_id = ? AND stat_date >= ?", apiKeyID, sinceDate(days)).
		Group("stat_date, model").Order("stat_date asc").Scan(&rows).Error
	return rows, err
}

// TrendByChannel 返回某渠道最近 N 天的日聚合趋势（按日期升序；表为 channel×model 维度，此处按日汇总）。
func (s *Service) TrendByChannel(channelID uint64, days int) ([]model.UsageDailyChannel, error) {
	var rows []channelAggRow
	err := s.repo.g.Model(&model.UsageDailyChannel{}).
		Select("stat_date, "+sumColsChannel).
		Where("channel_id = ? AND stat_date >= ?", channelID, sinceDate(days)).
		Group("stat_date").Order("stat_date asc").Scan(&rows).Error
	return toChannelModels(rows), err
}

// ModelsByChannel 某渠道最近 N 天按模型聚合（费用降序），供占比环形图与明细表。
func (s *Service) ModelsByChannel(channelID uint64, days int) ([]model.UsageDailyChannel, error) {
	var rows []channelAggRow
	err := s.repo.g.Model(&model.UsageDailyChannel{}).
		Select("model, "+sumColsChannel).
		Where("channel_id = ? AND stat_date >= ?", channelID, sinceDate(days)).
		Group("model").Order("cost_micros desc").Scan(&rows).Error
	return toChannelModels(rows), err
}

// DailyModelsByChannel 某渠道最近 N 天的日×模型明细（日期升序），供堆叠柱状图。
func (s *Service) DailyModelsByChannel(channelID uint64, days int) ([]model.UsageDailyChannel, error) {
	var rows []model.UsageDailyChannel
	err := s.repo.g.Model(&model.UsageDailyChannel{}).
		Select("stat_date, model, "+sumColsLite).
		Where("channel_id = ? AND stat_date >= ?", channelID, sinceDate(days)).
		Group("stat_date, model").Order("stat_date asc").Scan(&rows).Error
	return rows, err
}

// Analysis 用量分析三块结果（总量趋势/期间模型聚合/日×模型明细），admin 与 portal 端点同形共用；
// 三切片保证非 nil，JSON 输出 [] 而非 null。
type Analysis[T any] struct {
	Trend       []T
	Models      []T
	DailyModels []T
}

// clampAnalysisDays 用量分析天数白名单（7/14/30），非法回退 7。
func clampAnalysisDays(days int) int {
	switch days {
	case 7, 14, 30:
		return days
	}
	return 7
}

// orEmpty nil 切片归一为空切片。
func orEmpty[T any](rows []T) []T {
	if rows == nil {
		return []T{}
	}
	return rows
}

// AnalysisByApp 某应用的用量分析：天数白名单 + 三查询 + nil 归一，供 admin 与 portal 端点直出。
func (s *Service) AnalysisByApp(appID uint64, days int) (Analysis[model.UsageDaily], error) {
	days = clampAnalysisDays(days)
	trend, err1 := s.TrendByApp(appID, days)
	models, err2 := s.ModelsByApp(appID, days)
	daily, err3 := s.DailyModelsByApp(appID, days)
	if err := errors.Join(err1, err2, err3); err != nil {
		return Analysis[model.UsageDaily]{}, err
	}
	return Analysis[model.UsageDaily]{Trend: orEmpty(trend), Models: orEmpty(models), DailyModels: orEmpty(daily)}, nil
}

// AnalysisByKey 某令牌的用量分析：形状同 AnalysisByApp，数据源 usage_daily_key。
func (s *Service) AnalysisByKey(apiKeyID uint64, days int) (Analysis[model.UsageDailyKey], error) {
	days = clampAnalysisDays(days)
	trend, err1 := s.TrendByKey(apiKeyID, days)
	models, err2 := s.ModelsByKey(apiKeyID, days)
	daily, err3 := s.DailyModelsByKey(apiKeyID, days)
	if err := errors.Join(err1, err2, err3); err != nil {
		return Analysis[model.UsageDailyKey]{}, err
	}
	return Analysis[model.UsageDailyKey]{Trend: orEmpty(trend), Models: orEmpty(models), DailyModels: orEmpty(daily)}, nil
}

// AnalysisByChannel 某渠道的用量分析：形状同 AnalysisByApp，数据源 usage_daily_channel。
// 比应用/令牌多带 errors 与两个平均延迟（渠道维度关注可用性与快慢）。
func (s *Service) AnalysisByChannel(channelID uint64, days int) (Analysis[model.UsageDailyChannel], error) {
	days = clampAnalysisDays(days)
	trend, err1 := s.TrendByChannel(channelID, days)
	models, err2 := s.ModelsByChannel(channelID, days)
	daily, err3 := s.DailyModelsByChannel(channelID, days)
	if err := errors.Join(err1, err2, err3); err != nil {
		return Analysis[model.UsageDailyChannel]{}, err
	}
	return Analysis[model.UsageDailyChannel]{Trend: orEmpty(trend), Models: orEmpty(models), DailyModels: orEmpty(daily)}, nil
}

// OpsToday 今日运行指标（源 request_logs，含错误请求；聚合表只收成功流量）。
type OpsToday struct {
	Requests        int64 `json:"requests"`
	Errors          int64 `json:"errors"`
	AvgLatencyMs    int64 `json:"avg_latency_ms"`
	AvgFirstTokenMs int64 `json:"avg_first_token_ms"`
}

// OpsToday 统计今日请求总量/错误数/平均延迟/平均首 Token 延迟（仅流式且 >0 的请求计入）。
func (s *Service) OpsToday() (OpsToday, error) {
	var raw struct {
		Requests        int64
		Errors          int64
		AvgLatencyMs    float64
		AvgFirstTokenMs float64
	}
	today := time.Now().Format("2006-01-02")
	start, end, rerr := localDayRange(today)
	if rerr != nil {
		return OpsToday{}, rerr
	}
	err := s.repo.g.Model(&model.RequestLog{}).
		Select("COUNT(*) requests, COALESCE(SUM(CASE WHEN status >= 400 THEN 1 ELSE 0 END),0) errors, COALESCE(AVG(latency_ms),0) avg_latency_ms, COALESCE(AVG(CASE WHEN first_token_ms > 0 THEN first_token_ms END),0) avg_first_token_ms").
		Where("created_at >= ? AND created_at < ?", start, end).
		Scan(&raw).Error
	return OpsToday{Requests: raw.Requests, Errors: raw.Errors,
		AvgLatencyMs: int64(raw.AvgLatencyMs), AvgFirstTokenMs: int64(raw.AvgFirstTokenMs)}, err
}

// AppUsageRow 应用用量排行行（join 应用名；无主/已删应用回退占位名）。
type AppUsageRow struct {
	AppID            uint64 `json:"app_id"`
	Name             string `json:"name"`
	Requests         int64  `json:"requests"`
	PromptTokens     int64  `json:"prompt_tokens"`
	CompletionTokens int64  `json:"completion_tokens"`
	CostMicros       int64  `json:"cost_micros"`
}

// TopApps 应用用量排行（全时，按请求数降序）。
func (s *Service) TopApps(limit int) ([]AppUsageRow, error) {
	var rows []AppUsageRow
	err := s.repo.g.Model(&model.UsageDaily{}).
		Select("usage_daily.app_id, COALESCE(apps.name, '未知应用') name, SUM(usage_daily.requests) requests, SUM(usage_daily.prompt_tokens) prompt_tokens, SUM(usage_daily.completion_tokens) completion_tokens, SUM(usage_daily.cost_micros) cost_micros").
		Joins("LEFT JOIN apps ON apps.id = usage_daily.app_id").
		Group("usage_daily.app_id, apps.name").Order("requests desc").Limit(limit).Scan(&rows).Error
	return rows, err
}

// ChannelTodayRow 渠道今日聚合行（join 渠道名）。
type ChannelTodayRow struct {
	ChannelID uint64 `json:"channel_id"`
	Name      string `json:"name"`
	Requests  int64  `json:"requests"`
	Errors    int64  `json:"errors"`
}

// ChannelsToday 今日按渠道聚合请求数/错误数（按请求数降序），供仪表盘渠道健康概览。
func (s *Service) ChannelsToday() ([]ChannelTodayRow, error) {
	var rows []ChannelTodayRow
	start, end, err := localDayRange(time.Now().Format("2006-01-02"))
	if err != nil {
		return nil, err
	}
	err = s.repo.g.Model(&model.RequestLog{}).
		Select("request_logs.channel_id, COALESCE(channels.name, '未知渠道') name, COUNT(*) requests, COALESCE(SUM(CASE WHEN request_logs.status >= 400 THEN 1 ELSE 0 END),0) errors").
		Joins("LEFT JOIN channels ON channels.id = request_logs.channel_id").
		Where("request_logs.created_at >= ? AND request_logs.created_at < ? AND request_logs.channel_id > 0", start, end).
		Group("request_logs.channel_id, channels.name").Order("requests desc").Scan(&rows).Error
	return rows, err
}

// ChannelHealthRow 渠道健康聚合行（最近 N 天，按错误数/请求数排序）。
type ChannelHealthRow struct {
	ChannelID       uint64  `json:"channel_id"`
	Name            string  `json:"name"`
	Requests        int64   `json:"requests"`
	Errors          int64   `json:"errors"`
	ErrorRate       float64 `json:"error_rate"`
	StreamRequests  int64   `json:"stream_requests"`
	CostMicros      int64   `json:"cost_micros"`
	AvgLatencyMs    int64   `json:"avg_latency_ms"`
	AvgFirstTokenMs int64   `json:"avg_first_token_ms"`
}

// ChannelHealth 按渠道聚合最近 N 天的成功率/错误数/加权延迟（数据源 usage_daily_channel）。
func (s *Service) ChannelHealth(days int) ([]ChannelHealthRow, error) {
	if days <= 0 {
		days = 7
	}
	if days > 90 {
		days = 90
	}
	var raw []struct {
		ChannelID       uint64
		Name            string
		Requests        int64
		Errors          int64
		StreamRequests  int64
		CostMicros      int64
		AvgLatencyMs    float64
		AvgFirstTokenMs float64
	}
	err := s.repo.g.Model(&model.UsageDailyChannel{}).
		Select("usage_daily_channel.channel_id, COALESCE(channels.name, '未知渠道') name, "+sumColsChannel).
		Joins("LEFT JOIN channels ON channels.id = usage_daily_channel.channel_id").
		Where("usage_daily_channel.stat_date >= ?", sinceDate(days)).
		Group("usage_daily_channel.channel_id, channels.name").
		Order("errors desc, requests desc").Scan(&raw).Error
	if err != nil {
		return nil, err
	}
	out := make([]ChannelHealthRow, 0, len(raw))
	for _, r := range raw {
		rate := 0.0
		if r.Requests > 0 {
			rate = float64(r.Errors) / float64(r.Requests)
		}
		out = append(out, ChannelHealthRow{
			ChannelID: r.ChannelID, Name: r.Name, Requests: r.Requests, Errors: r.Errors,
			ErrorRate: rate, StreamRequests: r.StreamRequests, CostMicros: r.CostMicros,
			AvgLatencyMs: int64(math.Round(r.AvgLatencyMs)), AvgFirstTokenMs: int64(math.Round(r.AvgFirstTokenMs)),
		})
	}
	return out, nil
}
