package model

import (
	"github.com/ruanun/simple-ai-gateway/internal/db"
	"golang.org/x/crypto/bcrypt"
	"gorm.io/gorm"
)

// Migrations 返回迁移注册列表。
//
// 这里只保留一条干净的基线迁移：
//   - 全新库：0001 用当前 struct 一次性 AutoMigrate 建全表、补建 request_logs.created_at
//     索引并播种管理员/系统设置。
//   - 已有部署库：不再提供逐版本增量迁移。升级前先备份数据库，清空
//     schema_migrations 表（或直接重建库），再启动新版本；0001 会重新跑一遍
//     AutoMigrate 补齐当前 schema。
//
// 新增 schema 变更时，同样只改 0001 的 AutoMigrate 列表（项目尚未发布首个版本，
// 不维护跨版本迁移兼容）。
func Migrations(adminUser, adminPass string) []db.Migration {
	return []db.Migration{
		{
			ID: "0001_init_schema",
			Up: func(tx *gorm.DB) error {
				if err := tx.AutoMigrate(
					&Channel{}, &ChannelModel{},
					&Account{}, &Proxy{},
					&App{}, &AppMember{}, &APIKey{}, &KeyTag{}, &KeyTagRelation{},
					&ModelAlias{}, &AliasRoute{}, &ModelPrice{},
					&RequestLog{}, &AuditRecord{},
					&UsageDaily{}, &UsageHourly{}, &UsageDailyKey{}, &UsageDailyChannel{},
					&IdempotencyRecord{},
					&AdminUser{}, &SystemSetting{},
					&PortalUser{}, &AdminOpLog{},
				); err != nil {
					return err
				}
				// request_logs.created_at 索引：聚合器每分钟、仪表盘每次查询按时间范围过滤。
				// created_at 在共享 BaseModel 上，不能加 gorm index 标签（会给所有表建索引），故裸 SQL。
				if !tx.Migrator().HasIndex(&RequestLog{}, "idx_request_logs_created_at") {
					if err := tx.Exec("CREATE INDEX idx_request_logs_created_at ON request_logs(created_at)").Error; err != nil {
						return err
					}
				}
				// 播种管理员（role 随列默认值 super）；已有管理员不会被覆盖。
				h, err := bcrypt.GenerateFromPassword([]byte(adminPass), bcrypt.DefaultCost)
				if err != nil {
					return err
				}
				if err := tx.Where("username = ?", adminUser).FirstOrCreate(&AdminUser{
					Username:     adminUser,
					PasswordHash: string(h),
				}).Error; err != nil {
					return err
				}
				// 播种 SystemSetting 单例（id 自增为 1）。
				var n int64
				tx.Model(&SystemSetting{}).Count(&n)
				if n == 0 {
					return tx.Create(&SystemSetting{
						AuditRetentionDays:      30,
						RequestLogRetentionDays: 90,
					}).Error
				}
				return nil
			},
		},
	}
}
