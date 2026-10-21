// 模型价格表管理接口（含 models.dev 批量拉价 FetchModelsDevPrices 与模糊查价 LookupModelPrice）。
package admin

import (
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/ruanun/simple-ai-gateway/internal/httpx"
	"github.com/ruanun/simple-ai-gateway/internal/model"
)

// ---- ModelPrice（价格表）----
func (h *CRUDHandler) ListPrices(c *gin.Context) {
	var channelID uint64
	if v := c.Query("channel_id"); v != "" {
		channelID, _ = strconv.ParseUint(v, 10, 64)
	}
	rows, err := h.prices.ListByChannel(channelID)
	if err != nil {
		httpx.Internal(c, "查询失败")
		return
	}
	c.JSON(http.StatusOK, rows)
}
func (h *CRUDHandler) UpsertPrice(c *gin.Context) {
	var p model.ModelPrice
	if err := c.ShouldBindJSON(&p); err != nil || p.Model == "" {
		httpx.BadRequest(c, "请求参数不合法")
		return
	}
	if err := h.prices.Upsert(p); err != nil {
		httpx.Internal(c, "保存失败")
		return
	}
	c.JSON(http.StatusOK, gin.H{"ok": true})
}
func (h *CRUDHandler) DeletePrice(c *gin.Context) {
	id, _ := strconv.ParseUint(c.Param("id"), 10, 64)
	if err := h.prices.Delete(id); err != nil {
		httpx.Internal(c, "删除失败")
		return
	}
	c.JSON(http.StatusOK, gin.H{"ok": true})
}

// modelsDevCost models.dev 单模型价格（USD / 1M token）。
type modelsDevCost struct {
	Input      float64 `json:"input"`
	Output     float64 `json:"output"`
	CacheRead  float64 `json:"cache_read"`
	CacheWrite float64 `json:"cache_write"`
}

// modelsDevEntry 扁平化条目：模型 key + 带前缀 ID + 提供商 + 价格（已过滤无价条目）。
type modelsDevEntry struct {
	ModelKey string
	ID       string
	Provider string
	Cost     modelsDevCost
}

// fetchModelsDevEntries 拉取并解析 https://models.dev/api.json 为扁平条目列表，
// 过滤 cost 缺失或 input/output 全零的模型。错误信息可直接透出给管理端
// （FetchModelsDevPrices 与 LookupModelPrice 共用，此前两处逐字重复解析）。
func fetchModelsDevEntries() ([]modelsDevEntry, error) {
	resp, err := (&http.Client{Timeout: 30 * time.Second}).Get("https://models.dev/api.json")
	if err != nil {
		// err 细节只进日志，响应体不透出内部错误（约定：err.Error() 不进响应，此函数被两处 handler 共用，防后人误改）
		slog.Warn("拉取 models.dev 数据失败", "err", err)
		return nil, errors.New("获取 models.dev 数据失败")
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != 200 {
		slog.Warn("models.dev 返回非 200", "status", resp.StatusCode)
		return nil, errors.New("models.dev 返回状态码 " + strconv.Itoa(resp.StatusCode))
	}
	// 解析: { "providerId": { "models": { "modelId": { "cost": {...} } } } }
	var data map[string]json.RawMessage
	if json.Unmarshal(body, &data) != nil {
		slog.Warn("models.dev 响应解析失败")
		return nil, errors.New("models.dev 响应格式不合法")
	}
	var out []modelsDevEntry
	for provKey, provRaw := range data {
		var prov struct {
			Models map[string]struct {
				ID   string         `json:"id"`
				Name string         `json:"name"`
				Cost *modelsDevCost `json:"cost"`
			} `json:"models"`
		}
		if json.Unmarshal(provRaw, &prov) != nil {
			continue
		}
		for modelKey, m := range prov.Models {
			if m.Cost == nil || (m.Cost.Input == 0 && m.Cost.Output == 0) {
				continue
			}
			out = append(out, modelsDevEntry{ModelKey: modelKey, ID: m.ID, Provider: provKey, Cost: *m.Cost})
		}
	}
	return out, nil
}

// FetchModelsDevPrices 从 models.dev 拉取价格数据,匹配已有渠道模型中未定价的,返回建议价格列表。
func (h *CRUDHandler) FetchModelsDevPrices(c *gin.Context) {
	entries, err := fetchModelsDevEntries()
	if err != nil {
		httpx.Err(c, http.StatusBadGateway, "bad_gateway", err.Error())
		return
	}
	// 建 model → cost 的扁平 map(同名模型取第一个有 cost 的)
	priceMap := map[string]modelsDevCost{}
	for _, e := range entries {
		// 用 modelKey 作为匹配名(如 "qwen-plus")
		if _, exists := priceMap[e.ModelKey]; !exists {
			priceMap[e.ModelKey] = e.Cost
		}
		// 也用 ID(可能带 provider 前缀如 "xai/grok-4",取 / 后面的部分)
		if e.ID != "" {
			short := e.ID
			if idx := strings.LastIndex(e.ID, "/"); idx >= 0 {
				short = e.ID[idx+1:]
			}
			if _, exists := priceMap[short]; !exists {
				priceMap[short] = e.Cost
			}
		}
	}
	// 取所有渠道模型名(去重)
	var cms []model.ChannelModel
	h.g.Find(&cms)
	modelNames := map[string]bool{}
	for _, cm := range cms {
		modelNames[cm.Model] = true
	}
	// 取已有全局价格
	existingPrices, _ := h.prices.List()
	existingSet := map[string]bool{}
	for _, p := range existingPrices {
		if p.ChannelID == 0 {
			existingSet[p.Model] = true
		}
	}
	// 匹配:渠道模型 × priceMap,排除已有价格的
	type Suggestion struct {
		Model      string  `json:"model"`
		Input      float64 `json:"input"`
		Output     float64 `json:"output"`
		CacheRead  float64 `json:"cache_read"`
		CacheWrite float64 `json:"cache_write"`
		HasPrice   bool    `json:"has_price"` // 已有价格(供前端标注)
	}
	var suggestions []Suggestion
	for name := range modelNames {
		if cost, ok := priceMap[name]; ok {
			suggestions = append(suggestions, Suggestion{
				Model:      name,
				Input:      cost.Input,
				Output:     cost.Output,
				CacheRead:  cost.CacheRead,
				CacheWrite: cost.CacheWrite,
				HasPrice:   existingSet[name],
			})
		}
	}
	c.JSON(http.StatusOK, gin.H{"suggestions": suggestions, "total_upstream": len(priceMap), "matched": len(suggestions)})
}

// LookupModelPrice 从 models.dev 模糊搜索模型价格，供新增价格弹窗「查询价格」按钮使用。
func (h *CRUDHandler) LookupModelPrice(c *gin.Context) {
	var body struct {
		Query string `json:"query"`
	}
	if err := c.ShouldBindJSON(&body); err != nil || body.Query == "" {
		httpx.BadRequest(c, "查询关键字不能为空")
		return
	}
	query := strings.ToLower(body.Query)
	// 去掉常见前缀（如 MiniMax/、ZHIPU/、kimi/）取斜杠后半段
	cleanQuery := query
	if idx := strings.LastIndex(cleanQuery, "/"); idx >= 0 {
		cleanQuery = cleanQuery[idx+1:]
	}
	// 去掉末尾的日期后缀（6 位数字，如 -260628）
	if len(cleanQuery) > 7 && cleanQuery[len(cleanQuery)-7] == '-' {
		tail := cleanQuery[len(cleanQuery)-6:]
		allDigit := true
		for _, ch := range tail {
			if ch < '0' || ch > '9' {
				allDigit = false
				break
			}
		}
		if allDigit {
			cleanQuery = cleanQuery[:len(cleanQuery)-7]
		}
	}

	entries, err := fetchModelsDevEntries()
	if err != nil {
		httpx.Err(c, http.StatusBadGateway, "bad_gateway", err.Error())
		return
	}
	type Match struct {
		Model      string  `json:"model"`
		Provider   string  `json:"provider"`
		Input      float64 `json:"input"`
		Output     float64 `json:"output"`
		CacheRead  float64 `json:"cache_read"`
		CacheWrite float64 `json:"cache_write"`
	}
	var matches []Match
	seen := map[string]bool{}
	for _, e := range entries {
		lk := strings.ToLower(e.ModelKey)
		// 模糊匹配：cleanQuery 包含在 modelKey 里，或 modelKey 包含 cleanQuery
		if !strings.Contains(lk, cleanQuery) && !strings.Contains(cleanQuery, lk) {
			continue
		}
		key := e.ModelKey + "@" + e.Provider
		if seen[key] {
			continue
		}
		seen[key] = true
		matches = append(matches, Match{
			Model: e.ModelKey, Provider: e.Provider,
			Input: e.Cost.Input, Output: e.Cost.Output, CacheRead: e.Cost.CacheRead,
			CacheWrite: e.Cost.CacheWrite,
		})
	}
	// 限制返回数量
	if len(matches) > 20 {
		matches = matches[:20]
	}
	c.JSON(http.StatusOK, gin.H{"matches": matches})
}
