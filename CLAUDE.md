# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

> 本项目对话与代码注释一律使用**简体中文**（关键字、协议名等特殊情况除外）。生成文件的作者署名用 `ruanun`。

## 项目定位

`simple-ai-gateway` 是自托管的企业级 AI 网关：多协议入站同协议透传、三层智能路由、渠道多密钥池 + IP 代理池容灾、取证级审计回放、精细计费与配额。后端 Go 1.26 + Gin + GORM（默认纯 Go SQLite，零 cgo），前端 React 19 + Vite，编译为**单个可执行文件**部署（前端静态资源通过 `//go:embed` 内嵌）。

> **定位（重要）**：本项目专注「API 聚合 + 计费 + 多租户分发 + 审计」这一层（对标 new-api / one-hub / litellm 的商业分发面）。**不做订阅接入**——即不做 OAuth 接管 Claude/ChatGPT 等订阅账号再转成 API 的事；那类高维护、高风险的活交给专业上游项目（如 CLIProxyAPI、sub2api），它们把订阅转成 OpenAI/Anthropic 兼容端点后，在本网关里**注册成一条普通渠道**即可，其可用性由「渠道容灾/降级」天然覆盖。因此这里的「密钥池」是渠道多密钥的负载/容灾池（每个 `Account` 存一份上游 API Key），并非薅号防封的「号池」。

主分支为 `master`（v2 从零重写版本）。

## 常用命令

```bash
# —— 后端 ——
go run ./cmd/server -c config.yaml   # 启动后端，默认 :8080，首启建表并按 config 播种 admin，数据落 data/saig.db
# ⚠️ 安全启动护栏：jwt.secret / channel_key_aes_key / admin.password 三项为内置默认值或空时拒启。
# 首启前先 `cp config.example.yaml config.yaml` 并填入强随机凭证（openssl rand ...）。
go test ./...                    # 跑全部 Go 测试
go test ./internal/domain/gateway/ -run TestDispatch -v   # 跑单个测试（-run 接测试函数名/正则）
go vet ./...                     # 静态检查

# —— 前端（web/ 目录，包管理器用 pnpm）——
cd web && pnpm install
pnpm dev        # 开发服务器 :5173，已把 /admin 与 /v1 代理到后端 :8080
pnpm build      # 产出 web/dist（tsc -b && vite build）
pnpm lint       # eslint

# —— 打包单二进制（关键顺序，见下）——
cd web && pnpm build && cd .. && go build -o server.exe ./cmd/server
```

### ⚠️ 构建顺序陷阱

`web/embed.go` 用 `//go:embed dist/*` 内嵌前端产物，而 `web/dist/` 被 `.gitignore` 忽略、不入库。**因此 `go build`/`go run ./cmd/server` 之前，`web/dist/` 必须已存在**（先跑 `cd web && pnpm build`）。CI/Docker 也需先构建前端再编译后端。

### 本地联调辅助进程

`tools/` 下有独立的测试用上游/代理进程，可 `go run` 起来对拍：
- `tools/mock/mock.go`：模拟 OpenAI/Anthropic 上游
- `tools/proxy/proxy.go`：模拟出站代理，验证 IP 代理池；`-addr`/`-tag` 起多实例（如 `go run ./tools/proxy -addr 127.0.0.1:8898 -tag B`）演示代理池轮换

## 架构总览

### 请求主链路（`/v1/*` 业务面）

一次 chat 请求依次穿过（以 `internal/domain/gateway/handler.go` 的 `ChatHandler.Completions` 为主干）：

1. **中间件**（`internal/middleware`）：`APIKeyAuth` 校验 `sk-saig` 令牌（`apikey.Service` 带 5s 进程缓存 + Redis Pub/Sub 跨实例失效；有日成本上限的 key 不缓存用量字段）→ `APIKeyRateLimit` 按 key 级 RPM 限流。
2. **令牌治理** `govern()`：模型映射（`ModelMappings` 重写请求 model）→ 白名单校验（403）→ key 日成本上限（402，次日重置）。映射后的 model 贯穿后续路由/计费/日志/审计。
3. **路由解析** `router.Table.Resolve(model)`（`internal/router`）：内存路由表返回候选 `[]Candidate`（真实模型直调 + 别名展开）。别名按 `priority_weight` 分层 + 加权随机轮询。路由表是**热加载单例**，写操作后经 Redis Pub/Sub（`ChannelReloadRouter`）通知各实例 `rt.Build()` 重建。
4. **候选过滤**：`filterByProtocol`/`filterWithConvert`（**一渠道一协议**，协议=`Channel.Type`；同协议候选透传，开 `allow_convert` 的异协议渠道经 chat↔messages 转换兜底，转换候选恒排在透传候选之后；双协议上游各建一条渠道）+ `filterByAllowedChannels`（App 渠道 ACL）。
5. **配额/价格前置校验**：App 配额（402）、目标真实模型必须已配价格（否则 400，用候选的 `c.Model` 而非请求名查价）。
6. **派发** `Dispatch()`（`dispatch.go`）：按候选定序尝试，每候选内做 熔断过滤 → 渠道级 RPM 限流 → `MaxRetries` 次重试。每次尝试经 `AccountSelector` 选密钥池账号（先过滤过期/临时停用/限流与过载冷却账号），再在派发层做账号级 RPM + 在途并发占位（`accountslot.go`，Redis 模式沿用租约），经 `ProxyResolver` 选出站代理，最后 `DialWith` + `Relay`（流式 SSE 透传，等待首 token 时发 `: keepalive` 注释；anthropic 走 `RelayAnthropic`）。全失败返回 502。
7. **计费/落库**：成功且 status<400 才计费（`billing.Compute` × 渠道 `RateMultiplier`）→ 扣 App 配额 + 累加 key 日成本 → 异步审计投递 → 写 `RequestLog`。

### 三层路由 + 双池容灾

- **Provider（平台）→ Channel（渠道）→ Model（模型）**：`Channel` 持有 base_url/超时/优先级/权重/RPM/倍率；`ChannelModel` 声明渠道支持的真实模型。
- **密钥池（多密钥负载/容灾）**：`Account` 归属 Channel，上游凭证下沉到账号（`APIKeyEnc` AES 密文）。`AccountSelector`（`selector.go`）按 优先级+权重+健康 轮换，账号级熔断，渠道内重试时排除已试账号。账号级调度字段：`MaxConcurrency` / `RPMLimit` / `ExpiresAt` / `TempUnschedulableUntil` / `RateLimitResetAt` / `OverloadUntil`；用于同渠道多把 API Key 分摊速率限制、单 Key 故障/额度耗尽自动切换。
- **错误透传**：`Channel.ErrorPassthrough` 开启后，`respondDispatchFailure` 用最后一个上游错误的状态码 + `scrubDetail` 脱敏文案回客户端；未开启保持公共摘要（不含内部拓扑）。直接上游非重试错误本来就经 `scrubResp` 透传。
- **IP 代理池**：`Account.ProxyGroup` 绑定代理组，`ProxyResolver`（`proxyresolver.go`）经 http/https/socks5 出站，代理级熔断。密钥池与代理池协同实现「账号 + IP 双通道轮换」，用于区域直连/分流与提升可用性。

### Redis 可选（内存回退）模式

`server.NewWithShutdown` 依据 `redisCli != nil` 在**两套实现**间切换：限流 `ratelimit.Limiter`、熔断 `breaker.Breaker`（渠道/账号/代理各一）、会话粘连 `session.StickyStore`。无 Redis 时全部走进程内存实现，功能不降级但不跨实例共享。多实例部署才需要 Redis + Pub/Sub（价格缓存 `ChannelReloadPrice`、路由 `ChannelReloadRouter` 写后通知）。

### 计费预留（reserve-then-commit）

请求进入 `Prepare` 后、派发前，按请求体可推断的输入/输出上限 × 候选最高价估算最大成本，分别对 App 配额与 key 日成本做条件预留（`internal/domain/gateway/reserve.go`）：App 侧只预留到剩余额度为止，key 日限额同样只预留到当日剩余额度；响应后 `settleCommon` 按实际成本多退少补，失败/提前返回由 `middleware.AddCleanup` 全额释放。并发临界点因此不会再被「先查后扣」同时穿透；真实成本超出预留的部分在结算时补差。

### 计费单位

全程用 **USD 微量整数**：`1 USD = 1,000,000 micros`。价格表 `ModelPrice` 的 `*_per_1m` 字段单位为「USD / 1M token」，分项计价（input/output/cached/cache write）+ 按次计费（`type=times`）。价格解析 `prices.Resolve(channelID, model)`：先查渠道覆盖价（`channel_id != 0`），回退全局官方价（`channel_id = 0`）。别名场景按**实际命中的真实模型**（`out.Model`/`servedModel`）计费，不按别名名。

### 请求幂等

非流式 `/v1` POST 支持 `Idempotency-Key`（`internal/middleware/idempotency.go` + `idempotency_records` 表）：首次登记 processing 并缓存响应，同 key 同请求体直接回放且不重复计费；不同请求体 409；流式请求拒绝。记录 24h 过期，由 cleanup 定时清理。

### 可观测性与备份

- `GET /healthz`、`GET /readyz`（DB/Redis/审计存储探活）、`GET /metrics`（Prometheus 文本：构建信息、依赖健康、今日请求/错误/token/成本/延迟、进程内存/goroutine）。
- 管理端「渠道健康」页：`GET /admin/channels/health?days=7` 按渠道聚合成功率/错误数/加权平均延迟/首 Token/费用。
- 备份/恢复：`./server -c config.yaml --backup <dir>` 用 SQLite `VACUUM INTO` 生成一致快照并复制本地审计目录；`--restore <dir|file>` 先保留当前库再替换并跑迁移。MySQL/PG 用 `mysqldump`/`pg_dump`。

### 数据层与迁移

- `internal/model/model.go`：所有 GORM 模型（Channel/Account/Proxy/APIKey/App/ModelAlias/AliasRoute/ModelPrice/RequestLog/Audit*/Usage* 等）。
- `internal/model/migrations.go`：当前只保留**单条 0001_init_schema 基线**，通过 `internal/db/migrate.go` 的 `Migrate()` 幂等执行（`schema_migrations` 记录已应用）。新增 schema 变更直接改 0001 的 AutoMigrate 列表，不再维护逐版本增量迁移；不存在 `legacy.go`。旧部署库升级前备份并清空 `schema_migrations`（或重建库），启动后 0001 AutoMigrate 补齐当前 schema。
- 多驱动：`sqlite`（默认，`glebarez/sqlite` 纯 Go）/ `mysql` / `postgres`。
- 测试用内存库：`internal/dbtest.OpenInMemory(t)`，按测试名派生独立 shared-cache 命名空间，避免测试间污染。

### 取证级审计

`audit.AsyncWriter`（缓冲 channel + worker 池）非阻塞接收原始请求体 + 上游响应体，随进程存活。**正文不进数据库**：gzip 后经 `internal/storage` 的 `Store` 接口写入 `local`（缺省 `data/audit/`）或 `s3` 后端，key 形如 `audit/YYYY/MM/DD/<request_id>/{request,response}.gz`；`audit_records` 行只留对象 key 与取证元数据（size/orig_size/truncated/原文 SHA-256）。写顺序 Put 请求体 → Put 响应体 → insert 行，任一步失败即回滚已写对象、计数并记日志（fail-open）。读路径 `audit.Service.FetchBody` 只在详情页有权限角色点开时调用；清理 `audit.Service.Cleanup` 分批先删对象再删行，对象删失败则行保留下轮重试。仅在对应 App `AuditEnabled=true` 时投递。多实例部署必须 `storage.type: s3`（local 不跨节点）。`storage.New` 在装配点构造，配置错误或桶不可达拒启。用量聚合走独立定时任务 `usage.NewAggregator`（每分钟从 `request_logs` 重聚合当日到 `usage_daily`/`usage_hourly`/`usage_daily_key`），请求链路不再同步写聚合表。

### 服务装配

`internal/server/server.go` 的 `NewWithShutdown()` 是**唯一的依赖装配点**：构造所有 service/handler、注册路由、挂中间件、内嵌前端。业务面 `/v1/*` 挂 `APIKeyAuth`；管理面 `/admin/*` 挂 `JWTAuth`（`/admin/login` 除外）。管理面另有一层 `adminRoot` 组（`RequestID` + `AccessLog`）包住含登录在内的全部 admin 端点，提供访问日志与 `request_id`；错误详情由 `httpx.Err` 这个全站唯一错误出口统一记日志（见下「可观测性」）。`NoRoute` 兜底把非 `/v1`、`/admin` 的路径回退到内嵌的 SPA `index.html`。修改依赖注入或新增端点通常都在这个文件。

> 装配顺序：`deps.Proxy` 已改为在 `Deps` 字面量构造期直接注入（无装配顺序陷阱）。每个请求会构造一份 `Deps` 副本再设置 `Lookup`，避免并发写共享闭包。

## 前端

- `web/src/pages/`：每个管理页面一个组件（Channels/Aliases/ApiKeys/Apps/Prices/Proxies/Logs/Dashboard 等）。审计正文回放并入请求日志详情抽屉（无独立审计页）。
- `web/src/lib/i18n.tsx`：以中文原文为 key 的增量 i18n（`I18nProvider` + `useI18n` + `LocaleToggle`，localStorage 持久化）。导航、登录、仪表盘、渠道/令牌/应用/价格/别名/日志/设置/渠道健康、门户、调试台、用量分析等主要页面已迁移；接入文档正文与部分长篇帮助文案仍为中文。未翻译条目自动回落中文；新增页面用 `t('中文原文')` 包裹即可。
- `web/src/lib/api.ts`：极简 fetch 封装，`api.get/post/del`，JWT 存 localStorage（`saig_token`），401 自动清 token 跳登录。
- Tailwind CSS v4（`@tailwindcss/vite`）+ **shadcn/ui**（`web/src/components/ui`，复制式组件、Radix 内核，样式接站点 CSS 变量，未跑 CLI init）。配色：主蓝 `#1E40AF` + 强调琥珀 `#D97706`（见 `docs/UI设计规范.md`）。
- **组件策略：默认用 shadcn/ui 保统一**——新组件先查 shadcn 有无现成实现（复制进 `ui/` 并适配 CSS 变量）；交互类组件（弹层/下拉/tabs 等）必须 Radix 内核；仅当 shadcn 不满足需求或自研确实更好时才例外（如 badge 状态圆点、code-view、refresh）。原生 `alert/confirm/prompt/<select>` 禁用，分别用 `toast`、`confirmDialog/promptDialog`（`ui/confirm.tsx`，Promise 化）、`SimpleSelect`。

## 约定与注意事项

- 代码注释、commit 信息用简体中文。
- **可观测性：错误日志的唯一收敛点是 `httpx.Err`**。管理面/门户面所有错误响应都经它，内部按状态码分级记日志（5xx→`Error`、401/403→`Info`、其余 4xx→`Warn`），字段含 `method/path/status/code/message/client_ip/request_id`。因此**新增管理面 handler 无需为「返回错误」再写一条 slog**——重复记会产生两行。只有当你掌握了响应文案之外的信息（上游原始响应、内部 err 细节、重试过程）才值得另记一行，且敏感内容须先脱敏（`gateway.scrubSecrets` / 按已知凭证精确替换）。访问日志由 `middleware.AccessLog` 承担，只挂管理面。
- 同协议透传：`/v1/chat/completions`、`/v1/messages`、`/v1/responses` 三主端点**入什么协议出什么协议，不做跨协议转换**，只替换凭证 + 改写 model（`withModel`）。
- 出站 body 的 model 必须按候选 `c.Model` 改写（别名/映射后目标真实模型），出站正确性需用「会校验请求体的 mock」来测，普通透传 mock 会漏掉改写 bug。
- 敏感字段（`APIKeyEnc`、Proxy `Password`、`KeyHash`、`PasswordHash`）标 `json:"-"` 不出 API；审计/日志请求头需脱敏（`sanitizeHeaders`）。
- **GORM `default:` 标签 + 零值陷阱**：字段标了非零默认值（`Status default:1`、`Enabled`/`AuditEnabled default:true`、`Timeout default:60`、`Weight default:1` 等）时，`Create`/`Save` 这类**结构体写入路径会把该字段的零值替换成默认值再绑参**——`status=0`、`enabled=false` 写不进去，且**返回 200 但数据没变**，最难察觉。`Updates(map[string]any{...})` / `Update("col", v)` 不经此逻辑，是安全写法。停用/关闭类操作一律走 map 更新；确需经 `Create`（含 `OnConflict` Upsert）写零值时，冲突分支用 `clause.Assignments` 取实际值（见 `internal/domain/price/repo.go` 的 Upsert），插入分支则须把字段改 `*bool`/去 default 标签。改动涉及布尔或状态字段时，测试与评审必须**双向取值都验**（只验 true 方向会漏掉整个 bug）。
- `security.channel_key_aes_key` 长度必须为 16/24/32 字节，否则进程拒绝启动。
- **客户端 IP 采信规则**：装配点按 `server.trusted_proxies` 调 `SetTrustedProxies`，缺省为空即 `c.ClientIP()` 只信 TCP 对端地址、忽略 `X-Forwarded-For`；请求日志/审计/操作日志的 `client_ip` 与登录限流键统一走 `c.ClientIP()`，不要再用 `RemoteAddr` 另起口径。反代部署必须在配置里填代理地址，否则全站共用一个登录限流桶。
- 测试若经 `server.NewWithShutdown` 构造服务，必须 `cfg.Storage.Local.Root = t.TempDir()`：`storage.local.root` 为空拒启，正是为了防止零值配置把审计正文写进包目录留下垃圾。
- 全局 Git 约束（用户级）：本会话只读 git（`log`/`status`/`diff`/`show`），**不执行** commit/push/pull/merge/rebase/reset。

## 设计文档

`docs/` 下有：`docs/快速开始.md`/`docs/部署指南.md`/`docs/使用手册.md`（面向用户）、`docs/UI设计规范.md`。另有本地保留、不入库的内部工作文档：`docs/roadmap.md`（待办与遗留项）、`docs/superpowers/specs/`（各阶段详细设计 spec）；代码注释中的 `P4`/`P6a`/`P7a` 等标记对应这些阶段 spec。
</content>
</invoke>
