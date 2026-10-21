package gateway

import (
	"github.com/gin-gonic/gin"
	"github.com/ruanun/simple-ai-gateway/internal/billing"
)

// EmbeddingsHandler 处理 POST /v1/embeddings：派发到上游 /embeddings，按 prompt_tokens 计费。
type EmbeddingsHandler struct{ b BillingCtx }

func NewEmbeddingsHandler(b BillingCtx) *EmbeddingsHandler { return &EmbeddingsHandler{b: b} }

func (h *EmbeddingsHandler) Handle(c *gin.Context) {
	handleRawEndpoint(c, &h.b, "/embeddings", "embeddings", embeddingsBilling)
}

// embeddingsBilling token 计费：解析上游 usage → Resolve 价 → Compute × 渠道倍率。
func embeddingsBilling(b *BillingCtx, channelID uint64, servedModel string, mult float64, respBody []byte) (billing.Usage, bool, int64) {
	u, usageOK := billing.ParseUsageJSON(respBody)
	var cost int64
	if p, has := b.Prices.Resolve(channelID, servedModel); has && usageOK {
		cost = billing.ApplyMultiplier(billing.Compute(u, p), mult)
	}
	return u, usageOK, cost
}
