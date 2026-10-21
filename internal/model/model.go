package model

import (
	"strings"
	"time"
)

type BaseModel struct {
	ID        uint64    `gorm:"primaryKey;autoIncrement" json:"id"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

type Channel struct {
	BaseModel
	Name               string `gorm:"size:64;not null" json:"name"`
	Type               string `gorm:"size:32;default:openai" json:"type"`        // 渠道协议：openai | anthropic。一渠道一协议，双协议上游建两条渠道
	BaseURL            string `gorm:"size:256" json:"base_url"`                  // 可空=回退平台 base_url
	AllowConvert       bool   `gorm:"default:false" json:"allow_convert"`        // 允许跨协议转换(P12c 用)，默认关
	StripThinking      bool   `gorm:"default:false" json:"strip_thinking"`       // 出站剥离 thinking/reasoning_effort，用于不支持思考的上游(B)
	DisableStreamUsage bool   `gorm:"default:false" json:"disable_stream_usage"` // 关闭「注入 stream_options.include_usage」；默认注入(补流式计费)，对 stream_options 报错的怪上游可停用
	// ErrorPassthrough 全候选失败时透传最后一个上游错误的状态码与脱敏文案（默认 false=通用 502/503 摘要）。
	// 直接打到上游的非重试错误本来就脱敏透传；此开关只影响「所有候选都失败」的聚合兜底。
	ErrorPassthrough bool    `gorm:"default:false" json:"error_passthrough"`
	Status           int8    `gorm:"default:1" json:"status"`
	Timeout          int     `gorm:"default:60" json:"timeout"`
	Priority         int     `gorm:"default:0" json:"priority"`
	Weight           int     `gorm:"default:1" json:"weight"`
	MaxRetries       int     `gorm:"default:0" json:"max_retries"`
	RPMLimit         int     `gorm:"default:0" json:"rpm_limit"`
	RateMultiplier   float64 `gorm:"default:1" json:"rate_multiplier"` // 渠道倍率,实际成本=有效价×它,默认 1.0
	// 出站鉴权头风格：auto(默认) | x-api-key | bearer | both。仅 anthropic 协议渠道有意义。
	// auto = 官方域名(api.anthropic.com)发 x-api-key，其余中转站双发(Bearer + x-api-key)。
	AuthStyle string `gorm:"size:16;default:auto" json:"auth_style"`
	// 渠道级静态自定义请求头(JSON: {"HTTP-Referer":"..."})，出站合并进请求头；受保护名会被剥离。
	CustomHeaders string `gorm:"type:text" json:"custom_headers"`
	Remark        string `gorm:"size:256" json:"remark"`
}

type ChannelModel struct {
	BaseModel
	ChannelID uint64 `gorm:"index;not null" json:"channel_id"`
	Model     string `gorm:"size:128;index;not null" json:"model"`
	Enabled   bool   `gorm:"default:true" json:"enabled"`
}

// Account 上游账号(凭证)，归属一个 Channel，构成该渠道的密钥池(多把 API Key 负载/容灾)。
type Account struct {
	BaseModel
	ChannelID  uint64     `gorm:"index;not null" json:"channel_id"`
	Name       string     `gorm:"size:64" json:"name"`
	APIKeyEnc  string     `gorm:"size:512" json:"-"`        // 上游凭证密文(AES)，不出 JSON
	BaseURL    string     `gorm:"size:256" json:"base_url"` // 可选覆盖，空=用渠道 base_url
	Weight     int        `gorm:"default:1" json:"weight"`
	Priority   int        `gorm:"default:0" json:"priority"`
	Status     int8       `gorm:"default:1" json:"status"`
	FailCount  int        `json:"fail_count"`
	LastUsedAt *time.Time `json:"last_used_at"`
	ProxyGroup string     `gorm:"size:64" json:"proxy_group"` // 绑定的代理组名，空=不走代理

	// 账号级调度与限流（P1）：把上游每把 key 的并发/速率/额度窗口隔离，避免单 key 被
	// 渠道级共享 RPM 拖垮。0 值一律表示不限，保持旧数据行为不变。
	MaxConcurrency int        `gorm:"default:0" json:"max_concurrency"` // 同时在途请求上限；<=0 不限
	RPMLimit       int        `gorm:"default:0" json:"rpm_limit"`       // 每分钟请求上限；<=0 不限
	ExpiresAt      *time.Time `json:"expires_at"`                       // 凭证过期时间；过期后不再选号
	// 临时不可调度（人工/自动）：Until 之前跳过该账号；Reason 供管理端展示。
	TempUnschedulableUntil  *time.Time `json:"temp_unschedulable_until"`
	TempUnschedulableReason string     `gorm:"size:128" json:"temp_unschedulable_reason"`
	// 上游限流/过载冷却：Until 之前跳过该账号；由 429/529 或管理端写入。
	RateLimitResetAt *time.Time `json:"rate_limit_reset_at"`
	OverloadUntil    *time.Time `json:"overload_until"`
}

// Proxy 出站 IP 代理（http/https/socks5），按 group 分组，配合密钥池实现 IP+账号双通道轮换(分摊限流/容灾)。
// 注意：分组列名用 group_name（group 是 SQL 保留字），JSON 仍暴露为 group。
type Proxy struct {
	BaseModel
	Group      string     `gorm:"column:group_name;size:64;index" json:"group"`
	Name       string     `gorm:"size:64" json:"name"`
	Protocol   string     `gorm:"size:16;default:http" json:"protocol"` // http|https|socks5
	Host       string     `gorm:"size:128;not null" json:"host"`
	Port       int        `gorm:"not null" json:"port"`
	Username   string     `gorm:"size:128" json:"username"`
	Password   string     `gorm:"size:256" json:"-"` // 明文密码，不出 JSON（同 APIKeyEnc）
	Weight     int        `gorm:"default:1" json:"weight"`
	Priority   int        `gorm:"default:0" json:"priority"`
	Status     int8       `gorm:"default:1" json:"status"`
	FailCount  int        `json:"fail_count"`
	LastUsedAt *time.Time `json:"last_used_at"`
}

type APIKey struct {
	BaseModel
	Name     string     `gorm:"size:64;not null" json:"name"`
	KeyHash  string     `gorm:"size:64;uniqueIndex;not null" json:"-"`
	Key      string     `gorm:"size:64" json:"key"` // P11: 明文可回查（用户明确选择的取舍，见 spec 决策记录）；旧令牌为空
	Prefix   string     `gorm:"size:32" json:"prefix"`
	Status   int8       `gorm:"default:1" json:"status"`
	ExpireAt *time.Time `json:"expire_at"`
	RPM      int        `gorm:"default:0" json:"rpm"` // 三态上限（见 LimitUnlimited）：0=套用全局 default_key_rpm
	AppID    uint64     `gorm:"index" json:"app_id"`  // 所属配额主体 App

	// P4 令牌治理列
	DailyCostLimit int64  `gorm:"default:0" json:"daily_cost_limit"` // micros,0=不限
	DailyCostUsed  int64  `gorm:"default:0" json:"daily_cost_used"`  // micros
	DailyCostDate  string `gorm:"size:10" json:"daily_cost_date"`    // YYYY-MM-DD
	ModelWhitelist string `gorm:"size:512" json:"model_whitelist"`   // 逗号分隔,空=不限
	ModelMappings  string `gorm:"size:512" json:"model_mappings"`    // JSON {"from":"to"},空=无

	// DisabledByAdmin 管理员停用标记：管理端停用时置 true、启用时清除。为 true 时门户不得重新启用
	// （门户只能停用/删除），防止管理员因泄露等原因处置的令牌被使用方一键撤销。
	DisabledByAdmin bool `gorm:"default:false" json:"disabled_by_admin"`
}

// IdempotencyRecord 非流式 /v1 请求的幂等记录（Idempotency-Key）。
// scope 由 API Key + 端点组成，key_hash 为客户端 Idempotency-Key 的 SHA-256；
// request_hash 防止同一 key 复用于不同请求体。响应体只缓存小响应，过大/5xx 不缓存。
type IdempotencyRecord struct {
	BaseModel
	Scope          string    `gorm:"size:128;uniqueIndex:uniq_idempotency,priority:1;not null" json:"scope"`
	KeyHash        string    `gorm:"size:64;uniqueIndex:uniq_idempotency,priority:2;not null" json:"-"`
	RequestHash    string    `gorm:"size:64;not null" json:"-"`
	Status         string    `gorm:"size:16;not null" json:"status"`
	ResponseStatus int       `json:"response_status"`
	ResponseBody   []byte    `json:"-"`
	ExpiresAt      time.Time `gorm:"index" json:"expires_at"`
}

// KeyTag 令牌标签，用于密钥分类与按标签筛选。
type KeyTag struct {
	BaseModel
	Name string `gorm:"size:64;uniqueIndex;not null" json:"name"`
}

// KeyTagRelation 密钥与标签的多对多关系。
type KeyTagRelation struct {
	BaseModel
	APIKeyID uint64 `gorm:"index;uniqueIndex:uniq_key_tag,priority:1" json:"api_key_id"`
	TagID    uint64 `gorm:"index;uniqueIndex:uniq_key_tag,priority:2" json:"tag_id"`
}

type RequestLog struct {
	BaseModel
	APIKeyID  uint64 `gorm:"index" json:"api_key_id"`
	ChannelID uint64 `gorm:"index" json:"channel_id"`
	AccountID uint64 `gorm:"index" json:"account_id"`
	Model     string `gorm:"size:128" json:"model"`
	Status    int    `json:"status"`
	LatencyMs int64  `json:"latency_ms"`
	Stream    bool   `json:"stream"`
	ErrorMsg  string `gorm:"size:512" json:"error_msg"`

	AppID               uint64 `gorm:"index" json:"app_id"`
	PromptTokens        int    `json:"prompt_tokens"`
	CompletionTokens    int    `json:"completion_tokens"`
	TotalTokens         int    `json:"total_tokens"`
	CachedTokens        int    `json:"cached_tokens"`         // 缓存读 token（⊆ prompt_tokens）
	CacheCreationTokens int    `json:"cache_creation_tokens"` // 缓存写 token（⊆ prompt_tokens，仅 anthropic）
	ReasoningTokens     int    `json:"reasoning_tokens"`      // 推理 token（⊆ completion_tokens）
	CostMicros          int64  `json:"cost_micros"`
	UsageMissing        bool   `json:"usage_missing"`

	FirstTokenMs int64  `json:"first_token_ms"`                           // 流式首 Token 延迟(ms)，非流式为 0
	SessionID    string `gorm:"size:128;index" json:"session_id"`         // 会话 ID（同一对话的多次请求共享）
	RequestType  string `gorm:"size:16;default:chat" json:"request_type"` // chat|messages|responses|embeddings|images
	ClientIP     string `gorm:"size:64" json:"client_ip"`
	Headers      string `gorm:"type:text" json:"headers"`        // 请求头 JSON(已脱敏)
	RequestID    string `gorm:"size:64;index" json:"request_id"` // 与 AuditRecord.RequestID 对应，用于日志→审计正文关联

	Protocol         string `gorm:"size:16" json:"protocol"`          // 入站协议(openai|anthropic)
	UpstreamProtocol string `gorm:"size:16" json:"upstream_protocol"` // 本次实际出站协议
}

// 管理端角色常量（P9 RBAC 固定三角色，路由级强制见 middleware.AdminRBAC）
const (
	RoleSuper  = "super"  // 超管：全部
	RoleOps    = "ops"    // 运营：除用户管理/系统设置外全部
	RoleViewer = "viewer" // 只读：仅查看 + 改自己密码
)

type AdminUser struct {
	BaseModel
	Username     string `gorm:"size:64;uniqueIndex;not null" json:"username"`
	PasswordHash string `gorm:"size:128;not null" json:"-"`
	Status       int8   `gorm:"default:1" json:"status"`          // 1 启用 / 0 禁用
	Role         string `gorm:"size:8;default:super" json:"role"` // super | ops | viewer
	TokenVersion int    `gorm:"default:0" json:"-"`               // 改密/重置递增，使旧 JWT 失效(#17)
}

// PortalUser 使用方用户（门户登录主体），与 AdminUser（管理端）完全隔离。
// P9-B：用户拥有多个 App（App.OwnerUserID 归属），登录后仅见/管自己名下资源。
type PortalUser struct {
	BaseModel
	Username     string `gorm:"size:64;uniqueIndex;not null" json:"username"`
	PasswordHash string `gorm:"size:128" json:"-"` // OIDC-only 用户可为空（C 阶段）
	Email        string `gorm:"size:128" json:"email"`
	Status       int8   `gorm:"default:1" json:"status"`             // 1 启用 / 0 禁用
	Source       string `gorm:"size:16;default:local" json:"source"` // local | oidc
	TokenVersion int    `gorm:"default:0" json:"-"`                  // 改密/重置递增，使旧 JWT 失效(#25)
	// 显式列名：GORM 默认会把 OIDCSubject 拆成 o_id_c_subject（只识别 ID 缩写）
	OIDCSubject string `gorm:"column:oidc_subject;size:191;index" json:"-"` // iss+sub 绑定键（C 阶段），local 为空
}

// AdminOpLog 管理操作日志（P9 最小版）：仅记谁在何时对什么路径做了写操作，不记请求体。
type AdminOpLog struct {
	ID        uint64    `gorm:"primaryKey;autoIncrement" json:"id"`
	CreatedAt time.Time `gorm:"index" json:"created_at"`
	ActorType string    `gorm:"size:8" json:"actor_type"` // admin | user（user 为 B 阶段门户预留）
	Actor     string    `gorm:"size:64;index" json:"actor"`
	Role      string    `gorm:"size:8" json:"role"`
	Method    string    `gorm:"size:8" json:"method"`
	Path      string    `gorm:"size:256" json:"path"`
	Status    int       `json:"status"` // 响应码
	ClientIP  string    `gorm:"size:64" json:"client_ip"`
}

// ModelAlias 模型别名（如 smart），承载一组路由边按策略择优。
type ModelAlias struct {
	BaseModel
	Alias       string `gorm:"size:128;uniqueIndex;not null" json:"alias"`
	Description string `gorm:"size:256" json:"description"`
	Status      int8   `gorm:"default:1" json:"status"`
}

// AliasRoute 别名路由边：把别名映射到某渠道的真实模型，带优先级与权重。
type AliasRoute struct {
	BaseModel
	AliasID   uint64 `gorm:"index;not null" json:"alias_id"`
	ChannelID uint64 `gorm:"index;not null" json:"channel_id"`
	Model     string `gorm:"size:128;not null" json:"model"`
	Weight    int    `gorm:"default:1" json:"weight"`
	Priority  int    `gorm:"default:0" json:"priority"`
}

// App 配额主体：一组密钥的归属与额度边界。Quota/UsedQuota 单位均为 USD 微量整数（1 USD = 1e6 micros）。
type App struct {
	BaseModel
	Name            string `gorm:"size:64;uniqueIndex;not null" json:"name"`
	Quota           int64  `gorm:"default:0" json:"quota"`      // micros，0=不限
	UsedQuota       int64  `gorm:"default:0" json:"used_quota"` // micros
	Status          int8   `gorm:"default:1" json:"status"`
	AuditEnabled    bool   `gorm:"default:true" json:"audit_enabled"`    // 取证级审计开关
	AllowedChannels string `gorm:"size:512" json:"allowed_channels"`     // 逗号分隔渠道 ID,空=不限
	OwnerUserID     uint64 `gorm:"index;default:0" json:"owner_user_id"` // 归属门户用户,0=无主(管理员直管)

	// 以下两项是管理员对该租户的硬约束，门户不可改；令牌级同名设置只是租户自我约束，生效取交集。
	ModelWhitelist string `gorm:"size:512" json:"model_whitelist"` // 逗号分隔（按映射后的请求模型名匹配）,空=不限
	MaxConcurrency int    `json:"max_concurrency"`                 // 同时在途请求上限，三态（见 LimitUnlimited）：0=套用全局 default_app_concurrency
}

// LimitUnlimited 三态上限字段（令牌 RPM、App 并发上限）取此值表示不限，不套全局默认。
// 三态约定：0=套用全局默认；LimitUnlimited=不限；正整数=自身上限。
const LimitUnlimited = -1

// ValidLimit 三态上限的合法取值。
func ValidLimit(v int) bool { return v >= LimitUnlimited }

// LimitErrMsg 三态上限取值非法时的统一提示。
const LimitErrMsg = "须为 -1（不限）、0（按全局默认）或正整数"

// EffectiveLimit 解析三态上限：0 取全局默认 def，其余原样返回。结果 <=0 即不限
// （限流器/并发计数器对 limit<=0 一律放行），调用方无需再分支。
func EffectiveLimit(v, def int) int {
	if v == 0 {
		return def
	}
	return v
}

// NormalizeList 规整逗号分隔清单：去各项首尾空白、丢空项（白名单等写入时统一存储格式）。
func NormalizeList(s string) string {
	var out []string
	for _, p := range strings.Split(s, ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return strings.Join(out, ",")
}

// WhitelistAllows 逗号分隔白名单是否放行 name；白名单为空（含纯空白）即不限。
// 令牌级与 App 级白名单共用，保证两层匹配口径一致。
func WhitelistAllows(list, name string) bool {
	if strings.TrimSpace(list) == "" {
		return true
	}
	for _, m := range strings.Split(list, ",") {
		if strings.TrimSpace(m) == name {
			return true
		}
	}
	return false
}

// AppMember App↔门户用户 多对多成员：团队共享同一应用（配额/用量/日志/令牌）。
// 访问权唯一真相源（含 owner）；owner 始终是成员。
type AppMember struct {
	ID           uint64    `gorm:"primaryKey;autoIncrement" json:"id"`
	AppID        uint64    `gorm:"uniqueIndex:uk_app_member;not null" json:"app_id"`
	PortalUserID uint64    `gorm:"uniqueIndex:uk_app_member;not null" json:"portal_user_id"`
	CreatedAt    time.Time `json:"created_at"`
}

// AuditRecord 取证级审计记录：每个请求一行；正文外置存储层，行内存对象 key 与取证元数据。
type AuditRecord struct {
	BaseModel
	RequestID           string `gorm:"size:64;uniqueIndex;not null" json:"request_id"`
	APIKeyID            uint64 `gorm:"index" json:"api_key_id"`
	AppID               uint64 `gorm:"index" json:"app_id"`
	ChannelID           uint64 `gorm:"index" json:"channel_id"`
	AccountID           uint64 `gorm:"index" json:"account_id"`
	Model               string `gorm:"size:128;index" json:"model"`
	RequestType         string `gorm:"size:16;index" json:"request_type"`
	Status              int    `gorm:"index" json:"status"`
	LatencyMs           int64  `json:"latency_ms"`
	FirstTokenMs        int64  `json:"first_token_ms"`
	SessionID           string `gorm:"size:128;index" json:"session_id"`
	Stream              bool   `json:"stream"`
	PromptTokens        int    `json:"prompt_tokens"`
	CompletionTokens    int    `json:"completion_tokens"`
	CachedTokens        int    `json:"cached_tokens"`
	CacheCreationTokens int    `json:"cache_creation_tokens"`
	ReasoningTokens     int    `json:"reasoning_tokens"`
	CostMicros          int64  `json:"cost_micros"`
	ClientIP            string `gorm:"size:64" json:"client_ip"`
	UserAgent           string `gorm:"size:256" json:"user_agent"`
	EventCount          int    `json:"event_count"`

	// 正文已外置到存储层（2026-09-03）：行内只留对象 key 与取证元数据。
	// size=实际存储字节（截断后、压缩前），orig_size=原始长度，sha256=原文全文摘要。
	RequestKey        string `gorm:"size:255" json:"request_key"`
	RequestSize       int    `json:"request_size"`
	RequestOrigSize   int    `json:"request_orig_size"`
	RequestTruncated  bool   `json:"request_truncated"`
	RequestSHA256     string `gorm:"size:64" json:"request_sha256"`
	ResponseKey       string `gorm:"size:255" json:"response_key"`
	ResponseSize      int    `json:"response_size"`
	ResponseOrigSize  int    `json:"response_orig_size"`
	ResponseTruncated bool   `json:"response_truncated"`
	ResponseSHA256    string `gorm:"size:64" json:"response_sha256"`
}

// ModelPrice 模型价格表。per_1m 字段单位为「USD / 1,000,000 token」。
// 显式列标签避免 GORM 蛇形把 InputPer1M 命名成 input_per1_m，确保 OnConflict 列名一致。
// channel_id=0 表示全局官方价兜底；非零表示该渠道的覆盖价。
type ModelPrice struct {
	BaseModel
	ChannelID        uint64  `gorm:"uniqueIndex:uniq_channel_model;default:0;not null" json:"channel_id"` // 0=全局官方价
	Model            string  `gorm:"size:128;uniqueIndex:uniq_channel_model;not null" json:"model"`
	InputPer1M       float64 `gorm:"column:input_per_1m" json:"input_per_1m"`
	OutputPer1M      float64 `gorm:"column:output_per_1m" json:"output_per_1m"`
	CachedInputPer1M float64 `gorm:"column:cached_input_per_1m" json:"cached_input_per_1m"`
	CacheWritePer1M  float64 `gorm:"column:cache_write_per_1m" json:"cache_write_per_1m"` // 缓存写价(anthropic cache_creation)；0=未配置,计费回退 input 价(P12d)
	Currency         string  `gorm:"size:8;default:USD" json:"currency"`
	Enabled          bool    `gorm:"default:true" json:"enabled"`

	Type          string `gorm:"size:16;default:tokens" json:"type"` // tokens | times
	PerCallMicros int64  `gorm:"default:0" json:"per_call_micros"`   // type=times 每次额度(micros)
}

// UsageDaily 按 (日期, App, 模型) 的日聚合。
type UsageDaily struct {
	BaseModel
	StatDate            string `gorm:"size:10;index;uniqueIndex:uniq_daily,priority:1" json:"stat_date"` // YYYY-MM-DD
	AppID               uint64 `gorm:"index;uniqueIndex:uniq_daily,priority:2" json:"app_id"`
	Model               string `gorm:"size:128;uniqueIndex:uniq_daily,priority:3" json:"model"`
	Requests            int64  `json:"requests"`
	PromptTokens        int64  `json:"prompt_tokens"`
	CompletionTokens    int64  `json:"completion_tokens"`
	CachedTokens        int64  `json:"cached_tokens"`
	CacheCreationTokens int64  `json:"cache_creation_tokens"`
	ReasoningTokens     int64  `json:"reasoning_tokens"`
	CostMicros          int64  `json:"cost_micros"`
}

// TableName 固定表名，避免 GORM 复数化为 usage_dailies，使 Expr 表名前缀可控。
func (UsageDaily) TableName() string { return "usage_daily" }

// UsageHourly 按 (小时, App, 模型) 的小时聚合。
type UsageHourly struct {
	BaseModel
	StatHour            string `gorm:"size:13;index;uniqueIndex:uniq_hourly,priority:1" json:"stat_hour"` // YYYY-MM-DD HH
	AppID               uint64 `gorm:"index;uniqueIndex:uniq_hourly,priority:2" json:"app_id"`
	Model               string `gorm:"size:128;uniqueIndex:uniq_hourly,priority:3" json:"model"`
	Requests            int64  `json:"requests"`
	PromptTokens        int64  `json:"prompt_tokens"`
	CompletionTokens    int64  `json:"completion_tokens"`
	CachedTokens        int64  `json:"cached_tokens"`
	CacheCreationTokens int64  `json:"cache_creation_tokens"`
	ReasoningTokens     int64  `json:"reasoning_tokens"`
	CostMicros          int64  `json:"cost_micros"`
}

// TableName 固定表名，避免 GORM 复数化为 usage_hourlies。
func (UsageHourly) TableName() string { return "usage_hourly" }

// UsageDailyKey 按 (令牌, 模型) 的每日用量聚合。model 维度 2026-07-18 加入（原迁移 0002，已折叠进 0001 基线），
// 此前存量行 model 为 NULL（ALTER TABLE 加列不回填历史数据，GORM 读出为零值空串），历史未细分，
// 按 stat_date 分组的日汇总不受影响。
type UsageDailyKey struct {
	BaseModel
	StatDate            string `gorm:"size:10;uniqueIndex:uniq_daily_key,priority:1" json:"stat_date"`
	APIKeyID            uint64 `gorm:"uniqueIndex:uniq_daily_key,priority:2" json:"api_key_id"`
	Model               string `gorm:"size:128;uniqueIndex:uniq_daily_key,priority:3" json:"model"`
	Requests            int64  `json:"requests"`
	PromptTokens        int64  `json:"prompt_tokens"`
	CompletionTokens    int64  `json:"completion_tokens"`
	CachedTokens        int64  `json:"cached_tokens"`
	CacheCreationTokens int64  `json:"cache_creation_tokens"`
	ReasoningTokens     int64  `json:"reasoning_tokens"`
	CostMicros          int64  `json:"cost_micros"`
}

func (UsageDailyKey) TableName() string { return "usage_daily_key" }

// UsageDailyChannel 按 (日期, 渠道, 模型) 的每日聚合，供渠道级用量/成本/可用性分析与上游账单对账。
//
// 与 UsageDaily / UsageDailyKey 的关键区别：本表**不预过滤 status**——渠道维度最有价值的
// 指标之一是错误率，故 Requests/Errors 由聚合器用 CASE WHEN 分别统计；而 token 与 CostMicros
// 仍只累计成功请求（口径与 App/Key 表一致，保证渠道成本能与应用成本横向对账）。
//
// 两个平均值直接存均值而非 sum+count：聚合器每次从 request_logs 全量重算当日并绝对值覆盖写
// （不是增量累加），故不会累积漂移。跨天/跨模型再汇总时须按请求数加权，见 service.go 的
// sumColsChannel。
type UsageDailyChannel struct {
	BaseModel
	// StatDate/ChannelID 各带独立 index：查询恒为 channel_id = ? AND stat_date >= ?，
	// 而复合唯一索引以 stat_date 打头、服务不了 channel_id 的等值探测（与 UsageDaily 同理）。
	StatDate  string `gorm:"size:10;index;uniqueIndex:uniq_daily_channel,priority:1" json:"stat_date"`
	ChannelID uint64 `gorm:"index;uniqueIndex:uniq_daily_channel,priority:2" json:"channel_id"`
	Model     string `gorm:"size:128;uniqueIndex:uniq_daily_channel,priority:3" json:"model"`
	Requests  int64  `json:"requests"` // 成功请求数（status < 400）
	Errors    int64  `json:"errors"`   // 失败请求数（status >= 400）
	// StreamRequests 成功且有首 Token 计时（first_token_ms > 0）的请求数。
	// ⚠️ 它不等于「流式请求数」：FirstTokenMs 取 Milliseconds()，首 Token 在 1ms 内到达的
	// 流式请求会记为 0 而不计入（实测极快的本地 mock 上游即如此）。真实上游首 Token 通常
	// 远超 1ms，影响可忽略；此列的唯一职责是给 AvgFirstTokenMs 提供同口径的加权分母。
	// 存在的唯一理由是给 AvgFirstTokenMs 提供正确的加权分母——用 Requests 加权会严重失真：
	// 「1 条流式(1000ms)+999 条非流式」与「1000 条流式(10ms)」两天汇总，真值约 11ms，
	// 按 Requests 加权得 505ms（45 倍高估）。
	StreamRequests      int64 `json:"stream_requests"`
	PromptTokens        int64 `json:"prompt_tokens"`
	CompletionTokens    int64 `json:"completion_tokens"`
	CachedTokens        int64 `json:"cached_tokens"`
	CacheCreationTokens int64 `json:"cache_creation_tokens"`
	ReasoningTokens     int64 `json:"reasoning_tokens"`
	CostMicros          int64 `json:"cost_micros"`
	AvgLatencyMs        int64 `json:"avg_latency_ms"`     // 成功请求的平均总延迟
	AvgFirstTokenMs     int64 `json:"avg_first_token_ms"` // 成功流式请求的平均首 Token 延迟（>0 才计入）
}

// TableName 固定表名，避免 GORM 复数化。
func (UsageDailyChannel) TableName() string { return "usage_daily_channel" }

// SystemSetting 系统级设置(结构化单例,固定 ID=1)。目前承载审计/请求日志的定时清理配置。
type SystemSetting struct {
	BaseModel
	AuditCleanupEnabled      bool `gorm:"default:false" json:"audit_cleanup_enabled"`
	AuditRetentionDays       int  `gorm:"default:30" json:"audit_retention_days"`
	RequestLogCleanupEnabled bool `gorm:"default:false" json:"request_log_cleanup_enabled"`
	RequestLogRetentionDays  int  `gorm:"default:90" json:"request_log_retention_days"`

	// P9-B 使用方门户
	PortalRegisterEnabled bool  `gorm:"default:false" json:"portal_register_enabled"` // 开放注册开关
	PortalRegisterQuota   int64 `gorm:"default:1000000" json:"portal_register_quota"` // 注册自动建默认 App 的配额(micros),默认 1 USD

	// P9-C 通用 OIDC 第三方登录（client_secret 落库且管理端可回显，对齐令牌明文既有取舍）。
	// 显式列名：GORM 默认会把 OIDC 前缀拆成 o_id_c_*（只识别 ID 缩写）。
	OIDCEnabled      bool   `gorm:"column:oidc_enabled;default:false" json:"oidc_enabled"`
	OIDCIssuer       string `gorm:"column:oidc_issuer;size:256" json:"oidc_issuer"`
	OIDCClientID     string `gorm:"column:oidc_client_id;size:128" json:"oidc_client_id"`
	OIDCClientSecret string `gorm:"column:oidc_client_secret;size:256" json:"oidc_client_secret"`
	OIDCButtonText   string `gorm:"column:oidc_button_text;size:64;default:使用 OIDC 登录" json:"oidc_button_text"`
	// OIDCRedirectURL 回调地址（须与提供方登记的一致，以 /portal/api/oidc/callback 结尾）。由管理员显式配置，
	// 不再从请求 Host / X-Forwarded-Proto 推导——那两个头可被客户端或错配的反代左右。启用 OIDC 时必填。
	OIDCRedirectURL string `gorm:"column:oidc_redirect_url;size:256" json:"oidc_redirect_url"`

	// 门户注册人机验证：""=不启用 | image=图形验证码 | turnstile=Cloudflare Turnstile。
	// turnstile_secret_key 落库且管理端可回显，与 oidc_client_secret 同一取舍。
	CaptchaType        string `gorm:"size:16" json:"captcha_type"`
	TurnstileSiteKey   string `gorm:"size:128" json:"turnstile_site_key"`
	TurnstileSecretKey string `gorm:"size:128" json:"turnstile_secret_key"`
}
