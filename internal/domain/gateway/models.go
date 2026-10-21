package gateway

import (
	"net/http"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/ruanun/simple-ai-gateway/internal/domain/apikey"
	"github.com/ruanun/simple-ai-gateway/internal/domain/app"
	"github.com/ruanun/simple-ai-gateway/internal/middleware"
	"github.com/ruanun/simple-ai-gateway/internal/model"
	"github.com/ruanun/simple-ai-gateway/internal/router"
	"gorm.io/gorm"
)

// ModelsHandler 处理 GET /v1/models：返回当前 key 可访问的模型清单。
type ModelsHandler struct {
	g     *gorm.DB
	table *router.Table
	keys  *apikey.Service
	apps  *app.Service
}

func NewModelsHandler(g *gorm.DB, table *router.Table, keys *apikey.Service, apps *app.Service) *ModelsHandler {
	return &ModelsHandler{g: g, table: table, keys: keys, apps: apps}
}

// List 返回可访问模型清单（别名 + 启用渠道的真实模型），OpenAI models 格式。
// 按当前 API Key 的白名单/映射和 App 的 allowed_channels 过滤。
// 别名需通过路由表验证确实有可用候选渠道，避免返回无效别名。
// channel_models 需 JOIN channels 过滤掉已禁用渠道的模型，避免返回不可用模型。
func (h *ModelsHandler) List(c *gin.Context) {
	// 取当前 key 信息：复用 APIKeyAuth 已载入的 ctx 记录，避免回表重查同一行
	var key model.APIKey
	if rec := middleware.APIKeyFromCtx(c); rec != nil {
		key = *rec
	}

	// 收集候选模型（/v1/models 为 OpenAI 兼容端点，只列有 OpenAI 候选的模型）
	set := map[string]bool{}
	var aliases []model.ModelAlias
	h.g.Where("status = ?", 1).Find(&aliases)
	for _, a := range aliases {
		if cands := h.table.Resolve(a.Alias); hasOpenAICandidate(cands) {
			set[a.Alias] = true
		}
	}
	var cms []model.ChannelModel
	// openai 型渠道直接可服务；allow_convert=true 的异协议渠道可经 P12c 转换服务 openai 客户端，一并纳入清单。
	h.g.Joins("JOIN channels ON channels.id = channel_models.channel_id AND channels.status = 1").
		Where("channel_models.enabled = ? AND (channels.type = ? OR channels.type = ? OR channels.type IS NULL OR channels.allow_convert = ?)",
			true, "openai", "", true).Find(&cms)
	for _, cm := range cms {
		set[cm.Model] = true
	}

	var a *model.App
	if key.AppID != 0 {
		if row, err := h.apps.Get(key.AppID); err == nil {
			a = row
		}
	}

	// 令牌与 App 两层模型白名单，与 Prepare 同口径（model.WhitelistAllows）：清单只列能调用的模型
	for m := range set {
		if !model.WhitelistAllows(key.ModelWhitelist, m) || (a != nil && !model.WhitelistAllows(a.ModelWhitelist, m)) {
			delete(set, m)
		}
	}

	// 按 App allowed_channels 过滤（与 filterByAllowedChannels 共用 parseChannelIDs：非空但无合法 ID 时 fail-closed）
	if a != nil {
		if strings.TrimSpace(a.AllowedChannels) != "" {
			allowedCh := parseChannelIDs(a.AllowedChannels)
			filtered := map[string]bool{}
			if len(allowedCh) > 0 {
				for m := range set {
					cands := filterWithConvert(h.table.Resolve(m), "openai")
					for _, cd := range cands {
						if allowedCh[cd.ChannelID] {
							filtered[m] = true
							break
						}
					}
				}
			}
			set = filtered
		}
	}

	// 映射源模型：只加入目标模型已在可见集中的映射源（避免暴露不可调用的名字）
	if key.ModelMappings != "" {
		mapped := apikey.ParseMappings(key.ModelMappings)
		for from, to := range mapped {
			if set[to] {
				set[from] = true
			}
		}
	}

	data := make([]gin.H, 0, len(set))
	for id := range set {
		data = append(data, gin.H{"id": id, "object": "model", "owned_by": vendorOf(id)})
	}
	c.JSON(http.StatusOK, gin.H{"object": "list", "data": data})
}

// modelVendorPrefixes 模型名前缀 → 归属厂商，用于 /v1/models 的 owned_by 分组。
// 前缀小写匹配，列表顺序即优先级；未命中回退网关名。新增厂商在此追加即可。
var modelVendorPrefixes = []struct{ prefix, vendor string }{
	{"gpt", "openai"}, {"o1", "openai"}, {"o3", "openai"}, {"o4", "openai"},
	{"chatgpt", "openai"}, {"text-embedding", "openai"}, {"dall-e", "openai"}, {"whisper", "openai"},
	{"claude", "anthropic"},
	{"gemini", "google"}, {"gemma", "google"},
	{"glm", "zhipu"}, {"cogview", "zhipu"},
	{"deepseek", "deepseek"},
	{"qwen", "alibaba"}, {"qwq", "alibaba"},
	{"moonshot", "moonshot"}, {"kimi", "moonshot"},
	{"doubao", "bytedance"},
	{"grok", "xai"},
	{"llama", "meta"},
	{"mistral", "mistral"}, {"mixtral", "mistral"}, {"codestral", "mistral"},
	{"ernie", "baidu"},
	{"hunyuan", "tencent"},
	{"minimax", "minimax"}, {"abab", "minimax"},
	{"step-", "stepfun"},
	{"yi-", "01ai"},
}

// vendorOf 按模型名前缀推断归属厂商（/v1/models 的 owned_by 分组用）；未识别回退网关名。
func vendorOf(model string) string {
	m := strings.ToLower(strings.TrimSpace(model))
	for _, p := range modelVendorPrefixes {
		if strings.HasPrefix(m, p.prefix) {
			return p.vendor
		}
	}
	return "simple-ai-gateway"
}

// parseChannelIDs 将逗号分隔的渠道 ID 字符串解析为集合；非法/非正 ID 跳过。
// filterByAllowedChannels（filter.go）与本文件的 App 渠道过滤共用此唯一实现，
// 「非空但全部解析失败 → 空集合 → fail-closed」的口径由各调用方保持一致。
func parseChannelIDs(s string) map[uint64]bool {
	m := map[uint64]bool{}
	for _, p := range strings.Split(s, ",") {
		p = strings.TrimSpace(p)
		if id, err := strconv.ParseUint(p, 10, 64); err == nil && id > 0 {
			m[id] = true
		}
	}
	return m
}

// hasOpenAICandidate 该模型是否有可服务 openai 客户端的候选（透传或转换，与派发口径一致）。
func hasOpenAICandidate(cands []router.Candidate) bool {
	return len(filterWithConvert(cands, "openai")) > 0
}
