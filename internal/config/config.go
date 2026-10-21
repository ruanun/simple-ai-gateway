package config

import (
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/ruanun/simple-ai-gateway/internal/pwpolicy"
	"gopkg.in/yaml.v3"
)

type Config struct {
	Server     ServerConfig     `yaml:"server"`
	Database   DatabaseConfig   `yaml:"database"`
	Redis      RedisConfig      `yaml:"redis"`
	Admin      AdminConfig      `yaml:"admin"`
	JWT        JWTConfig        `yaml:"jwt"`
	Security   SecurityConfig   `yaml:"security"`
	Resilience ResilienceConfig `yaml:"resilience"`
	Alert      AlertConfig      `yaml:"alert"`
	Log        LogConfig        `yaml:"log"`
	Audit      AuditConfig      `yaml:"audit"`
	RequestLog RequestLogConfig `yaml:"request_log"`
	Session    SessionConfig    `yaml:"session"`
	Jobs       JobsConfig       `yaml:"jobs"`
	Storage    StorageConfig    `yaml:"storage"`
}

// SessionConfig 会话粘连 TTL（账号级/渠道级）。
type SessionConfig struct {
	StickyTTL        time.Duration `yaml:"sticky_ttl"`         // 账号粘连，缺省 5m
	ChannelStickyTTL time.Duration `yaml:"channel_sticky_ttl"` // 渠道粘连，缺省 10m
}

// JobsConfig 后台定时任务周期。
type JobsConfig struct {
	UsageAggregateInterval time.Duration `yaml:"usage_aggregate_interval"` // 用量聚合，缺省 1m
	CleanupInterval        time.Duration `yaml:"cleanup_interval"`         // 审计/日志保留期清理，缺省 6h
}

// LogConfig 进程日志配置（业务面请求记录走 RequestLog 表，与此无关）。
type LogConfig struct {
	Level  string `yaml:"level"`  // debug / info / warn / error，缺省 info
	Format string `yaml:"format"` // text / json，缺省 text
	Output string `yaml:"output"` // stderr（缺省）/ stdout / 文件路径（append，不轮转）
}

type ServerConfig struct {
	Port int `yaml:"port"`
	// TrustedProxies 可信反向代理的 IP/CIDR 列表。只有请求确实来自其中之一时，
	// c.ClientIP() 才采信 X-Forwarded-For / X-Real-IP；缺省为空即一律只信 TCP 对端地址，
	// 否则任何调用方伪造该头就能改写请求日志/审计/操作日志里的 client_ip，并绕过按 IP 的登录限流。
	TrustedProxies []string `yaml:"trusted_proxies"`
}

// DatabaseConfig 数据库连接配置，含连接池参数。
type DatabaseConfig struct {
	Driver          string `yaml:"driver"`
	DSN             string `yaml:"dsn"`
	MaxOpenConns    int    `yaml:"max_open_conns"`    // 最大打开连接数
	MaxIdleConns    int    `yaml:"max_idle_conns"`    // 最大空闲连接数
	ConnMaxLifetime int    `yaml:"conn_max_lifetime"` // 连接最大存活时间（秒）
}

// RedisConfig Redis 可选组件配置。
type RedisConfig struct {
	Enabled  bool   `yaml:"enabled"`
	Addr     string `yaml:"addr"`
	Password string `yaml:"password"`
	DB       int    `yaml:"db"`
}
type AdminConfig struct {
	Username string `yaml:"username"`
	Password string `yaml:"password"`
}
type JWTConfig struct {
	Secret      string `yaml:"secret"`
	ExpireHours int    `yaml:"expire_hours"`
}
type SecurityConfig struct {
	APIKeyPrefix           string   `yaml:"apikey_prefix"`
	ChannelKeyAESKey       string   `yaml:"channel_key_aes_key"`
	LoginRateLimit         int      `yaml:"login_rate_limit"`         // 登录/注册匿名端点每 IP 每分钟次数，缺省 10
	V1AuthFailLimit        int      `yaml:"v1_auth_fail_limit"`       // /v1 每 IP 每分钟鉴权失败上限，达限后该 IP 本分钟直接 429（不查库）；缺省 30，<=0 关闭
	LoginUserFailLimit     int      `yaml:"login_user_fail_limit"`    // 同一用户名每分钟登录失败上限（跨 IP 合计），达限后该用户名本分钟直接 429；缺省 5，<=0 关闭
	UpstreamBlockPrivate   bool     `yaml:"upstream_block_private"`   // 渠道出站是否拒绝回环/私网地址（防 SSRF 打内网）；缺省 false（本机/内网中转常注册为渠道），云元数据地址始终拒绝
	DefaultAppConcurrency  int      `yaml:"default_app_concurrency"`  // App 未设并发上限（0）时套用的同时在途请求上限；缺省 20，<=0 表示未设置的 App 不限；App 设 -1 单独放开
	DefaultKeyRPM          int      `yaml:"default_key_rpm"`          // 令牌 RPM 未设置（0）时套用的兜底上限；缺省 600，<=0 表示未设置的令牌不限流；令牌 RPM=-1 单独放开
	AnthropicOfficialHosts []string `yaml:"anthropic_official_hosts"` // anthropic auto 鉴权分流官方域名单，缺省 ["api.anthropic.com"]
}

// AlertConfig 告警最小版（Phase 3b）：一个 webhook + format 塑形 + TTL 去重。
type AlertConfig struct {
	Enabled     bool          `yaml:"enabled"`
	WebhookURL  string        `yaml:"webhook_url"`
	Format      string        `yaml:"format"` // generic(默认) | slack | feishu | dingtalk
	DedupWindow time.Duration `yaml:"dedup_window"`
}

// AuditConfig 取证审计异步写参数。缓冲满时新条目会被丢弃（fail-open 保主链路），
// 高并发部署应按流量调大 buffer_size。max_body_bytes：0（缺省）=正文不截断（取证完整优先）；
// >0 时单条正文超限截断并标记（原长/truncated/SHA256 元数据保留）。
// workers：后台写 worker 数（缺省 4）。正文经存储层落盘/上传（S3 每条两次网络 Put），
// 单消费者吞吐只有个位数每秒，多 worker 才不至于缓冲溢出。
type AuditConfig struct {
	BufferSize   int `yaml:"buffer_size"`    // 缓冲队列长度，缺省 256
	MaxBodyBytes int `yaml:"max_body_bytes"` // 单条正文截断上限；0(缺省)=不截断，>0=截断并标记
	Workers      int `yaml:"workers"`        // 后台写 worker 数，缺省 4
}

// StorageConfig 审计正文存储后端。正文（请求体/响应体）不进数据库，经此后端落对象存储。
// type=local 落本地目录（单实例零配置）；type=s3 走 S3 兼容协议（AWS/MinIO/R2/OSS/COS），
// 多实例部署必须用 s3（local 不跨节点）。
type StorageConfig struct {
	Type  string             `yaml:"type"` // local | s3；空视为 local
	Local LocalStorageConfig `yaml:"local"`
	S3    S3StorageConfig    `yaml:"s3"`
}

// LocalStorageConfig 本地目录后端。
type LocalStorageConfig struct {
	Root string `yaml:"root"` // 根目录，缺省 data/audit；为空则拒启
}

// S3StorageConfig S3 兼容后端。access_key 留空时走 IAM/环境变量/凭证文件链。
type S3StorageConfig struct {
	Endpoint  string `yaml:"endpoint"`   // 不带协议，如 s3.amazonaws.com / minio:9000
	Bucket    string `yaml:"bucket"`     // 须预先创建，本项目不自动建桶
	Region    string `yaml:"region"`     // 可空
	AccessKey string `yaml:"access_key"` // 留空走凭证链
	SecretKey string `yaml:"secret_key"`
	UseSSL    bool   `yaml:"use_ssl"`    // 缺省 true
	PathStyle bool   `yaml:"path_style"` // MinIO/自建通常 true
	Prefix    string `yaml:"prefix"`     // 可选对象 key 前缀
}

// RequestLogConfig 请求日志写入参数。
type RequestLogConfig struct {
	// AsyncBuffer >0 时启用异步写（缓冲长度）；0（缺省）保持同步写。
	// 异步权衡：进程崩溃丢缓冲内条目、缓冲满丢弃新条目（均记警告）。
	AsyncBuffer int `yaml:"async_buffer"`
}

// BreakerParams 单级熔断器参数（channel/account/proxy 各一份）。
type BreakerParams struct {
	Window          time.Duration `yaml:"window"`
	Cooldown        time.Duration `yaml:"cooldown"`
	FailThreshold   int           `yaml:"fail_threshold"`
	ErrorRate       float64       `yaml:"error_rate"`
	MinSamples      int           `yaml:"min_samples"`
	ProbeBackoffMax int           `yaml:"probe_backoff_max"`
	RecoverFactor   float64       `yaml:"recover_factor"`
}

// ProbeConfig 主动探测器参数（Phase 3a）。
type ProbeConfig struct {
	Enabled     bool          `yaml:"enabled"`
	Interval    time.Duration `yaml:"interval"`
	Concurrency int           `yaml:"concurrency"`
	Timeout     time.Duration `yaml:"timeout"`
}

// ResilienceConfig 熔断/重试参数。
type ResilienceConfig struct {
	Breaker struct {
		Channel BreakerParams `yaml:"channel"`
		Account BreakerParams `yaml:"account"`
		Proxy   BreakerParams `yaml:"proxy"`
	} `yaml:"breaker"`
	Retry struct {
		MaxWall time.Duration `yaml:"max_wall"`
	} `yaml:"retry"`
	Probe               ProbeConfig `yaml:"probe"`
	AccountModelBackoff struct {
		Base time.Duration `yaml:"base"` // 账号×模型配额冷却指数退避基数，缺省 1m
		Max  time.Duration `yaml:"max"`  // 退避上限，缺省 30m
	} `yaml:"account_model_backoff"`
}

// 内置默认凭证：仅用于零配置的本地开发首启。生产部署必须覆盖，
// 否则 CheckProductionSafety 会拦下（默认 JWT secret 可伪造管理令牌、
// 默认 AES key 可解密上游凭证、默认 admin 口令可直接接管管理面）。
const (
	defaultAdminPassword = "sag12345"
	defaultJWTSecret     = "change-me-dev-secret"
	defaultAESKey        = "0123456789abcdef0123456789abcdef"
)

func defaults() Config {
	return Config{
		Server:   ServerConfig{Port: 8080},
		Database: DatabaseConfig{Driver: "sqlite", DSN: "data/saig.db"},
		Redis:    RedisConfig{Enabled: false, Addr: "127.0.0.1:6379", DB: 0},
		Admin:    AdminConfig{Username: "admin", Password: defaultAdminPassword},
		JWT:      JWTConfig{Secret: defaultJWTSecret, ExpireHours: 24},
		Security: SecurityConfig{
			APIKeyPrefix: "sk-saig", ChannelKeyAESKey: defaultAESKey,
			LoginRateLimit: 10, V1AuthFailLimit: 30, LoginUserFailLimit: 5, DefaultKeyRPM: 600, DefaultAppConcurrency: 20,
			AnthropicOfficialHosts: []string{"api.anthropic.com"},
		},
		Resilience: ResilienceConfig{
			Breaker: struct {
				Channel BreakerParams `yaml:"channel"`
				Account BreakerParams `yaml:"account"`
				Proxy   BreakerParams `yaml:"proxy"`
			}{
				Channel: BreakerParams{Window: 30 * time.Second, Cooldown: 60 * time.Second, FailThreshold: 5, ErrorRate: 0.5, MinSamples: 20, ProbeBackoffMax: 8, RecoverFactor: 0.3},
				Account: BreakerParams{Window: 30 * time.Second, Cooldown: 60 * time.Second, FailThreshold: 3, ErrorRate: 0.5, MinSamples: 10, ProbeBackoffMax: 8, RecoverFactor: 0.3},
				Proxy:   BreakerParams{Window: 30 * time.Second, Cooldown: 60 * time.Second, FailThreshold: 3, ErrorRate: 0.5, MinSamples: 10, ProbeBackoffMax: 8, RecoverFactor: 0.3},
			},
			Retry: struct {
				MaxWall time.Duration `yaml:"max_wall"`
			}{MaxWall: 30 * time.Second},
			Probe: ProbeConfig{Enabled: true, Interval: 30 * time.Second, Concurrency: 8, Timeout: 10 * time.Second},
			AccountModelBackoff: struct {
				Base time.Duration `yaml:"base"`
				Max  time.Duration `yaml:"max"`
			}{Base: time.Minute, Max: 30 * time.Minute},
		},
		Alert:      AlertConfig{Enabled: false, Format: "generic", DedupWindow: 5 * time.Minute},
		Log:        LogConfig{Level: "info", Format: "text", Output: "stderr"},
		Audit:      AuditConfig{BufferSize: 256, Workers: 4},
		RequestLog: RequestLogConfig{AsyncBuffer: 0},
		Session:    SessionConfig{StickyTTL: 5 * time.Minute, ChannelStickyTTL: 10 * time.Minute},
		Jobs:       JobsConfig{UsageAggregateInterval: time.Minute, CleanupInterval: 6 * time.Hour},
		Storage:    StorageConfig{Type: "local", Local: LocalStorageConfig{Root: "data/audit"}, S3: S3StorageConfig{UseSSL: true}},
	}
}

// minJWTSecretLen HS256 签名密钥最小长度：短于 32 字节可被离线暴力破解后伪造管理令牌。
const minJWTSecretLen = 32

// isPlaceholder 判定是否为「未改的值」：去空白后为空、等于任一内置默认值，或含配置模板里的
// CHANGE_ME 类占位符（模板 __CHANGE_ME__ 是公开的，照抄等于用公开密钥）。
func isPlaceholder(v string, builtins ...string) bool {
	t := strings.TrimSpace(v)
	if t == "" {
		return true
	}
	for _, d := range builtins {
		if t == d {
			return true
		}
	}
	return strings.Contains(strings.ReplaceAll(strings.ToUpper(t), "_", ""), "CHANGEME")
}

// CheckProductionSafety 检查是否仍在使用内置默认凭证、模板占位符或弱值。
// 返回所有不安全项的中文说明；非空即表示不可安全对外启动，调用方应拒绝启动。
// 覆盖 Critical #1（默认管理员口令）与 #2（默认 JWT Secret），并顺带堵住默认 AES key。
func (c Config) CheckProductionSafety() []string {
	var issues []string
	switch secret := strings.TrimSpace(c.JWT.Secret); {
	case isPlaceholder(secret, defaultJWTSecret):
		issues = append(issues, "jwt.secret 仍为内置默认值、模板占位符或为空（可被离线伪造管理令牌）")
	case len(secret) < minJWTSecretLen:
		issues = append(issues, fmt.Sprintf("jwt.secret 长度不足 %d 字节（可被暴力破解后伪造管理令牌），建议 openssl rand -hex 32", minJWTSecretLen))
	}
	if isPlaceholder(c.Security.ChannelKeyAESKey, defaultAESKey) {
		issues = append(issues, "security.channel_key_aes_key 仍为内置默认值、模板占位符或为空（上游凭证可被公开密钥解密）")
	}
	switch {
	case isPlaceholder(c.Admin.Password, defaultAdminPassword):
		issues = append(issues, "admin.password 仍为内置默认值、模板占位符或为空（管理面可被默认口令接管）")
	case !pwpolicy.Valid(c.Admin.Password):
		issues = append(issues, "admin.password 不符合密码策略："+pwpolicy.ErrMsg)
	}
	return issues
}

// Load 读取配置：未显式指定 path 且无 config.yaml 时返回默认值；
// 指定/探测到配置文件后，读取或解析失败一律返回 error（不再静默回退弱默认，
// 避免配置写错时无声运行在默认口令/密钥上，运维毫无感知）。
func Load(path string) (Config, error) {
	cfg := defaults()
	if path == "" {
		if _, err := os.Stat("config.yaml"); err == nil {
			path = "config.yaml"
		}
	}
	if path == "" {
		return cfg, nil
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return cfg, fmt.Errorf("读取配置文件 %s 失败: %w", path, err)
	}
	if err := yaml.Unmarshal(b, &cfg); err != nil {
		return cfg, fmt.Errorf("解析配置文件 %s 失败: %w", path, err)
	}
	return cfg, nil
}
