package config

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestLoadDefaults(t *testing.T) {
	cfg, err := Load("")
	require.NoError(t, err)
	require.Equal(t, 8080, cfg.Server.Port)
	require.Equal(t, "sqlite", cfg.Database.Driver)
	require.Equal(t, "sk-saig", cfg.Security.APIKeyPrefix)
}

func TestLoadFromFile(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "config.yaml")
	require.NoError(t, os.WriteFile(p, []byte("server:\n  port: 9999\n"), 0o644))
	cfg, err := Load(p)
	require.NoError(t, err)
	require.Equal(t, 9999, cfg.Server.Port)
}

// TestLoadMalformedYAMLErrors 验证配置文件语法错误时报错拒绝，而非静默回退默认。
func TestLoadMalformedYAMLErrors(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "config.yaml")
	// 非法 YAML（值处出现无法解析的制表符缩进结构）
	require.NoError(t, os.WriteFile(p, []byte("server:\n\tport: bad: : :\n"), 0o644))
	_, err := Load(p)
	require.Error(t, err)
}

// TestLoadMissingExplicitFileErrors 验证显式指定但不存在的配置文件报错。
func TestLoadMissingExplicitFileErrors(t *testing.T) {
	_, err := Load(filepath.Join(t.TempDir(), "nope.yaml"))
	require.Error(t, err)
}

// TestDefaultsIncludeRedis 验证默认配置包含正确的 Redis 初始值。
func TestDefaultsIncludeRedis(t *testing.T) {
	cfg := defaults()
	require.False(t, cfg.Redis.Enabled)
	require.Equal(t, "127.0.0.1:6379", cfg.Redis.Addr)
	require.Equal(t, 0, cfg.Redis.DB)
}

// TestCheckProductionSafetyDefaults 验证默认配置（内置口令/密钥）被判定为不可安全启动：
// 默认 admin 口令、默认 JWT secret、默认 AES key 三项都应被拦下。
func TestCheckProductionSafetyDefaults(t *testing.T) {
	issues := defaults().CheckProductionSafety()
	require.Len(t, issues, 3)
}

// TestCheckProductionSafetyEmpty 验证空口令/密钥同样被判定为不安全（不能靠留空绕过护栏）。
func TestCheckProductionSafetyEmpty(t *testing.T) {
	cfg := defaults()
	cfg.JWT.Secret = ""
	cfg.Security.ChannelKeyAESKey = ""
	cfg.Admin.Password = ""
	require.Len(t, cfg.CheckProductionSafety(), 3)
}

// TestCheckProductionSafetyOK 验证三项都覆盖为强值后放行（返回空）。
func TestCheckProductionSafetyOK(t *testing.T) {
	cfg := defaults()
	cfg.JWT.Secret = "a-very-long-random-production-secret-value"
	cfg.Security.ChannelKeyAESKey = "fedcba9876543210fedcba9876543210" // 32 字节且非默认
	cfg.Admin.Password = "S0me-Str0ng-Passw0rd!"
	require.Empty(t, cfg.CheckProductionSafety())
}

// 回归：配置模板原样照抄（只改了 AES 长度问题之外什么都没改）必须拒启。
// 曾经模板写 __CHANGE_ME__ 而护栏只认内置默认值，照文档部署即可用公开占位符伪造 super JWT。
func TestCheckProductionSafetyRejectsExampleTemplate(t *testing.T) {
	cfg, err := Load(filepath.Join("..", "..", "config.example.yaml"))
	require.NoError(t, err)
	issues := cfg.CheckProductionSafety()
	require.Len(t, issues, 3, "模板里三项凭证都是占位符，须全部拦下: %v", issues)
}

// 占位符变体、纯空白、弱值同样拒启。
func TestCheckProductionSafetyWeakValues(t *testing.T) {
	strong := func() Config {
		cfg := defaults()
		cfg.JWT.Secret = "a-very-long-random-production-secret-value"
		cfg.Security.ChannelKeyAESKey = "fedcba9876543210fedcba9876543210"
		cfg.Admin.Password = "S0me-Str0ng-Passw0rd!"
		return cfg
	}
	cases := []struct {
		name string
		mut  func(*Config)
	}{
		{"jwt 模板占位符", func(c *Config) { c.JWT.Secret = "__CHANGE_ME__" }},
		{"jwt 占位符小写变体", func(c *Config) { c.JWT.Secret = "please-changeme-to-a-long-random-string-xx" }},
		{"jwt 纯空白", func(c *Config) { c.JWT.Secret = "   " }},
		{"jwt 过短", func(c *Config) { c.JWT.Secret = "short-secret" }},
		{"jwt 默认值加空白", func(c *Config) { c.JWT.Secret = " change-me-dev-secret " }},
		{"aes 模板占位符", func(c *Config) { c.Security.ChannelKeyAESKey = "__CHANGE_ME_TO_16_24_OR_32_BYTES__" }},
		{"admin 模板占位符", func(c *Config) { c.Admin.Password = "__CHANGE_ME__" }},
		{"admin 过短", func(c *Config) { c.Admin.Password = "abc" }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := strong()
			tc.mut(&cfg)
			require.Len(t, cfg.CheckProductionSafety(), 1)
		})
	}
	require.Empty(t, strong().CheckProductionSafety(), "强值基线须放行")
}

// TestLoadYAMLWithRedis 验证 YAML 中的 Redis 和数据库连接池字段能正确解析。
func TestLoadYAMLWithRedis(t *testing.T) {
	yaml := `
database:
  driver: postgres
  dsn: "host=localhost user=pg dbname=saig"
  max_open_conns: 20
  max_idle_conns: 5
  conn_max_lifetime: 300
redis:
  enabled: true
  addr: "192.168.1.10:6379"
  password: "secret"
  db: 1
`
	dir := t.TempDir()
	p := filepath.Join(dir, "config.yaml")
	require.NoError(t, os.WriteFile(p, []byte(yaml), 0o644))

	cfg, err := Load(p)
	require.NoError(t, err)

	// 数据库驱动与连接池
	require.Equal(t, "postgres", cfg.Database.Driver)
	require.Equal(t, 20, cfg.Database.MaxOpenConns)
	require.Equal(t, 5, cfg.Database.MaxIdleConns)
	require.Equal(t, 300, cfg.Database.ConnMaxLifetime)

	// Redis 配置
	require.True(t, cfg.Redis.Enabled)
	require.Equal(t, "192.168.1.10:6379", cfg.Redis.Addr)
	require.Equal(t, "secret", cfg.Redis.Password)
	require.Equal(t, 1, cfg.Redis.DB)
}

func TestResilienceDefaults(t *testing.T) {
	c := defaults()
	require.Equal(t, 5, c.Resilience.Breaker.Channel.FailThreshold)
	require.Equal(t, 3, c.Resilience.Breaker.Account.FailThreshold)
	require.Equal(t, 30*time.Second, c.Resilience.Breaker.Channel.Window)
	require.Equal(t, 60*time.Second, c.Resilience.Breaker.Channel.Cooldown)
	require.Equal(t, 30*time.Second, c.Resilience.Retry.MaxWall)
	require.InDelta(t, 0.3, c.Resilience.Breaker.Channel.RecoverFactor, 1e-9)
}

func TestResilienceYAMLOverride(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "c.yaml")
	require.NoError(t, os.WriteFile(p, []byte("resilience:\n  breaker:\n    channel:\n      fail_threshold: 9\n  retry:\n    max_wall: 12s\n"), 0o600))
	c, err := Load(p)
	require.NoError(t, err)
	require.Equal(t, 9, c.Resilience.Breaker.Channel.FailThreshold)
	require.Equal(t, 12*time.Second, c.Resilience.Retry.MaxWall)
	// 未覆盖字段保留缺省
	require.Equal(t, 60*time.Second, c.Resilience.Breaker.Channel.Cooldown)
}

func TestProbeDefaults(t *testing.T) {
	c := defaults()
	require.True(t, c.Resilience.Probe.Enabled)
	require.Equal(t, 30*time.Second, c.Resilience.Probe.Interval)
	require.Equal(t, 8, c.Resilience.Probe.Concurrency)
	require.Equal(t, 10*time.Second, c.Resilience.Probe.Timeout)
}

func TestProbeYAMLOverride(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "c.yaml")
	require.NoError(t, os.WriteFile(p, []byte("resilience:\n  probe:\n    enabled: false\n    interval: 5s\n"), 0o600))
	c, err := Load(p)
	require.NoError(t, err)
	require.False(t, c.Resilience.Probe.Enabled)
	require.Equal(t, 5*time.Second, c.Resilience.Probe.Interval)
	require.Equal(t, 8, c.Resilience.Probe.Concurrency) // 未覆盖保留缺省
}

func TestLogDefaults(t *testing.T) {
	c := defaults()
	require.Equal(t, "info", c.Log.Level)
	require.Equal(t, "text", c.Log.Format)
	require.Equal(t, "stderr", c.Log.Output)
}

func TestLogYAMLOverride(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "c.yaml")
	require.NoError(t, os.WriteFile(p, []byte("log:\n  level: debug\n  format: json\n  output: data/saig.log\n"), 0o600))
	c, err := Load(p)
	require.NoError(t, err)
	require.Equal(t, "debug", c.Log.Level)
	require.Equal(t, "json", c.Log.Format)
	require.Equal(t, "data/saig.log", c.Log.Output)
}

func TestAlertDefaults(t *testing.T) {
	c := defaults()
	require.False(t, c.Alert.Enabled)
	require.Equal(t, "", c.Alert.WebhookURL)
	require.Equal(t, "generic", c.Alert.Format)
	require.Equal(t, 5*time.Minute, c.Alert.DedupWindow)
}

func TestAlertYAMLOverride(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "c.yaml")
	require.NoError(t, os.WriteFile(p, []byte("alert:\n  enabled: true\n  webhook_url: https://hook.example/x\n  format: feishu\n  dedup_window: 2m\n"), 0o600))
	c, err := Load(p)
	require.NoError(t, err)
	require.True(t, c.Alert.Enabled)
	require.Equal(t, "https://hook.example/x", c.Alert.WebhookURL)
	require.Equal(t, "feishu", c.Alert.Format)
	require.Equal(t, 2*time.Minute, c.Alert.DedupWindow)
}

// TestAuditConfigDefaults 未配置 audit 段时给出内置默认。
func TestAuditConfigDefaults(t *testing.T) {
	t.Chdir(t.TempDir())
	cfg, err := Load("")
	require.NoError(t, err)
	require.Equal(t, 256, cfg.Audit.BufferSize)
	require.Equal(t, 0, cfg.Audit.MaxBodyBytes, "audit.max_body_bytes 缺省应为 0（不截断，取证完整优先）")
}

// TestAuditConfigOverride yaml 覆盖生效。
func TestAuditConfigOverride(t *testing.T) {
	p := filepath.Join(t.TempDir(), "c.yaml")
	require.NoError(t, os.WriteFile(p, []byte("audit:\n  buffer_size: 1024\n  max_body_bytes: 2097152\n"), 0o600))
	cfg, err := Load(p)
	require.NoError(t, err)
	require.Equal(t, 1024, cfg.Audit.BufferSize)
	require.Equal(t, 2*1024*1024, cfg.Audit.MaxBodyBytes)
}

// TestRequestLogConfigDefaults 未配置 request_log 段时保持同步写（缺省 0）。
func TestRequestLogConfigDefaults(t *testing.T) {
	t.Chdir(t.TempDir())
	cfg, err := Load("")
	require.NoError(t, err)
	require.Equal(t, 0, cfg.RequestLog.AsyncBuffer)
}

// TestRequestLogConfigOverride yaml 覆盖生效。
func TestRequestLogConfigOverride(t *testing.T) {
	p := filepath.Join(t.TempDir(), "c.yaml")
	require.NoError(t, os.WriteFile(p, []byte("request_log:\n  async_buffer: 64\n"), 0o600))
	cfg, err := Load(p)
	require.NoError(t, err)
	require.Equal(t, 64, cfg.RequestLog.AsyncBuffer)
}

// TestSessionJobsDefaults 粘连 TTL / 任务周期 / 登录限流 / keyed 退避 / 官方域名单缺省值与原硬编码逐位相同。
func TestSessionJobsDefaults(t *testing.T) {
	cfg, err := Load("")
	require.NoError(t, err)
	require.Equal(t, 5*time.Minute, cfg.Session.StickyTTL)
	require.Equal(t, 10*time.Minute, cfg.Session.ChannelStickyTTL)
	require.Equal(t, time.Minute, cfg.Jobs.UsageAggregateInterval)
	require.Equal(t, 6*time.Hour, cfg.Jobs.CleanupInterval)
	require.Equal(t, 10, cfg.Security.LoginRateLimit)
	require.Equal(t, 30, cfg.Security.V1AuthFailLimit)
	require.Equal(t, 5, cfg.Security.LoginUserFailLimit)
	require.Equal(t, 600, cfg.Security.DefaultKeyRPM)
	require.Equal(t, 20, cfg.Security.DefaultAppConcurrency)
	require.Equal(t, []string{"api.anthropic.com"}, cfg.Security.AnthropicOfficialHosts)
	require.Equal(t, time.Minute, cfg.Resilience.AccountModelBackoff.Base)
	require.Equal(t, 30*time.Minute, cfg.Resilience.AccountModelBackoff.Max)
}

// TestSessionJobsYAMLOverride yaml 覆盖生效。
func TestSessionJobsYAMLOverride(t *testing.T) {
	p := filepath.Join(t.TempDir(), "c.yaml")
	require.NoError(t, os.WriteFile(p, []byte(`
session:
  sticky_ttl: 2m
  channel_sticky_ttl: 20m
jobs:
  usage_aggregate_interval: 30s
  cleanup_interval: 12h
security:
  login_rate_limit: 5
  v1_auth_fail_limit: 0
  login_user_fail_limit: 0
  default_key_rpm: 0
  anthropic_official_hosts: ["api.anthropic.com", "gw.example.com"]
resilience:
  account_model_backoff:
    base: 2m
    max: 1h
`), 0o600))
	cfg, err := Load(p)
	require.NoError(t, err)
	require.Equal(t, 2*time.Minute, cfg.Session.StickyTTL)
	require.Equal(t, 20*time.Minute, cfg.Session.ChannelStickyTTL)
	require.Equal(t, 30*time.Second, cfg.Jobs.UsageAggregateInterval)
	require.Equal(t, 12*time.Hour, cfg.Jobs.CleanupInterval)
	require.Equal(t, 5, cfg.Security.LoginRateLimit)
	require.Equal(t, 0, cfg.Security.V1AuthFailLimit, "显式 0（关闭）须覆盖缺省 30")
	require.Equal(t, 0, cfg.Security.LoginUserFailLimit, "显式 0（关闭）须覆盖缺省 5")
	require.Equal(t, 0, cfg.Security.DefaultKeyRPM, "显式 0（关闭）须覆盖缺省 600")
	require.Equal(t, []string{"api.anthropic.com", "gw.example.com"}, cfg.Security.AnthropicOfficialHosts)
	require.Equal(t, 2*time.Minute, cfg.Resilience.AccountModelBackoff.Base)
	require.Equal(t, time.Hour, cfg.Resilience.AccountModelBackoff.Max)
}

// TestStorageConfigDefaults 未配置 storage/audit.workers 时给出内置默认：local + data/audit，S3 走 TLS，4 个写 worker。
func TestStorageConfigDefaults(t *testing.T) {
	t.Chdir(t.TempDir())
	cfg, err := Load("")
	require.NoError(t, err)
	require.Equal(t, "local", cfg.Storage.Type)
	require.Equal(t, "data/audit", cfg.Storage.Local.Root)
	require.True(t, cfg.Storage.S3.UseSSL, "s3.use_ssl 缺省应为 true")
	require.Equal(t, 4, cfg.Audit.Workers)
}

// TestStorageConfigOverride yaml 覆盖生效，且未覆盖的键保留默认。
func TestStorageConfigOverride(t *testing.T) {
	p := filepath.Join(t.TempDir(), "c.yaml")
	require.NoError(t, os.WriteFile(p, []byte(
		"storage:\n  type: s3\n  s3:\n    endpoint: minio:9000\n    bucket: saig-audit\n    region: us-east-1\n    access_key: ak\n    secret_key: sk\n    use_ssl: false\n    path_style: true\n    prefix: prod\naudit:\n  workers: 8\n"), 0o600))
	cfg, err := Load(p)
	require.NoError(t, err)
	require.Equal(t, "s3", cfg.Storage.Type)
	require.Equal(t, "minio:9000", cfg.Storage.S3.Endpoint)
	require.Equal(t, "saig-audit", cfg.Storage.S3.Bucket)
	require.Equal(t, "us-east-1", cfg.Storage.S3.Region)
	require.Equal(t, "ak", cfg.Storage.S3.AccessKey)
	require.Equal(t, "sk", cfg.Storage.S3.SecretKey)
	require.False(t, cfg.Storage.S3.UseSSL, "显式 use_ssl: false 必须能关掉")
	require.True(t, cfg.Storage.S3.PathStyle)
	require.Equal(t, "prod", cfg.Storage.S3.Prefix)
	require.Equal(t, 8, cfg.Audit.Workers)
	require.Equal(t, "data/audit", cfg.Storage.Local.Root, "未覆盖的 local.root 保留默认")
}

// server.trusted_proxies 从 YAML 读入；缺省为空（不信任任何代理头）。
func TestServerTrustedProxiesYAML(t *testing.T) {
	def, err := Load("")
	require.NoError(t, err)
	require.Empty(t, def.Server.TrustedProxies, "缺省不信任任何代理")

	dir := t.TempDir()
	p := filepath.Join(dir, "c.yaml")
	require.NoError(t, os.WriteFile(p, []byte("server:\n  trusted_proxies: [\"10.0.0.0/8\", \"127.0.0.1\"]\n"), 0o644))
	c, err := Load(p)
	require.NoError(t, err)
	require.Equal(t, []string{"10.0.0.0/8", "127.0.0.1"}, c.Server.TrustedProxies)
}
