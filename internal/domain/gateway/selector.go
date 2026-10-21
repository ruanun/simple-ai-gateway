package gateway

import (
	"sync"
	"sync/atomic"
	"time"

	"github.com/ruanun/simple-ai-gateway/internal/accountpick"
	"github.com/ruanun/simple-ai-gateway/internal/breaker"
	"github.com/ruanun/simple-ai-gateway/internal/crypto"
	"github.com/ruanun/simple-ai-gateway/internal/domain/account"
	"github.com/ruanun/simple-ai-gateway/internal/domain/channel"
	"github.com/ruanun/simple-ai-gateway/internal/model"
	"github.com/ruanun/simple-ai-gateway/internal/session"
)

// AccountSelector 把"渠道→选账号→ChannelInfo"封装为 Deps.Lookup，
// 并提供账号级熔断回调 RecordResult。账号级 breaker 独立于渠道级 breaker。
// 线程安全：breaker 内部加锁、counter 用 atomic、rng 每次新建。
// 数据访问统一走 channel/account 域 service，不直接持有 *gorm.DB。
type AccountSelector struct {
	channels *channel.Service
	aesKey   string
	acc      *account.Service
	brk      breaker.Breaker // 账号级熔断（独立于渠道熔断）
	keyed    breaker.Keyed   // 账号×模型配额冷却（P12a），可为 nil
	now      func() time.Time
	counter  uint64              // rng 种子源（atomic 自增），只影响选择随机性，不影响正确性
	sticky   session.StickyStore // 会话粘连映射（可为 nil=不启用）

	// recoverFactor 半开账号软探测系数：以 (1-recoverFactor) 概率跳过半开账号，恢复期少打流量；
	// 1 等价不跳过（旧行为）。透传给 accountpick.Pick。
	recoverFactor float64

	// —— 渠道/账号短 TTL 缓存（cacheTTL<=0 禁用）——
	// 每次派发尝试都查渠道+账号列表，带重试的请求会放大数倍 DB 往返；
	// 2s 级 TTL 把它压到每渠道每 2s 一次。代价：管理端禁用渠道/改账号在
	// 最长 TTL 内对派发不可见（与路由表 Pub/Sub 异步重建的时延同量级，可接受）。
	// customHeaders 在缓存填充时解析一次，随条目复用。
	cacheTTL time.Duration
	cacheMu  sync.RWMutex
	chCache  map[uint64]ttlEntry[chanVal]
	accCache map[uint64]ttlEntry[[]model.Account]
}

// chanVal 渠道缓存值：渠道行 + 已解析的 customHeaders（解析一次随条目复用）。
type chanVal struct {
	ch   *model.Channel
	hdrs map[string]string
}

// ttlEntry TTL 缓存条目（cachedGet 用）。
type ttlEntry[V any] struct {
	v  V
	at time.Time
}

// cachedGet 加锁查 TTL 缓存，未命中/过期回源并写回；ttl<=0 直接回源（缓存禁用，测试强一致）。
// 命中路径用读锁：派发热路径（每候选/每重试各一次）里不同 key 的命中互不串行。
// 并发未命中可能重复回源、末写胜出——与抽取前「检查与回源之间解锁」的窗口语义一致。
// key 泛化为 comparable：渠道/账号缓存用 uint64、代理组缓存（proxyresolver.go）用组名 string。
func cachedGet[K comparable, V any](mu *sync.RWMutex, cache map[K]ttlEntry[V], ttl time.Duration, now time.Time, key K, fetch func() (V, error)) (V, error) {
	if ttl <= 0 {
		return fetch()
	}
	mu.RLock()
	e, ok := cache[key]
	mu.RUnlock()
	if ok && now.Sub(e.at) < ttl {
		return e.v, nil
	}
	v, err := fetch()
	if err != nil {
		var zero V
		return zero, err
	}
	mu.Lock()
	cache[key] = ttlEntry[V]{v: v, at: now}
	mu.Unlock()
	return v, nil
}

// DefaultLookupCacheTTL 生产装配建议值（server.NewWithShutdown 使用）。
const DefaultLookupCacheTTL = 2 * time.Second

// SetCacheTTL 开启渠道/账号查询缓存；d<=0 禁用（默认禁用，测试环境保持强一致）。
// 仅限装配期单线程调用一次；服务运行中调用会与并发读缓存产生数据竞争。
func (s *AccountSelector) SetCacheTTL(d time.Duration) {
	s.cacheTTL = d
	s.chCache = map[uint64]ttlEntry[chanVal]{}
	s.accCache = map[uint64]ttlEntry[[]model.Account]{}
}

// cachedChannel 取渠道（含已解析 customHeaders）；缓存未启用/过期/未命中时回源并填充。
func (s *AccountSelector) cachedChannel(channelID uint64) (*model.Channel, map[string]string, error) {
	v, err := cachedGet(&s.cacheMu, s.chCache, s.cacheTTL, s.now(), channelID, func() (chanVal, error) {
		ch, err := s.channels.Get(channelID)
		if err != nil {
			return chanVal{}, err
		}
		return chanVal{ch: ch, hdrs: channel.ParseCustomHeaders(channelID, ch.CustomHeaders)}, nil
	})
	if err != nil {
		return nil, nil, err
	}
	return v.ch, v.hdrs, nil
}

// cachedAccounts 取渠道启用账号列表；缓存策略同 cachedChannel。
func (s *AccountSelector) cachedAccounts(channelID uint64) ([]model.Account, error) {
	return cachedGet(&s.cacheMu, s.accCache, s.cacheTTL, s.now(), channelID, func() ([]model.Account, error) {
		return s.acc.ListEnabledByChannel(channelID)
	})
}

// NewAccountSelector 构造账号选择器。now 为 nil 时用 time.Now。
// sticky 为 nil 时不启用会话粘连。keyed 为 nil 时不启用账号×模型配额冷却。
// recoverFactor 见字段注释，透传给 accountpick.Pick。
func NewAccountSelector(channels *channel.Service, aesKey string, acc *account.Service, accBreaker breaker.Breaker, keyed breaker.Keyed, now func() time.Time, sticky session.StickyStore, recoverFactor float64) *AccountSelector {
	if now == nil {
		now = time.Now
	}
	return &AccountSelector{channels: channels, aesKey: aesKey, acc: acc, brk: accBreaker, keyed: keyed, now: now, sticky: sticky, recoverFactor: recoverFactor}
}

// Lookup 实现 Deps.Lookup：查渠道→取启用账号→选一个未熔断账号→ChannelInfo(带 AccountID)。
// 任一环节失败（渠道不存在/无启用账号/全熔断/解密失败）返回 (zero, false)，
// 由 Dispatch 跳到下一渠道候选。
func (s *AccountSelector) Lookup(channelID uint64, modelName string) (ChannelInfo, bool) {
	return s.lookup(channelID, modelName, nil)
}

// LookupExcluding 实现 Deps.LookupExcluding：在 Lookup 基础上跳过 exclude 集中的账号，
// 避免渠道内重试时重选回刚失败的账号。exclude 为 nil 时等价于 Lookup。
func (s *AccountSelector) LookupExcluding(channelID uint64, modelName string, exclude map[uint64]bool) (ChannelInfo, bool) {
	return s.lookup(channelID, modelName, exclude)
}

// lookup 选账号核心实现：exclude 集中的账号在 Pick 阶段被跳过。
func (s *AccountSelector) lookup(channelID uint64, modelName string, exclude map[uint64]bool) (ChannelInfo, bool) {
	ch, customHdr, err := s.cachedChannel(channelID)
	if err != nil {
		return ChannelInfo{}, false
	}
	if ch.Status != 1 {
		return ChannelInfo{}, false
	}
	accounts, err := s.cachedAccounts(channelID)
	if err != nil || len(accounts) == 0 {
		return ChannelInfo{}, false
	}
	accounts = schedulableAccounts(accounts, s.now())
	if len(accounts) == 0 {
		return ChannelInfo{}, false
	}
	seed := int(atomic.AddUint64(&s.counter, 1))
	rng := newRng(seed)
	picked := accountpick.Pick(accounts, s.brk, s.keyed, modelName, s.now(), rng, exclude, s.recoverFactor)
	if picked == nil {
		return ChannelInfo{}, false
	}
	key, err := crypto.Decrypt(s.aesKey, picked.APIKeyEnc)
	if err != nil {
		return ChannelInfo{}, false
	}
	return buildChannelInfo(ch, picked, key, customHdr), true
}

// buildChannelInfo 渠道行 + 选中账号 → ChannelInfo（lookup / lookupForAccount 共用尾段）：
// base_url 账号覆盖渠道、协议空回退 openai、倍率 <=0 兜底 1。
func buildChannelInfo(ch *model.Channel, acc *model.Account, key string, customHdr map[string]string) ChannelInfo {
	// base_url 解析链：账号覆盖 → 渠道。
	baseURL := ch.BaseURL
	if acc.BaseURL != "" {
		baseURL = acc.BaseURL
	}
	// type 直接用 Channel.Type
	provType := ch.Type
	if provType == "" {
		provType = "openai"
	}
	mult := ch.RateMultiplier
	if mult <= 0 {
		mult = 1
	}
	return ChannelInfo{
		BaseURL:               baseURL,
		UpstreamKey:           key,
		Timeout:               ch.Timeout,
		MaxRetries:            ch.MaxRetries,
		RPMLimit:              ch.RPMLimit,
		AccountID:             acc.ID,
		AccountRPMLimit:       acc.RPMLimit,
		AccountMaxConcurrency: acc.MaxConcurrency,
		ProxyGroup:            acc.ProxyGroup, // 账号绑定的代理组带进派发链路（P6a）
		RateMultiplier:        mult,
		ProviderType:          provType,
		AuthStyle:             ch.AuthStyle,
		CustomHeaders:         customHdr,
	}
}

// LookupWithSession 带会话粘连的 Lookup：优先复用粘连账号，不健康则逃逸重选。
func (s *AccountSelector) LookupWithSession(sessionID string, channelID uint64, modelName string) (ChannelInfo, bool) {
	return s.lookupWithSession(sessionID, channelID, modelName, nil)
}

// LookupExcludingWithSession 带会话粘连 + 排除集。
func (s *AccountSelector) LookupExcludingWithSession(sessionID string, channelID uint64, modelName string, exclude map[uint64]bool) (ChannelInfo, bool) {
	return s.lookupWithSession(sessionID, channelID, modelName, exclude)
}

// lookupWithSession 粘连路由核心：先查 sticky map，命中且健康→直接用（刷新 TTL）；
// 不健康→删映射+正常选；未命中→正常选后写入 map。
func (s *AccountSelector) lookupWithSession(sessionID string, channelID uint64, modelName string, exclude map[uint64]bool) (ChannelInfo, bool) {
	// 尝试粘连
	if sessionID != "" && s.sticky != nil {
		if accID, ok := s.sticky.Get(sessionID, channelID); ok {
			if exclude == nil || !exclude[accID] {
				info, ok := s.lookupForAccount(channelID, accID, modelName)
				if ok {
					// 刷新 TTL
					s.sticky.Set(sessionID, channelID, accID)
					return info, true
				}
			}
			// 粘连账号不健康或被排除 → 逃逸
			s.sticky.Delete(sessionID, channelID)
		}
	}
	// 正常选择
	info, ok := s.lookup(channelID, modelName, exclude)
	if ok && sessionID != "" && s.sticky != nil && info.AccountID != 0 {
		s.sticky.Set(sessionID, channelID, info.AccountID)
	}
	return info, ok
}

// lookupForAccount 尝试用指定账号构造 ChannelInfo（粘连复用）；账号不可用返回 false。
func (s *AccountSelector) lookupForAccount(channelID, accountID uint64, modelName string) (ChannelInfo, bool) {
	ch, customHdr, err := s.cachedChannel(channelID)
	if err != nil {
		return ChannelInfo{}, false
	}
	if ch.Status != 1 {
		return ChannelInfo{}, false
	}
	accounts, err := s.cachedAccounts(channelID)
	if err != nil {
		return ChannelInfo{}, false
	}
	var acc *model.Account
	for i := range accounts {
		if accounts[i].ID == accountID {
			acc = &accounts[i]
			break
		}
	}
	if acc == nil {
		return ChannelInfo{}, false
	}
	if !accountSchedulableAt(acc, s.now()) {
		return ChannelInfo{}, false
	}
	// 检查熔断
	if !s.brk.Allow(accountID, s.now()) {
		return ChannelInfo{}, false
	}
	// 账号×模型配额冷却检查（粘连账号对该模型冷却 → 逃逸重选）。
	if s.keyed != nil && modelName != "" && !s.keyed.Allow(breaker.AccountModelKey(accountID, modelName), s.now()) {
		return ChannelInfo{}, false
	}
	key, err := crypto.Decrypt(s.aesKey, acc.APIKeyEnc)
	if err != nil {
		return ChannelInfo{}, false
	}
	return buildChannelInfo(ch, acc, key, customHdr), true
}

// RecordResult 账号级熔断回调 + 账号健康字段回写（M4/P12a）。accountID==0 跳过。
// 成功：清账号熔断 + 清该账号×模型退避 + 回写 last_used_at 并清零 fail_count
// （60s 窗口节流与失败后立即回写的配对语义收敛在 account.Service.TouchUsed/IncrFailCount 内）；
// 配额失败(quotaHit，429/403)：记账号×模型指数退避冷却，不再立即熔断整个账号；
// retryAfter 为上游 Retry-After 头解析出的建议等待时长（无则传 0），冷却时长取
// max(指数退避, retryAfter)，尊重服务端明确给出的等待建议（坑d）。
// 其他失败：计入账号级滑窗熔断 + fail_count 自增（每次即写）。
// 健康字段 UPDATE 容错（错误忽略），不阻断主链路。
func (s *AccountSelector) RecordResult(accountID uint64, modelName string, ok bool, quotaHit bool, retryAfter time.Duration) {
	if accountID == 0 {
		return
	}
	now := s.now()
	if ok {
		s.brk.RecordResult(accountID, now, true, false)
		if s.keyed != nil && modelName != "" {
			s.keyed.RecordSuccess(breaker.AccountModelKey(accountID, modelName))
		}
		_ = s.acc.TouchUsed(accountID, now) // 健康字段回写容错，不阻断主链路（60s 节流在 Service 内）
		return
	}
	if quotaHit {
		if s.keyed != nil && modelName != "" {
			s.keyed.RecordQuotaFailure(breaker.AccountModelKey(accountID, modelName), now, retryAfter)
		} else {
			s.brk.RecordResult(accountID, now, false, true) // 未装配 keyed 时退回旧行为：账号级立即冷却
		}
	} else {
		s.brk.RecordResult(accountID, now, false, false)
	}
	_ = s.acc.IncrFailCount(accountID) // 健康字段回写容错，不阻断主链路
}

// schedulableAccounts 过滤掉过期/临时不可调度/限流冷却/过载冷却的账号。
// 返回新切片，不修改缓存中的原切片。
func schedulableAccounts(accounts []model.Account, now time.Time) []model.Account {
	out := make([]model.Account, 0, len(accounts))
	for i := range accounts {
		if accountSchedulableAt(&accounts[i], now) {
			out = append(out, accounts[i])
		}
	}
	return out
}

// accountSchedulableAt 账号当前是否可参与调度。
// 时间窗口统一用 now < until 判定为不可用；等于 until 即视为已到期可重新调度。
func accountSchedulableAt(a *model.Account, now time.Time) bool {
	if a == nil || a.Status != 1 {
		return false
	}
	if a.ExpiresAt != nil && !now.Before(*a.ExpiresAt) {
		return false
	}
	if a.TempUnschedulableUntil != nil && now.Before(*a.TempUnschedulableUntil) {
		return false
	}
	if a.RateLimitResetAt != nil && now.Before(*a.RateLimitResetAt) {
		return false
	}
	if a.OverloadUntil != nil && now.Before(*a.OverloadUntil) {
		return false
	}
	return true
}

// newRng 简单可复现 rng（与 accountpick 测试同构）。
func newRng(seed int) func(n int) int {
	st := int64(seed*2654435761 + 1)
	return func(n int) int {
		if n <= 0 {
			return 0
		}
		st = (st*1103515245 + 12345) & 0x7fffffff
		return int(st) % n
	}
}
