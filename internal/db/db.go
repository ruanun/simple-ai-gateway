package db

import (
	"time"

	"github.com/glebarez/sqlite"
	"github.com/ruanun/simple-ai-gateway/internal/config"
	"gorm.io/driver/mysql"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// Open 根据 cfg.Driver 选择数据库驱动打开连接，支持 sqlite / mysql / postgres。
// 未知 Driver 时回退到 sqlite。
func Open(cfg config.DatabaseConfig) (*gorm.DB, error) {
	var dialector gorm.Dialector
	switch cfg.Driver {
	case "mysql":
		dialector = mysql.Open(cfg.DSN)
	case "postgres":
		dialector = postgres.Open(cfg.DSN)
	default:
		// sqlite：先自检数据目录（建目录 + 探针写），把权限问题在这里翻译成人话；
		// 否则驱动只会抛出 `out of memory (14)` 这类 SQLITE_CANTOPEN 包装错误，误导排查方向。
		if dir := sqliteDataDir(cfg.DSN); dir != "" {
			if err := preflightSQLiteDir(dir); err != nil {
				return nil, err
			}
		}
		dialector = sqlite.Open(cfg.DSN)
	}
	g, err := gorm.Open(dialector, &gorm.Config{Logger: logger.Default.LogMode(logger.Error)})
	if err != nil {
		return nil, err
	}
	// 连接池参数仅对 mysql/postgres 生效
	if cfg.Driver == "mysql" || cfg.Driver == "postgres" {
		sqlDB, err := g.DB()
		if err == nil {
			if cfg.MaxOpenConns > 0 {
				sqlDB.SetMaxOpenConns(cfg.MaxOpenConns)
			}
			if cfg.MaxIdleConns > 0 {
				sqlDB.SetMaxIdleConns(cfg.MaxIdleConns)
			}
			if cfg.ConnMaxLifetime > 0 {
				sqlDB.SetConnMaxLifetime(time.Duration(cfg.ConnMaxLifetime) * time.Second)
			}
		}
	}
	return g, nil
}
