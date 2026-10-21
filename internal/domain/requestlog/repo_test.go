package requestlog

import (
	"testing"
	"time"

	"github.com/ruanun/simple-ai-gateway/internal/dbtest"
	"github.com/ruanun/simple-ai-gateway/internal/model"
)

func TestDeleteBefore(t *testing.T) {
	g := dbtest.OpenInMemory(t)
	if err := g.AutoMigrate(&model.RequestLog{}); err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	old := model.RequestLog{Model: "old"}
	recent := model.RequestLog{Model: "recent"}
	g.Create(&old)
	g.Create(&recent)
	// 把 old 的 created_at 改到 10 天前
	g.Model(&old).Update("created_at", now.AddDate(0, 0, -10))

	repo := NewRepo(g)
	n, err := repo.DeleteBefore(now.AddDate(0, 0, -5))
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("应删除 1 条,实际 %d", n)
	}
	var remaining int64
	g.Model(&model.RequestLog{}).Count(&remaining)
	if remaining != 1 {
		t.Fatalf("应剩 1 条,实际 %d", remaining)
	}
}
