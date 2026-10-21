package gateway

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/ruanun/simple-ai-gateway/internal/breaker"
	"github.com/ruanun/simple-ai-gateway/internal/crypto"
	"github.com/ruanun/simple-ai-gateway/internal/dbtest"
	"github.com/ruanun/simple-ai-gateway/internal/domain/account"
	"github.com/ruanun/simple-ai-gateway/internal/domain/channel"
	"github.com/ruanun/simple-ai-gateway/internal/domain/proxy"
	"github.com/ruanun/simple-ai-gateway/internal/model"
	"github.com/ruanun/simple-ai-gateway/internal/ratelimit"
	"github.com/ruanun/simple-ai-gateway/internal/router"
	"github.com/stretchr/testify/require"
)

// ---- A. 错误分类（坑b）----

// TestClassifyFailure 覆盖 classifyFailure 的全部分支：客户端取消不计熔断，
// 出站超时（DeadlineExceeded，来源是 http.Client.Timeout 打到慢/挂上游）与其他传输错误计入；
// err==nil 时只看 status 是否 429。
func TestClassifyFailure(t *testing.T) {
	cases := []struct {
		name       string
		err        error
		status     int
		wantRecord bool
		wantIs429  bool
	}{
		{"context canceled 不计熔断", context.Canceled, 0, false, false},
		{"context deadline exceeded 计入熔断(上游慢/挂死)", context.DeadlineExceeded, 0, true, false},
		{"其他传输错误计入", errors.New("connection reset"), 0, true, false},
		{"nil err + 429 计入且标 429", nil, http.StatusTooManyRequests, true, true},
		{"nil err + 500 计入但非 429", nil, http.StatusInternalServerError, true, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec, is429 := classifyFailure(context.Background(), tc.err, tc.status)
			require.Equal(t, tc.wantRecord, rec, "recordBreaker 不符")
			require.Equal(t, tc.wantIs429, is429, "is429 不符")
		})
	}
}

// TestClassifyFailureWrappedContextErr 验证 errors.Is 语义：包裹后的 context.Canceled 仍不计熔断；
// 包裹后的 context.DeadlineExceeded（真实链路里 http.Client.Timeout 返回
// *url.Error{Err: DeadlineExceeded}，Go 1.23+ 起 errors.Is 可匹配）应计入——
// 该错误的现实来源就是慢/挂上游，必须触发熔断隔离。
// 这里传 context.Background()：出站请求虽绑了首字节预算 ctx，但其 cause 未标
// errFirstByteTimeout 时按客户端取消处理（预算超时那一向见 TestClassifyFailureFirstByteTimeoutCounted）。
func TestClassifyFailureWrappedContextErr(t *testing.T) {
	wrappedCanceled := &urlErrLike{msg: "dial tcp: operation was canceled", err: context.Canceled}
	rec, is429 := classifyFailure(context.Background(), wrappedCanceled, 0)
	require.False(t, rec, "包裹的 context.Canceled 不应计熔断")
	require.False(t, is429)

	wrappedDeadline := &urlErrLike{msg: "Client.Timeout exceeded while awaiting headers", err: context.DeadlineExceeded}
	rec, is429 = classifyFailure(context.Background(), wrappedDeadline, 0)
	require.True(t, rec, "包裹的 context.DeadlineExceeded(上游超时)应计入熔断")
	require.False(t, is429)
}

// urlErrLike 模拟 *url.Error 的 Unwrap 语义，供 errors.Is 穿透。
type urlErrLike struct {
	msg string
	err error
}

func (e *urlErrLike) Error() string { return e.msg }
func (e *urlErrLike) Unwrap() error { return e.err }

// ---- D. Retry-After 解析（坑d）----

// TestParseRetryAfter 覆盖整数秒/HTTP-date/空/非法输入四类。
func TestParseRetryAfter(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	require.Equal(t, time.Duration(0), parseRetryAfter("", now))
	require.Equal(t, 5*time.Second, parseRetryAfter("5", now))
	require.Equal(t, time.Duration(0), parseRetryAfter("0", now), "非正整数按 0 处理")
	require.Equal(t, time.Duration(0), parseRetryAfter("not-a-date", now), "非法格式按 0 处理")

	future := now.Add(10 * time.Second)
	require.Equal(t, 10*time.Second, parseRetryAfter(future.UTC().Format(http.TimeFormat), now))

	past := now.Add(-10 * time.Second)
	require.Equal(t, time.Duration(0), parseRetryAfter(past.UTC().Format(http.TimeFormat), now), "已过去的 HTTP-date 按 0 处理")
}

// ---- A（续）：上游响应超时必须计入熔断 ----

// TestDispatchUpstreamTimeoutTripsBreaker 验证上游 hang 触发 http.Client.Timeout
// （底层错误经 errors.Is 判定为 context.DeadlineExceeded）应记入渠道熔断——
// 出站请求不绑客户端 ctx，该错误的唯一现实来源就是慢/挂上游；不计会让挂死上游
// 永不被隔离、每个请求白等满 Timeout（审查发现的行为回退，已修复）。
func TestDispatchUpstreamTimeoutTripsBreaker(t *testing.T) {
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(1500 * time.Millisecond) // 拖住，触发客户端 1s 超时
		w.WriteHeader(200)
	}))
	defer up.Close()
	brk := breaker.NewMemoryBreaker(breaker.Params{Window: 30 * time.Second, Cooldown: 60 * time.Second, FailThreshold: 1, ErrorRate: 0.5, MinSamples: 20, ProbeBackoffMax: 8})
	d := convDeps(func(id uint64, m string) (ChannelInfo, bool) {
		return ChannelInfo{BaseURL: up.URL, Timeout: 1, ProviderType: "openai"}, true // 1s 客户端超时
	})
	d.Breaker = brk
	cands := []router.Candidate{{ChannelID: 1, Model: "m", ProviderType: "openai", Outbound: "openai", Inbound: "openai"}}
	w := httptest.NewRecorder()
	out := Dispatch(w, cands, []byte(`{"model":"m","messages":[]}`), d)
	require.Equal(t, http.StatusBadGateway, out.Status, "唯一候选超时后全失败 502")
	require.False(t, brk.Allow(1, d.Now()), "上游超时应打开渠道熔断(FailThreshold=1)")
}

// ---- B. 跨候选墙钟总超时（坑a）----

// TestDispatchWallClockTimeout 候选一直 500 且 MaxRetries 很高、MaxWall 很小 → 应在墙钟耗尽时
// 止损返回 504，而不是把 MaxRetries 全部试完（单候选场景下墙钟检查必须也在重试(attempts)循环内生效）。
func TestDispatchWallClockTimeout(t *testing.T) {
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(500) }))
	defer up.Close()
	d := convDeps(func(id uint64, m string) (ChannelInfo, bool) {
		return ChannelInfo{BaseURL: up.URL, Timeout: 5, ProviderType: "openai", MaxRetries: 100}, true
	})
	d.MaxWall = 1 * time.Millisecond
	cands := []router.Candidate{{ChannelID: 1, Model: "m", ProviderType: "openai", Outbound: "openai", Inbound: "openai"}}
	w := httptest.NewRecorder()
	out := Dispatch(w, cands, []byte(`{"model":"m","messages":[]}`), d)
	require.Equal(t, http.StatusGatewayTimeout, out.Status)
}

// TestDispatchRawWallClockTimeout DispatchRaw 侧同等场景。
func TestDispatchRawWallClockTimeout(t *testing.T) {
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(500) }))
	defer up.Close()
	d := convDeps(func(id uint64, m string) (ChannelInfo, bool) {
		return ChannelInfo{BaseURL: up.URL, Timeout: 5, ProviderType: "openai", MaxRetries: 100}, true
	})
	d.MaxWall = 1 * time.Millisecond
	cands := []router.Candidate{{ChannelID: 1, Model: "m"}}
	out := DispatchRaw(cands, "/embeddings", []byte(`{"model":"m"}`), d, false)
	require.Equal(t, http.StatusGatewayTimeout, out.Status)
}

// ---- E. 唯一候选也要驱动熔断半开转换（FIX#1：sole-candidate 熔断永不自愈）----

// TestDispatchSoleCandidateBreakerSelfHeals 覆盖回归：某模型永远只有一个候选渠道时，
// 旧代码 `!sole && !d.Breaker.Allow(...)` 短路导致 sole 场景下 Allow 从不被调用——而
// open→halfOpen 的状态转换恰恰发生在 Allow 内部（冷却到期才置 halfOpen 并放行），于是
// 该渠道永远不会离开 stOpen：Recovering() 恒为 true，RecordResult 在 stOpen 分支直接
// 忽略结果，熔断永久卡死、无法自愈。
//
// 本测试驱动同一渠道开闸→冷却到期→唯一候选再次成功探测，断言探测成功后熔断应完全闭合
// （Allow=true 且 Recovering=false，而不是"仍处于恢复期"的半吊子状态）。
func TestDispatchSoleCandidateBreakerSelfHeals(t *testing.T) {
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"ok"}`))
	}))
	defer up.Close()

	brk := breaker.NewMemoryBreaker(breaker.Params{Window: 30 * time.Second, Cooldown: 60 * time.Second, FailThreshold: 1, ErrorRate: 0.5, MinSamples: 20, ProbeBackoffMax: 8})
	openAt := time.Unix(1_000_000, 0)
	brk.RecordResult(1, openAt, false, true) // is429 立即开闸
	require.False(t, brk.Allow(1, openAt), "刚开闸应拒绝")

	after := openAt.Add(61 * time.Second) // 冷却已过，唯一候选应能拿到一次半开探测机会

	d := convDeps(func(id uint64, m string) (ChannelInfo, bool) {
		return ChannelInfo{BaseURL: up.URL, Timeout: 5, ProviderType: "openai"}, true
	})
	d.Breaker = brk
	d.Now = func() time.Time { return after }
	cands := []router.Candidate{{ChannelID: 1, Model: "m", ProviderType: "openai", Outbound: "openai", Inbound: "openai"}} // 唯一候选
	w := httptest.NewRecorder()
	out := Dispatch(w, cands, []byte(`{"model":"m","messages":[]}`), d)
	require.Equal(t, 200, out.Status, "唯一候选冷却已过应能成功完成一次半开探测")

	require.True(t, brk.Allow(1, after), "探测成功后熔断应闭合、可放行")
	require.False(t, brk.Recovering(1, after), "探测成功后不应仍处于恢复期(半开卡死=未自愈)")
}

// ---- C. 渠道级半开概率跳过（坑c）----

// TestDispatchRecoverRollFallbackWhenAllSkipped 唯一候选恰好处于半开、RecoverRoll 恒跳过时，
// 若不兜底会导致该渠道恢复期彻底无候选可用；应二次遍历忽略跳过，仍能成功派发。
func TestDispatchRecoverRollFallbackWhenAllSkipped(t *testing.T) {
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"ok"}`))
	}))
	defer up.Close()
	brk := breaker.NewMemoryBreaker(breaker.Params{Window: 30 * time.Second, Cooldown: 60 * time.Second, FailThreshold: 1, ErrorRate: 0.5, MinSamples: 20, ProbeBackoffMax: 8})
	openAt := time.Unix(1_000_000, 0)
	brk.RecordResult(1, openAt, false, true) // is429 立即开闸
	half := openAt.Add(61 * time.Second)     // 冷却已过 → Recovering()==true（尚未真正探测）

	d := convDeps(func(id uint64, m string) (ChannelInfo, bool) {
		return ChannelInfo{BaseURL: up.URL, Timeout: 5, ProviderType: "openai"}, true
	})
	d.Breaker = brk
	d.Now = func() time.Time { return half }
	d.RecoverRoll = func() bool { return true } // 恒跳过半开
	cands := []router.Candidate{{ChannelID: 1, Model: "m", ProviderType: "openai", Outbound: "openai", Inbound: "openai"}}
	w := httptest.NewRecorder()
	out := Dispatch(w, cands, []byte(`{"model":"m","messages":[]}`), d)
	require.Equal(t, 200, out.Status, "唯一候选恒被半开跳过时应兜底二次遍历，不能全军覆没")
}

// TestDispatchRecoverRollFallbackDoesNotDoubleConsumeLimiter IMPORTANT#1 回归：渠道 RPM 上限=1、
// 唯一候选处于半开、RecoverRoll 恒跳过。主遍历里该候选已经消费了本分钟唯一的限流令牌，被半开跳过后
// 收进 deferred；兜底遍历必须直接打流量、不能重新走一次 Limiter.Allow——否则会对同一分钟窗口二次扣
// 限流令牌，导致本该被兜底救回的候选反被限流打成 429（旧实现"整轮重跑"的 bug）。
func TestDispatchRecoverRollFallbackDoesNotDoubleConsumeLimiter(t *testing.T) {
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"ok"}`))
	}))
	defer up.Close()
	brk := breaker.NewMemoryBreaker(breaker.Params{Window: 30 * time.Second, Cooldown: 60 * time.Second, FailThreshold: 1, ErrorRate: 0.5, MinSamples: 20, ProbeBackoffMax: 8})
	openAt := time.Unix(1_000_000, 0)
	brk.RecordResult(1, openAt, false, true) // is429 立即开闸
	half := openAt.Add(61 * time.Second)     // 冷却已过 → Recovering()==true（尚未真正探测）

	d := convDeps(func(id uint64, m string) (ChannelInfo, bool) {
		return ChannelInfo{BaseURL: up.URL, Timeout: 5, ProviderType: "openai", RPMLimit: 1}, true // 每分钟仅 1 个令牌
	})
	d.Breaker = brk
	d.Now = func() time.Time { return half }
	d.RecoverRoll = func() bool { return true } // 恒跳过半开
	cands := []router.Candidate{{ChannelID: 1, Model: "m", ProviderType: "openai", Outbound: "openai", Inbound: "openai"}}
	w := httptest.NewRecorder()
	out := Dispatch(w, cands, []byte(`{"model":"m","messages":[]}`), d)
	require.Equal(t, 200, out.Status, "RPM=1 时兜底遍历不应二次扣限流，应成功而非被自己限流打 429/502")
}

// TestDispatchRecoverRollSkipsToHealthyCandidate 半开渠道1 + 健康渠道2：RecoverRoll 恒跳过时，
// 应绕开半开渠道1、直接派到健康候选2（未被兜底二次遍历触发，因为已有候选被真正尝试）。
func TestDispatchRecoverRollSkipsToHealthyCandidate(t *testing.T) {
	var badHit int
	bad := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		badHit++
		w.WriteHeader(200)
	}))
	defer bad.Close()
	good := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"ok"}`))
	}))
	defer good.Close()

	brk := breaker.NewMemoryBreaker(breaker.Params{Window: 30 * time.Second, Cooldown: 60 * time.Second, FailThreshold: 1, ErrorRate: 0.5, MinSamples: 20, ProbeBackoffMax: 8})
	openAt := time.Unix(1_000_000, 0)
	brk.RecordResult(1, openAt, false, true) // 渠道1 开闸
	half := openAt.Add(61 * time.Second)

	infos := map[uint64]ChannelInfo{
		1: {BaseURL: bad.URL, Timeout: 5, ProviderType: "openai"},
		2: {BaseURL: good.URL, Timeout: 5, ProviderType: "openai"},
	}
	d := Deps{
		Breaker: brk, Limiter: ratelimit.NewMemoryLimiter(),
		Lookup:      func(id uint64, m string) (ChannelInfo, bool) { i, ok := infos[id]; return i, ok },
		Now:         func() time.Time { return half },
		RecoverRoll: func() bool { return true }, // 恒跳过半开
	}
	cands := []router.Candidate{
		{ChannelID: 1, Model: "m", ProviderType: "openai", Outbound: "openai", Inbound: "openai"},
		{ChannelID: 2, Model: "m", ProviderType: "openai", Outbound: "openai", Inbound: "openai"},
	}
	w := httptest.NewRecorder()
	out := Dispatch(w, cands, []byte(`{"model":"m","messages":[]}`), d)
	require.Equal(t, 200, out.Status)
	require.Equal(t, uint64(2), out.ChannelID, "半开渠道1应被跳过，落到健康候选2")
	require.Equal(t, 0, badHit, "半开渠道不应被真正打到")
}

// ---- A（续）：pok=false（账号无可用代理）不应打开渠道熔断（坑b）----

// TestDispatchRawProxyEmptyDoesNotOpenChannelBreaker DispatchRaw 侧覆盖：账号绑了代理组但组内
// 无可用代理，属本地配置问题、未真正打到上游，不应记入渠道熔断（对齐 dispatch.go 同名修复）。
func TestDispatchRawProxyEmptyDoesNotOpenChannelBreaker(t *testing.T) {
	var upstreamHit int
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		upstreamHit++
		w.WriteHeader(200)
	}))
	defer up.Close()

	g := dbtest.OpenInMemory(t)
	require.NoError(t, g.AutoMigrate(&model.Proxy{}))
	proxySvc := proxy.NewService(proxy.NewRepo(g)) // 组 pg 内无任何代理
	resolver := NewProxyResolver(proxySvc, breaker.NewMemoryBreaker(breaker.Params{Window: time.Minute, Cooldown: time.Minute, FailThreshold: 3, ErrorRate: 0.5, MinSamples: 20, ProbeBackoffMax: 8}), nil, 1.0)

	chBrk := breaker.NewMemoryBreaker(breaker.Params{Window: time.Minute, Cooldown: time.Minute, FailThreshold: 1, ErrorRate: 0.5, MinSamples: 20, ProbeBackoffMax: 8})
	d := Deps{
		Breaker: chBrk, Limiter: ratelimit.NewMemoryLimiter(),
		Lookup: func(id uint64, m string) (ChannelInfo, bool) {
			return ChannelInfo{BaseURL: up.URL, Timeout: 5, AccountID: 7, ProxyGroup: "pg"}, true
		},
		Proxy: resolver,
		Now:   func() time.Time { return time.Unix(1_000_000, 0) },
	}
	out := DispatchRaw([]router.Candidate{{ChannelID: 1}}, "/embeddings", []byte(`{"model":"m"}`), d, false)
	require.Equal(t, 502, out.Status)
	require.Equal(t, 0, upstreamHit, "无可用代理时不应打到上游")
	require.True(t, chBrk.Allow(1, time.Unix(1_000_000, 0)), "本地未达上游失败不应打开渠道熔断（坑b）")
}

// TestDispatchProxyEmptyThenReselectsAccount 覆盖 pok=false 换号路径：渠道内第一个账号绑了空代理组
// （账号无可用代理，未真正打到上游、不留痕熔断记录），MaxRetries>0 且有第二个账号可用时，
// 应重选到第二个账号并成功派发——证明 pok=false-without-record 分支不会打断渠道内账号轮换（IMPORTANT#2）。
func TestDispatchProxyEmptyThenReselectsAccount(t *testing.T) {
	var upstreamHit int
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		upstreamHit++
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"ok"}`))
	}))
	defer up.Close()

	g := dbtest.OpenInMemory(t)
	require.NoError(t, g.AutoMigrate(&model.Proxy{}))
	proxySvc := proxy.NewService(proxy.NewRepo(g)) // 组 pg 内无任何代理
	resolver := NewProxyResolver(proxySvc, breaker.NewMemoryBreaker(breaker.Params{Window: time.Minute, Cooldown: time.Minute, FailThreshold: 3, ErrorRate: 0.5, MinSamples: 20, ProbeBackoffMax: 8}), nil, 1.0)

	call := 0
	d := Deps{
		Breaker: breaker.NewMemoryBreaker(breaker.Params{Window: time.Minute, Cooldown: time.Minute, FailThreshold: 5, ErrorRate: 0.5, MinSamples: 20, ProbeBackoffMax: 8}),
		Limiter: ratelimit.NewMemoryLimiter(),
		Lookup: func(id uint64, m string) (ChannelInfo, bool) {
			call++
			if call == 1 {
				// 第一个账号绑了空代理组：clientForInfo 返回 pok=false，未真正打到上游。
				return ChannelInfo{BaseURL: up.URL, Timeout: 5, MaxRetries: 1, AccountID: 1, ProxyGroup: "pg"}, true
			}
			// 渠道内重试重选到第二个账号：不绑代理组，直连可拨通。
			return ChannelInfo{BaseURL: up.URL, Timeout: 5, MaxRetries: 1, AccountID: 2}, true
		},
		Proxy: resolver,
		Now:   func() time.Time { return time.Unix(1_000_000, 0) },
	}
	w := httptest.NewRecorder()
	out := Dispatch(w, []router.Candidate{{ChannelID: 1, Model: "m"}}, []byte(`{"model":"m","messages":[]}`), d)
	require.Equal(t, 200, out.Status, "第一个账号无可用代理，应换到第二个账号成功而非直接失败")
	require.Equal(t, uint64(2), out.AccountID, "应使用重选后的第二个账号")
	require.Equal(t, 1, upstreamHit, "只有第二个账号真正打到上游")
}

// ---- D（续）：429 + Retry-After 端到端喂 keyed 账号×模型冷却 ----

// TestDispatch429RetryAfterFeedsKeyedCooldown 覆盖坑d 端到端：上游 429 带 Retry-After:5，
// 冷却时长应取 max(指数退避, Retry-After)——退避基数 1s 远小于 5s，4s 时仍应冷却中、6s 后应恢复，
// 证明解析出的 Retry-After 而非默认退避决定了冷却时长。
func TestDispatch429RetryAfterFeedsKeyedCooldown(t *testing.T) {
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Retry-After", "5")
		w.WriteHeader(http.StatusTooManyRequests)
	}))
	defer up.Close()

	g := dbtest.OpenInMemory(t)
	require.NoError(t, g.AutoMigrate(&model.Channel{}, &model.Account{}))
	aes := "0123456789abcdef0123456789abcdef"
	ch := model.Channel{Name: "ch", Type: "openai", BaseURL: up.URL, Status: 1, Timeout: 5}
	require.NoError(t, g.Create(&ch).Error)
	enc, err := crypto.Encrypt(aes, "sk-1")
	require.NoError(t, err)
	acc := model.Account{ChannelID: ch.ID, Name: "a1", APIKeyEnc: enc, Weight: 1, Status: 1}
	require.NoError(t, g.Create(&acc).Error)

	fixed := time.Unix(1_700_000_000, 0)
	accSvc := account.NewService(account.NewRepo(g))
	chanSvc := channel.NewService(channel.NewRepo(g), aes)
	keyed := breaker.NewMemoryKeyed(time.Second, 30*time.Minute) // base=1s，远小于 Retry-After=5s
	accBrk := breaker.NewMemoryBreaker(breaker.Params{Window: time.Minute, Cooldown: time.Minute, FailThreshold: 3, ErrorRate: 0.5, MinSamples: 20, ProbeBackoffMax: 8})
	sel := NewAccountSelector(chanSvc, aes, accSvc, accBrk, keyed, func() time.Time { return fixed }, nil, 1.0)

	chBrk := breaker.NewMemoryBreaker(breaker.Params{Window: time.Minute, Cooldown: time.Minute, FailThreshold: 5, ErrorRate: 0.5, MinSamples: 20, ProbeBackoffMax: 8})
	d := Deps{
		Breaker: chBrk, Limiter: ratelimit.NewMemoryLimiter(),
		Lookup: sel.Lookup, LookupExcluding: sel.LookupExcluding, RecordAccountResult: sel.RecordResult,
		Now: func() time.Time { return fixed },
	}
	cands := []router.Candidate{{ChannelID: ch.ID, Model: "m", ProviderType: "openai", Outbound: "openai", Inbound: "openai"}}
	w := httptest.NewRecorder()
	out := Dispatch(w, cands, []byte(`{"model":"m","messages":[]}`), d)
	require.Equal(t, http.StatusBadGateway, out.Status, "唯一账号 429 耗尽重试后 502")

	key := breaker.AccountModelKey(acc.ID, "m")
	require.False(t, keyed.Allow(key, fixed.Add(4*time.Second)), "冷却应遵循 Retry-After=5s，而非更短的默认 1s 退避")
	require.True(t, keyed.Allow(key, fixed.Add(6*time.Second)), "超过 Retry-After 后应恢复")
}

// ---- E. 首字节预算硬墙钟 ----

// TestClassifyFailureFirstByteTimeoutCounted 墙钟砍掉出站请求时，client.Do 返回的 error 同时
// 满足 errors.Is(err, context.Canceled)——必须先查 context.Cause 才能把它和「客户端真取消」
// 区分开。顺序写反等于改动完全失效（首字节超时仍不计熔断），故两向都验。
func TestClassifyFailureFirstByteTimeoutCounted(t *testing.T) {
	// 墙钟砍：cause 为 errFirstByteTimeout → 计入熔断
	budgetCtx, budgetCancel := context.WithCancelCause(context.Background())
	budgetCancel(errFirstByteTimeout)
	rec, is429 := classifyFailure(budgetCtx, &urlErrLike{msg: "context canceled", err: context.Canceled}, 0)
	require.True(t, rec, "首字节预算超时是上游故障信号，必须计入熔断")
	require.False(t, is429)

	// 客户端真取消：cause 为 context.Canceled 本身 → 不计入熔断（坑b 原意）
	clientCtx, clientCancel := context.WithCancelCause(context.Background())
	clientCancel(nil)
	rec, is429 = classifyFailure(clientCtx, &urlErrLike{msg: "context canceled", err: context.Canceled}, 0)
	require.False(t, rec, "客户端取消不应计入熔断")
	require.False(t, is429)

	// 无 ctx 参与的普通传输错误仍照旧计入
	rec, _ = classifyFailure(context.Background(), errors.New("connection reset"), 0)
	require.True(t, rec)
}

// TestFirstByteBudgetCappedByMaxWall 预算取「渠道 Timeout」与「墙钟剩余」的较小值；
// MaxWall=0（不限）时退化为渠道 Timeout；sole 豁免候选额外压到 10s。
//
// 这里以纯函数覆盖 spec 的测试项 9（sole 短预算）——端到端验证需要真等 10 秒，
// 不适合放进常跑单测；「预算真能砍住慢上游」由 TestDispatchHardWallClockCutsSlowUpstream 保证。
func TestFirstByteBudgetCappedByMaxWall(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	d := Deps{Now: func() time.Time { return now }, MaxWall: 20 * time.Second}
	info := ChannelInfo{Timeout: 300}

	require.Equal(t, 20*time.Second, firstByteBudget(d, info, now, false), "墙钟剩余小于渠道 Timeout 时以墙钟为准")

	d.MaxWall = 0
	require.Equal(t, 300*time.Second, firstByteBudget(d, info, now, false), "MaxWall=0 退化为渠道 Timeout")

	d.MaxWall = 20 * time.Second
	require.Equal(t, 10*time.Second, firstByteBudget(d, info, now, true), "sole 豁免候选额外压到 10s")
}

// TestDispatchHardWallClockCutsSlowUpstream 渠道 Timeout(300s) 远大于 MaxWall(1s) 的慢上游：
// 实际耗时必须 ≈ MaxWall 而非渠道 Timeout。这是本轮的核心诉求——改造前 max_wall 只在候选
// 间隙检查、管不住已进入 DialWith 的单次调用，请求会一路跑到渠道 Timeout 被 CDN 边缘超时接管。
// 变异验证：把 DialWith 还原成不绑 ctx，此用例必然超时失败。
func TestDispatchHardWallClockCutsSlowUpstream(t *testing.T) {
	// sleep 取 3s：既远大于 MaxWall(1s) 足以证明预算生效，又不让 up.Close() 白等太久
	// （httptest 的 Close 会等 handler goroutine 退出，与 Dispatch 的返回时机无关）。
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(3 * time.Second) // 远超 MaxWall，永远等不到首字节
		w.WriteHeader(200)
	}))
	defer up.Close()
	d := convDeps(func(id uint64, m string) (ChannelInfo, bool) {
		return ChannelInfo{BaseURL: up.URL, Timeout: 300, ProviderType: "openai"}, true
	})
	d.MaxWall = 1 * time.Second
	cands := []router.Candidate{{ChannelID: 1, Model: "m", ProviderType: "openai", Outbound: "openai", Inbound: "openai"}}
	w := httptest.NewRecorder()

	start := time.Now()
	out := Dispatch(w, cands, []byte(`{"model":"m","messages":[]}`), d)
	elapsed := time.Since(start)

	require.Less(t, elapsed, 2*time.Second, "首字节预算必须砍在墙钟(1s)附近，而不是等满上游 3s / 渠道 Timeout 300s")
	require.Contains(t, []int{http.StatusBadGateway, http.StatusGatewayTimeout}, out.Status)
}

// TestDispatchStreamingBodyNotCutByBudget 首字节在预算内到达后，body 缓慢流出**不得**被墙钟砍断——
// 预算只覆盖「发出请求到收到响应头」，拿到响应头即 timer.Stop()。这是最容易回归的点：
// 若把 ctx 一直挂到 body 读完，流式 SSE 会在墙钟到期时被截断。
func TestDispatchStreamingBodyNotCutByBudget(t *testing.T) {
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(200)
		fl, _ := w.(http.Flusher)
		// 首字节立刻给，随后分片慢慢发，总时长跨过 MaxWall
		for i := 0; i < 3; i++ {
			_, _ = w.Write([]byte("data: {\"choices\":[{\"delta\":{\"content\":\"x\"}}]}\n\n"))
			if fl != nil {
				fl.Flush()
			}
			time.Sleep(400 * time.Millisecond)
		}
		_, _ = w.Write([]byte("data: [DONE]\n\n"))
		if fl != nil {
			fl.Flush()
		}
	}))
	defer up.Close()
	d := convDeps(func(id uint64, m string) (ChannelInfo, bool) {
		return ChannelInfo{BaseURL: up.URL, Timeout: 300, ProviderType: "openai"}, true
	})
	d.MaxWall = 500 * time.Millisecond // 故意小于 body 总时长
	cands := []router.Candidate{{ChannelID: 1, Model: "m", ProviderType: "openai", Outbound: "openai", Inbound: "openai"}}
	w := httptest.NewRecorder()

	out := Dispatch(w, cands, []byte(`{"model":"m","messages":[],"stream":true}`), d)

	require.Equal(t, 200, out.Status, "首字节已到达，流式响应不应被墙钟砍断")
	require.Contains(t, w.Body.String(), "[DONE]", "SSE 必须完整传完")
}
