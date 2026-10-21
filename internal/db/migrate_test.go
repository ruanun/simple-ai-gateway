package db

import (
	"testing"

	"github.com/ruanun/simple-ai-gateway/internal/dbtest"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestMigrateIsIdempotent(t *testing.T) {
	g := dbtest.OpenInMemory(t)
	reg := []Migration{{
		ID: "0001_demo",
		Up: func(tx *gorm.DB) error {
			return tx.Exec("CREATE TABLE IF NOT EXISTS demo(id INTEGER PRIMARY KEY)").Error
		},
	}}
	require.NoError(t, Migrate(g, reg))
	require.NoError(t, Migrate(g, reg)) // 第二次不报错
	var applied int64
	g.Table("schema_migrations").Count(&applied)
	require.Equal(t, int64(1), applied)
}
