package gateway

import (
	"github.com/gin-gonic/gin"
	"github.com/ruanun/simple-ai-gateway/internal/billing"
)

// ImagesHandler 处理 POST /v1/images/generations：派发到上游 /images/generations，按次计费。
type ImagesHandler struct{ b BillingCtx }

func NewImagesHandler(b BillingCtx) *ImagesHandler { return &ImagesHandler{b: b} }

func (h *ImagesHandler) Handle(c *gin.Context) {
	handleRawEndpoint(c, &h.b, "/images/generations", "images", imagesBilling)
}

// imagesBilling 按次计费：价格 type=times 时 ComputeTimes × 渠道倍率；不解析 usage（返回零值）。
func imagesBilling(b *BillingCtx, channelID uint64, servedModel string, mult float64, _ []byte) (billing.Usage, bool, int64) {
	var cost int64
	if p, has := b.Prices.Resolve(channelID, servedModel); has && p.Type == "times" {
		cost = billing.ApplyMultiplier(billing.ComputeTimes(p), mult)
	}
	return billing.Usage{}, false, cost
}
