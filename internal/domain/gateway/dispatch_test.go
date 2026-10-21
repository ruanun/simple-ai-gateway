package gateway

import (
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"testing"
	"time"

	"github.com/ruanun/simple-ai-gateway/internal/breaker"
	"github.com/ruanun/simple-ai-gateway/internal/dbtest"
	"github.com/ruanun/simple-ai-gateway/internal/domain/proxy"
	"github.com/ruanun/simple-ai-gateway/internal/model"
	"github.com/ruanun/simple-ai-gateway/internal/ratelimit"
	"github.com/ruanun/simple-ai-gateway/internal/router"
	"github.com/stretchr/testify/require"
)

// newForwardProxy 起一个最小 http 正向代理：收到带绝对 URL 的请求（GET/POST 明文 http）即转发到
// 目标上游，并对每次代理转发计数（*counter）。仅用于测试经代理出站。
func newForwardProxy(counter *int) *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		*counter++
		// 正向代理：请求行携带绝对 URL（r.RequestURI / r.URL 含 scheme+host）。
		outReq, err := http.NewRequest(r.Method, r.URL.String(), r.Body)
		if err != nil {
			w.WriteHeader(http.StatusBadGateway)
			return
		}
		for k, vs := range r.Header {
			for _, v := range vs {
				outReq.Header.Add(k, v)
			}
		}
		resp, err := http.DefaultTransport.RoundTrip(outReq)
		if err != nil {
			w.WriteHeader(http.StatusBadGateway)
			return
		}
		defer resp.Body.Close()
		for k, vs := range resp.Header {
			for _, v := range vs {
				w.Header().Add(k, v)
			}
		}
		w.WriteHeader(resp.StatusCode)
		_, _ = io.Copy(w, resp.Body)
	}))
}

// splitHostPort 从 httptest server URL 解析出 host 与 int 端口。
func splitHostPort(t *testing.T, rawURL string) (string, int) {
	t.Helper()
	u, err := url.Parse(rawURL)
	require.NoError(t, err)
	host, portStr, err := net.SplitHostPort(u.Host)
	require.NoError(t, err)
	port, err := strconv.Atoi(portStr)
	require.NoError(t, err)
	return host, port
}

func TestDispatchRetryThenSucceed(t *testing.T) {
	bad := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(500) }))
	defer bad.Close()
	good := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"ok"}`))
	}))
	defer good.Close()

	infos := map[uint64]ChannelInfo{
		1: {BaseURL: bad.URL, Timeout: 5},
		2: {BaseURL: good.URL, Timeout: 5},
	}
	d := Deps{
		Breaker: breaker.NewMemoryBreaker(breaker.Params{Window: time.Minute, Cooldown: time.Minute, FailThreshold: 3, ErrorRate: 0.5, MinSamples: 20, ProbeBackoffMax: 8}),
		Limiter: ratelimit.NewMemoryLimiter(),
		Lookup:  func(id uint64, modelName string) (ChannelInfo, bool) { i, ok := infos[id]; return i, ok },
		Now:     func() time.Time { return time.Unix(1_000_000, 0) },
	}
	w := httptest.NewRecorder()
	out := Dispatch(w, []router.Candidate{{ChannelID: 1}, {ChannelID: 2}}, []byte(`{"model":"m"}`), d)
	require.Equal(t, uint64(2), out.ChannelID)
	require.Equal(t, 200, out.Status)
	require.Contains(t, w.Body.String(), `"id":"ok"`)
}

// TestDispatchReselectsAccountOnRetry 验证渠道内重试时重选账号：
// 第一次 Lookup 返回坏上游（500），第二次返回好上游，应成功且用到第二个账号。
func TestDispatchReselectsAccountOnRetry(t *testing.T) {
	bad := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(500) }))
	defer bad.Close()
	good := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"ok"}`))
	}))
	defer good.Close()
	call := 0
	d := Deps{
		Breaker: breaker.NewMemoryBreaker(breaker.Params{Window: time.Minute, Cooldown: time.Minute, FailThreshold: 5, ErrorRate: 0.5, MinSamples: 20, ProbeBackoffMax: 8}), Limiter: ratelimit.NewMemoryLimiter(),
		Lookup: func(id uint64, modelName string) (ChannelInfo, bool) {
			call++
			if call == 1 {
				return ChannelInfo{BaseURL: bad.URL, Timeout: 5, MaxRetries: 1, AccountID: 1}, true
			}
			return ChannelInfo{BaseURL: good.URL, Timeout: 5, MaxRetries: 1, AccountID: 2}, true
		},
		Now: func() time.Time { return time.Unix(1_000_000, 0) },
	}
	w := httptest.NewRecorder()
	out := Dispatch(w, []router.Candidate{{ChannelID: 1}}, []byte(`{"model":"m"}`), d)
	require.Equal(t, 200, out.Status)
	require.Equal(t, uint64(2), out.AccountID) // 重试用了第二个账号
}

// TestDispatchExcludesTriedAccountOnRetry 覆盖 I2：普通失败(500)重试时不重选回刚失败账号。
// Lookup 总返回坏账号 1（模拟加权随机可能选回它）；只有 LookupExcluding(排除已试 {1})
// 才返回好账号 2。断言成功且用到账号 2，证明重试走的是 LookupExcluding 排除路径。
func TestDispatchExcludesTriedAccountOnRetry(t *testing.T) {
	bad := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(500) }))
	defer bad.Close()
	good := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"ok"}`))
	}))
	defer good.Close()

	badInfo := ChannelInfo{BaseURL: bad.URL, Timeout: 5, MaxRetries: 1, AccountID: 1}
	goodInfo := ChannelInfo{BaseURL: good.URL, Timeout: 5, MaxRetries: 1, AccountID: 2}
	var excludeSeen map[uint64]bool
	d := Deps{
		Breaker: breaker.NewMemoryBreaker(breaker.Params{Window: time.Minute, Cooldown: time.Minute, FailThreshold: 5, ErrorRate: 0.5, MinSamples: 20, ProbeBackoffMax: 8}), Limiter: ratelimit.NewMemoryLimiter(),
		// Lookup 永远返回坏账号（无排除时会一直选回它）。
		Lookup: func(id uint64, modelName string) (ChannelInfo, bool) { return badInfo, true },
		// 重试时排除已试账号 → 返回好账号。
		LookupExcluding: func(id uint64, modelName string, exclude map[uint64]bool) (ChannelInfo, bool) {
			excludeSeen = exclude
			if exclude[1] {
				return goodInfo, true
			}
			return badInfo, true
		},
		Now: func() time.Time { return time.Unix(1_000_000, 0) },
	}
	w := httptest.NewRecorder()
	out := Dispatch(w, []router.Candidate{{ChannelID: 1}}, []byte(`{"model":"m"}`), d)
	require.Equal(t, 200, out.Status)
	require.Equal(t, uint64(2), out.AccountID, "重试应排除坏账号 1、改用好账号 2")
	require.True(t, excludeSeen[1], "重试应把首次失败的账号 1 放入排除集")
}

// TestDispatchAccount429DoesNotTripChannel 覆盖 I1：账号 429 失败不应让渠道 breaker 立即熔断
// （否则单账号限流误伤整条渠道）。渠道两候选：候选1 账号恒 429、候选2 好上游。
// 渠道 breaker 阈值=3：若账号 429 被当成渠道 is429 立即熔断，候选1 一次 429 就开闸；
// 这里断言候选1 单次 429 后渠道 breaker 仍 Allow（未立即熔断），并最终降级到候选2 成功。
func TestDispatchAccount429DoesNotTripChannel(t *testing.T) {
	bad := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusTooManyRequests)
	}))
	defer bad.Close()
	good := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"ok"}`))
	}))
	defer good.Close()

	brk := breaker.NewMemoryBreaker(breaker.Params{Window: time.Minute, Cooldown: time.Minute, FailThreshold: 3, ErrorRate: 0.5, MinSamples: 20, ProbeBackoffMax: 8})
	now := time.Unix(1_000_000, 0)
	infos := map[uint64]ChannelInfo{
		1: {BaseURL: bad.URL, Timeout: 5, AccountID: 11},  // 账号恒 429
		2: {BaseURL: good.URL, Timeout: 5, AccountID: 22}, // 好上游
	}
	d := Deps{
		Breaker: brk, Limiter: ratelimit.NewMemoryLimiter(),
		Lookup: func(id uint64, modelName string) (ChannelInfo, bool) { i, ok := infos[id]; return i, ok },
		Now:    func() time.Time { return now },
	}
	w := httptest.NewRecorder()
	out := Dispatch(w, []router.Candidate{{ChannelID: 1}, {ChannelID: 2}}, []byte(`{"model":"m"}`), d)
	require.Equal(t, 200, out.Status)
	require.Equal(t, uint64(2), out.ChannelID)
	// 关键：候选1 经历一次账号 429 后，渠道 breaker 不应立即熔断（阈值=3，单次失败不开闸）。
	require.True(t, brk.Allow(1, now), "账号 429 不应让渠道 breaker 立即熔断")
}

func TestDispatchAllFail(t *testing.T) {
	bad := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(500) }))
	defer bad.Close()
	d := Deps{
		Breaker: breaker.NewMemoryBreaker(breaker.Params{Window: time.Minute, Cooldown: time.Minute, FailThreshold: 3, ErrorRate: 0.5, MinSamples: 20, ProbeBackoffMax: 8}), Limiter: ratelimit.NewMemoryLimiter(),
		Lookup: func(id uint64, modelName string) (ChannelInfo, bool) {
			return ChannelInfo{BaseURL: bad.URL, Timeout: 5}, true
		},
		Now: func() time.Time { return time.Unix(1_000_000, 0) },
	}
	w := httptest.NewRecorder()
	out := Dispatch(w, []router.Candidate{{ChannelID: 1}}, []byte(`{"model":"m"}`), d)
	require.Equal(t, 502, out.Status)
}

// TestDispatchViaProxy 验证 Dispatch 经账号 ProxyGroup 选中的 http 正向代理出站：
// 起一个记录请求的正向代理 server，建代理组指向它、ChannelInfo.ProxyGroup 指向该组，
// 断言代理 server 收到了出站请求且整体成功。
func TestDispatchViaProxy(t *testing.T) {
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"ok"}`))
	}))
	defer up.Close()

	var viaProxy int
	px := newForwardProxy(&viaProxy)
	defer px.Close()

	g := dbtest.OpenInMemory(t)
	require.NoError(t, g.AutoMigrate(&model.Proxy{}))
	g.Exec("DELETE FROM proxies") // -count=N 同进程重跑：清空 shared-cache 内存库残留
	svc := proxy.NewService(proxy.NewRepo(g))
	host, port := splitHostPort(t, px.URL)
	_, err := svc.Create(model.Proxy{Group: "pg", Name: "p1", Protocol: "http", Host: host, Port: port, Weight: 1, Status: 1})
	require.NoError(t, err)

	resolver := NewProxyResolver(svc, breaker.NewMemoryBreaker(breaker.Params{Window: time.Minute, Cooldown: time.Minute, FailThreshold: 3, ErrorRate: 0.5, MinSamples: 20, ProbeBackoffMax: 8}), nil, 1.0)
	d := Deps{
		Breaker: breaker.NewMemoryBreaker(breaker.Params{Window: time.Minute, Cooldown: time.Minute, FailThreshold: 3, ErrorRate: 0.5, MinSamples: 20, ProbeBackoffMax: 8}), Limiter: ratelimit.NewMemoryLimiter(),
		Lookup: func(id uint64, modelName string) (ChannelInfo, bool) {
			return ChannelInfo{BaseURL: up.URL, Timeout: 5, AccountID: 1, ProxyGroup: "pg"}, true
		},
		Proxy: resolver,
		Now:   func() time.Time { return time.Unix(1_000_000, 0) },
	}
	w := httptest.NewRecorder()
	out := Dispatch(w, []router.Candidate{{ChannelID: 1}}, []byte(`{"model":"m"}`), d)
	require.Equal(t, 200, out.Status, "经代理应成功，resp=%s", w.Body.String())
	require.Greater(t, viaProxy, 0, "出站请求应经正向代理")
	require.Contains(t, w.Body.String(), `"id":"ok"`)
}

func TestDispatchPropagatesRateMultiplier(t *testing.T) {
	good := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"ok"}`))
	}))
	defer good.Close()
	d := Deps{
		Breaker: breaker.NewMemoryBreaker(breaker.Params{Window: time.Minute, Cooldown: time.Minute, FailThreshold: 3, ErrorRate: 0.5, MinSamples: 20, ProbeBackoffMax: 8}), Limiter: ratelimit.NewMemoryLimiter(),
		Lookup: func(id uint64, modelName string) (ChannelInfo, bool) {
			return ChannelInfo{BaseURL: good.URL, Timeout: 5, RateMultiplier: 2}, true
		},
		Now: func() time.Time { return time.Unix(1_000_000, 0) },
	}
	w := httptest.NewRecorder()
	out := Dispatch(w, []router.Candidate{{ChannelID: 1}}, []byte(`{"model":"m"}`), d)
	require.Equal(t, 200, out.Status)
	require.Equal(t, 2.0, out.RateMultiplier) // 命中渠道倍率随 Outcome 带回
}

// TestDispatchProxyGroupNoAvailableIsAccountFailure 覆盖 R2：账号绑了代理组但组内无可用代理。
// 这是本地配置问题、未真正打到上游，按坑b不应计入渠道/账号熔断（Task 5b 前会误记为账号失败，
// 已改为只留痕跳过原因）；这里组为空 → 单候选下全失败返回 502，上游未被打到，且渠道熔断未被触发。
func TestDispatchProxyGroupNoAvailableIsAccountFailure(t *testing.T) {
	var upstreamHit int
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		upstreamHit++
		_, _ = w.Write([]byte(`{"id":"ok"}`))
	}))
	defer up.Close()

	g := dbtest.OpenInMemory(t)
	require.NoError(t, g.AutoMigrate(&model.Proxy{}))
	svc := proxy.NewService(proxy.NewRepo(g)) // 组 pg 内无任何代理
	resolver := NewProxyResolver(svc, breaker.NewMemoryBreaker(breaker.Params{Window: time.Minute, Cooldown: time.Minute, FailThreshold: 3, ErrorRate: 0.5, MinSamples: 20, ProbeBackoffMax: 8}), nil, 1.0)

	var accFail int
	chBrk := breaker.NewMemoryBreaker(breaker.Params{Window: time.Minute, Cooldown: time.Minute, FailThreshold: 1, ErrorRate: 0.5, MinSamples: 20, ProbeBackoffMax: 8}) // 阈值 1：一旦误记必开
	d := Deps{
		Breaker: chBrk, Limiter: ratelimit.NewMemoryLimiter(),
		Lookup: func(id uint64, modelName string) (ChannelInfo, bool) {
			return ChannelInfo{BaseURL: up.URL, Timeout: 5, AccountID: 7, ProxyGroup: "pg"}, true
		},
		RecordAccountResult: func(accountID uint64, modelName string, ok, quotaHit bool, retryAfter time.Duration) {
			if !ok {
				accFail++
			}
		},
		Proxy: resolver,
		Now:   func() time.Time { return time.Unix(1_000_000, 0) },
	}
	w := httptest.NewRecorder()
	out := Dispatch(w, []router.Candidate{{ChannelID: 1}}, []byte(`{"model":"m"}`), d)
	require.Equal(t, 502, out.Status, "组内无可用代理 → 全失败 502")
	require.Equal(t, 0, upstreamHit, "无可用代理时不应打到上游")
	require.Equal(t, 0, accFail, "本地未达上游失败不应计入账号失败（坑b）")
	require.True(t, chBrk.Allow(1, time.Unix(1_000_000, 0)), "本地未达上游失败不应打开渠道熔断（坑b）")
}

func TestDispatchAnthropicPassthrough(t *testing.T) {
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "/v1/messages", r.URL.Path)
		require.Equal(t, "test-key", r.Header.Get("x-api-key"))
		require.Equal(t, "2023-06-01", r.Header.Get("anthropic-version"))
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"msg_1","type":"message","role":"assistant","content":[{"type":"text","text":"hello"}],"model":"claude-3","stop_reason":"end_turn","usage":{"input_tokens":10,"output_tokens":5}}`))
	}))
	defer up.Close()
	d := Deps{
		Breaker: breaker.NewMemoryBreaker(breaker.Params{Window: time.Minute, Cooldown: time.Minute, FailThreshold: 3, ErrorRate: 0.5, MinSamples: 20, ProbeBackoffMax: 8}), Limiter: ratelimit.NewMemoryLimiter(),
		Lookup: func(id uint64, modelName string) (ChannelInfo, bool) {
			return ChannelInfo{BaseURL: up.URL, UpstreamKey: "test-key", Timeout: 5, ProviderType: "anthropic"}, true
		},
		Now: func() time.Time { return time.Unix(1_000_000, 0) },
	}
	w := httptest.NewRecorder()
	out := Dispatch(w, []router.Candidate{{ChannelID: 1, Model: "claude-3", ProviderType: "anthropic"}},
		[]byte(`{"model":"claude-3","messages":[{"role":"user","content":"hi"}]}`), d)
	require.Equal(t, 200, out.Status)
	require.Equal(t, 10, out.Usage.PromptTokens)
	require.Equal(t, 5, out.Usage.CompletionTokens)
	require.True(t, out.UsageOK)
	require.Contains(t, w.Body.String(), `"text":"hello"`)
}

// TestDispatch403DoesNotTripChannelImmediately 覆盖 P12a：上游 403(配额耗尽)不应让渠道 breaker
// 立即熔断（否则误伤同渠道其他模型）。403 属账号×模型配额维度，渠道 breaker 只记滑窗失败。
func TestDispatch403DoesNotTripChannelImmediately(t *testing.T) {
	forbidden := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
	}))
	defer forbidden.Close()

	brk := breaker.NewMemoryBreaker(breaker.Params{Window: time.Minute, Cooldown: time.Minute, FailThreshold: 3, ErrorRate: 0.5, MinSamples: 20, ProbeBackoffMax: 8}) // 阈值 3：单次 403 不应开闸
	var quotaCalls []string
	d := Deps{
		Breaker: brk, Limiter: ratelimit.NewMemoryLimiter(),
		Lookup: func(id uint64, modelName string) (ChannelInfo, bool) {
			return ChannelInfo{BaseURL: forbidden.URL, Timeout: 5, AccountID: 1}, true
		},
		RecordAccountResult: func(accountID uint64, modelName string, ok, quotaHit bool, retryAfter time.Duration) {
			if !ok && quotaHit {
				quotaCalls = append(quotaCalls, modelName)
			}
		},
		Now: func() time.Time { return time.Unix(1_000_000, 0) },
	}
	w := httptest.NewRecorder()
	out := Dispatch(w, []router.Candidate{{ChannelID: 1, Model: "model-a"}}, []byte(`{"model":"model-a"}`), d)
	require.Equal(t, http.StatusBadGateway, out.Status, "唯一候选 403 重试耗尽后 502")
	require.True(t, brk.Allow(1, time.Unix(1_000_000, 0)), "单次 403 不应立即熔断渠道")
	require.NotEmpty(t, quotaCalls, "403 应以 quotaHit=true 回调账号级记录")
	require.Len(t, quotaCalls, 1, "MaxRetries=0 时唯一候选只尝试一次，回调应恰好一次")
	require.Equal(t, "model-a", quotaCalls[0], "回调应带上候选真实模型名")
}

// TestDispatch403FailoverToNextChannel 覆盖 P12a：候选1 上游恒 403（配额耗尽）时，
// 不应因该候选耗尽重试就整体失败，应降级到候选2 成功（渠道级 failover 与账号×模型冷却解耦）。
func TestDispatch403FailoverToNextChannel(t *testing.T) {
	forbidden := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
	}))
	defer forbidden.Close()
	good := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"ok"}`))
	}))
	defer good.Close()

	infos := map[uint64]ChannelInfo{
		1: {BaseURL: forbidden.URL, Timeout: 5, AccountID: 1}, // 候选1 账号恒 403
		2: {BaseURL: good.URL, Timeout: 5, AccountID: 2},      // 候选2 好上游
	}
	d := Deps{
		Breaker: breaker.NewMemoryBreaker(breaker.Params{Window: time.Minute, Cooldown: time.Minute, FailThreshold: 3, ErrorRate: 0.5, MinSamples: 20, ProbeBackoffMax: 8}), Limiter: ratelimit.NewMemoryLimiter(),
		Lookup: func(id uint64, modelName string) (ChannelInfo, bool) { i, ok := infos[id]; return i, ok },
		Now:    func() time.Time { return time.Unix(1_000_000, 0) },
	}
	w := httptest.NewRecorder()
	out := Dispatch(w, []router.Candidate{{ChannelID: 1, Model: "model-a"}, {ChannelID: 2, Model: "model-a"}},
		[]byte(`{"model":"model-a"}`), d)
	require.Equal(t, 200, out.Status)
	require.Equal(t, uint64(2), out.ChannelID, "候选1 恒 403 应降级到候选2")
}

func TestDispatchRewritesModelToCandidateModel(t *testing.T) {
	var gotModel string
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var b struct {
			Model string `json:"model"`
		}
		raw, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(raw, &b)
		gotModel = b.Model
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"ok"}`))
	}))
	defer up.Close()
	d := Deps{
		Breaker: breaker.NewMemoryBreaker(breaker.Params{Window: time.Minute, Cooldown: time.Minute, FailThreshold: 3, ErrorRate: 0.5, MinSamples: 20, ProbeBackoffMax: 8}), Limiter: ratelimit.NewMemoryLimiter(),
		Lookup: func(id uint64, modelName string) (ChannelInfo, bool) {
			return ChannelInfo{BaseURL: up.URL, Timeout: 5}, true
		},
		Now: func() time.Time { return time.Unix(1_000_000, 0) },
	}
	w := httptest.NewRecorder()
	// 别名请求体 model="smart",候选目标 Model="gpt-4o-real" → 上游应收到 gpt-4o-real。
	out := Dispatch(w, []router.Candidate{{ChannelID: 1, Model: "gpt-4o-real"}}, []byte(`{"model":"smart","stream":false}`), d)
	require.Equal(t, 200, out.Status)
	require.Equal(t, "gpt-4o-real", gotModel)
	require.Equal(t, "gpt-4o-real", out.Model) // Outcome 带回命中模型
}

// TestDispatchOutboundAnthropicUsesBaseURL anthropic 出站使用渠道 base_url（AnthropicAdapter 路径）。
func TestDispatchOutboundAnthropicUsesBaseURL(t *testing.T) {
	var gotPath string
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"msg_1","type":"message","content":[],"usage":{"input_tokens":1,"output_tokens":1}}`))
	}))
	defer up.Close()
	d := Deps{
		Breaker: breaker.NewMemoryBreaker(breaker.Params{Window: time.Minute, Cooldown: time.Minute, FailThreshold: 3, ErrorRate: 0.5, MinSamples: 20, ProbeBackoffMax: 8}), Limiter: ratelimit.NewMemoryLimiter(),
		Lookup: func(id uint64, modelName string) (ChannelInfo, bool) {
			return ChannelInfo{BaseURL: up.URL, UpstreamKey: "k", Timeout: 5, ProviderType: "anthropic"}, true
		},
		Now: func() time.Time { return time.Unix(1_000_000, 0) },
	}
	w := httptest.NewRecorder()
	out := Dispatch(w, []router.Candidate{{ChannelID: 1, Model: "m", Outbound: "anthropic"}},
		[]byte(`{"model":"m"}`), d)
	require.Equal(t, 200, out.Status)
	require.Equal(t, "/v1/messages", gotPath)
	require.Equal(t, "anthropic", out.Protocol)
}
