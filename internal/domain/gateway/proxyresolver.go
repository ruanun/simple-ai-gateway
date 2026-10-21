package gateway

import (
	"net/http"
	"sync"
	"sync/atomic"
	"time"

	"github.com/ruanun/simple-ai-gateway/internal/breaker"
	"github.com/ruanun/simple-ai-gateway/internal/domain/proxy"
	"github.com/ruanun/simple-ai-gateway/internal/model"
	"github.com/ruanun/simple-ai-gateway/internal/proxydial"
	"github.com/ruanun/simple-ai-gateway/internal/proxypick"
)

// ProxyResolver 按代理组选代理并构造带代理的 http.Client；提供代理级熔断回调。
// 代理级 breaker 独立于渠道级/账号级 breaker。
// 数据访问统一走 proxy 域 service（查询 + 健康字段回写），不直接持有 *gorm.DB。
type ProxyResolver struct {
	proxies *proxy.Service
	brk     breaker.Breaker
	now     func() time.Time
	counter uint64 // rng 种子源（atomic 自增），只影响选择随机性，不影响正确性

	// recoverFactor 半开代理软探测系数：以 (1-recoverFactor) 概率跳过半开代理，恢复期少打流量；
	// 1 等价不跳过（旧行为）。透传给 proxypick.Pick。
	recoverFactor float64

	// —— 代理组列表短 TTL 缓存（cacheTTL<=0 禁用），与 selector.go 渠道/账号缓存同口径 ——
	// 每次派发尝试（含重试）都按组查一次启用代理列表，2s 级 TTL 把它压到每组每 2s 一次。
	// 代价同渠道/账号缓存：管理端增删/停用代理在最长 TTL(2s) 内对派发不可见（运维知会见 roadmap）。
	cacheTTL time.Duration
	cacheMu  sync.RWMutex
	grpCache map[string]ttlEntry[[]model.Proxy]
}

// NewProxyResolver 构造代理解析器。now 为 nil 时用 time.Now。
// recoverFactor 见字段注释，透传给 proxypick.Pick。
func NewProxyResolver(p *proxy.Service, brk breaker.Breaker, now func() time.Time, recoverFactor float64) *ProxyResolver {
	if now == nil {
		now = time.Now
	}
	return &ProxyResolver{proxies: p, brk: brk, now: now, recoverFactor: recoverFactor}
}

// SetCacheTTL 开启代理组列表查询缓存；d<=0 禁用（默认禁用，测试环境保持强一致）。
// 仅限装配期单线程调用一次；服务运行中调用会与并发读缓存产生数据竞争。
func (r *ProxyResolver) SetCacheTTL(d time.Duration) {
	r.cacheTTL = d
	r.grpCache = map[string]ttlEntry[[]model.Proxy]{}
}

// cachedProxies 取组内启用代理列表；缓存未启用/过期/未命中时回源并填充（策略同 selector.cachedAccounts）。
func (r *ProxyResolver) cachedProxies(group string) ([]model.Proxy, error) {
	return cachedGet(&r.cacheMu, r.grpCache, r.cacheTTL, r.now(), group, func() ([]model.Proxy, error) {
		return r.proxies.ListEnabledByGroup(group)
	})
}

// ClientFor 返回 (client, proxyID, ok)：
//   - group=="": 直连 client，proxyID=0，ok=true。
//   - group!="" 且组内选到可用代理: 带代理 client，proxyID>0，ok=true。
//   - group!="" 但组内无可用代理（无启用/全熔断/构造失败）: nil,0,false（调用方当作本次账号失败）。
func (r *ProxyResolver) ClientFor(group string, timeoutSec int, stream bool) (*http.Client, uint64, bool) {
	if group == "" {
		cli, _ := proxydial.BuildClient(nil, timeoutSec, stream)
		return cli, 0, true
	}
	ps, err := r.cachedProxies(group)
	if err != nil || len(ps) == 0 {
		return nil, 0, false
	}
	seed := int(atomic.AddUint64(&r.counter, 1))
	picked := proxypick.Pick(ps, r.brk, r.now(), newRng(seed), r.recoverFactor)
	if picked == nil {
		return nil, 0, false
	}
	cli, err := proxydial.BuildClient(picked, timeoutSec, stream)
	if err != nil {
		return nil, 0, false
	}
	return cli, picked.ID, true
}

// RecordResult 代理级熔断回调 + 健康回写。proxyID==0（直连/未选代理）跳过。
// 成功清熔断 + 更新 last_used_at/清零 fail_count（60s 窗口节流与失败后立即回写的
// 配对语义收敛在 proxy.Service.TouchUsed/IncrFailCount 内）；
// 失败计入熔断（is429 立即熔断）+ fail_count+1（每次即写）。
// 健康回写的 DB 错误忽略（不阻断派发主链路）。
func (r *ProxyResolver) RecordResult(proxyID uint64, ok bool, is429 bool) {
	if proxyID == 0 {
		return
	}
	now := r.now()
	if ok {
		r.brk.RecordResult(proxyID, now, true, false)
		_ = r.proxies.TouchUsed(proxyID, now) // 健康字段回写容错，不阻断主链路（60s 节流在 Service 内）
		return
	}
	r.brk.RecordResult(proxyID, now, false, is429)
	_ = r.proxies.IncrFailCount(proxyID) // 健康字段回写容错，不阻断主链路
}
