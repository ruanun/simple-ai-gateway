package cleanup

import (
	"log/slog"
	"time"

	"github.com/ruanun/simple-ai-gateway/internal/audit"
	"github.com/ruanun/simple-ai-gateway/internal/domain/requestlog"
	"github.com/ruanun/simple-ai-gateway/internal/domain/setting"
	"github.com/ruanun/simple-ai-gateway/internal/model"
	"gorm.io/gorm"
)

// Cleaner 定时清理过期审计/请求日志/管理操作日志。参照 usage.Aggregator 的 ticker+goroutine 模式。
type Cleaner struct {
	audit   *audit.Service
	logs    *requestlog.Service
	setting *setting.Service
	g       *gorm.DB // 操作日志清理直连（P9-A，无独立 service）
	ticker  *time.Ticker
	stop    chan struct{}
	now     func() time.Time
}

// NewCleaner 创建并启动定时清理(每 interval 跑一次,启动即跑一次)。
func NewCleaner(a *audit.Service, l *requestlog.Service, s *setting.Service, g *gorm.DB, interval time.Duration) *Cleaner {
	c := &Cleaner{audit: a, logs: l, setting: s, g: g, ticker: time.NewTicker(interval), stop: make(chan struct{}), now: time.Now}
	go c.run()
	return c
}

// NewCleanerStatic 不启动 goroutine,供测试手动调 RunOnce。
func NewCleanerStatic(a *audit.Service, l *requestlog.Service, s *setting.Service, g *gorm.DB) *Cleaner {
	return &Cleaner{audit: a, logs: l, setting: s, g: g, stop: make(chan struct{}), now: time.Now}
}

func (c *Cleaner) run() {
	c.RunOnce()
	for {
		select {
		case <-c.ticker.C:
			c.RunOnce()
		case <-c.stop:
			c.ticker.Stop()
			return
		}
	}
}

// RunOnce 读设置,尊重开关,按保留天数删除过期记录,返回各自删除条数。
func (c *Cleaner) RunOnce() (auditDeleted, logDeleted int64) {
	s, err := c.setting.Get()
	if err != nil {
		slog.Error("cleanup: 读取设置失败", "err", err)
		return 0, 0
	}
	now := c.now()
	if s.AuditCleanupEnabled {
		n, err := c.audit.Cleanup(now.AddDate(0, 0, -s.AuditRetentionDays))
		if err != nil {
			slog.Error("cleanup: 审计清理失败", "err", err)
		}
		auditDeleted = n
	}
	// 幂等记录 24h 过期，与请求日志保留期无关，始终清理。
	if c.g != nil {
		if err := c.g.Where("expires_at < ?", now).Delete(&model.IdempotencyRecord{}).Error; err != nil {
			slog.Error("cleanup: 幂等记录清理失败", "err", err)
		}
	}
	if s.RequestLogCleanupEnabled {
		n, err := c.logs.DeleteBefore(now.AddDate(0, 0, -s.RequestLogRetentionDays))
		if err != nil {
			slog.Error("cleanup: 请求日志清理失败", "err", err)
		}
		logDeleted = n
		// 管理操作日志跟随请求日志保留期，无独立开关（P9-A 决策）
		if c.g != nil {
			if err := c.g.Where("created_at < ?", now.AddDate(0, 0, -s.RequestLogRetentionDays)).
				Delete(&model.AdminOpLog{}).Error; err != nil {
				slog.Error("cleanup: 操作日志清理失败", "err", err)
			}
		}
	}
	return
}

// Stop 停止定时任务(优雅退出用)。
func (c *Cleaner) Stop() { close(c.stop) }
