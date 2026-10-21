package db_test

import (
	"os"
	"testing"

	"github.com/ruanun/simple-ai-gateway/internal/config"
	"github.com/ruanun/simple-ai-gateway/internal/db"
	"github.com/ruanun/simple-ai-gateway/internal/model"
	"github.com/stretchr/testify/require"
)

// 方言实测（roadmap 2.5「迁移多方言实测」）：单条基线迁移在真实 MySQL/PostgreSQL 上跑通。
// 环境变量门控（沿用 breaker 包 TEST_REDIS_ADDR 先例），未设置时跳过，日常套件零依赖：
//
//	docker run -d --rm --name saig-test-mysql -e MYSQL_ROOT_PASSWORD=saigtest -e MYSQL_DATABASE=saig -p 13306:3306 mysql:8
//	docker run -d --rm --name saig-test-pg -e POSTGRES_PASSWORD=saigtest -e POSTGRES_DB=saig -p 15432:5432 postgres:16-alpine
//	TEST_MYSQL_DSN='root:saigtest@tcp(127.0.0.1:13306)/saig?charset=utf8mb4&parseTime=True&loc=Local' \
//	TEST_PG_DSN='host=127.0.0.1 port=15432 user=postgres password=saigtest dbname=saig sslmode=disable' \
//	go test ./internal/db/ -run TestMigrateDialect -v
func TestMigrateDialectMySQL(t *testing.T) {
	dsn := os.Getenv("TEST_MYSQL_DSN")
	if dsn == "" {
		t.Skip("未设置 TEST_MYSQL_DSN，跳过 MySQL 方言实测")
	}
	runDialectSuite(t, "mysql", dsn)
}

func TestMigrateDialectPostgres(t *testing.T) {
	dsn := os.Getenv("TEST_PG_DSN")
	if dsn == "" {
		t.Skip("未设置 TEST_PG_DSN，跳过 PostgreSQL 方言实测")
	}
	runDialectSuite(t, "postgres", dsn)
}

func runDialectSuite(t *testing.T, driver, dsn string) {
	t.Helper()
	g, err := db.Open(config.DatabaseConfig{Driver: driver, DSN: dsn})
	require.NoError(t, err, "打开 %s 连接失败", driver)

	// 清库保证每次都是「全新建库」验证（测试库可弃置；重跑不受上次残留影响）。
	require.NoError(t, g.Migrator().DropTable(
		&model.Channel{}, &model.ChannelModel{},
		&model.Account{}, &model.Proxy{},
		&model.App{}, &model.AppMember{}, &model.APIKey{}, &model.KeyTag{}, &model.KeyTagRelation{},
		&model.ModelAlias{}, &model.AliasRoute{}, &model.ModelPrice{},
		&model.RequestLog{}, &model.AuditRecord{},
		&model.UsageDaily{}, &model.UsageHourly{}, &model.UsageDailyKey{}, &model.UsageDailyChannel{},
		&model.IdempotencyRecord{},
		&model.AdminUser{}, &model.SystemSetting{},
		&model.PortalUser{}, &model.AdminOpLog{},
		"schema_migrations",
	), "清库失败")

	// —— 建库：单条基线迁移 ——
	regs := model.Migrations("admin", "Dialect-Test-Pass-1234")
	require.Len(t, regs, 1)
	require.NoError(t, db.Migrate(g, regs), "%s 迁移失败", driver)
	require.NoError(t, db.Migrate(g, regs), "%s 幂等重跑失败", driver)

	for _, tbl := range []any{
		&model.Channel{}, &model.Account{}, &model.APIKey{}, &model.RequestLog{},
		&model.UsageDailyKey{}, &model.UsageDailyChannel{}, &model.IdempotencyRecord{},
		&model.AuditRecord{}, &model.PortalUser{}, &model.AdminOpLog{},
	} {
		require.True(t, g.Migrator().HasTable(tbl), "%s 缺表 %T", driver, tbl)
	}
	require.True(t, g.Migrator().HasIndex(&model.RequestLog{}, "idx_request_logs_created_at"),
		"%s 缺 request_logs.created_at 索引", driver)
	var adminCnt int64
	g.Model(&model.AdminUser{}).Where("username = ?", "admin").Count(&adminCnt)
	require.Equal(t, int64(1), adminCnt, "管理员应已播种")

	// 关键新增列在各方言下都要落地（防止只测 SQLite 漏掉 MySQL/PG 的 DDL 差异）。
	for _, col := range []string{"disabled_by_admin"} {
		require.True(t, g.Migrator().HasColumn(&model.APIKey{}, col), "%s api_keys 缺 %s", driver, col)
	}
	for _, col := range []string{"max_concurrency", "rpm_limit", "expires_at", "temp_unschedulable_until", "temp_unschedulable_reason", "rate_limit_reset_at", "overload_until"} {
		require.True(t, g.Migrator().HasColumn(&model.Account{}, col), "%s accounts 缺 %s", driver, col)
	}
	for _, col := range []string{"model_whitelist", "max_concurrency"} {
		require.True(t, g.Migrator().HasColumn(&model.App{}, col), "%s apps 缺 %s", driver, col)
	}
	for _, col := range []string{"error_passthrough"} {
		require.True(t, g.Migrator().HasColumn(&model.Channel{}, col), "%s channels 缺 %s", driver, col)
	}
	for _, col := range []string{"captcha_type", "turnstile_site_key", "turnstile_secret_key", "oidc_redirect_url"} {
		require.True(t, g.Migrator().HasColumn(&model.SystemSetting{}, col), "%s system_settings 缺 %s", driver, col)
	}
	for _, col := range []string{"request_key", "response_key", "request_sha256", "response_sha256"} {
		require.True(t, g.Migrator().HasColumn(&model.AuditRecord{}, col), "%s audit_records 缺 %s", driver, col)
	}
}
