package backup

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/ruanun/simple-ai-gateway/internal/config"
	"github.com/ruanun/simple-ai-gateway/internal/db"
	"github.com/ruanun/simple-ai-gateway/internal/model"
	"github.com/stretchr/testify/require"
)

func TestCreateAndRestoreSQLite(t *testing.T) {
	root := t.TempDir()
	dbPath := filepath.Join(root, "data", "saig.db")
	auditDir := filepath.Join(root, "audit")
	require.NoError(t, os.MkdirAll(auditDir, 0o700))
	require.NoError(t, os.MkdirAll(filepath.Join(auditDir, "2026", "01", "01"), 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(auditDir, "2026", "01", "01", "r.gz"), []byte("audit"), 0o600))

	cfg := config.Config{}
	cfg.Database.Driver = "sqlite"
	cfg.Database.DSN = dbPath
	cfg.Storage.Local.Root = auditDir
	cfg.Admin.Username = "admin"
	cfg.Admin.Password = "sag12345"

	g, err := db.Open(cfg.Database)
	require.NoError(t, err)
	require.NoError(t, db.Migrate(g, model.Migrations(cfg.Admin.Username, cfg.Admin.Password)))
	require.NoError(t, g.Create(&model.Channel{Name: "before", Type: "openai", Status: 1}).Error)
	sqlDB, err := g.DB()
	require.NoError(t, err)
	require.NoError(t, sqlDB.Close())

	outDir := filepath.Join(root, "backup")
	file, err := Create(cfg, outDir)
	require.NoError(t, err)
	require.FileExists(t, file)
	require.FileExists(t, filepath.Join(outDir, "audit", "2026", "01", "01", "r.gz"))

	// 备份后新增一行，恢复后应只剩备份时点的一行。
	g, err = db.Open(cfg.Database)
	require.NoError(t, err)
	require.NoError(t, g.Create(&model.Channel{Name: "after", Type: "openai", Status: 1}).Error)
	sqlDB, err = g.DB()
	require.NoError(t, err)
	require.NoError(t, sqlDB.Close())

	require.NoError(t, Restore(cfg, outDir))
	g, err = db.Open(cfg.Database)
	require.NoError(t, err)
	defer func() { sqlDB, _ := g.DB(); _ = sqlDB.Close() }()
	var count int64
	require.NoError(t, g.Model(&model.Channel{}).Where("name = ?", "after").Count(&count).Error)
	require.Equal(t, int64(0), count, "恢复后不应包含备份之后写入的数据")
	require.NoError(t, g.Model(&model.Channel{}).Where("name = ?", "before").Count(&count).Error)
	require.Equal(t, int64(1), count)
}
