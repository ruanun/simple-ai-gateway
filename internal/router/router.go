package router

import (
	"math/rand"
	"sort"
	"sync"

	"github.com/ruanun/simple-ai-gateway/internal/model"
	"gorm.io/gorm"
)

// Candidate 一条可用的上游路由候选（渠道 + 真实模型 + 优先级/权重）。
type Candidate struct {
	ChannelID          uint64
	Model              string
	Priority           int
	Weight             int
	ProviderType       string // 渠道协议 = Channel.Type（"openai"默认/空 或 "anthropic"），一渠道一协议
	Outbound           string // 本次出站协议，候选过滤阶段(filterByProtocol/filterWithConvert)填入；空=回退 ProviderType(兼容)
	Inbound            string // 本次入站协议，候选过滤阶段填入；转换派发需知道从哪个协议转(P12c)
	AllowConvert       bool   // 渠道是否允许跨协议转换，Build 时随渠道带出(P12c)
	NeedsConvert       bool   // 本候选需协议转换，候选过滤阶段标记(P12c)
	StripThinking      bool   // 渠道标记不支持 thinking，透传出站剥离 thinking/reasoning_effort(B)
	DisableStreamUsage bool   // 渠道关闭流式 usage 注入（默认注入 include_usage 补计费）
}

// Table 内存路由表：别名/直调两类映射，DB 全量构建 + 热加载整体替换。
type Table struct {
	mu      sync.RWMutex
	aliases map[string][]Candidate
	direct  map[string][]Candidate
	rng     func(n int) int // 可注入；默认 rand.Intn
}

func NewTable() *Table {
	return &Table{aliases: map[string][]Candidate{}, direct: map[string][]Candidate{}, rng: rand.Intn}
}

// Build 从 DB 全量构建（热加载时整体替换内部 map）。
func (t *Table) Build(g *gorm.DB) error {
	// 预查所有渠道的 type + allow_convert（Channel 直接读取，无需 JOIN）
	type chType struct {
		ChannelID          uint64
		ProviderType       string
		AllowConvert       bool
		StripThinking      bool
		DisableStreamUsage bool
	}
	var cts []chType
	if err := g.Model(&model.Channel{}).
		Select("id as channel_id, type as provider_type, allow_convert, strip_thinking, disable_stream_usage").
		Scan(&cts).Error; err != nil {
		return err
	}
	// 渠道 id → 渠道协议属性（一张 map 存整结构体；未知渠道取零值，与原先四张平行 map 的缺省语义一致）。
	chInfo := map[uint64]chType{}
	for _, ct := range cts {
		chInfo[ct.ChannelID] = ct
	}

	// 预查启用渠道 ID 集合，别名路由只保留启用渠道的候选
	enabledCh := map[uint64]bool{}
	var chIDs []uint64
	if err := g.Model(&model.Channel{}).Where("status = ?", 1).Pluck("id", &chIDs).Error; err != nil {
		return err
	}
	for _, id := range chIDs {
		enabledCh[id] = true
	}

	// 预查启用的 (渠道,真实模型) 集合：别名候选必须命中，否则会绕过渠道模型级禁用/删除(#29)——
	// direct 路由靠 JOIN channel_models WHERE enabled=true 天然受约束，别名此前只查渠道启用漏了这层。
	type cmKey struct {
		ch    uint64
		model string
	}
	enabledChModel := map[cmKey]bool{}
	type cmRow struct {
		ChannelID uint64
		Model     string
	}
	var cms []cmRow
	if err := g.Model(&model.ChannelModel{}).Select("channel_id, model").Where("enabled = ?", true).Scan(&cms).Error; err != nil {
		return err
	}
	for _, cm := range cms {
		enabledChModel[cmKey{cm.ChannelID, cm.Model}] = true
	}

	aliases := map[string][]Candidate{}
	var als []model.ModelAlias
	if err := g.Where("status = ?", 1).Find(&als).Error; err != nil {
		return err
	}
	for _, a := range als {
		var routes []model.AliasRoute
		if err := g.Where("alias_id = ?", a.ID).Find(&routes).Error; err != nil {
			return err
		}
		cs := make([]Candidate, 0, len(routes))
		for _, r := range routes {
			if !enabledCh[r.ChannelID] {
				continue
			}
			if !enabledChModel[cmKey{r.ChannelID, r.Model}] {
				continue // 该渠道未启用此真实模型（禁用/删除）→ 别名不得绕过(#29)
			}
			ci := chInfo[r.ChannelID]
			cs = append(cs, Candidate{ChannelID: r.ChannelID, Model: r.Model, Priority: r.Priority, Weight: max1(r.Weight), ProviderType: ci.ProviderType, AllowConvert: ci.AllowConvert, StripThinking: ci.StripThinking, DisableStreamUsage: ci.DisableStreamUsage})
		}
		aliases[a.Alias] = cs
	}

	direct := map[string][]Candidate{}
	type row struct {
		Model              string
		ChannelID          uint64
		Priority           int
		Weight             int
		ProviderType       string
		AllowConvert       bool
		StripThinking      bool
		DisableStreamUsage bool
	}
	var rows []row
	if err := g.Model(&model.ChannelModel{}).
		Select("channel_models.model, channel_models.channel_id, channels.priority, channels.weight, channels.type as provider_type, channels.allow_convert, channels.strip_thinking, channels.disable_stream_usage").
		Joins("JOIN channels ON channels.id = channel_models.channel_id").
		Where("channel_models.enabled = ? AND channels.status = ?", true, 1).
		Scan(&rows).Error; err != nil {
		return err
	}
	for _, r := range rows {
		direct[r.Model] = append(direct[r.Model], Candidate{ChannelID: r.ChannelID, Model: r.Model, Priority: r.Priority, Weight: max1(r.Weight), ProviderType: r.ProviderType, AllowConvert: r.AllowConvert, StripThinking: r.StripThinking, DisableStreamUsage: r.DisableStreamUsage})
	}
	t.mu.Lock()
	t.aliases, t.direct = aliases, direct
	t.mu.Unlock()
	return nil
}

// Resolve 返回按"优先级降序分层、层内加权随机"定序的候选全列表。
func (t *Table) Resolve(model string) []Candidate {
	t.mu.RLock()
	cs, ok := t.aliases[model]
	if !ok {
		cs = t.direct[model]
	}
	t.mu.RUnlock()
	if len(cs) == 0 {
		return nil
	}
	// 复制后分层
	cp := append([]Candidate(nil), cs...)
	prios := map[int][]Candidate{}
	for _, c := range cp {
		prios[c.Priority] = append(prios[c.Priority], c)
	}
	keys := make([]int, 0, len(prios))
	for k := range prios {
		keys = append(keys, k)
	}
	sort.Sort(sort.Reverse(sort.IntSlice(keys)))
	out := make([]Candidate, 0, len(cp))
	for _, k := range keys {
		out = append(out, t.weightedOrder(prios[k])...)
	}
	return out
}

// weightedOrder 在一层内按权重无放回抽样定序。
func (t *Table) weightedOrder(layer []Candidate) []Candidate {
	pool := append([]Candidate(nil), layer...)
	res := make([]Candidate, 0, len(pool))
	for len(pool) > 0 {
		total := 0
		for _, c := range pool {
			total += c.Weight
		}
		pick := t.rng(total)
		idx, acc := 0, 0
		for i, c := range pool {
			acc += c.Weight
			if pick < acc {
				idx = i
				break
			}
		}
		res = append(res, pool[idx])
		pool = append(pool[:idx], pool[idx+1:]...)
	}
	return res
}

func max1(w int) int {
	if w < 1 {
		return 1
	}
	return w
}

// Invalidate 清空路由表，确保 Build 失败后不会继续派发旧路由（fail-closed）。
func (t *Table) Invalidate() {
	t.mu.Lock()
	t.aliases = map[string][]Candidate{}
	t.direct = map[string][]Candidate{}
	t.mu.Unlock()
}

// --- 测试辅助（导出给同包测试用） ---
func (t *Table) SetAliasForTest(alias string, cs []Candidate)  { t.aliases[alias] = cs }
func (t *Table) SetDirectForTest(model string, cs []Candidate) { t.direct[model] = cs }
