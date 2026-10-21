package server

import (
	"fmt"
	"io/fs"
	"log/slog"
	"math/rand"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	goredis "github.com/redis/go-redis/v9"
	"github.com/ruanun/simple-ai-gateway/internal/admin"
	"github.com/ruanun/simple-ai-gateway/internal/audit"
	"github.com/ruanun/simple-ai-gateway/internal/breaker"
	"github.com/ruanun/simple-ai-gateway/internal/captcha"
	"github.com/ruanun/simple-ai-gateway/internal/config"
	"github.com/ruanun/simple-ai-gateway/internal/domain/account"
	"github.com/ruanun/simple-ai-gateway/internal/domain/alias"
	"github.com/ruanun/simple-ai-gateway/internal/domain/apikey"
	"github.com/ruanun/simple-ai-gateway/internal/domain/app"
	"github.com/ruanun/simple-ai-gateway/internal/domain/channel"
	"github.com/ruanun/simple-ai-gateway/internal/domain/cleanup"
	"github.com/ruanun/simple-ai-gateway/internal/domain/gateway"
	"github.com/ruanun/simple-ai-gateway/internal/domain/price"
	"github.com/ruanun/simple-ai-gateway/internal/domain/probe"
	"github.com/ruanun/simple-ai-gateway/internal/domain/proxy"
	"github.com/ruanun/simple-ai-gateway/internal/domain/requestlog"
	"github.com/ruanun/simple-ai-gateway/internal/domain/setting"
	"github.com/ruanun/simple-ai-gateway/internal/domain/usage"
	"github.com/ruanun/simple-ai-gateway/internal/httpx"
	"github.com/ruanun/simple-ai-gateway/internal/ipgeo"
	"github.com/ruanun/simple-ai-gateway/internal/middleware"
	"github.com/ruanun/simple-ai-gateway/internal/model"
	"github.com/ruanun/simple-ai-gateway/internal/notify"
	"github.com/ruanun/simple-ai-gateway/internal/portal"
	"github.com/ruanun/simple-ai-gateway/internal/proxydial"
	"github.com/ruanun/simple-ai-gateway/internal/ratelimit"
	saigredis "github.com/ruanun/simple-ai-gateway/internal/redis"
	"github.com/ruanun/simple-ai-gateway/internal/router"
	"github.com/ruanun/simple-ai-gateway/internal/session"
	"github.com/ruanun/simple-ai-gateway/internal/storage"
	"github.com/ruanun/simple-ai-gateway/internal/v1proto"
	"github.com/ruanun/simple-ai-gateway/web"
	"gorm.io/gorm"
)

// NewWithShutdown 装配 gin 引擎：业务面 /v1 挂 APIKeyAuth + APIKeyRateLimit，管理面 /admin 挂 JWTAuth。
// redisCli 不为 nil 时使用 Redis 实现限流/熔断/会话粘连；否则回退到进程内存实现。
// 额外返回 shutdown 函数：停止后台定时任务并 flush 审计队列，供 main 收到退出信号时调用(E5)。
func NewWithShutdown(cfg config.Config, g *gorm.DB, redisCli *goredis.Client) (*gin.Engine, func(), error) {
	r := gin.New()
	// 可信代理：gin 缺省信任所有来源的 X-Forwarded-For，client_ip 取证字段可被任意调用方伪造。
	// 按配置收紧——未配置传 nil 即只信 TCP 对端地址；写错（非 IP/CIDR）在此拒启，不静默退化。
	if err := r.SetTrustedProxies(cfg.Server.TrustedProxies); err != nil {
		return nil, nil, fmt.Errorf("server.trusted_proxies 配置非法: %w", err)
	}
	// 浏览器安全头（CSP/防点击劫持/nosniff）最先挂，panic 兜底的 500 响应也带上。
	r.Use(middleware.SecurityHeaders())
	r.Use(middleware.Recover())
	// 入站请求体大小上限（含未鉴权端点），防超大 body 内存 DoS。
	r.Use(middleware.BodyLimit(middleware.DefaultMaxRequestBody))

	// 路由表单例
	rt := router.NewTable()
	if err := rt.Build(g); err != nil {
		return nil, nil, fmt.Errorf("build router: %w", err)
	}

	// 按 Redis 是否可用选择限流/熔断/会话粘连实现
	proxydial.SetBlockPrivate(cfg.Security.UpstreamBlockPrivate)
	var limiter ratelimit.Limiter
	var concurrency ratelimit.Concurrency // App 在途并发上限
	var brk breaker.Breaker
	var accBreaker breaker.Breaker
	var proxyBreaker breaker.Breaker
	var accModelBrk breaker.Keyed // 账号×模型配额冷却（P12a）
	var stickyMap session.StickyStore
	var chSticky session.ChannelStickyStore

	// 熔断参数从 config.Resilience 装配（缺省即改造前硬编码值：channel 5 / account,proxy 3 / 30s窗口 / 60s冷却）。
	rc := cfg.Resilience
	toParams := func(p config.BreakerParams) breaker.Params {
		return breaker.Params{Window: p.Window, Cooldown: p.Cooldown, FailThreshold: p.FailThreshold, ErrorRate: p.ErrorRate, MinSamples: p.MinSamples, ProbeBackoffMax: p.ProbeBackoffMax}
	}
	if redisCli != nil {
		limiter = ratelimit.NewRedisLimiter(redisCli)
		concurrency = ratelimit.NewRedisConcurrency(redisCli)
		brk = breaker.NewRedisBreaker(redisCli, "ch", toParams(rc.Breaker.Channel))
		accBreaker = breaker.NewRedisBreaker(redisCli, "acc", toParams(rc.Breaker.Account))
		proxyBreaker = breaker.NewRedisBreaker(redisCli, "proxy", toParams(rc.Breaker.Proxy))
		accModelBrk = breaker.NewRedisKeyed(redisCli, rc.AccountModelBackoff.Base, rc.AccountModelBackoff.Max)
		stickyMap = session.NewRedisStickyStore(redisCli, cfg.Session.StickyTTL)
		chSticky = session.NewRedisChannelSticky(redisCli, cfg.Session.ChannelStickyTTL)
	} else {
		limiter = ratelimit.NewMemoryLimiter()
		concurrency = ratelimit.NewMemoryConcurrency()
		brk = breaker.NewMemoryBreaker(toParams(rc.Breaker.Channel))
		accBreaker = breaker.NewMemoryBreaker(toParams(rc.Breaker.Account))
		proxyBreaker = breaker.NewMemoryBreaker(toParams(rc.Breaker.Proxy))
		accModelBrk = breaker.NewMemoryKeyed(rc.AccountModelBackoff.Base, rc.AccountModelBackoff.Max)
		stickyMap = session.NewMemoryStickyStore(cfg.Session.StickyTTL)
		chSticky = session.NewMemoryChannelSticky(cfg.Session.ChannelStickyTTL)
	}

	// IP 代理池（P6a）：代理 service + 代理解析器。构造于 deps 字面量之前、作为字段直接进字面量——
	// 从语法上杜绝「先拷贝 deps 再补 Proxy」的装配顺序陷阱（历史坑：BillingCtx 拷到无 Proxy 的旧副本）。
	proxySvc := proxy.NewService(proxy.NewRepo(g))
	proxyResolver := gateway.NewProxyResolver(proxySvc, proxyBreaker, time.Now, rc.Breaker.Proxy.RecoverFactor)
	proxyResolver.SetCacheTTL(gateway.DefaultLookupCacheTTL) // 代理组列表查询缓存（与渠道/账号缓存同口径的 2s 有界陈旧）

	// anthropic auto 鉴权分流官方域名单（缺省仅 api.anthropic.com），由 config 覆盖。
	gateway.SetAnthropicOfficialHosts(cfg.Security.AnthropicOfficialHosts)

	deps := gateway.Deps{Breaker: brk, Limiter: limiter, Now: time.Now, MaxWall: rc.Retry.MaxWall,
		AccountLimiter: limiter, AccountConcurrency: concurrency,
		RecoverRoll: func() bool { return recoverRoll(rc.Breaker.Channel.RecoverFactor) },
		Proxy:       proxyResolver}

	keySvc := apikey.NewService(apikey.NewRepo(g), cfg.Security.APIKeyPrefix)
	// 鉴权缓存跨实例失效：本地删条目后广播，其他实例清空自己的 L1（key 增删是低频操作）。
	keySvc.SetAuthCacheNotify(func() {
		if err := saigredis.Publish(redisCli, saigredis.ChannelReloadAPIKey); err != nil {
			slog.Error("apikey cache invalidation publish 失败", "err", err)
		}
	})
	saigredis.Subscribe(redisCli, saigredis.ChannelReloadAPIKey, keySvc.InvalidateAllAuthCache)
	logSvc := requestlog.NewService(requestlog.NewRepo(g))
	if cfg.RequestLog.AsyncBuffer > 0 {
		logSvc.EnableAsync(cfg.RequestLog.AsyncBuffer) // 高并发可选：请求日志异步落库
	}
	aliasSvc := alias.NewService(alias.NewRepo(g))
	appSvc := app.NewService(app.NewRepo(g))
	priceSvc := price.NewService(price.NewRepo(g))
	usageSvc := usage.NewService(usage.NewRepo(g))
	// Prometheus 指标：公开聚合数字，不暴露渠道/账号/凭证拓扑。
	r.GET("/metrics", (&metricsHandler{db: g, redis: redisCli, usage: usageSvc, now: time.Now}).Serve)

	// 密钥池：渠道/账号 service + 账号选择器（封装 Lookup/RecordResult，数据访问走域 service）。
	chanSvc := channel.NewService(channel.NewRepo(g), cfg.Security.ChannelKeyAESKey)
	accSvc := account.NewService(account.NewRepo(g))
	selector := gateway.NewAccountSelector(chanSvc, cfg.Security.ChannelKeyAESKey, accSvc, accBreaker, accModelBrk, time.Now, stickyMap, rc.Breaker.Account.RecoverFactor)
	selector.SetCacheTTL(gateway.DefaultLookupCacheTTL) // 派发热路径渠道/账号查询缓存（2s 有界陈旧）

	// 审计正文存储层（local/s3）：正文不进数据库。配置错误或 S3 桶不可达时拒启，与安全护栏同级。
	store, err := storage.New(cfg.Storage)
	if err != nil {
		return nil, nil, fmt.Errorf("初始化审计存储失败: %w", err)
	}

	// 存活/就绪端点：不挂鉴权，供 Docker/K8s/反代探活。
	healthH := &healthHandler{db: g, redis: redisCli, store: store}
	r.GET("/healthz", healthH.Live)
	r.GET("/health", healthH.Live)
	r.GET("/readyz", healthH.Ready)

	// 取证级审计：异步写（缓冲 channel + worker 池：正文 gzip 后进存储层，元数据落 audit_records）+ 查询/回放/清理 service。
	// 生命周期由下方 shutdown 统一接管（退出时 Stop 会 flush 已入队条目，E5）。
	// 0/负值语义由 NewAsyncWriter 归一（buffer_size 回落 256；max_body_bytes 0=不截断，取证完整优先）；workers 回落 4。
	auditWriter := audit.NewAsyncWriter(g, store, cfg.Audit.BufferSize, cfg.Audit.MaxBodyBytes, cfg.Audit.Workers)
	auditSvc := audit.NewService(g, store)

	// 系统设置 + 定时清理任务（周期见 config.Jobs.CleanupInterval，缺省 6 小时跑一次审计/请求日志的保留期清理，尊重设置开关）。
	// 显式 0/负值回落内置默认（time.NewTicker 不接受非正间隔，零值 config.Config{} 场景下需兜底，测试常这样构造）。
	settingSvc := setting.NewService(setting.NewRepo(g))
	cleaner := cleanup.NewCleaner(auditSvc, logSvc, settingSvc, g, orDefault(cfg.Jobs.CleanupInterval, 6*time.Hour))

	// 用量聚合定时任务：从 request_logs 重新聚合当日统计到 usage_daily/usage_hourly/usage_daily_key，
	// 请求链路不再同步写聚合表，减少每请求 2-3 次额外 DB 写。启动即立即聚合一次。
	// 保留引用交由 shutdown 停止，避免 ticker 泄漏(E5)。
	aggregator := usage.NewAggregator(g, orDefault(cfg.Jobs.UsageAggregateInterval, time.Minute))

	// 主动探测器（Phase 3a）：定时探半开渠道恢复熔断。与 dispatch 共用同一 channel breaker(brk)。
	// enabled=false 时不启动(默认 true，见 config 缺省)；生命周期交下方 shutdown 统一 Stop。
	var prober *probe.Prober
	if cfg.Resilience.Probe.Enabled {
		prober = probe.NewProber(probe.Deps{
			Breaker: brk, Channels: chanSvc, Accounts: accSvc, AESKey: cfg.Security.ChannelKeyAESKey,
			// 保留 proxyID 供探测失败归因（此前用 _ 丢弃，代理故障被误记渠道熔断）。
			ClientFor: func(group string, sec int) (*http.Client, uint64, bool) {
				return proxyResolver.ClientFor(group, sec, false)
			},
			RecordProxyResult: proxyResolver.RecordResult,
			ProbeModels:       gateway.ProbeModels,
		}, cfg.Resilience.Probe)
	}

	// 告警最小版（Phase 3b）：enabled 且配了 webhook_url 才建 WebhookNotifier，否则 Nop。
	var notifier notify.Notifier = notify.NopNotifier{}
	var webhookN *notify.WebhookNotifier
	if cfg.Alert.Enabled && cfg.Alert.WebhookURL != "" {
		webhookN = notify.NewWebhookNotifier(cfg.Alert.WebhookURL, cfg.Alert.Format, cfg.Alert.DedupWindow)
		notifier = webhookN
	}

	// 业务面：五个协议端点共用一份 BillingCtx（编排/计费/落库依赖）。
	bc := gateway.BillingCtx{
		Table: rt, Deps: deps,
		Apps: appSvc, Prices: priceSvc, Logs: logSvc, Selector: selector, Now: time.Now, Audit: auditWriter, Keys: keySvc,
		ChSticky: chSticky, Notifier: notifier,
		Concurrency: concurrency, DefaultAppConcurrency: cfg.Security.DefaultAppConcurrency,
	}
	v1 := r.Group("/v1", middleware.RunCleanups(), middleware.V1AuthFailLimit(limiter, time.Now, cfg.Security.V1AuthFailLimit), middleware.APIKeyAuth(keySvc), middleware.Idempotency(g), middleware.RequestID(), middleware.APIKeyRateLimit(limiter, time.Now, cfg.Security.DefaultKeyRPM))
	v1.POST("/chat/completions", gateway.NewChatHandler(bc).Completions)
	// 多协议入站端点（统一转 OpenAI chat 链路）。
	v1.POST("/messages", gateway.NewMessagesHandler(bc).Handle)
	v1.POST("/messages/count_tokens", gateway.NewCountTokensHandler(bc).Messages)
	v1.POST("/responses", gateway.NewResponsesHandler(bc).Handle)
	v1.POST("/responses/input_tokens", gateway.NewCountTokensHandler(bc).Responses)
	v1.POST("/embeddings", gateway.NewEmbeddingsHandler(bc).Handle)
	v1.POST("/images/generations", gateway.NewImagesHandler(bc).Handle)
	v1.GET("/models", gateway.NewModelsHandler(g, rt, keySvc, appSvc).List)

	// Pub/Sub：写后通知其他实例 reload 价格缓存
	priceSvc.SetNotify(func() {
		if err := saigredis.Publish(redisCli, saigredis.ChannelReloadPrice); err != nil {
			slog.Error("price reload publish 失败", "err", err)
		}
	})
	// 订阅：收到通知后本地 reload
	saigredis.Subscribe(redisCli, saigredis.ChannelReloadPrice, func() { priceSvc.Reload() })
	saigredis.Subscribe(redisCli, saigredis.ChannelReloadRouter, func() {
		if err := rt.Build(g); err != nil {
			slog.Error("router reload 失败", "err", err)
			rt.Invalidate()
		}
	})

	// 管理面
	loginGuard := middleware.NewLoginGuard(limiter, time.Now, cfg.Security.LoginUserFailLimit)
	authH := admin.NewAuthHandler(g, cfg.JWT.Secret, cfg.JWT.ExpireHours, loginGuard)
	// 路由表写后通知其他实例 reload
	routerNotify := func() error { return saigredis.Publish(redisCli, saigredis.ChannelReloadRouter) }
	crud := admin.NewCRUDHandler(g, keySvc, logSvc, aliasSvc, rt, cfg.Security.ChannelKeyAESKey, appSvc, priceSvc, usageSvc, routerNotify, auditSvc)
	// 管理面可观测性底座：RequestID 在最外层生成标识，AccessLog 紧随其后记「发生了什么请求」，
	// 与 httpx.Err 的「为什么失败」按 request_id 串联。挂在 adminRoot 而非 a 组，
	// 是为了同时覆盖下面单独注册的 /admin/login——登录失败恰是最需要留痕的场景。
	// 业务面 /v1 不挂：每次调用已落 RequestLog 表并可回放，再记一行是重复放大。
	// breaker/status 是前端渠道页 10s 一次的轮询，排除掉免得淹没真正的操作（失败仍有错误日志）。
	adminRoot := r.Group("/admin", middleware.RequestID(), middleware.AccessLog("/admin/breaker/status"))
	// 登录/注册匿名端点按客户端 IP 限流，缓解口令爆破。
	loginRL := middleware.LoginRateLimit(limiter, time.Now, cfg.Security.LoginRateLimit)
	adminRoot.POST("/login", loginRL, authH.Login)
	// JWT 之外按用户名回查账号存在且启用并取实时角色——禁用/删除/改角色对已签发 token 立即生效
	lookupAdminRole := func(username string) (uint64, string, int, bool) {
		var u model.AdminUser
		if err := g.Select("id, role, token_version").Where("username = ? AND status = 1", username).First(&u).Error; err != nil {
			return 0, "", 0, false
		}
		if u.Role == "" {
			u.Role = model.RoleSuper // 存量行未回填时兜底
		}
		return u.ID, u.Role, u.TokenVersion, true
	}
	// 顺序：请求标识/访问日志（adminRoot 已挂）→ 认证 → 操作日志（包住 RBAC 与 handler，
	// 403 拒绝也留痕）→ RBAC 强制
	a := adminRoot.Group("", middleware.JWTAuth(cfg.JWT.Secret, lookupAdminRole), middleware.OpLog(g), middleware.AdminRBAC())
	a.GET("/channels", crud.ListChannels)
	a.GET("/channels/health", crud.ChannelHealth)
	a.POST("/channels", crud.CreateChannel)
	a.POST("/channels/:id", crud.UpdateChannel)
	a.POST("/channels/:id/fetch-models", crud.FetchUpstreamModels)
	a.DELETE("/channels/:id", crud.DeleteChannel)
	a.GET("/channel-models", crud.ListChannelModels)
	a.POST("/channel-models", crud.CreateChannelModel)
	a.DELETE("/channel-models/:id", crud.DeleteChannelModel)
	a.POST("/channel-models/:id/status", crud.SetChannelModelStatus)
	// 密钥池（账号）CRUD
	a.GET("/accounts", crud.ListAccounts)
	a.POST("/accounts", crud.CreateAccount)
	a.POST("/accounts/:id", crud.UpdateAccount)
	a.DELETE("/accounts/:id", crud.DeleteAccount)
	a.POST("/accounts/:id/status", crud.SetAccountStatus)
	a.POST("/accounts/:id/proxy", crud.SetAccountProxy)
	// IP 代理池 CRUD（P6a）
	a.GET("/proxies", crud.ListProxies)
	a.POST("/proxies", crud.CreateProxy)
	a.DELETE("/proxies/:id", crud.DeleteProxy)
	a.POST("/proxies/:id/status", crud.SetProxyStatus)
	a.GET("/api-keys", crud.ListAPIKeys)
	a.POST("/api-keys", crud.CreateAPIKey)
	a.POST("/api-keys/:id", crud.UpdateAPIKey) // 治理字段更新
	a.DELETE("/api-keys/:id", crud.DeleteAPIKey)
	// 令牌标签：CRUD + 绑定/解绑。
	a.GET("/key-tags", crud.ListKeyTags)
	a.POST("/key-tags", crud.CreateKeyTag)
	a.DELETE("/key-tags/:id", crud.DeleteKeyTag)
	a.POST("/api-keys/:id/tags", crud.BindKeyTag)
	a.DELETE("/api-keys/:id/tags/:tag_id", crud.UnbindKeyTag)
	a.GET("/request-logs", crud.ListRequestLogs)
	a.GET("/request-logs/:id", crud.GetRequestLog)
	// 别名 / 路由边
	a.GET("/aliases", crud.ListAliases)
	a.POST("/aliases", crud.CreateAlias)
	a.POST("/aliases/:id", crud.UpdateAlias)
	a.DELETE("/aliases/:id", crud.DeleteAlias)
	a.GET("/aliases/:id/routes", crud.ListRoutes)
	a.POST("/alias-routes", crud.AddRoute)
	a.POST("/alias-routes/batch", crud.BatchAddAliasRoutes)
	a.POST("/alias-routes/:id", crud.UpdateRoute)
	a.DELETE("/alias-routes/:id", crud.DeleteRoute)
	// 应用（配额主体）
	a.GET("/apps", crud.ListApps)
	a.POST("/apps", crud.CreateApp)
	a.POST("/apps/:id", crud.UpdateApp)
	a.DELETE("/apps/:id", crud.DeleteApp)
	a.POST("/apps/:id/audit", crud.SetAppAudit)
	a.GET("/apps/:id/usage-trend", crud.AppUsageTrend)
	a.GET("/api-keys/:id/usage-trend", crud.KeyUsageTrend)
	a.GET("/apps/:id/usage-analysis", crud.AppUsageAnalysis)
	a.GET("/api-keys/:id/usage-analysis", crud.KeyUsageAnalysis)
	a.GET("/channels/:id/usage-analysis", crud.ChannelUsageAnalysis)
	// 价格表
	a.GET("/prices", crud.ListPrices)
	a.POST("/prices", crud.UpsertPrice)
	a.DELETE("/prices/:id", crud.DeletePrice)
	a.POST("/prices/sync-models-dev", crud.FetchModelsDevPrices)
	a.POST("/prices/lookup-model", crud.LookupModelPrice)
	// 仪表盘
	a.GET("/dashboard", crud.Dashboard)
	// 系统设置：读写 + 立即清理（一次性触发 Cleaner.RunOnce）。
	captchaSvc := captcha.New(redisCli) // 管理端测试 Turnstile 与门户注册共用
	settingH := admin.NewSettingHandler(settingSvc, cleaner, captchaSvc)
	a.GET("/settings", settingH.Get)
	a.POST("/settings", settingH.Update)
	a.POST("/settings/cleanup", settingH.Cleanup)
	a.POST("/settings/turnstile-test", settingH.TestTurnstile)
	// IP 归属查询（单例持有内存缓存，跨请求复用）。
	ipH := admin.NewIPHandler(ipgeo.NewClient())
	a.GET("/ip-info/:ip", ipH.Lookup)
	// 用户管理（P9 最小集）：管理员账号 CRUD + 修改自己密码。
	userH := admin.NewUserHandler(g)
	a.GET("/users", userH.List)
	a.POST("/users", userH.Create)
	a.POST("/users/:id", userH.Update)
	a.DELETE("/users/:id", userH.Delete)
	a.POST("/profile/password", loginRL, userH.ChangePassword) // 校验旧密码，同受口令尝试限流
	// 管理操作日志（P9-A 最小版）：super/ops 可查，viewer 不可见（RBAC 强制）。
	a.GET("/op-logs", admin.NewOpLogHandler(g).List)
	// 使用方用户管理（P9-B）：建号/重置密码/启停/删除 + App 归属分配。
	puH := admin.NewPortalUserHandler(g)
	a.GET("/portal-users", puH.List)
	a.POST("/portal-users", puH.Create)
	a.POST("/portal-users/:id", puH.Update)
	a.DELETE("/portal-users/:id", puH.Delete)
	a.POST("/apps/:id/owner", puH.SetAppOwner)
	// App 成员管理（多成员共享）：owner 概念保留，AppMember 为访问权唯一真相源。
	a.GET("/apps/:id/members", puH.ListAppMembers)
	a.POST("/apps/:id/members", puH.AddAppMember)
	a.DELETE("/apps/:id/members/:userId", puH.RemoveAppMember)
	// 告警联通测试（Phase 3b）：webhookN 为 nil（未启用）时 handler 返回未启用提示，不 panic。
	alertH := admin.NewAlertHandler(webhookN)
	a.POST("/alert/test", alertH.Test)
	// 熔断状态查询 + 手动重置（渠道 + 账号级 + 账号×模型配额冷却 + 代理级）。内存模式仅当前实例，Redis 模式全局。
	breakerH := admin.NewBreakerHandler(brk, accBreaker, accModelBrk, chanSvc, accSvc, proxyBreaker, proxySvc, time.Now)
	a.GET("/breaker/status", breakerH.Status)
	a.POST("/breaker/reset", breakerH.Reset)

	// 使用方门户（P9-B）：API 走 /portal/api/* 前缀，/portal/* 页面路径留给前端 SPA。
	portalH := portal.NewHandler(g, keySvc, usageSvc, settingSvc, cfg.JWT.Secret, cfg.JWT.ExpireHours, loginGuard, captchaSvc)
	// 门户面与管理面共用 httpx 错误出口，可观测性底座必须同构挂一份：只挂 admin 的话，
	// portal 的错误日志会没有 request_id、也没有配套的「发生了什么请求」行，串不起同一次操作。
	// 登录/注册/OIDC 回调这些匿名端点同样收进组内——它们最需要留痕。
	portalRoot := r.Group("/portal/api", middleware.RequestID(), middleware.AccessLog())
	portalRoot.POST("/login", loginRL, portalH.Login)
	portalRoot.POST("/register", loginRL, portalH.Register)
	portalRoot.GET("/captcha", loginRL, portalH.Captcha) // 图形验证码，同受按 IP 限流（按端点分桶）
	portalRoot.GET("/config", portalH.Config)            // 公开：登录页渲染注册/OIDC 开关
	portalRoot.GET("/oidc/login", portalH.OIDCLogin)
	portalRoot.GET("/oidc/callback", portalH.OIDCCallback)
	portalRoot.POST("/oidc/exchange", portalH.OIDCExchange) // 前端取走回调签发的门户 JWT（handoff cookie）
	p := portalRoot.Group("", middleware.PortalAuth(cfg.JWT.Secret, portalH.LookupUser), middleware.OpLog(g))
	p.GET("/me", portalH.Me)
	p.POST("/password", loginRL, portalH.ChangePassword) // 校验旧密码，同受口令尝试限流
	p.GET("/apps", portalH.ListApps)
	p.GET("/apps/:id/usage-trend", portalH.AppUsageTrend)
	p.GET("/apps/:id/usage-analysis", portalH.AppUsageAnalysis)
	p.GET("/api-keys", portalH.ListKeys)
	p.POST("/api-keys", portalH.CreateKey)
	p.POST("/api-keys/:id", portalH.UpdateKey)
	p.DELETE("/api-keys/:id", portalH.DeleteKey)
	p.GET("/request-logs", portalH.ListRequestLogs)
	// 前端静态文件（嵌入 web/dist）
	sub, _ := fs.Sub(web.DistFS, "dist")
	indexHTML, _ := fs.ReadFile(sub, "index.html")
	fileServer := http.FileServer(http.FS(sub))
	r.NoRoute(func(c *gin.Context) {
		path := c.Request.URL.Path
		// /v1 未知路径 404 走双协议信封，与 /v1 面其余错误出口一致（按路径推导协议）。
		if strings.HasPrefix(path, "/v1/") {
			v1proto.Respond(c, http.StatusNotFound, v1proto.ProtoForPath(path), "not found")
			return
		}
		// /admin、/portal/api 面：统一 httpx 错误形状。
		if strings.HasPrefix(path, "/admin/") || strings.HasPrefix(path, "/portal/api/") {
			httpx.NotFound(c, "接口不存在")
			return
		}
		clean := strings.TrimPrefix(path, "/")
		if clean != "" && clean != "index.html" {
			if _, err := fs.Stat(sub, clean); err == nil {
				fileServer.ServeHTTP(c.Writer, c.Request)
				return
			}
		}
		c.Data(http.StatusOK, "text/html; charset=utf-8", indexHTML)
	})

	// 优雅关闭：停后台定时任务，再 flush 审计队列（auditWriter.Stop 等已入队条目落库完成）。
	// 调用方应在 http server 停止接收新请求后再调用，确保退出前审计不丢、ticker 不泄漏(E5)。
	shutdown := func() {
		cleaner.Stop()
		aggregator.Stop()
		if prober != nil {
			prober.Stop()
		}
		logSvc.Stop()
		auditWriter.Stop()
		if webhookN != nil {
			webhookN.Stop()
		}
	}
	return r, shutdown, nil
}

// recoverRoll 渠道半开概率跳过的默认实现：以 (1-factor) 概率跳过本次半开渠道，
// factor 越大越倾向直接放行探测（factor=1 恒不跳过，等价旧行为）。
func recoverRoll(factor float64) bool { return rand.Float64() >= factor }

// orDefault 非正值回落内置默认：config 显式 0/负值、以及零值 config.Config{} 测试构造场景
// 的统一兜底（time.NewTicker 等不接受非正参数）。
func orDefault[T int | time.Duration](v, def T) T {
	if v <= 0 {
		return def
	}
	return v
}
