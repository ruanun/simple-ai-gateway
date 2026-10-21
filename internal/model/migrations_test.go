package model

import (
	"testing"

	"github.com/ruanun/simple-ai-gateway/internal/db"
	"github.com/ruanun/simple-ai-gateway/internal/dbtest"
	"github.com/stretchr/testify/require"
	"golang.org/x/crypto/bcrypt"
)

// auditRecordColumns 是审计正文外置后 audit_records 的「对象 key + 取证元数据」列。
var auditRecordColumns = []string{
	"request_key", "request_size", "request_orig_size", "request_truncated", "request_sha256",
	"response_key", "response_size", "response_orig_size", "response_truncated", "response_sha256",
}

func TestInitSchemaMigration(t *testing.T) {
	g := dbtest.OpenInMemory(t)
	regs := Migrations("admin", "sag12345")
	require.Len(t, regs, 1, "只保留一条干净的基线迁移")
	require.Equal(t, "0001_init_schema", regs[0].ID)

	require.NoError(t, db.Migrate(g, regs))
	// 幂等
	require.NoError(t, db.Migrate(g, regs))

	// 所有业务表存在
	tables := []interface{}{
		&Channel{}, &ChannelModel{}, &Account{}, &Proxy{},
		&App{}, &AppMember{}, &APIKey{}, &KeyTag{}, &KeyTagRelation{},
		&ModelAlias{}, &AliasRoute{}, &ModelPrice{},
		&RequestLog{}, &AuditRecord{},
		&UsageDaily{}, &UsageHourly{}, &UsageDailyKey{}, &UsageDailyChannel{},
		&IdempotencyRecord{},
		&AdminUser{}, &SystemSetting{},
		&PortalUser{}, &AdminOpLog{},
	}
	for _, tbl := range tables {
		require.True(t, g.Migrator().HasTable(tbl), "table should exist: %T", tbl)
	}

	// 管理员已种子（status/role 随列默认值）
	var users []AdminUser
	require.NoError(t, g.Where("username = ?", "admin").Find(&users).Error)
	require.Len(t, users, 1)
	require.NoError(t, bcrypt.CompareHashAndPassword([]byte(users[0].PasswordHash), []byte("sag12345")))
	require.Equal(t, int8(1), users[0].Status, "种子管理员应默认启用")
	require.Equal(t, RoleSuper, users[0].Role, "种子管理员应默认 super")

	// P9 关键列（显式列名防 GORM 把 OIDC 拆成 o_id_c_*）
	require.True(t, g.Migrator().HasColumn(&App{}, "owner_user_id"), "App 应有归属列")
	require.True(t, g.Migrator().HasColumn(&PortalUser{}, "oidc_subject"), "门户用户应有 oidc_subject 显式列")
	require.True(t, g.Migrator().HasColumn(&SystemSetting{}, "oidc_enabled"), "设置应有 OIDC 列")

	// 出站请求头治理列存在
	require.True(t, g.Migrator().HasColumn(&Channel{}, "auth_style"), "channels 应有 auth_style 列")
	require.True(t, g.Migrator().HasColumn(&Channel{}, "custom_headers"), "channels 应有 custom_headers 列")

	// 渠道 thinking 剥离 / 流式 usage / 错误透传开关列存在
	require.True(t, g.Migrator().HasColumn(&Channel{}, "strip_thinking"), "channels 应有 strip_thinking 列")
	require.True(t, g.Migrator().HasColumn(&Channel{}, "disable_stream_usage"), "channels 应有 disable_stream_usage 列")
	require.True(t, g.Migrator().HasColumn(&Channel{}, "error_passthrough"), "channels 应有 error_passthrough 列")

	// 审计正文外置：audit_records 使用对象 key + 元数据列
	for _, col := range auditRecordColumns {
		require.True(t, g.Migrator().HasColumn(&AuditRecord{}, col), "audit_records 应有 %s 列", col)
	}

	// 账号级调度列存在
	for _, col := range []string{"max_concurrency", "rpm_limit", "expires_at", "temp_unschedulable_until", "temp_unschedulable_reason", "rate_limit_reset_at", "overload_until"} {
		require.True(t, g.Migrator().HasColumn(&Account{}, col), "accounts 应有 %s 列", col)
	}

	// JWT 撤销的 token_version 列存在（admin + portal 两侧）
	require.True(t, g.Migrator().HasColumn(&AdminUser{}, "token_version"), "admin_users 应有 token_version 列")
	require.True(t, g.Migrator().HasColumn(&PortalUser{}, "token_version"), "portal_users 应有 token_version 列")

	// 复合唯一索引生效：model_prices (channel_id, model)
	require.NoError(t, g.Create(&ModelPrice{Model: "gpt-4o", ChannelID: 0, InputPer1M: 2.5, Enabled: true}).Error)
	require.NoError(t, g.Create(&ModelPrice{Model: "gpt-4o", ChannelID: 1, InputPer1M: 2.0, Enabled: true}).Error)
	require.Error(t, g.Create(&ModelPrice{Model: "gpt-4o", ChannelID: 0, InputPer1M: 9, Enabled: true}).Error)

	// usage_daily_key 有 model 列，三元唯一键允许同 (日期,令牌) 多模型行、拦截完全重复
	require.True(t, g.Migrator().HasColumn(&UsageDailyKey{}, "model"), "usage_daily_key 应有 model 列")
	require.NoError(t, g.Create(&UsageDailyKey{StatDate: "2026-07-01", APIKeyID: 1, Model: "gpt-4o", Requests: 1}).Error)
	require.NoError(t, g.Create(&UsageDailyKey{StatDate: "2026-07-01", APIKeyID: 1, Model: "gpt-4o-mini", Requests: 1}).Error)
	require.Error(t, g.Create(&UsageDailyKey{StatDate: "2026-07-01", APIKeyID: 1, Model: "gpt-4o", Requests: 9}).Error)

	// request_logs.created_at 索引由基线补建
	require.True(t, g.Migrator().HasIndex(&RequestLog{}, "idx_request_logs_created_at"), "request_logs 应有 created_at 索引")

	// SystemSetting 单例已播种默认值
	var ss SystemSetting
	require.NoError(t, g.First(&ss, 1).Error)
	require.Equal(t, 30, ss.AuditRetentionDays)
	require.Equal(t, 90, ss.RequestLogRetentionDays)
	require.False(t, ss.AuditCleanupEnabled)
	require.False(t, ss.RequestLogCleanupEnabled)

	// P12e：缓存/推理明细列存在
	require.NoError(t, g.Create(&RequestLog{Model: "m", CachedTokens: 7, CacheCreationTokens: 3, ReasoningTokens: 5}).Error)
	var rl RequestLog
	require.NoError(t, g.Where("model = ?", "m").First(&rl).Error)
	require.Equal(t, 7, rl.CachedTokens)
	require.Equal(t, 3, rl.CacheCreationTokens)
	require.Equal(t, 5, rl.ReasoningTokens)
}

// TestUsageDailyChannelTable 建表后可写入并读回，表名固定为 usage_daily_channel。
func TestUsageDailyChannelTable(t *testing.T) {
	g := dbtest.OpenInMemory(t)
	require.NoError(t, g.AutoMigrate(&UsageDailyChannel{}))
	require.Equal(t, "usage_daily_channel", UsageDailyChannel{}.TableName())

	row := UsageDailyChannel{
		StatDate: "2026-07-31", ChannelID: 7, Model: "gpt-4o",
		Requests: 10, Errors: 2, CostMicros: 12345,
		AvgLatencyMs: 800, AvgFirstTokenMs: 120,
	}
	require.NoError(t, g.Create(&row).Error)

	var got UsageDailyChannel
	require.NoError(t, g.Where("stat_date = ? AND channel_id = ? AND model = ?", "2026-07-31", 7, "gpt-4o").First(&got).Error)
	require.Equal(t, int64(10), got.Requests)
	require.Equal(t, int64(2), got.Errors)
	require.Equal(t, int64(800), got.AvgLatencyMs)
}

// TestUsageDailyChannelUniqueIndex (日期, 渠道, 模型) 三元唯一：同渠道多模型可共存，完全重复被拦截。
func TestUsageDailyChannelUniqueIndex(t *testing.T) {
	g := dbtest.OpenInMemory(t)
	require.NoError(t, g.AutoMigrate(&UsageDailyChannel{}))
	require.NoError(t, g.Create(&UsageDailyChannel{StatDate: "2026-07-31", ChannelID: 1, Model: "gpt-4o", Requests: 1}).Error)
	require.NoError(t, g.Create(&UsageDailyChannel{StatDate: "2026-07-31", ChannelID: 1, Model: "gpt-4o-mini", Requests: 1}).Error)
	require.NoError(t, g.Create(&UsageDailyChannel{StatDate: "2026-07-31", ChannelID: 2, Model: "gpt-4o", Requests: 1}).Error)
	require.Error(t, g.Create(&UsageDailyChannel{StatDate: "2026-07-31", ChannelID: 1, Model: "gpt-4o", Requests: 9}).Error,
		"同 (日期,渠道,模型) 重复插入应被唯一索引拦截")
}
