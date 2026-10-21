package gateway

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/ruanun/simple-ai-gateway/internal/billing"
	"github.com/ruanun/simple-ai-gateway/internal/domain/apikey"
	"github.com/ruanun/simple-ai-gateway/internal/domain/app"
)

// CtxBillingReservation 在 gin.Context 上挂载本次请求的在途额度预留句柄。
const CtxBillingReservation = "billing_reservation"

// 预留估算上限：把「未知输出」封在可预期范围内，避免 max_tokens 填 1e12 或恶意超大 body
// 让预留额无限膨胀；超限后仍按上限预留，真实成本高出的部分由 Settle 补差。
const (
	reserveDefaultOutputTokens = 4096
	reserveMaxOutputTokens     = 65536
	reserveMaxInputTokens      = 1_000_000
	reserveMaxChoices          = 8
	reserveMaxCostMicros       = int64(1_000_000_000_000) // $1,000,000 上限，防数值溢出
)

// BillingReservation 一次请求的在途额度预留：请求前把「最大估算成本」计入 App 已用额度与
// key 当日成本，响应后按实际成本多退少补，失败/提前返回时全额释放。
//
// 与 sub2api 的 inflight reservation 相比是精简版：不依赖 Redis、不做 TTL/续期，靠
// 请求收尾 + middleware cleanup 保证释放；多实例下由 DB 条件更新保证原子性。
type BillingReservation struct {
	apps *app.Service
	keys *apikey.Service

	appID, keyID         uint64
	appAmount, keyAmount int64
	at                   time.Time

	mu      sync.Mutex
	settled bool
}

// Settle 按实际成本结算：把预留额调整为 actual（差额可正可负），幂等。
func (r *BillingReservation) Settle(actual int64) error {
	if r == nil {
		return nil
	}
	r.mu.Lock()
	if r.settled {
		r.mu.Unlock()
		return nil
	}
	r.settled = true
	r.mu.Unlock()

	var errs []error
	if r.appID != 0 && r.apps != nil {
		if delta := actual - r.appAmount; delta != 0 {
			if err := r.apps.AdjustUsed(r.appID, delta); err != nil {
				errs = append(errs, err)
			}
		}
	}
	if r.keyID != 0 && r.keys != nil {
		if delta := actual - r.keyAmount; delta != 0 {
			if err := r.keys.AdjustDailyCost(r.keyID, delta, r.at); err != nil {
				errs = append(errs, err)
			}
		}
	}
	return errors.Join(errs...)
}

// Release 全额释放预留（失败/提前返回路径），幂等；已 Settle 时为 no-op。
func (r *BillingReservation) Release() {
	if r == nil {
		return
	}
	if err := r.Settle(0); err != nil {
		slog.Error("释放计费预留失败", "app", r.appID, "key", r.keyID, "err", err)
	}
}

func reservationFromCtx(c *gin.Context) *BillingReservation {
	if c == nil {
		return nil
	}
	v, ok := c.Get(CtxBillingReservation)
	if !ok {
		return nil
	}
	r, _ := v.(*BillingReservation)
	return r
}

// reserve 请求前额度预留：估算候选中的最大成本，分别占 App 配额与 key 日成本。
// 返回 (预留句柄, HTTP 状态码, 错误文案)；状态码 0 表示成功。
func (b *BillingCtx) reserve(p prepared, raw []byte) (*BillingReservation, int, string) {
	if b.Apps == nil || b.Keys == nil {
		return nil, 0, "" // 测试/简化装配：无计费依赖时不做预留
	}
	amount := b.estimateMaxCost(p, raw)
	if amount <= 0 {
		return nil, 0, ""
	}
	r := &BillingReservation{
		apps: b.Apps, keys: b.Keys,
		appID: p.AppID, keyID: p.Kid,
		at: b.now(),
	}
	if p.AppID != 0 {
		reserved, err := b.Apps.Reserve(p.AppID, amount)
		if err != nil {
			if errors.Is(err, app.ErrQuotaExceeded) {
				return nil, http.StatusPaymentRequired, "app quota exhausted"
			}
			slog.Error("App 配额预留失败", "app", p.AppID, "amount", amount, "err", err)
			return nil, http.StatusInternalServerError, "quota reserve failed"
		}
		r.appAmount = reserved
	}
	if p.Kid != 0 && p.KeyDailyLimit > 0 {
		reserved, err := b.Keys.ReserveDaily(p.Kid, amount, r.at)
		if err != nil {
			_ = r.apps.AdjustUsed(r.appID, -r.appAmount) // 回滚已占的 App 额度
			if errors.Is(err, apikey.ErrDailyCostExceeded) {
				return nil, http.StatusPaymentRequired, "daily cost limit exceeded"
			}
			slog.Error("key 日成本预留失败", "key", p.Kid, "amount", amount, "err", err)
			return nil, http.StatusInternalServerError, "daily cost reserve failed"
		}
		r.keyAmount = reserved
	}
	if r.appAmount == 0 && r.keyAmount == 0 {
		return nil, 0, "" // App 不限额度且 key 无日限额：走原有后扣路径
	}
	return r, 0, ""
}

// estimateMaxCost 用请求体可推断的输入/输出上限 × 候选最高价估算本次最大成本。
// 估算偏差由 Settle 多退少补；这里的目标是让并发请求在临界点不会同时穿透配额。
func (b *BillingCtx) estimateMaxCost(p prepared, raw []byte) int64 {
	if b.Prices == nil || len(p.Cands) == 0 {
		return 0
	}
	inputTokens := estimateInputTokens(raw)
	outputTokens := estimateOutputTokens(p.in.obj)
	choices := estimateChoices(p.in.obj)
	var maxCost int64
	for _, c := range p.Cands {
		pr, ok := b.Prices.Resolve(c.ChannelID, c.Model)
		if !ok {
			continue
		}
		var cost int64
		if pr.Type == "times" {
			cost = billing.ComputeTimes(pr) * int64(choices)
		} else {
			cost = billing.Compute(billing.Usage{PromptTokens: inputTokens, CompletionTokens: outputTokens}, pr)
			if choices > 1 {
				cost *= int64(choices)
			}
		}
		if cost > maxCost {
			maxCost = cost
		}
	}
	if maxCost <= 0 {
		return 0
	}
	// 20% 余量覆盖流式 usage 尾部/多模态估算偏差。
	maxCost = maxCost * 6 / 5
	if maxCost > reserveMaxCostMicros {
		maxCost = reserveMaxCostMicros
	}
	return maxCost
}

func estimateInputTokens(raw []byte) int {
	n := len(raw) / 3
	if n < 1 {
		n = 1
	}
	if n > reserveMaxInputTokens {
		n = reserveMaxInputTokens
	}
	return n
}

func estimateOutputTokens(obj map[string]any) int {
	for _, key := range []string{"max_tokens", "max_completion_tokens", "max_output_tokens"} {
		if v, ok := numberAsInt(obj[key]); ok && v > 0 {
			if v > reserveMaxOutputTokens {
				v = reserveMaxOutputTokens
			}
			return v
		}
	}
	return reserveDefaultOutputTokens
}

func estimateChoices(obj map[string]any) int {
	if v, ok := numberAsInt(obj["n"]); ok && v > 1 {
		if v > reserveMaxChoices {
			v = reserveMaxChoices
		}
		return v
	}
	return 1
}

func numberAsInt(v any) (int, bool) {
	switch n := v.(type) {
	case json.Number:
		i, err := n.Int64()
		if err != nil {
			return 0, false
		}
		return int(i), true
	case float64:
		return int(n), true
	case int:
		return n, true
	case int64:
		return int(n), true
	default:
		return 0, false
	}
}
