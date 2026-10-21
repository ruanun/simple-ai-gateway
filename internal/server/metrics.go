package server

import (
	"context"
	"fmt"
	"net/http"
	"runtime"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	goredis "github.com/redis/go-redis/v9"
	"github.com/ruanun/simple-ai-gateway/internal/buildinfo"
	"github.com/ruanun/simple-ai-gateway/internal/domain/usage"
	"gorm.io/gorm"
)

// metricsHandler 输出最小 Prometheus 文本指标：进程/构建信息、依赖健康、今日用量与错误。
// 刻意不引入 prometheus/client_golang：当前指标量小，手写文本格式零依赖、可审计。
// /metrics 为公开端点，只暴露聚合数字，不含渠道/账号/凭证等内部拓扑。
type metricsHandler struct {
	db    *gorm.DB
	redis *goredis.Client
	usage *usage.Service
	now   func() time.Time
}

func (h *metricsHandler) nowTime() time.Time {
	if h.now != nil {
		return h.now()
	}
	return time.Now()
}

func (h *metricsHandler) Serve(c *gin.Context) {
	ctx, cancel := context.WithTimeout(c.Request.Context(), 2*time.Second)
	defer cancel()

	dbUp := 0
	if h.db != nil {
		if sqlDB, err := h.db.DB(); err == nil && sqlDB.PingContext(ctx) == nil {
			dbUp = 1
		}
	}
	redisUp := 0
	if h.redis != nil && h.redis.Ping(ctx).Err() == nil {
		redisUp = 1
	}

	var kpi usage.KPIData
	var ops usage.OpsToday
	usageErr := false
	if h.usage != nil {
		today := h.nowTime().Format("2006-01-02")
		if v, err := h.usage.KPIByDate(today); err == nil {
			kpi = v
		} else {
			usageErr = true
		}
		if v, err := h.usage.OpsToday(); err == nil {
			ops = v
		} else {
			usageErr = true
		}
	}

	var mem runtime.MemStats
	runtime.ReadMemStats(&mem)

	w := c.Writer
	w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
	w.WriteHeader(http.StatusOK)

	fmt.Fprintf(w, "# HELP saig_up 进程存活\n# TYPE saig_up gauge\nsaig_up 1\n")
	fmt.Fprintf(w, "# HELP saig_build_info 构建信息\n# TYPE saig_build_info gauge\n")
	fmt.Fprintf(w, "saig_build_info{version=%q,commit=%q,go=%q} 1\n",
		metricLabel(buildinfo.Version), metricLabel(buildinfo.Commit), metricLabel(buildinfo.GoVersion))
	fmt.Fprintf(w, "# HELP saig_db_up 数据库可达\n# TYPE saig_db_up gauge\nsaig_db_up %d\n", dbUp)
	fmt.Fprintf(w, "# HELP saig_redis_up Redis 可达（未启用=0）\n# TYPE saig_redis_up gauge\nsaig_redis_up %d\n", redisUp)
	fmt.Fprintf(w, "# HELP saig_goroutines 当前 goroutine 数\n# TYPE saig_goroutines gauge\nsaig_goroutines %d\n", runtime.NumGoroutine())
	fmt.Fprintf(w, "# HELP saig_memory_alloc_bytes 进程已分配字节\n# TYPE saig_memory_alloc_bytes gauge\nsaig_memory_alloc_bytes %d\n", mem.Alloc)
	fmt.Fprintf(w, "# HELP saig_memory_heap_inuse_bytes 堆在用字节\n# TYPE saig_memory_heap_inuse_bytes gauge\nsaig_memory_heap_inuse_bytes %d\n", mem.HeapInuse)

	fmt.Fprintf(w, "# HELP saig_requests_today_total 今日请求数\n# TYPE saig_requests_today_total gauge\nsaig_requests_today_total %d\n", ops.Requests)
	fmt.Fprintf(w, "# HELP saig_errors_today_total 今日 4xx/5xx 请求数\n# TYPE saig_errors_today_total gauge\nsaig_errors_today_total %d\n", ops.Errors)
	fmt.Fprintf(w, "# HELP saig_tokens_today_total 今日 token 数\n# TYPE saig_tokens_today_total gauge\nsaig_tokens_today_total %d\n", kpi.PromptTokens+kpi.CompletionTokens)
	fmt.Fprintf(w, "# HELP saig_cost_micros_today 今日成本（USD micros）\n# TYPE saig_cost_micros_today gauge\nsaig_cost_micros_today %d\n", kpi.CostMicros)
	fmt.Fprintf(w, "# HELP saig_avg_latency_ms_today 今日平均延迟(ms)\n# TYPE saig_avg_latency_ms_today gauge\nsaig_avg_latency_ms_today %d\n", ops.AvgLatencyMs)
	fmt.Fprintf(w, "# HELP saig_avg_first_token_ms_today 今日平均首 Token 延迟(ms，仅流式)\n# TYPE saig_avg_first_token_ms_today gauge\nsaig_avg_first_token_ms_today %d\n", ops.AvgFirstTokenMs)
	if usageErr {
		fmt.Fprintf(w, "# HELP saig_metrics_internal_errors_total 指标采集内部错误\n# TYPE saig_metrics_internal_errors_total counter\nsaig_metrics_internal_errors_total 1\n")
	}
}

// metricLabel 转义 Prometheus label value 中的反斜杠、双引号、换行。
func metricLabel(s string) string {
	s = strings.ReplaceAll(s, `\`, `\\`)
	s = strings.ReplaceAll(s, `"`, `\"`)
	return strings.ReplaceAll(s, "\n", `\n`)
}
