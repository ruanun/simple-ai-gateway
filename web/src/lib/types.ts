// 跨页面共享的域类型：与后端接口返回的 JSON 形状一一对应。
// 原则：同一接口返回的实体只保留一份全字段声明，各页面按需取用（TS 结构类型天然兼容子集用法）；
// 管理端与门户端形状确实不同的（如令牌）不强并，导出基础型 + 两端各自扩展。

// 渠道（/admin/channels）
export type Channel = {
  id: number
  type: string
  name: string
  base_url: string
  status: number
  timeout: number
  priority: number
  weight: number
  rate_multiplier: number
  max_retries: number
  rpm_limit: number
  remark: string
  allow_convert: boolean
  strip_thinking: boolean
  disable_stream_usage: boolean
  error_passthrough: boolean
  auth_style: string
  custom_headers: string
}

// 应用（/admin/apps 与 /portal/api/apps 均序列化自同一 model.App）
export type App = {
  id: number
  name: string
  quota: number
  used_quota: number
  status: number
  audit_enabled: boolean
  allowed_channels: string
  owner_user_id: number
  model_whitelist: string  // 管理员对租户的模型白名单（逗号分隔，空=不限），门户不可改
  max_concurrency: number  // 同时在途请求上限：0=按全局默认，-1=不限
}

// 使用方用户（/admin/portal-users）
export type PortalUser = {
  id: number
  username: string
  email: string
  status: number
  source: string
  app_count: number
  created_at: string
}

// 请求日志行（/admin/request-logs 列表项，全字段基型；门户/详情用 Pick/Partial 派生）
export type RequestLogRow = {
  id: number
  model: string
  request_type: string
  status: number
  latency_ms: number
  first_token_ms: number
  stream: boolean
  error_msg: string
  created_at: string
  prompt_tokens: number
  completion_tokens: number
  cached_tokens: number
  cache_creation_tokens: number
  reasoning_tokens: number
  cost_micros: number
  usage_missing: boolean
  channel_id: number
  app_id: number
  api_key_id: number
  session_id: string
  client_ip: string
  headers: string
  account_id?: number
  request_id?: string
  protocol: string
  upstream_protocol: string
}

// 用量趋势行（usage-analysis 接口的 trend 数组行；原 usage-trend 端点已随 UsageTrendDialog 移除）
export type TrendRow = {
  stat_date: string
  requests: number
  prompt_tokens: number
  completion_tokens: number
  cached_tokens: number
  cost_micros: number
}

// 模型聚合行（usage-analysis 端点 models 块）
export type ModelUsageRow = {
  model: string
  requests: number
  prompt_tokens: number
  completion_tokens: number
  cached_tokens: number
  cost_micros: number
}

// 日×模型明细行（usage-analysis 端点 daily_models 块）
export type DailyModelRow = {
  stat_date: string
  model: string
  requests: number
  prompt_tokens: number
  completion_tokens: number
  cost_micros: number
}

// 用量分析端点响应（/admin 与 /portal/api 同形）
export type UsageAnalysisData = {
  trend: TrendRow[]
  models: ModelUsageRow[]
  daily_models: DailyModelRow[]
}

// 渠道趋势/模型行：在通用行基础上多 errors 与两个平均延迟（渠道维度关注可用性与快慢）
export type ChannelTrendRow = TrendRow & {
  errors: number
  avg_latency_ms: number
  avg_first_token_ms: number
}

export type ChannelModelUsageRow = ModelUsageRow & {
  errors: number
  avg_latency_ms: number
  avg_first_token_ms: number
}

// 渠道用量分析响应：字段是 UsageAnalysisData 的超集，可直接喂给通用 AnalysisView。
// daily_models 沿用 lite 口径（堆叠图不需要 errors 与延迟），与应用/令牌相同。
export type ChannelUsageAnalysisData = {
  trend: ChannelTrendRow[]
  models: ChannelModelUsageRow[]
  daily_models: DailyModelRow[]
}

// 令牌公共核心字段：管理端与门户端页面关注字段差异大，不强并，各自在核心上扩展
export type ApiKeyBase = {
  id: number
  name: string
  prefix: string
  status: number
  app_id: number
}

// 管理端令牌（/admin/api-keys）：治理字段均可选
export type AdminApiKey = ApiKeyBase & {
  key?: string // P11: 明文（旧令牌为空，不可回查）
  rpm?: number
  daily_cost_limit?: number
  daily_cost_used?: number
  model_whitelist?: string
  model_mappings?: string
  disabled_by_admin?: boolean // 管理员停用：门户不能重新启用
}

// 门户端令牌（/portal/api/api-keys）：自助令牌恒有明文与日成本字段
export type PortalApiKey = ApiKeyBase & {
  key: string
  daily_cost_limit: number
  daily_cost_used: number
  disabled_by_admin: boolean // 管理员停用：门户不能重新启用，只能删除
}

// —— 以下按端点补充，原则同上：裸序列化后端 model 的端点写全字段，定制 DTO 按实际响应形状 ——

// 仪表盘（/admin/dashboard 定制聚合响应）
export type DashKPI = { requests: number; prompt_tokens: number; completion_tokens: number; cached_tokens: number; cost_micros: number }
export type DashTrend = { stat_hour: string; requests: number; prompt_tokens: number; completion_tokens: number; cached_tokens: number; cost_micros: number }
export type DashTop = { model: string; requests: number; prompt_tokens: number; completion_tokens: number; cost_micros: number }
export type DashOps = { requests: number; errors: number; avg_latency_ms: number; avg_first_token_ms: number }
export type DashTopApp = { app_id: number; name: string; requests: number; prompt_tokens: number; completion_tokens: number; cost_micros: number }
export type DashChannelToday = { channel_id: number; name: string; requests: number; errors: number }
export type DashData = { kpi: DashKPI; kpi_total: DashKPI; trend: DashTrend[]; top: DashTop[]; app_count: number; ops: DashOps; top_apps: DashTopApp[]; channels: DashChannelToday[] }

// 模型别名（/admin/aliases）：route_count 是列表端点聚合的路由边数，非别名表字段
export type ModelAlias = { id: number; alias: string; description: string; status: number; route_count: number }

// 别名路由边（/admin/aliases/:id/routes）
export type AliasRoute = { id: number; alias_id: number; channel_id: number; model: string; weight: number; priority: number }

// 模型价格（/admin/prices）：channel_id=0 全局官方价，非零=渠道覆盖价；per_1m 单位 USD/1M token
// 全局价与渠道覆盖价共用一份全字段声明（后端 ModelPrice 全字段序列化）
export type ModelPrice = {
  id: number
  channel_id: number
  model: string
  input_per_1m: number
  output_per_1m: number
  cached_input_per_1m: number
  cache_write_per_1m: number
  currency: string
  enabled: boolean
  type: string // tokens | times
  per_call_micros: number
}

// models.dev 同步建议行（/admin/prices/sync-models-dev 定制 DTO）
export type PriceSuggestion = { model: string; input: number; output: number; cache_read: number; cache_write: number; has_price: boolean }

// 出站代理（/admin/proxies）。命名避开 JS 全局 Proxy
export type ProxyItem = {
  id: number
  group: string
  name: string
  protocol: string
  host: string
  port: number
  username: string
  weight: number
  priority: number
  status: number
  fail_count: number
  last_used_at: string | null
}

// 密钥池账号（/admin/accounts）：上游 API Key 密文（APIKeyEnc）不出 JSON
export type Account = {
  id: number
  channel_id: number
  name: string
  base_url: string
  weight: number
  priority: number
  status: number
  fail_count: number
  last_used_at: string | null
  proxy_group: string
  max_concurrency: number
  rpm_limit: number
  expires_at: string | null
  temp_unschedulable_until: string | null
  temp_unschedulable_reason: string
  rate_limit_reset_at: string | null
  overload_until: string | null
}

// 渠道健康聚合（/admin/channels/health）
export type ChannelHealthRow = {
  channel_id: number
  name: string
  requests: number
  errors: number
  error_rate: number
  stream_requests: number
  cost_micros: number
  avg_latency_ms: number
  avg_first_token_ms: number
}

// 渠道模型（/admin/channel-models）
export type ChannelModel = { id: number; channel_id: number; model: string; enabled: boolean }

// 应用成员（/admin/apps/:id/members 定制 DTO：聚合 username/owner，非 app_members 表裸序列化）
export type AppMemberInfo = { user_id: number; username: string; is_owner: boolean }

// 操作日志行（/admin/op-logs）
export type OpLog = { id: number; created_at: string; actor: string; role: string; method: string; path: string; status: number; client_ip: string }

// 管理员用户（/admin/users）
export type AdminUser = { id: number; username: string; status: number; role: string; created_at: string }

// 系统设置（/admin/settings 读写同形）
export type Setting = {
  audit_cleanup_enabled: boolean
  audit_retention_days: number
  request_log_cleanup_enabled: boolean
  request_log_retention_days: number
  portal_register_enabled: boolean
  portal_register_quota: number
  oidc_enabled: boolean
  oidc_issuer: string
  oidc_client_id: string
  oidc_client_secret: string
  oidc_button_text: string
  oidc_redirect_url: string // OIDC 回调地址（须与提供方登记一致），启用 OIDC 时必填
  captcha_type: '' | 'image' | 'turnstile' // 门户注册人机验证方式，'' = 不启用
  turnstile_site_key: string
  turnstile_secret_key: string
}

// 请求日志详情（GET /admin/request-logs/:id 定制响应：日志行 + 审计正文与取证字段）
export type LogDetail = {
  log: RequestLogRow
  has_audit: boolean
  request_body: string
  response_body: string
  user_agent: string
  request_truncated?: boolean
  request_orig_size?: number
  response_truncated?: boolean
  response_orig_size?: number
  request_sha256?: string
  response_sha256?: string
}
