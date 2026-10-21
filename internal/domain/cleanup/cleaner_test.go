package cleanup

import (
	"testing"
	"time"

	"github.com/ruanun/simple-ai-gateway/internal/audit"
	"github.com/ruanun/simple-ai-gateway/internal/dbtest"
	"github.com/ruanun/simple-ai-gateway/internal/domain/requestlog"
	"github.com/ruanun/simple-ai-gateway/internal/domain/setting"
	"github.com/ruanun/simple-ai-gateway/internal/model"
	"github.com/ruanun/simple-ai-gateway/internal/storage"
	"gorm.io/gorm"
)

func setup(t *testing.T) (*gorm.DB, *Cleaner, *setting.Service) {
	g := dbtest.OpenInMemory(t)
	if err := g.AutoMigrate(&model.RequestLog{}, &model.AuditRecord{}, &model.SystemSetting{}, &model.AdminOpLog{}); err != nil {
		t.Fatal(err)
	}
	setSvc := setting.NewService(setting.NewRepo(g))
	c := NewCleanerStatic(audit.NewService(g, storage.NewMemory()), requestlog.NewService(requestlog.NewRepo(g)), setSvc, g)
	return g, c, setSvc
}

// mkOld 造一条 N 天前的请求日志。
func mkOld(g *gorm.DB, days int) {
	lg := model.RequestLog{Model: "x"}
	g.Create(&lg)
	g.Model(&lg).Update("created_at", time.Now().AddDate(0, 0, -days))
}

// mkOldAudit 造一条 N 天前的审计记录（正文已外置，行内只需 key 占位）。reqID 需唯一(AuditRecord.RequestID 是唯一非空索引)。
func mkOldAudit(g *gorm.DB, reqID string, days int) {
	ar := model.AuditRecord{RequestID: reqID, RequestKey: "audit/x/" + reqID + "/request.gz", ResponseKey: "audit/x/" + reqID + "/response.gz"}
	g.Create(&ar)
	g.Model(&ar).Update("created_at", time.Now().AddDate(0, 0, -days))
}

func TestRunOnceRespectsDisabled(t *testing.T) {
	g, c, setSvc := setup(t)
	mkOld(g, 100)
	_ = setSvc.Update(&model.SystemSetting{RequestLogCleanupEnabled: false, RequestLogRetentionDays: 90})
	c.RunOnce()
	var n int64
	g.Model(&model.RequestLog{}).Count(&n)
	if n != 1 {
		t.Fatalf("开关关闭不应删除,实际剩 %d", n)
	}
}

func TestRunOnceDeletesExpiredLogs(t *testing.T) {
	g, c, setSvc := setup(t)
	mkOld(g, 100) // 早于 90 天,应删
	mkOld(g, 10)  // 未过期,保留
	_ = setSvc.Update(&model.SystemSetting{RequestLogCleanupEnabled: true, RequestLogRetentionDays: 90})
	_, logDeleted := c.RunOnce()
	if logDeleted != 1 {
		t.Fatalf("应删 1 条,实际 %d", logDeleted)
	}
	var n int64
	g.Model(&model.RequestLog{}).Count(&n)
	if n != 1 {
		t.Fatalf("应剩 1 条,实际 %d", n)
	}
}

func TestRunOnceDeletesExpiredAudits(t *testing.T) {
	g, c, setSvc := setup(t)
	mkOldAudit(g, "a1", 100) // 早于 90 天,应删
	mkOldAudit(g, "a2", 10)  // 未过期,保留
	_ = setSvc.Update(&model.SystemSetting{AuditCleanupEnabled: true, AuditRetentionDays: 90})
	auditDeleted, _ := c.RunOnce()
	if auditDeleted != 1 {
		t.Fatalf("应删 1 条审计,实际 %d", auditDeleted)
	}
	var n int64
	g.Model(&model.AuditRecord{}).Count(&n)
	if n != 1 {
		t.Fatalf("应剩 1 条审计,实际 %d", n)
	}
}
