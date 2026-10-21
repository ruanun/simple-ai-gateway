package usage

import (
	"fmt"
	"log/slog"
	"math"
	"time"

	"github.com/ruanun/simple-ai-gateway/internal/model"
	"gorm.io/gorm"
)

// Aggregator 定时从 request_logs 聚合用量到统计表
// （usage_daily / usage_hourly / usage_daily_key / usage_daily_channel）。
// 每次聚合是幂等的：用 FirstOrCreate + Updates 覆盖写，源数据来自 request_logs，绝对值重算。
type Aggregator struct {
	g      *gorm.DB
	ticker *time.Ticker
	stop   chan struct{}
}

// NewAggregator 创建并启动聚合任务（每 interval 跑一次，启动后立即跑一次）。
func NewAggregator(g *gorm.DB, interval time.Duration) *Aggregator {
	a := &Aggregator{g: g, ticker: time.NewTicker(interval), stop: make(chan struct{})}
	go a.run()
	return a
}

// NewAggregatorStatic 创建不自动启动后台 goroutine 的 Aggregator，仅供测试中手动调用 Aggregate()。
func NewAggregatorStatic(g *gorm.DB) *Aggregator {
	return &Aggregator{g: g}
}

func (a *Aggregator) run() {
	a.Aggregate() // 启动立即跑一次，补上启动前缺失的数据
	for {
		select {
		case <-a.ticker.C:
			a.Aggregate()
		case <-a.stop:
			a.ticker.Stop()
			return
		}
	}
}

// Stop 停止定时任务。
func (a *Aggregator) Stop() {
	close(a.stop)
}

// localDayRange 返回本地时区下某天(YYYY-MM-DD)的 [start, end) 时间范围。
// 用范围条件替代 DATE() 函数包列，使过滤能走 created_at 索引；
// SQLite(文本存储)/MySQL/PG 三方言下 time.Time 参数均按各自驱动正确序列化比较。
func localDayRange(day string) (time.Time, time.Time, error) {
	start, err := time.ParseInLocation("2006-01-02", day, time.Local)
	if err != nil {
		return time.Time{}, time.Time{}, err
	}
	return start, start.AddDate(0, 0, 1), nil
}

// hourExpr 返回当前数据库方言对应的小时截断 SQL 片段（格式：YYYY-MM-DD HH）。
func (a *Aggregator) hourExpr() string {
	switch a.g.Dialector.Name() {
	case "mysql":
		return "DATE_FORMAT(created_at, '%Y-%m-%d %H')"
	case "postgres":
		// created_at 在 PG 为 timestamptz（存 UTC 瞬时），单次 AT TIME ZONE 即得目标时区墙钟。
		// PG 不识别 'Local' 时区名（真 PG 实测 SQLSTATE 22023，此前 hourly 聚合静默失败），
		// 且 Windows 下 Go 拿不到可靠 IANA 名（time.Local.String()=="Local"），故取进程当前
		// UTC 偏移内联为 INTERVAL。局限：偏移是查询时快照，DST 切换时段的桶可能偏移一小时
		// （hourly 只重算最近 2 小时会随后自愈；固定偏移时区如中国无此问题）。
		_, off := time.Now().Zone()
		sign := "+"
		if off < 0 {
			sign = "-"
			off = -off
		}
		return fmt.Sprintf("TO_CHAR(created_at AT TIME ZONE INTERVAL '%s%02d:%02d', 'YYYY-MM-DD HH24')", sign, off/3600, (off%3600)/60)
	default:
		// SQLite: RFC3339 含时区偏移，转本地时间后截断到小时粒度
		return "strftime('%Y-%m-%d %H', created_at, 'localtime')"
	}
}

// Aggregate 从 request_logs 重新聚合当天数据到统计表（可在测试中手动调用）。
func (a *Aggregator) Aggregate() {
	today := time.Now().Format("2006-01-02")
	// hourly 只重算最近 2 小时的槽（减少扫描量；当天更早的槽已经稳定）
	t := time.Now().Add(-2 * time.Hour)
	sinceHour := time.Date(t.Year(), t.Month(), t.Day(), t.Hour(), 0, 0, 0, t.Location())

	if err := a.aggregateDaily(today); err != nil {
		slog.Error("usage: aggregateDaily 失败", "err", err)
	}
	if err := a.aggregateHourly(sinceHour); err != nil {
		slog.Error("usage: aggregateHourly 失败", "err", err)
	}
	if err := a.aggregateDailyKey(today); err != nil {
		slog.Error("usage: aggregateDailyKey 失败", "err", err)
	}
	if err := a.aggregateDailyChannel(today); err != nil {
		slog.Error("usage: aggregateDailyChannel 失败", "err", err)
	}
}

// aggregateDaily 按 (date, app_id, model) 聚合当天 usage_daily。
func (a *Aggregator) aggregateDaily(today string) error {
	type row struct {
		AppID               uint64
		Model               string
		Requests            int64
		PromptTokens        int64
		CompletionTokens    int64
		CachedTokens        int64
		CacheCreationTokens int64
		ReasoningTokens     int64
		CostMicros          int64
	}
	var rows []row
	start, end, err := localDayRange(today)
	if err != nil {
		return err
	}
	if err := a.g.Model(&model.RequestLog{}).
		Select("app_id, model, COUNT(*) as requests, COALESCE(SUM(prompt_tokens),0) as prompt_tokens, COALESCE(SUM(completion_tokens),0) as completion_tokens, COALESCE(SUM(cached_tokens),0) as cached_tokens, COALESCE(SUM(cache_creation_tokens),0) as cache_creation_tokens, COALESCE(SUM(reasoning_tokens),0) as reasoning_tokens, COALESCE(SUM(cost_micros),0) as cost_micros").
		Where("created_at >= ? AND created_at < ? AND status < 400", start, end).
		Group("app_id, model").
		Scan(&rows).Error; err != nil {
		return err
	}
	for _, r := range rows {
		var d model.UsageDaily
		if err := a.g.Where("stat_date = ? AND app_id = ? AND model = ?", today, r.AppID, r.Model).
			FirstOrCreate(&d, model.UsageDaily{StatDate: today, AppID: r.AppID, Model: r.Model}).Error; err != nil {
			return err
		}
		if err := a.g.Model(&d).Where("id = ?", d.ID).Updates(map[string]any{
			"requests":              r.Requests,
			"prompt_tokens":         r.PromptTokens,
			"completion_tokens":     r.CompletionTokens,
			"cached_tokens":         r.CachedTokens,
			"cache_creation_tokens": r.CacheCreationTokens,
			"reasoning_tokens":      r.ReasoningTokens,
			"cost_micros":           r.CostMicros,
		}).Error; err != nil {
			return err
		}
	}
	return nil
}

// aggregateHourly 按 (stat_hour, app_id) 聚合最近若干小时的 usage_hourly（model 存空串，仅做趋势汇总）。
func (a *Aggregator) aggregateHourly(since time.Time) error {
	type row struct {
		StatHour            string
		AppID               uint64
		Requests            int64
		PromptTokens        int64
		CompletionTokens    int64
		CachedTokens        int64
		CacheCreationTokens int64
		ReasoningTokens     int64
		CostMicros          int64
	}
	var rows []row
	// 用方言感知的 hourExpr() 生成小时截断 SQL，兼容 SQLite/MySQL/PostgreSQL。
	hourSQL := a.hourExpr()
	if err := a.g.Model(&model.RequestLog{}).
		Select(fmt.Sprintf("%s as stat_hour, app_id, COUNT(*) as requests, COALESCE(SUM(prompt_tokens),0) as prompt_tokens, COALESCE(SUM(completion_tokens),0) as completion_tokens, COALESCE(SUM(cached_tokens),0) as cached_tokens, COALESCE(SUM(cache_creation_tokens),0) as cache_creation_tokens, COALESCE(SUM(reasoning_tokens),0) as reasoning_tokens, COALESCE(SUM(cost_micros),0) as cost_micros", hourSQL)).
		Where("created_at >= ? AND status < 400", since).
		Group("stat_hour, app_id").
		Scan(&rows).Error; err != nil {
		return err
	}
	for _, r := range rows {
		var h model.UsageHourly
		if err := a.g.Where("stat_hour = ? AND app_id = ? AND model = ?", r.StatHour, r.AppID, "").
			FirstOrCreate(&h, model.UsageHourly{StatHour: r.StatHour, AppID: r.AppID, Model: ""}).Error; err != nil {
			return err
		}
		if err := a.g.Model(&h).Where("id = ?", h.ID).Updates(map[string]any{
			"requests":              r.Requests,
			"prompt_tokens":         r.PromptTokens,
			"completion_tokens":     r.CompletionTokens,
			"cached_tokens":         r.CachedTokens,
			"cache_creation_tokens": r.CacheCreationTokens,
			"reasoning_tokens":      r.ReasoningTokens,
			"cost_micros":           r.CostMicros,
		}).Error; err != nil {
			return err
		}
	}
	return nil
}

// aggregateDailyKey 按 (date, api_key_id, model) 聚合当天 usage_daily_key。
func (a *Aggregator) aggregateDailyKey(today string) error {
	type row struct {
		APIKeyID            uint64
		Model               string
		Requests            int64
		PromptTokens        int64
		CompletionTokens    int64
		CachedTokens        int64
		CacheCreationTokens int64
		ReasoningTokens     int64
		CostMicros          int64
	}
	var rows []row
	start, end, err := localDayRange(today)
	if err != nil {
		return err
	}
	if err := a.g.Model(&model.RequestLog{}).
		Select("api_key_id, model, COUNT(*) as requests, COALESCE(SUM(prompt_tokens),0) as prompt_tokens, COALESCE(SUM(completion_tokens),0) as completion_tokens, COALESCE(SUM(cached_tokens),0) as cached_tokens, COALESCE(SUM(cache_creation_tokens),0) as cache_creation_tokens, COALESCE(SUM(reasoning_tokens),0) as reasoning_tokens, COALESCE(SUM(cost_micros),0) as cost_micros").
		Where("created_at >= ? AND created_at < ? AND status < 400 AND api_key_id > 0", start, end).
		Group("api_key_id, model").
		Scan(&rows).Error; err != nil {
		return err
	}
	for _, r := range rows {
		var dk model.UsageDailyKey
		if err := a.g.Where("stat_date = ? AND api_key_id = ? AND model = ?", today, r.APIKeyID, r.Model).
			FirstOrCreate(&dk, model.UsageDailyKey{StatDate: today, APIKeyID: r.APIKeyID, Model: r.Model}).Error; err != nil {
			return err
		}
		if err := a.g.Model(&dk).Where("id = ?", dk.ID).Updates(map[string]any{
			"requests":              r.Requests,
			"prompt_tokens":         r.PromptTokens,
			"completion_tokens":     r.CompletionTokens,
			"cached_tokens":         r.CachedTokens,
			"cache_creation_tokens": r.CacheCreationTokens,
			"reasoning_tokens":      r.ReasoningTokens,
			"cost_micros":           r.CostMicros,
		}).Error; err != nil {
			return err
		}
	}
	return nil
}

// aggregateDailyChannel 按 (date, channel_id, model) 聚合当天 usage_daily_channel。
//
// 与 aggregateDaily / aggregateDailyKey 的唯一结构性差异：WHERE **不带** status < 400，
// 改用 CASE WHEN 分流——渠道维度需要错误数，而 token/cost 仍只累计成功请求
// （口径与 App/Key 表一致，保证渠道成本可与应用成本横向对账）。
//
// ⚠️ 两个 AVG 刻意不写 ELSE：不满足条件的行取值 NULL 被 AVG 跳过。若写成 ELSE 0，
// 非流式请求（first_token_ms=0）会被当作 0 拉低首字节均值。
// SUM/AVG(CASE WHEN) 是标准 SQL，SQLite/MySQL/PG 三方言通用，无需方言分支。
func (a *Aggregator) aggregateDailyChannel(today string) error {
	type row struct {
		ChannelID           uint64
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
		// ⚠️ 两个平均值必须用 float64 暂存：SQL 的 AVG() 返回浮点，直接 Scan 进 int64
		// 会报 "converting driver.Value type float64 (\"1000.5\") to a int64: invalid syntax"，
		// 且该错误发生在写入循环之前——一个非整数均值就会让当天所有渠道一行都写不进去。
		// 同包 OpsToday 早有此模式（service.go），此处对齐。
		AvgLatencyMs    float64
		AvgFirstTokenMs float64
	}
	var rows []row
	start, end, err := localDayRange(today)
	if err != nil {
		return err
	}
	sel := "channel_id, model, " +
		"SUM(CASE WHEN status < 400 THEN 1 ELSE 0 END) as requests, " +
		"SUM(CASE WHEN status >= 400 THEN 1 ELSE 0 END) as errors, " +
		// 与 avg_first_token_ms 的过滤条件严格一致——它是该均值在读侧汇总时的加权分母。
		"SUM(CASE WHEN status < 400 AND first_token_ms > 0 THEN 1 ELSE 0 END) as stream_requests, " +
		"COALESCE(SUM(CASE WHEN status < 400 THEN prompt_tokens ELSE 0 END),0) as prompt_tokens, " +
		"COALESCE(SUM(CASE WHEN status < 400 THEN completion_tokens ELSE 0 END),0) as completion_tokens, " +
		"COALESCE(SUM(CASE WHEN status < 400 THEN cached_tokens ELSE 0 END),0) as cached_tokens, " +
		"COALESCE(SUM(CASE WHEN status < 400 THEN cache_creation_tokens ELSE 0 END),0) as cache_creation_tokens, " +
		"COALESCE(SUM(CASE WHEN status < 400 THEN reasoning_tokens ELSE 0 END),0) as reasoning_tokens, " +
		"COALESCE(SUM(CASE WHEN status < 400 THEN cost_micros ELSE 0 END),0) as cost_micros, " +
		"COALESCE(AVG(CASE WHEN status < 400 THEN latency_ms END),0) as avg_latency_ms, " +
		"COALESCE(AVG(CASE WHEN status < 400 AND first_token_ms > 0 THEN first_token_ms END),0) as avg_first_token_ms"
	if err := a.g.Model(&model.RequestLog{}).
		Select(sel).
		Where("created_at >= ? AND created_at < ? AND channel_id > 0", start, end).
		Group("channel_id, model").
		Scan(&rows).Error; err != nil {
		return err
	}
	for _, r := range rows {
		var dc model.UsageDailyChannel
		if err := a.g.Where("stat_date = ? AND channel_id = ? AND model = ?", today, r.ChannelID, r.Model).
			FirstOrCreate(&dc, model.UsageDailyChannel{StatDate: today, ChannelID: r.ChannelID, Model: r.Model}).Error; err != nil {
			return err
		}
		if err := a.g.Model(&dc).Where("id = ?", dc.ID).Updates(map[string]any{
			"requests":              r.Requests,
			"errors":                r.Errors,
			"stream_requests":       r.StreamRequests,
			"prompt_tokens":         r.PromptTokens,
			"completion_tokens":     r.CompletionTokens,
			"cached_tokens":         r.CachedTokens,
			"cache_creation_tokens": r.CacheCreationTokens,
			"reasoning_tokens":      r.ReasoningTokens,
			"cost_micros":           r.CostMicros,
			// 四舍五入而非截断：聚合值会被读侧再次加权汇总，截断误差会逐层累积。
			"avg_latency_ms":     int64(math.Round(r.AvgLatencyMs)),
			"avg_first_token_ms": int64(math.Round(r.AvgFirstTokenMs)),
		}).Error; err != nil {
			return err
		}
	}
	return nil
}
