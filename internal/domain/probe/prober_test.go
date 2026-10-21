package probe

import (
	"errors"
	"net/http"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ruanun/simple-ai-gateway/internal/breaker"
	"github.com/ruanun/simple-ai-gateway/internal/config"
	"github.com/ruanun/simple-ai-gateway/internal/crypto"
	"github.com/ruanun/simple-ai-gateway/internal/model"
	"github.com/stretchr/testify/require"
)

const testAES = "0123456789abcdef0123456789abcdef"

type fakeChans struct{ chs []model.Channel }

func (f fakeChans) List() ([]model.Channel, error) { return f.chs, nil }

type fakeAccts struct{ byCh map[uint64]*model.Account }

func (f fakeAccts) FirstEnabledByChannel(id uint64) (*model.Account, error) { return f.byCh[id], nil }

func ch(id uint64) model.Channel {
	return model.Channel{BaseModel: model.BaseModel{ID: id}, Type: "openai", Status: 1, BaseURL: "http://x"}
}
func acc(id, chID uint64) *model.Account {
	enc, _ := crypto.Encrypt(testAES, "sk-up")
	return &model.Account{BaseModel: model.BaseModel{ID: id}, ChannelID: chID, Status: 1, APIKeyEnc: enc}
}
func cfg() config.ProbeConfig {
	return config.ProbeConfig{Enabled: true, Interval: time.Second, Concurrency: 4, Timeout: 10 * time.Second}
}
func p(brkParams breaker.Params) *breaker.MemoryBreaker { return breaker.NewMemoryBreaker(brkParams) }

// fakeCountingBreaker 包装真实 MemoryBreaker、额外记录 RecordResult 调用次数，
// 供归因测试断言「代理网络故障时渠道熔断不应被记结果」。
type fakeCountingBreaker struct {
	*breaker.MemoryBreaker
	recordCalls int32
}

func (b *fakeCountingBreaker) RecordResult(id uint64, now time.Time, ok bool, is429 bool) {
	atomic.AddInt32(&b.recordCalls, 1)
	b.MemoryBreaker.RecordResult(id, now, ok, is429)
}

// 半开渠道探测 200 → 熔断器闭合。
func TestProbeOnceRecoveringChannelHealed(t *testing.T) {
	now := time.Unix(1000, 0)
	brk := p(breaker.Params{Window: 30 * time.Second, Cooldown: 60 * time.Second, FailThreshold: 1, ErrorRate: 0.5, MinSamples: 20, ProbeBackoffMax: 8})
	brk.RecordResult(1, now, false, false) // 渠道1 开
	later := now.Add(61 * time.Second)     // 冷却过 → Recovering
	require.True(t, brk.Recovering(1, later))

	d := Deps{
		Breaker:   brk,
		Channels:  fakeChans{chs: []model.Channel{ch(1)}},
		Accounts:  fakeAccts{byCh: map[uint64]*model.Account{1: acc(10, 1)}},
		AESKey:    testAES,
		ClientFor: func(group string, sec int) (*http.Client, uint64, bool) { return &http.Client{}, 0, true },
		ProbeModels: func(cli *http.Client, baseURL, key, protocol, authStyle string, hdr map[string]string) (int, []byte, error) {
			return 200, []byte(`{}`), nil // 探测成功
		},
	}
	pr := NewProberStatic(d, cfg(), func() time.Time { return later })
	pr.probeOnce(later)
	require.True(t, brk.Allow(1, later))       // 已闭合(放行)
	require.False(t, brk.Recovering(1, later)) // 不再恢复态
}

// 半开渠道探测 500 → 保持熔断(重开)。
func TestProbeOnceRecoveringChannelStillDown(t *testing.T) {
	now := time.Unix(1000, 0)
	brk := p(breaker.Params{Window: 30 * time.Second, Cooldown: 60 * time.Second, FailThreshold: 1, ErrorRate: 0.5, MinSamples: 20, ProbeBackoffMax: 8})
	brk.RecordResult(1, now, false, false)
	later := now.Add(61 * time.Second)
	d := Deps{
		Breaker: brk, Channels: fakeChans{chs: []model.Channel{ch(1)}},
		Accounts: fakeAccts{byCh: map[uint64]*model.Account{1: acc(10, 1)}}, AESKey: testAES,
		ClientFor: func(g string, s int) (*http.Client, uint64, bool) { return &http.Client{}, 0, true },
		ProbeModels: func(cli *http.Client, b, k, pr, a string, h map[string]string) (int, []byte, error) {
			return 500, nil, nil
		},
	}
	pr := NewProberStatic(d, cfg(), func() time.Time { return later })
	pr.probeOnce(later)
	require.False(t, brk.Allow(1, later)) // 探测失败 → 重开、仍冷却中
}

// 非 Recovering(健康 closed)渠道不被探测。
func TestProbeOnceSkipsHealthyChannel(t *testing.T) {
	now := time.Unix(1000, 0)
	brk := p(breaker.Params{Window: 30 * time.Second, Cooldown: 60 * time.Second, FailThreshold: 5, ErrorRate: 0.5, MinSamples: 20, ProbeBackoffMax: 8})
	var probed int32
	d := Deps{
		Breaker: brk, Channels: fakeChans{chs: []model.Channel{ch(1)}},
		Accounts: fakeAccts{byCh: map[uint64]*model.Account{1: acc(10, 1)}}, AESKey: testAES,
		ClientFor: func(g string, s int) (*http.Client, uint64, bool) { return &http.Client{}, 0, true },
		ProbeModels: func(cli *http.Client, b, k, pr, a string, h map[string]string) (int, []byte, error) {
			atomic.AddInt32(&probed, 1)
			return 200, nil, nil
		},
	}
	pr := NewProberStatic(d, cfg(), func() time.Time { return now })
	pr.probeOnce(now)
	require.Equal(t, int32(0), atomic.LoadInt32(&probed), "健康渠道不应被探测")
}

// 已禁用(Status!=1)的渠道即便处于 Recovering(半开)也不被探测：Status 过滤先于 Recovering 判断。
func TestProbeOnceSkipsDisabledChannel(t *testing.T) {
	now := time.Unix(1000, 0)
	brk := p(breaker.Params{Window: 30 * time.Second, Cooldown: 60 * time.Second, FailThreshold: 1, ErrorRate: 0.5, MinSamples: 20, ProbeBackoffMax: 8})
	brk.RecordResult(1, now, false, false) // 渠道1 打开熔断
	later := now.Add(61 * time.Second)     // 冷却已过 → Recovering(若不看 Status 本该被探测)
	require.True(t, brk.Recovering(1, later))

	disabled := ch(1)
	disabled.Status = 0 // 已禁用
	var probed int32
	d := Deps{
		Breaker: brk, Channels: fakeChans{chs: []model.Channel{disabled}},
		Accounts: fakeAccts{byCh: map[uint64]*model.Account{1: acc(10, 1)}}, AESKey: testAES,
		ClientFor: func(g string, s int) (*http.Client, uint64, bool) { return &http.Client{}, 0, true },
		ProbeModels: func(cli *http.Client, b, k, pr, a string, h map[string]string) (int, []byte, error) {
			atomic.AddInt32(&probed, 1)
			return 200, nil, nil
		},
	}
	pr := NewProberStatic(d, cfg(), func() time.Time { return later })
	pr.probeOnce(later)
	require.Equal(t, int32(0), atomic.LoadInt32(&probed), "已禁用渠道不应被探测")
}

// 无启用账号的渠道跳过(不 panic、不动熔断)。
func TestProbeOnceSkipsChannelWithoutAccount(t *testing.T) {
	now := time.Unix(1000, 0)
	brk := p(breaker.Params{Window: 30 * time.Second, Cooldown: 60 * time.Second, FailThreshold: 1, ErrorRate: 0.5, MinSamples: 20, ProbeBackoffMax: 8})
	brk.RecordResult(1, now, false, false)
	later := now.Add(61 * time.Second)
	var probed int32
	d := Deps{
		Breaker: brk, Channels: fakeChans{chs: []model.Channel{ch(1)}},
		Accounts: fakeAccts{byCh: map[uint64]*model.Account{}}, AESKey: testAES, // 无账号
		ClientFor: func(g string, s int) (*http.Client, uint64, bool) { return &http.Client{}, 0, true },
		ProbeModels: func(cli *http.Client, b, k, pr, a string, h map[string]string) (int, []byte, error) {
			atomic.AddInt32(&probed, 1)
			return 200, nil, nil
		},
	}
	pr := NewProberStatic(d, cfg(), func() time.Time { return later })
	require.NotPanics(t, func() { pr.probeOnce(later) })
	require.Equal(t, int32(0), atomic.LoadInt32(&probed))
}

// NewProber 启动后台 ticker 定时探测，Stop 优雅退出、不 panic(空渠道列表下安全跑几轮)。
func TestProberRunStop(t *testing.T) {
	now := time.Unix(1000, 0)
	brk := p(breaker.Params{Window: 30 * time.Second, Cooldown: 60 * time.Second, FailThreshold: 1, ErrorRate: 0.5, MinSamples: 20, ProbeBackoffMax: 8})
	d := Deps{
		Breaker: brk, Channels: fakeChans{}, Accounts: fakeAccts{byCh: map[uint64]*model.Account{}}, AESKey: testAES,
		ClientFor: func(g string, s int) (*http.Client, uint64, bool) { return &http.Client{}, 0, true },
		ProbeModels: func(cli *http.Client, b, k, pr, a string, h map[string]string) (int, []byte, error) {
			return 200, nil, nil
		},
	}
	_ = now
	pr := NewProber(d, config.ProbeConfig{Enabled: true, Interval: 10 * time.Millisecond, Concurrency: 4, Timeout: time.Second})
	time.Sleep(25 * time.Millisecond) // 让 ticker 跑几次(空渠道列表，安全)
	require.NotPanics(t, func() { pr.Stop() })
}

// NewProber 对非正 interval(0/负数，如 YAML 误填)钳制成默认值，不让 time.NewTicker panic 拖垮启动。
func TestNewProberClampsNonPositiveInterval(t *testing.T) {
	brk := p(breaker.Params{Window: 30 * time.Second, Cooldown: 60 * time.Second, FailThreshold: 1, ErrorRate: 0.5, MinSamples: 20, ProbeBackoffMax: 8})
	d := Deps{
		Breaker: brk, Channels: fakeChans{}, Accounts: fakeAccts{byCh: map[uint64]*model.Account{}}, AESKey: testAES,
		ClientFor: func(g string, s int) (*http.Client, uint64, bool) { return &http.Client{}, 0, true },
		ProbeModels: func(cli *http.Client, b, k, pr, a string, h map[string]string) (int, []byte, error) {
			return 200, nil, nil
		},
	}
	require.NotPanics(t, func() {
		pr := NewProber(d, config.ProbeConfig{Enabled: true, Interval: 0, Concurrency: 1, Timeout: time.Second})
		pr.Stop()
	})
}

// probeOnce 并发探测受 Concurrency 封顶：10 个半开渠道、Concurrency=2，注入的 ProbeModels 靠
// 原子计数 + 短暂 sleep 让重叠可观测，断言任意时刻同时在跑的探测数不超过配置的并发上限。
func TestProbeOnceConcurrencyCap(t *testing.T) {
	now := time.Unix(1000, 0)
	brk := p(breaker.Params{Window: 30 * time.Second, Cooldown: 60 * time.Second, FailThreshold: 1, ErrorRate: 0.5, MinSamples: 20, ProbeBackoffMax: 8})
	const n = 10
	chs := make([]model.Channel, 0, n)
	accts := map[uint64]*model.Account{}
	for i := uint64(1); i <= n; i++ {
		brk.RecordResult(i, now, false, false) // 各渠道先打开熔断
		chs = append(chs, ch(i))
		accts[i] = acc(100+i, i)
	}
	later := now.Add(61 * time.Second) // 冷却已过 → 全部 Recovering(半开)

	var inflight, maxInflight, calls int32
	d := Deps{
		Breaker: brk, Channels: fakeChans{chs: chs}, Accounts: fakeAccts{byCh: accts}, AESKey: testAES,
		ClientFor: func(g string, s int) (*http.Client, uint64, bool) { return &http.Client{}, 0, true },
		ProbeModels: func(cli *http.Client, b, k, pr, a string, h map[string]string) (int, []byte, error) {
			atomic.AddInt32(&calls, 1)
			cur := atomic.AddInt32(&inflight, 1)
			for {
				m := atomic.LoadInt32(&maxInflight)
				if cur <= m || atomic.CompareAndSwapInt32(&maxInflight, m, cur) {
					break
				}
			}
			time.Sleep(20 * time.Millisecond) // 拉长单次探测耗时，让并发重叠可被计数捕获
			atomic.AddInt32(&inflight, -1)
			return 200, nil, nil
		},
	}
	pr := NewProberStatic(d, config.ProbeConfig{Enabled: true, Interval: time.Second, Concurrency: 2, Timeout: 10 * time.Second}, func() time.Time { return later })
	pr.probeOnce(later)

	require.Equal(t, int32(n), atomic.LoadInt32(&calls), "10 个半开渠道都应被探测到")
	require.LessOrEqual(t, atomic.LoadInt32(&maxInflight), int32(2), "并发探测数不应超过 Concurrency 上限")
	require.GreaterOrEqual(t, atomic.LoadInt32(&maxInflight), int32(2), "应发生并发(≥2 同时在飞)")
}

// 经代理探测网络失败(perr!=nil 且 proxyID!=0)：无法区分「代理故障」与「渠道不可达」，
// 归因给代理——记代理熔断失败，渠道熔断不记结果(保持半开，等下轮探测或真实流量裁决)。
func TestProbeProxyNetworkErrorAttributedToProxy(t *testing.T) {
	now := time.Unix(1000, 0)
	inner := p(breaker.Params{Window: 30 * time.Second, Cooldown: 60 * time.Second, FailThreshold: 1, ErrorRate: 0.5, MinSamples: 20, ProbeBackoffMax: 8})
	inner.RecordResult(1, now, false, false) // 渠道1 开
	later := now.Add(61 * time.Second)       // 冷却过 → Recovering(半开)
	require.True(t, inner.Recovering(1, later))
	brk := &fakeCountingBreaker{MemoryBreaker: inner}

	var proxyCalls []struct {
		ID uint64
		OK bool
	}
	d := Deps{
		Breaker:  brk,
		Channels: fakeChans{chs: []model.Channel{ch(1)}},
		Accounts: fakeAccts{byCh: map[uint64]*model.Account{1: acc(10, 1)}},
		AESKey:   testAES,
		ClientFor: func(group string, sec int) (*http.Client, uint64, bool) {
			return &http.Client{}, 9, true // 走代理，代理ID=9
		},
		RecordProxyResult: func(id uint64, ok, is429 bool) {
			proxyCalls = append(proxyCalls, struct {
				ID uint64
				OK bool
			}{id, ok})
		},
		ProbeModels: func(cli *http.Client, baseURL, key, protocol, authStyle string, hdr map[string]string) (int, []byte, error) {
			return 0, nil, errors.New("proxy connect refused")
		},
	}
	NewProberStatic(d, cfg(), func() time.Time { return later }).probeOnce(later)
	require.Equal(t, 1, len(proxyCalls))
	require.Equal(t, uint64(9), proxyCalls[0].ID)
	require.False(t, proxyCalls[0].OK)
	require.Zero(t, brk.recordCalls, "渠道熔断不应被记结果（归因给代理）")
}

// 经代理探测拿到 200：代理链路已证明通畅，渠道按状态码闭合 + 代理记成功。
func TestProbeProxySuccessRecordsProxyHealthy(t *testing.T) {
	now := time.Unix(1000, 0)
	inner := p(breaker.Params{Window: 30 * time.Second, Cooldown: 60 * time.Second, FailThreshold: 1, ErrorRate: 0.5, MinSamples: 20, ProbeBackoffMax: 8})
	inner.RecordResult(1, now, false, false)
	later := now.Add(61 * time.Second)
	require.True(t, inner.Recovering(1, later))
	brk := &fakeCountingBreaker{MemoryBreaker: inner}

	var proxyCalls []struct {
		ID uint64
		OK bool
	}
	d := Deps{
		Breaker:  brk,
		Channels: fakeChans{chs: []model.Channel{ch(1)}},
		Accounts: fakeAccts{byCh: map[uint64]*model.Account{1: acc(10, 1)}},
		AESKey:   testAES,
		ClientFor: func(group string, sec int) (*http.Client, uint64, bool) {
			return &http.Client{}, 9, true
		},
		RecordProxyResult: func(id uint64, ok, is429 bool) {
			proxyCalls = append(proxyCalls, struct {
				ID uint64
				OK bool
			}{id, ok})
		},
		ProbeModels: func(cli *http.Client, baseURL, key, protocol, authStyle string, hdr map[string]string) (int, []byte, error) {
			return 200, []byte(`{}`), nil
		},
	}
	NewProberStatic(d, cfg(), func() time.Time { return later }).probeOnce(later)
	require.Equal(t, int32(1), brk.recordCalls, "渠道熔断应被记结果一次")
	require.True(t, brk.Allow(1, later), "探测成功→渠道闭合")
	require.Equal(t, []struct {
		ID uint64
		OK bool
	}{{9, true}}, proxyCalls, "代理应记成功一次")
}
