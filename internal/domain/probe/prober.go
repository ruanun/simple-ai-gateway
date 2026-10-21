// Package probe 提供主动健康探测：定时对半开(Recovering)渠道发 GET /models，探成功即让熔断器闭合。
// 仅渠道级；与 dispatch 共用同一 channel breaker。补齐低/无流量渠道的自愈。
package probe

import (
	"log/slog"
	"net/http"
	"sync"
	"time"

	"github.com/ruanun/simple-ai-gateway/internal/breaker"
	"github.com/ruanun/simple-ai-gateway/internal/config"
	"github.com/ruanun/simple-ai-gateway/internal/crypto"
	"github.com/ruanun/simple-ai-gateway/internal/domain/channel"
	"github.com/ruanun/simple-ai-gateway/internal/model"
)

// ChannelLister/AccountProvider：探测器只需的最小依赖（便于单测注入）。
type ChannelLister interface {
	List() ([]model.Channel, error)
}
type AccountProvider interface {
	FirstEnabledByChannel(channelID uint64) (*model.Account, error)
}

// Deps 探测器依赖（注入解耦 gateway 重包，便于无 DB/HTTP 单测）。
type Deps struct {
	Breaker  breaker.Breaker
	Channels ChannelLister
	Accounts AccountProvider
	AESKey   string
	// ClientFor 按代理组建 client（直连/代理）；第二返回值为选中代理 ID（0=直连），
	// 供探测失败时区分「代理故障」与「渠道故障」归因（此前该 ID 在装配层被丢弃，
	// 代理挂掉会被误记成渠道熔断失败）。
	ClientFor func(group string, timeoutSec int) (*http.Client, uint64, bool)
	// RecordProxyResult 代理级熔断结果回调（nil 安全跳过；proxyID==0 直连跳过）。
	RecordProxyResult func(proxyID uint64, ok, is429 bool)
	ProbeModels       func(cli *http.Client, baseURL, key, protocol, authStyle string, hdr map[string]string) (int, []byte, error)
}

// Prober 主动探测器。
type Prober struct {
	d      Deps
	cfg    config.ProbeConfig
	now    func() time.Time
	ticker *time.Ticker
	stop   chan struct{}
}

// NewProberStatic 不启动 goroutine，供测试手动调 probeOnce。
func NewProberStatic(d Deps, cfg config.ProbeConfig, now func() time.Time) *Prober {
	if now == nil {
		now = time.Now
	}
	return &Prober{d: d, cfg: cfg, now: now, stop: make(chan struct{})}
}

// NewProber 创建并启动定时探测(每 interval 跑一次)。
func NewProber(d Deps, cfg config.ProbeConfig) *Prober {
	if cfg.Interval <= 0 {
		// time.NewTicker 对 duration<=0 会 panic，非法配置(如 YAML 误填 0s/负数)兜底成默认值，避免拖垮整个网关启动。
		cfg.Interval = 30 * time.Second
	}
	p := &Prober{d: d, cfg: cfg, now: time.Now, ticker: time.NewTicker(cfg.Interval), stop: make(chan struct{})}
	go p.run()
	return p
}

func (p *Prober) run() {
	for {
		select {
		case <-p.ticker.C:
			p.probeOnce(p.now())
		case <-p.stop:
			p.ticker.Stop()
			return
		}
	}
}

// Stop 停止定时探测(优雅退出用)。
func (p *Prober) Stop() { close(p.stop) }

// probeOnce 枚举启用渠道，只探 Recovering 的，errgroup 风格并发(信号量+WaitGroup)封顶 concurrency。
func (p *Prober) probeOnce(now time.Time) {
	chs, err := p.d.Channels.List()
	if err != nil {
		slog.Error("probe: 列渠道失败", "err", err)
		return
	}
	conc := p.cfg.Concurrency
	if conc < 1 {
		conc = 1
	}
	sem := make(chan struct{}, conc)
	var wg sync.WaitGroup
	for _, ch := range chs {
		if ch.Status != 1 {
			continue
		}
		if !p.d.Breaker.Recovering(ch.ID, now) {
			continue // 只探半开(冷却已过)渠道
		}
		wg.Add(1)
		sem <- struct{}{}
		go func(ch model.Channel) {
			defer wg.Done()
			defer func() {
				<-sem
				if r := recover(); r != nil {
					slog.Error("probe: 渠道探测 panic", "channel", ch.ID, "panic", r)
				}
			}()
			p.probeChannel(ch, now)
		}(ch)
	}
	wg.Wait()
}

// probeChannel 探单个半开渠道：先备好账号/凭证/client(备不齐直接跳过、不动熔断) →
// 确认能真正发起探测后才 Allow 触发半开转换 → GET /models → RecordResult(200 闭合/否则重开)。
// 顺序很关键：账号缺失的渠道本来就探不了，不该让它被 Allow 拖进半开态却一直没人 RecordResult 收尾。
// 经代理出站的网络层错误归因给代理熔断（详见函数内注释），不再误记渠道。
func (p *Prober) probeChannel(ch model.Channel, now time.Time) {
	acc, err := p.d.Accounts.FirstEnabledByChannel(ch.ID)
	if err != nil || acc == nil {
		slog.Warn("probe: 渠道无可用账号，跳过", "channel", ch.ID)
		return
	}
	key, err := crypto.Decrypt(p.d.AESKey, acc.APIKeyEnc)
	if err != nil {
		slog.Error("probe: 账号解密失败，跳过", "channel", ch.ID, "account", acc.ID, "err", err)
		return
	}
	timeoutSec := int(p.cfg.Timeout / time.Second)
	if timeoutSec < 1 {
		timeoutSec = 1
	}
	cli, proxyID, ok := p.d.ClientFor(acc.ProxyGroup, timeoutSec)
	if !ok {
		slog.Warn("probe: 代理组无可用代理，跳过", "channel", ch.ID, "proxy_group", acc.ProxyGroup)
		return
	}
	customHdr := channel.ParseCustomHeaders(ch.ID, ch.CustomHeaders)
	p.d.Breaker.Allow(ch.ID, now) // 触发 open→halfOpen(否则 RecordResult 在 stOpen 被忽略)；必须在 RecordResult 之前
	status, _, perr := p.d.ProbeModels(cli, ch.BaseURL, key, ch.Type, ch.AuthStyle, customHdr)
	if perr != nil && proxyID != 0 {
		// 经代理出站的网络层错误无法区分「代理故障」与「渠道不可达」——归因给代理：
		// 记代理熔断失败，渠道熔断不记结果（保持半开，等下轮探测或真实流量裁决）。
		// 原实现把它记成渠道失败会误重开渠道熔断并抬升退避（代理越久不修、渠道惩罚越重）。
		// 渠道不会卡死：半开态 Allow 恒放行、Recovering 恒 true（下轮必重探），
		// 内存/Redis 模式各有惰性过期/TTL 兜底。
		p.recordProxy(proxyID, false, false)
		slog.Warn("probe: 经代理探测网络失败，归因代理，渠道保持半开", "channel", ch.ID, "proxy", proxyID, "err", perr)
		return
	}
	ok200 := perr == nil && status == http.StatusOK
	p.d.Breaker.RecordResult(ch.ID, now, ok200, false)
	// 拿到状态码即代理链路通畅（perr==nil），记代理成功；perr!=nil 走到这里必为直连（proxyID=0，recordProxy 内部跳过）。
	p.recordProxy(proxyID, perr == nil, false)
	if ok200 {
		slog.Info("probe: 探测成功，熔断闭合", "channel", ch.ID, "status", status)
	} else {
		slog.Warn("probe: 探测失败，保持熔断", "channel", ch.ID, "status", status, "err", perr)
	}
}

// recordProxy 安全回调代理级结果：RecordProxyResult 为 nil 或 proxyID==0（直连）时跳过。
func (p *Prober) recordProxy(proxyID uint64, ok, is429 bool) {
	if p.d.RecordProxyResult != nil && proxyID != 0 {
		p.d.RecordProxyResult(proxyID, ok, is429)
	}
}
