package admin

import (
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/ruanun/simple-ai-gateway/internal/breaker"
	"github.com/ruanun/simple-ai-gateway/internal/domain/account"
	"github.com/ruanun/simple-ai-gateway/internal/domain/channel"
	"github.com/ruanun/simple-ai-gateway/internal/domain/proxy"
	"github.com/ruanun/simple-ai-gateway/internal/httpx"
)

// BreakerHandler 熔断状态查询与手动重置（渠道 + 账号级 + 账号×模型配额冷却 + 代理级）。
// 多实例语义：内存模式仅反映/作用于当前实例；Redis 模式全局共享。
type BreakerHandler struct {
	chBrk    breaker.Breaker
	accBrk   breaker.Breaker
	accModel breaker.Keyed // 账号×模型配额冷却（429/403），可为 nil
	channels *channel.Service
	accounts *account.Service
	proxyBrk breaker.Breaker
	proxies  *proxy.Service
	now      func() time.Time
}

func NewBreakerHandler(chBrk, accBrk breaker.Breaker, accModel breaker.Keyed, channels *channel.Service, accounts *account.Service, proxyBrk breaker.Breaker, proxies *proxy.Service, now func() time.Time) *BreakerHandler {
	return &BreakerHandler{chBrk: chBrk, accBrk: accBrk, accModel: accModel, channels: channels, accounts: accounts, proxyBrk: proxyBrk, proxies: proxies, now: now}
}

type chBreakerItem struct {
	ID         uint64 `json:"id"`
	State      string `json:"state"`
	OpenUntil  int64  `json:"open_until"`
	ProbeLevel int    `json:"probe_level"`
}

type accBreakerItem struct {
	ID         uint64 `json:"id"`
	ChannelID  uint64 `json:"channel_id"`
	State      string `json:"state"`
	OpenUntil  int64  `json:"open_until"`
	ProbeLevel int    `json:"probe_level"`
}

// quotaCoolItem 账号×模型配额冷却项（429/403 触发的 keyed 熔断，与账号失败率熔断相互独立）。
type quotaCoolItem struct {
	AccountID uint64 `json:"account_id"`
	ChannelID uint64 `json:"channel_id"`
	Model     string `json:"model"`
	OpenUntil int64  `json:"open_until"`
}

// Status 返回当前处于熔断/半开的渠道与账号（闭合项不返回）。
func (h *BreakerHandler) Status(c *gin.Context) {
	now := h.now()
	chans, err := h.channels.List()
	if err != nil {
		slog.Error("熔断状态查询渠道列表失败", "err", err)
		httpx.Internal(c, "查询渠道失败")
		return
	}
	chIDs := make([]uint64, len(chans))
	for i, ch := range chans {
		chIDs[i] = ch.ID
	}
	accs, err := h.accounts.ListAll()
	if err != nil {
		slog.Error("熔断状态查询账号列表失败", "err", err)
		httpx.Internal(c, "查询账号失败")
		return
	}
	accIDs := make([]uint64, len(accs))
	accChan := make(map[uint64]uint64, len(accs))
	for i, a := range accs {
		accIDs[i] = a.ID
		accChan[a.ID] = a.ChannelID
	}
	chSnap := h.chBrk.Snapshot(chIDs, now)
	accSnap := h.accBrk.Snapshot(accIDs, now)
	chOut := make([]chBreakerItem, 0, len(chSnap))
	for id, s := range chSnap {
		chOut = append(chOut, chBreakerItem{ID: id, State: s.State, OpenUntil: s.OpenUntil, ProbeLevel: s.ProbeLevel})
	}
	accOut := make([]accBreakerItem, 0, len(accSnap))
	for id, s := range accSnap {
		accOut = append(accOut, accBreakerItem{ID: id, ChannelID: accChan[id], State: s.State, OpenUntil: s.OpenUntil, ProbeLevel: s.ProbeLevel})
	}
	// 账号×模型配额冷却（429/403）：与账号失败率熔断独立，此前未接入导致状态页漏报。
	quotaOut := make([]quotaCoolItem, 0)
	if h.accModel != nil {
		for key, until := range h.accModel.SnapshotPrefix("am:", now) {
			// 键格式 am:<accountID>:<model>（model 可能含冒号，SplitN 限 3 段）
			parts := strings.SplitN(key, ":", 3)
			if len(parts) != 3 {
				continue
			}
			accID, err := strconv.ParseUint(parts[1], 10, 64)
			if err != nil {
				continue
			}
			quotaOut = append(quotaOut, quotaCoolItem{AccountID: accID, ChannelID: accChan[accID], Model: parts[2], OpenUntil: until})
		}
	}
	// 代理级熔断（与渠道/账号同款快照；此前未接入导致代理熔断状态页不可见）。
	proxyOut := make([]chBreakerItem, 0)
	if h.proxies != nil && h.proxyBrk != nil {
		prows, perr := h.proxies.List()
		if perr != nil {
			slog.Error("熔断状态查询代理列表失败", "err", perr)
			httpx.Internal(c, "查询代理失败")
			return
		}
		pIDs := make([]uint64, len(prows))
		for i, pr := range prows {
			pIDs[i] = pr.ID
		}
		for id, s := range h.proxyBrk.Snapshot(pIDs, now) {
			proxyOut = append(proxyOut, chBreakerItem{ID: id, State: s.State, OpenUntil: s.OpenUntil, ProbeLevel: s.ProbeLevel})
		}
	}
	c.JSON(http.StatusOK, gin.H{"channels": chOut, "accounts": accOut, "account_quotas": quotaOut, "proxies": proxyOut})
}

// Reset 重置指定渠道/账号的熔断状态（只清 breaker，不动 DB fail_count）。
func (h *BreakerHandler) Reset(c *gin.Context) {
	var body struct {
		Level string `json:"level"`
		ID    uint64 `json:"id"`
	}
	if err := c.ShouldBindJSON(&body); err != nil || body.ID == 0 {
		httpx.BadRequest(c, "请求参数不合法")
		return
	}
	switch body.Level {
	case "channel":
		h.chBrk.Reset(body.ID)
	case "account":
		h.accBrk.Reset(body.ID)
		// 同步清该账号全部模型的配额冷却（429/403 keyed 熔断）——此前漏清导致
		// "重置账号"对最常见的配额冷却场景完全无效，账号仍被选号排除到冷却自然到期。
		if h.accModel != nil {
			h.accModel.ResetPrefix(breaker.AccountModelPrefix(body.ID))
		}
	case "proxy":
		h.proxyBrk.Reset(body.ID)
	default:
		httpx.BadRequest(c, "level 仅支持 channel/account/proxy")
		return
	}
	c.JSON(http.StatusOK, gin.H{"ok": true})
}
