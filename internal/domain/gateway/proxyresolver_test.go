package gateway

import (
	"testing"
	"time"

	"github.com/ruanun/simple-ai-gateway/internal/breaker"
	"github.com/ruanun/simple-ai-gateway/internal/dbtest"
	"github.com/ruanun/simple-ai-gateway/internal/domain/proxy"
	"github.com/ruanun/simple-ai-gateway/internal/model"
	"github.com/ruanun/simple-ai-gateway/internal/proxydial"
	"github.com/stretchr/testify/require"
)

// TestProxyResolverEmptyGroupDirect group=="" 时返回直连 client、proxyID=0、ok=true。
func TestProxyResolverEmptyGroupDirect(t *testing.T) {
	g := dbtest.OpenInMemory(t)
	require.NoError(t, g.AutoMigrate(&model.Proxy{}))
	svc := proxy.NewService(proxy.NewRepo(g))
	r := NewProxyResolver(svc, breaker.NewMemoryBreaker(breaker.Params{Window: time.Minute, Cooldown: time.Minute, FailThreshold: 3, ErrorRate: 0.5, MinSamples: 20, ProbeBackoffMax: 8}), nil, 1.0)

	cli, id, ok := r.ClientFor("", 30, false)
	require.True(t, ok)
	require.Zero(t, id)
	require.NotNil(t, cli)
	direct, _ := proxydial.BuildClient(nil, 30, false)
	require.Same(t, direct.Transport, cli.Transport, "直连走带出站地址校验的共享 transport")
	require.NotNil(t, cli.CheckRedirect, "出站须禁止跟随重定向")
}

// TestProxyResolverPicksProxy group 非空且组内有可用代理 → 返回带代理 client + proxyID + ok=true。
func TestProxyResolverPicksProxy(t *testing.T) {
	g := dbtest.OpenInMemory(t)
	require.NoError(t, g.AutoMigrate(&model.Proxy{}))
	g.Exec("DELETE FROM proxies") // -count=N 同进程重跑：清空 shared-cache 内存库残留
	svc := proxy.NewService(proxy.NewRepo(g))
	p, err := svc.Create(model.Proxy{Group: "g1", Name: "p1", Protocol: "http", Host: "127.0.0.1", Port: 1080, Weight: 1, Status: 1})
	require.NoError(t, err)

	r := NewProxyResolver(svc, breaker.NewMemoryBreaker(breaker.Params{Window: time.Minute, Cooldown: time.Minute, FailThreshold: 3, ErrorRate: 0.5, MinSamples: 20, ProbeBackoffMax: 8}), nil, 1.0)
	cli, id, ok := r.ClientFor("g1", 30, false)
	require.True(t, ok)
	require.Equal(t, p.ID, id)
	require.NotNil(t, cli.Transport) // 带代理 transport
}

// TestProxyResolverNoEnabledProxy group 非空但组内无可用代理 → ok=false（调用方当作账号失败）。
func TestProxyResolverNoEnabledProxy(t *testing.T) {
	g := dbtest.OpenInMemory(t)
	require.NoError(t, g.AutoMigrate(&model.Proxy{}))
	g.Exec("DELETE FROM proxies")
	svc := proxy.NewService(proxy.NewRepo(g))
	// 仅一个停用代理。
	_, err := svc.Create(model.Proxy{Group: "g1", Name: "p1", Protocol: "http", Host: "127.0.0.1", Port: 1080, Weight: 1, Status: 0})
	require.NoError(t, err)

	r := NewProxyResolver(svc, breaker.NewMemoryBreaker(breaker.Params{Window: time.Minute, Cooldown: time.Minute, FailThreshold: 3, ErrorRate: 0.5, MinSamples: 20, ProbeBackoffMax: 8}), nil, 1.0)
	cli, id, ok := r.ClientFor("g1", 30, false)
	require.False(t, ok)
	require.Nil(t, cli)
	require.Zero(t, id)

	// 整组缺失同样 ok=false。
	_, _, ok2 := r.ClientFor("missing", 30, false)
	require.False(t, ok2)
}

// TestProxyResolverRecordResultTripsBreaker 代理连续失败应触发代理级熔断，使其后续被 Pick 跳过。
func TestProxyResolverRecordResultTripsBreaker(t *testing.T) {
	g := dbtest.OpenInMemory(t)
	require.NoError(t, g.AutoMigrate(&model.Proxy{}))
	g.Exec("DELETE FROM proxies")
	svc := proxy.NewService(proxy.NewRepo(g))
	p, err := svc.Create(model.Proxy{Group: "g1", Name: "p1", Protocol: "http", Host: "127.0.0.1", Port: 1080, Weight: 1, Status: 1})
	require.NoError(t, err)

	brk := breaker.NewMemoryBreaker(breaker.Params{Window: time.Minute, Cooldown: time.Minute, FailThreshold: 1, ErrorRate: 0.5, MinSamples: 20, ProbeBackoffMax: 8})
	r := NewProxyResolver(svc, brk, func() time.Time { return time.Unix(1_000_000, 0) }, 1.0)

	// 选得到。
	_, _, ok := r.ClientFor("g1", 30, false)
	require.True(t, ok)

	// 代理 429 立即熔断 → 组内唯一代理被跳过 → ok=false。
	r.RecordResult(p.ID, false, true)
	_, _, ok2 := r.ClientFor("g1", 30, false)
	require.False(t, ok2, "唯一代理熔断后应选不到")

	// proxyID=0 安全跳过，不 panic。
	r.RecordResult(0, false, false)
}

// TestProxyResolverRecordResultWritesHealth 健康回写走 proxy 域 service：失败累加 fail_count；成功清零 fail_count 且回写 last_used_at。
func TestProxyResolverRecordResultWritesHealth(t *testing.T) {
	g := dbtest.OpenInMemory(t)
	require.NoError(t, g.AutoMigrate(&model.Proxy{}))
	g.Exec("DELETE FROM proxies")
	svc := proxy.NewService(proxy.NewRepo(g))
	p, err := svc.Create(model.Proxy{Group: "g1", Name: "p1", Protocol: "http", Host: "127.0.0.1", Port: 1080, Weight: 1, Status: 1})
	require.NoError(t, err)

	fixed := time.Unix(1_700_000_000, 0)
	r := NewProxyResolver(svc, breaker.NewMemoryBreaker(breaker.Params{Window: time.Minute, Cooldown: time.Minute, FailThreshold: 3, ErrorRate: 0.5, MinSamples: 20, ProbeBackoffMax: 8}), func() time.Time { return fixed }, 1.0)

	// 失败两次 → fail_count 累加到 2。
	r.RecordResult(p.ID, false, false)
	r.RecordResult(p.ID, false, false)
	var after model.Proxy
	require.NoError(t, g.First(&after, p.ID).Error)
	require.Equal(t, 2, after.FailCount, "失败应累加 fail_count")

	// 成功一次 → fail_count 清零 + last_used_at 非空。
	r.RecordResult(p.ID, true, false)
	var ok model.Proxy
	require.NoError(t, g.First(&ok, p.ID).Error)
	require.Equal(t, 0, ok.FailCount, "成功应清零 fail_count")
	require.NotNil(t, ok.LastUsedAt, "成功应回写 last_used_at")
}
