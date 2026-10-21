package middleware

import (
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/ruanun/simple-ai-gateway/internal/domain/apikey"
	"github.com/ruanun/simple-ai-gateway/internal/model"
)

const CtxAPIKeyID = "apikey_id"
const CtxAPIKeyRPM = "apikey_rpm"

// CtxAPIKeyAuthFailed APIKeyAuth 判定鉴权失败（缺 key / key 无效）时置 true，供 V1AuthFailLimit 计数。
// 以显式标记而非响应 401 判定：上游透传的 401 不是本网关的鉴权失败，不得计入。
const CtxAPIKeyAuthFailed = "apikey_auth_failed"

// CtxAPIKeyRec 存 Authenticate 已载入的完整 key 行（*model.APIKey），
// 网关侧治理/模型清单直接复用，消除每请求回表重查同一行。
const CtxAPIKeyRec = "apikey_rec"

// APIKeyFromCtx 取 APIKeyAuth 载入的完整 key 行；未经鉴权中间件（或类型不符）返回 nil。
func APIKeyFromCtx(c *gin.Context) *model.APIKey {
	if v, ok := c.Get(CtxAPIKeyRec); ok {
		if rec, ok := v.(*model.APIKey); ok {
			return rec
		}
	}
	return nil
}

func APIKeyAuth(svc *apikey.Service) gin.HandlerFunc {
	return func(c *gin.Context) {
		h := c.GetHeader("Authorization")
		token := strings.TrimSpace(strings.TrimPrefix(h, "Bearer "))
		// 回退到 x-api-key 头（Anthropic 等协议入站用），与 Bearer 鉴权同一把 key。
		if token == "" {
			token = strings.TrimSpace(c.GetHeader("x-api-key"))
		}
		if token == "" {
			c.Set(CtxAPIKeyAuthFailed, true)
			abortV1Error(c, http.StatusUnauthorized, "missing api key")
			return
		}
		rec, err := svc.Authenticate(token)
		if err != nil {
			c.Set(CtxAPIKeyAuthFailed, true)
			abortV1Error(c, http.StatusUnauthorized, "invalid api key")
			return
		}
		c.Set(CtxAPIKeyID, rec.ID)
		c.Set(CtxAPIKeyRPM, rec.RPM)
		c.Set(CtxAPIKeyRec, rec)
		c.Next()
	}
}
