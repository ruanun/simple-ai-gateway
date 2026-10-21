package middleware

import (
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/ruanun/simple-ai-gateway/internal/ratelimit"
)

// TestLoginRateLimit 验证同一对端 IP 超过阈值后被 429，且不同 IP 相互独立。
func TestLoginRateLimit(t *testing.T) {
	gin.SetMode(gin.TestMode)
	l := ratelimit.NewMemoryLimiter()
	now := func() time.Time { return time.Unix(0, 0) } // 固定窗口，便于计数
	r := gin.New()
	r.POST("/admin/login", LoginRateLimit(l, now, 3), func(c *gin.Context) {
		c.Status(http.StatusOK)
	})

	do := func(remote string) int {
		req := httptest.NewRequest(http.MethodPost, "/admin/login", nil)
		req.RemoteAddr = remote
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
		return w.Code
	}

	// 同一 IP：前 3 次放行，第 4 次限流。
	for i := 0; i < 3; i++ {
		if code := do("1.2.3.4:5000"); code != http.StatusOK {
			t.Fatalf("第 %d 次应放行, got=%d", i+1, code)
		}
	}
	if code := do("1.2.3.4:6000"); code != http.StatusTooManyRequests {
		t.Fatalf("超阈值应 429, got=%d", code)
	}
	// 另一 IP 独立计数，不受影响。
	if code := do("9.9.9.9:5000"); code != http.StatusOK {
		t.Fatalf("不同 IP 应独立放行, got=%d", code)
	}
}

// 配置了可信代理后，限流键应取代理转发的真实客户端 IP：反代后面的不同客户端各自计数，
// 而不是全部挤在代理的对端 IP 一个桶里被互相锁死。
func TestLoginRateLimitUsesForwardedClientIPBehindTrustedProxy(t *testing.T) {
	gin.SetMode(gin.TestMode)
	l := ratelimit.NewMemoryLimiter()
	now := func() time.Time { return time.Unix(0, 0) }
	r := gin.New()
	if err := r.SetTrustedProxies([]string{"10.0.0.1"}); err != nil {
		t.Fatal(err)
	}
	r.POST("/admin/login", LoginRateLimit(l, now, 1), func(c *gin.Context) { c.Status(http.StatusOK) })

	do := func(xff string) int {
		req := httptest.NewRequest(http.MethodPost, "/admin/login", nil)
		req.RemoteAddr = "10.0.0.1:4000" // 反代的对端地址
		req.Header.Set("X-Forwarded-For", xff)
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
		return w.Code
	}
	if code := do("203.0.113.9"); code != http.StatusOK {
		t.Fatalf("客户端 A 首次应放行, got=%d", code)
	}
	if code := do("198.51.100.7"); code != http.StatusOK {
		t.Fatalf("客户端 B 应独立计数放行, got=%d", code)
	}
	if code := do("203.0.113.9"); code != http.StatusTooManyRequests {
		t.Fatalf("客户端 A 超阈值应 429, got=%d", code)
	}
}

// 未配置可信代理时 X-Forwarded-For 必须被忽略，否则攻击者伪造该头即可无限绕过限流。
func TestLoginRateLimitIgnoresForwardedForWithoutTrustedProxy(t *testing.T) {
	gin.SetMode(gin.TestMode)
	l := ratelimit.NewMemoryLimiter()
	now := func() time.Time { return time.Unix(0, 0) }
	r := gin.New()
	if err := r.SetTrustedProxies(nil); err != nil {
		t.Fatal(err)
	}
	r.POST("/admin/login", LoginRateLimit(l, now, 1), func(c *gin.Context) { c.Status(http.StatusOK) })

	do := func(xff string) int {
		req := httptest.NewRequest(http.MethodPost, "/admin/login", nil)
		req.RemoteAddr = "1.2.3.4:5000"
		req.Header.Set("X-Forwarded-For", xff)
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
		return w.Code
	}
	if code := do("203.0.113.9"); code != http.StatusOK {
		t.Fatalf("首次应放行, got=%d", code)
	}
	if code := do("198.51.100.7"); code != http.StatusTooManyRequests {
		t.Fatalf("伪造不同 XFF 仍应按对端 IP 计数被 429, got=%d", code)
	}
}

// IPv6 同一 /64 前缀视为同一客户端：攻击者单机即可轮换海量 /64 内地址，
// 按完整地址建键会让登录限流对其完全失效。
func TestLoginRateLimitGroupsIPv6By64(t *testing.T) {
	gin.SetMode(gin.TestMode)
	l := ratelimit.NewMemoryLimiter()
	now := func() time.Time { return time.Unix(0, 0) }
	r := gin.New()
	r.POST("/admin/login", LoginRateLimit(l, now, 2), func(c *gin.Context) { c.Status(http.StatusOK) })

	do := func(remote string) int {
		req := httptest.NewRequest(http.MethodPost, "/admin/login", nil)
		req.RemoteAddr = remote
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
		return w.Code
	}
	if code := do("[2001:db8:1:2::1]:5000"); code != http.StatusOK {
		t.Fatalf("首次应放行, got=%d", code)
	}
	if code := do("[2001:db8:1:2::ffff]:5000"); code != http.StatusOK {
		t.Fatalf("同 /64 第 2 次应放行, got=%d", code)
	}
	if code := do("[2001:db8:1:2:abcd::9]:5000"); code != http.StatusTooManyRequests {
		t.Fatalf("同 /64 换地址仍应计入同一桶并 429, got=%d", code)
	}
	if code := do("[2001:db8:1:3::1]:5000"); code != http.StatusOK {
		t.Fatalf("不同 /64 应独立计数, got=%d", code)
	}
}

func TestRateKeyIP(t *testing.T) {
	cases := map[string]string{
		"1.2.3.4":              "1.2.3.4",
		"::ffff:1.2.3.4":       "1.2.3.4",
		"2001:db8:1:2:3:4:5:6": "2001:db8:1:2::/64",
		"not-an-ip":            "not-an-ip",
		"":                     "",
	}
	for in, want := range cases {
		if got := rateKeyIP(in); got != want {
			t.Errorf("rateKeyIP(%q)=%q, want %q", in, got, want)
		}
	}
}

// 令牌未设置 RPM（0）时套用兜底上限；显式设置的 RPM 优先；-1 显式不限流；兜底 <=0 时未设置的令牌不限流。
func TestAPIKeyRateLimitDefaultRPM(t *testing.T) {
	gin.SetMode(gin.TestMode)
	now := func() time.Time { return time.Unix(0, 0) }
	build := func(keyRPM, defaultRPM int) *gin.Engine {
		r := gin.New()
		r.GET("/v1/models", func(c *gin.Context) {
			c.Set(CtxAPIKeyID, uint64(1))
			c.Set(CtxAPIKeyRPM, keyRPM)
		}, APIKeyRateLimit(ratelimit.NewMemoryLimiter(), now, defaultRPM), func(c *gin.Context) { c.Status(http.StatusOK) })
		return r
	}
	// passes 连打 n 次，返回放行次数。
	passes := func(r *gin.Engine, n int) int {
		ok := 0
		for i := 0; i < n; i++ {
			w := httptest.NewRecorder()
			r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/v1/models", nil))
			if w.Code == http.StatusOK {
				ok++
			}
		}
		return ok
	}
	if got := passes(build(0, 2), 5); got != 2 {
		t.Fatalf("未设置 RPM 应按兜底 2 放行, got=%d", got)
	}
	if got := passes(build(3, 2), 5); got != 3 {
		t.Fatalf("显式 RPM 3 应优先于兜底, got=%d", got)
	}
	if got := passes(build(0, 0), 5); got != 5 {
		t.Fatalf("兜底关闭时未设置 RPM 的令牌不限流, got=%d", got)
	}
	if got := passes(build(-1, 2), 5); got != 5 {
		t.Fatalf("RPM=-1 显式不限流，不受兜底约束, got=%d", got)
	}
}

// 同一用户名失败合计达阈值即锁定；成功退还名额不计入；大小写变体同桶；其他用户名与 scope 不受影响。
func TestLoginGuard(t *testing.T) {
	now := func() time.Time { return time.Unix(0, 0) }
	g := NewLoginGuard(ratelimit.NewMemoryLimiter(), now, 2)
	fail := func(user string) bool { _, ok := g.Acquire("admin", user); return ok }

	for i := 0; i < 5; i++ { // 成功登录退还名额，反复成功不锁
		succeed, ok := g.Acquire("admin", "alice")
		if !ok {
			t.Fatalf("第 %d 次成功登录不应被锁", i+1)
		}
		succeed()
	}
	if !fail("alice") || !fail("ALICE") {
		t.Fatal("未达阈值的失败应放行（大小写变体同桶计数）")
	}
	if fail("alice") {
		t.Fatal("失败达阈值后应锁定")
	}
	if !fail("bob") {
		t.Fatal("其他用户名不应受影响")
	}
	if _, ok := g.Acquire("portal", "alice"); !ok {
		t.Fatal("管理端与门户账号体系独立，不应互相锁定")
	}
	g.now = func() time.Time { return time.Unix(60, 0) }
	if !fail("alice") {
		t.Fatal("进入下一窗口应解锁")
	}
}

// 回归：并发请求须在比对前原子占位，不能全部在首个失败落地前穿过检查。
func TestLoginGuardConcurrent(t *testing.T) {
	g := NewLoginGuard(ratelimit.NewMemoryLimiter(), func() time.Time { return time.Unix(0, 0) }, 5)
	var mu sync.Mutex
	passed := 0
	var wg sync.WaitGroup
	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, ok := g.Acquire("admin", "alice"); ok {
				time.Sleep(10 * time.Millisecond) // 模拟 bcrypt 耗时，期间不退还（密码错误）
				mu.Lock()
				passed++
				mu.Unlock()
			}
		}()
	}
	wg.Wait()
	if passed != 5 {
		t.Fatalf("100 个并发错误尝试只应放行 5 个, got=%d", passed)
	}
}

// 退还按占位时刻的窗口：比对跨分钟边界时不得误退新窗口的计数。
func TestLoginGuardReleaseUsesAcquireWindow(t *testing.T) {
	cur := time.Unix(59, 0)
	g := NewLoginGuard(ratelimit.NewMemoryLimiter(), func() time.Time { return cur }, 1)
	succeed, ok := g.Acquire("admin", "alice") // 窗口 0 占位
	if !ok {
		t.Fatal("首次应放行")
	}
	cur = time.Unix(60, 0)
	if _, ok := g.Acquire("admin", "alice"); !ok { // 窗口 1 一次失败，占满
		t.Fatal("新窗口首次应放行")
	}
	succeed() // 退还的是窗口 0，窗口 1 仍满
	if _, ok := g.Acquire("admin", "alice"); ok {
		t.Fatal("跨窗口退还不应释放新窗口的名额")
	}
}

// limit<=0 返回 nil，nil 守卫恒放行且 succeed 可安全调用。
func TestLoginGuardDisabled(t *testing.T) {
	g := NewLoginGuard(ratelimit.NewMemoryLimiter(), time.Now, 0)
	if g != nil {
		t.Fatal("limit<=0 应返回 nil")
	}
	for i := 0; i < 10; i++ {
		succeed, ok := g.Acquire("admin", "alice")
		if !ok {
			t.Fatal("关闭时不应锁定")
		}
		succeed()
	}
}
