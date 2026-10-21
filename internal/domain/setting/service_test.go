package setting

import (
	"testing"

	"github.com/ruanun/simple-ai-gateway/internal/dbtest"
	"github.com/ruanun/simple-ai-gateway/internal/model"
)

func newSvc(t *testing.T) *Service {
	g := dbtest.OpenInMemory(t)
	if err := g.AutoMigrate(&model.SystemSetting{}); err != nil {
		t.Fatal(err)
	}
	return NewService(NewRepo(g))
}

func TestGetReturnsDefaultWhenEmpty(t *testing.T) {
	s := newSvc(t)
	got, err := s.Get()
	if err != nil {
		t.Fatal(err)
	}
	if got.AuditRetentionDays != 30 || got.RequestLogRetentionDays != 90 {
		t.Fatalf("默认值不符: %+v", got)
	}
}

func TestUpdateRoundTrip(t *testing.T) {
	s := newSvc(t)
	err := s.Update(&model.SystemSetting{
		AuditCleanupEnabled: true, AuditRetentionDays: 15,
		RequestLogCleanupEnabled: true, RequestLogRetentionDays: 45,
	})
	if err != nil {
		t.Fatal(err)
	}
	got, _ := s.Get()
	if !got.AuditCleanupEnabled || got.AuditRetentionDays != 15 || got.RequestLogRetentionDays != 45 {
		t.Fatalf("往返不符: %+v", got)
	}
}

func TestUpdateClampsRetentionFloor(t *testing.T) {
	s := newSvc(t)
	_ = s.Update(&model.SystemSetting{AuditRetentionDays: 0, RequestLogRetentionDays: -5})
	got, _ := s.Get()
	if got.AuditRetentionDays != 1 || got.RequestLogRetentionDays != 1 {
		t.Fatalf("天数下限未归一: %+v", got)
	}
}

func TestUpdatePreservesCreatedAt(t *testing.T) {
	s := newSvc(t)
	_ = s.Update(&model.SystemSetting{AuditRetentionDays: 30, RequestLogRetentionDays: 90})
	first, _ := s.Get()
	_ = s.Update(&model.SystemSetting{AuditRetentionDays: 20, RequestLogRetentionDays: 80})
	second, _ := s.Get()
	if second.CreatedAt.IsZero() || !second.CreatedAt.Equal(first.CreatedAt) {
		t.Fatalf("CreatedAt 应保留: first=%v second=%v", first.CreatedAt, second.CreatedAt)
	}
}
