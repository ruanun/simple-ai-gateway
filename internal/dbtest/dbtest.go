// Package dbtest 提供测试用的数据库辅助函数，仅在测试中引用，避免把 testing 链接进生产二进制。
package dbtest

import (
	"fmt"
	"strings"
	"testing"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// OpenInMemory 用于测试。
// 每个测试使用以测试名派生的独立 shared-cache 命名，既保留多连接共享同一内存库的能力，
// 又避免不同测试之间通过进程级 `file::memory:?cache=shared` 互相污染数据。
func OpenInMemory(t *testing.T) *gorm.DB {
	t.Helper()
	// 用测试名构造唯一的命名内存库；非法字符替换为下划线。
	name := strings.NewReplacer("/", "_", " ", "_", ":", "_").Replace(t.Name())
	dsn := fmt.Sprintf("file:memdb_%s?mode=memory&cache=shared", name)
	g, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatalf("open memory db: %v", err)
	}
	return g
}
