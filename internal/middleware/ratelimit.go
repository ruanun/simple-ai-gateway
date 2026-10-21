package middleware

import (
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/ruanun/simple-ai-gateway/internal/httpx"
	"github.com/ruanun/simple-ai-gateway/internal/model"
	"github.com/ruanun/simple-ai-gateway/internal/ratelimit"
)

// APIKeyRateLimit 按 API Key 维度固定窗口限流。
// 从 ctx 取 APIKeyAuth 塞入的 CtxAPIKeyID 与 CtxAPIKeyRPM，按三态上限解析（model.EffectiveLimit）：
// 令牌 rpm=0（未设置）套用 defaultRPM 兜底——否则未配 RPM 的令牌一旦泄露或被滥用即可无上限地刷上游额度；
// rpm=-1（管理端显式放开）或 defaultRPM<=0 时不限流。
func APIKeyRateLimit(l ratelimit.Limiter, now func() time.Time, defaultRPM int) gin.HandlerFunc {
	return func(c *gin.Context) {
		idv, _ := c.Get(CtxAPIKeyID)
		id, _ := idv.(uint64)
		rpmv, _ := c.Get(CtxAPIKeyRPM)
		rpm, _ := rpmv.(int)
		if !l.Allow("apikey:"+strconv.FormatUint(id, 10), model.EffectiveLimit(rpm, defaultRPM), now()) {
			abortV1Error(c, http.StatusTooManyRequests, "rate limited")
			return
		}
		c.Next()
	}
}

// LoginRateLimit 对登录/注册等匿名端点按客户端做固定窗口限流，缓解口令爆破。
// 键取 c.ClientIP()：装配层已按 server.trusted_proxies 调用 SetTrustedProxies，
// 未配置时它就是 TCP 对端地址（X-Forwarded-For 被忽略，伪造头无法绕过限流）；
// 配置了可信反代后才采信转发头——否则反代后面的所有客户端会挤在代理 IP 一个桶里，
// 任何人打满阈值就把全站登录锁死。
// IPv6 按 /64 前缀归并（见 rateKeyIP）。
// limit<=0 时用默认 10 次/分钟；按 端点+IP 维度隔离，避免不同端点互相挤占额度。
func LoginRateLimit(l ratelimit.Limiter, now func() time.Time, limit int) gin.HandlerFunc {
	if limit <= 0 {
		limit = 10
	}
	return func(c *gin.Context) {
		if !l.Allow("login:"+c.FullPath()+":"+rateKeyIP(c.ClientIP()), limit, now()) {
			// 本中间件挂在 /admin/login 与 /portal/api/login|register 上（非 /v1 面），
			// 走 httpx 统一形状。
			httpx.Err(c, http.StatusTooManyRequests, "too_many_requests", "尝试过于频繁，请稍后再试")
			return
		}
		c.Next()
	}
}

// V1AuthFailLimit 挂在 APIKeyAuth 之前，按客户端 IP 统计 /v1 鉴权失败次数：
// 本窗口失败达 limit 后该 IP 的后续请求直接 429，不再进 APIKeyAuth 做哈希 + 查库，
// 堵住「无效 key 高频刷 /v1 打满数据库」的廉价 DoS 面。只数失败（APIKeyAuth 置
// CtxAPIKeyAuthFailed），合法 key 的正常流量不计数；IP 口径同 LoginRateLimit（c.ClientIP + IPv6 /64）。
// limit<=0 关闭。
func V1AuthFailLimit(l ratelimit.Limiter, now func() time.Time, limit int) gin.HandlerFunc {
	if limit <= 0 {
		return func(c *gin.Context) { c.Next() }
	}
	return func(c *gin.Context) {
		key := "v1authfail:" + rateKeyIP(c.ClientIP())
		if l.Exceeded(key, limit, now()) {
			abortV1Error(c, http.StatusTooManyRequests, "too many failed authentication attempts")
			return
		}
		c.Next()
		if c.GetBool(CtxAPIKeyAuthFailed) {
			l.Allow(key, limit, now())
		}
	}
}

// LoginGuard 按用户名统计登录失败次数，补 LoginRateLimit（按 IP）的盲区：
// 攻击者换大量源 IP 对同一账号分布式爆破时，每个 IP 都不触发 429，只有按用户名合计才拦得住。
// 本窗口（1 分钟）失败达 limit 后，该用户名的登录一律 429，不再查库/跑 bcrypt。
// 不存在的用户名同样计数，锁定表现与真实账号一致，不泄露账号是否存在。
// 代价是攻击者可借此把某账号锁住至多一个窗口，属常规权衡。nil 或 limit<=0 时关闭。
//
// 必须「比对前原子占位、成功后退还」，不能「先只读查锁、失败后再计数」：后者在 bcrypt 的
// 几十毫秒里没有计数，多 IP 并发的请求能全部在首个失败落地前通过检查，锁定形同虚设。
type LoginGuard struct {
	l     ratelimit.Limiter
	now   func() time.Time
	limit int
}

// NewLoginGuard 构造登录失败守卫；limit<=0 返回 nil（关闭）。
func NewLoginGuard(l ratelimit.Limiter, now func() time.Time, limit int) *LoginGuard {
	if limit <= 0 {
		return nil
	}
	return &LoginGuard{l: l, now: now, limit: limit}
}

// Acquire 在比对密码前为该用户名原子占一次尝试名额；ok=false 表示本窗口失败已达上限，应直接 429。
// 登录成功（密码正确）时调用返回的 succeed 退还名额，使计数只反映失败；失败则什么都不做。
// succeed 按占位时刻的窗口退还，比对跨分钟边界也不会误退新窗口。scope 区分管理端/门户。
func (g *LoginGuard) Acquire(scope, username string) (succeed func(), ok bool) {
	if g == nil {
		return func() {}, true
	}
	key, at := loginFailKey(scope, username), g.now()
	if !g.l.Allow(key, g.limit, at) {
		return nil, false
	}
	return func() { g.l.Release(key, at) }, true
}

// loginFailKey 用户名转小写并截断到 64 字节（用户名上限即 64）：大小写变体归同一桶，
// 超长的任意用户名也撑不大键。
func loginFailKey(scope, username string) string {
	u := strings.ToLower(username)
	if len(u) > 64 {
		u = u[:64]
	}
	return "loginfail:" + scope + ":" + u
}

// rateKeyIP 把客户端 IP 归一为按 IP 限流的键：IPv4（含 IPv4 映射的 IPv6）取原地址；
// IPv6 取所在 /64 前缀——/64 是单个终端/站点的常规分配粒度，单机即可在其中轮换海量地址，
// 按完整地址建键会让限流形同虚设。解析失败原样返回。
func rateKeyIP(ip string) string {
	parsed := net.ParseIP(ip)
	if parsed == nil {
		return ip
	}
	if v4 := parsed.To4(); v4 != nil {
		return v4.String()
	}
	return parsed.Mask(net.CIDRMask(64, 128)).String() + "/64"
}
