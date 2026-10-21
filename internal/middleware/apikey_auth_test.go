package middleware

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/ruanun/simple-ai-gateway/internal/dbtest"
	"github.com/ruanun/simple-ai-gateway/internal/domain/apikey"
	"github.com/ruanun/simple-ai-gateway/internal/model"
	"github.com/ruanun/simple-ai-gateway/internal/ratelimit"
	"github.com/stretchr/testify/require"
)

func TestAPIKeyAuth(t *testing.T) {
	gin.SetMode(gin.TestMode)
	g := dbtest.OpenInMemory(t)
	require.NoError(t, g.AutoMigrate(&model.APIKey{}))
	svc := apikey.NewService(apikey.NewRepo(g), "sk-saig")
	plain, _, _ := svc.Generate("k", nil)

	r := gin.New()
	r.Use(APIKeyAuth(svc))
	r.POST("/v1/x", func(c *gin.Context) { c.String(200, "ok") })

	// 无 key -> 401
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/v1/x", nil))
	require.Equal(t, 401, w.Code)

	// 有效 key（Bearer） -> 200
	w = httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/x", nil)
	req.Header.Set("Authorization", "Bearer "+plain)
	r.ServeHTTP(w, req)
	require.Equal(t, 200, w.Code)

	// 同一把 key 走 x-api-key 头（Anthropic 入站）也应 200。
	w = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodPost, "/v1/x", nil)
	req.Header.Set("x-api-key", plain)
	r.ServeHTTP(w, req)
	require.Equal(t, 200, w.Code)
}

// 同一 IP 鉴权失败达阈值后，本窗口内直接 429、不再进入 APIKeyAuth 查库（含合法 key）；
// 鉴权成功的请求不计数；不同 IP 互不影响。
func TestV1AuthFailLimit(t *testing.T) {
	gin.SetMode(gin.TestMode)
	g := dbtest.OpenInMemory(t)
	require.NoError(t, g.AutoMigrate(&model.APIKey{}))
	svc := apikey.NewService(apikey.NewRepo(g), "sk-saig")
	plain, _, _ := svc.Generate("k", nil)
	l := ratelimit.NewMemoryLimiter()
	now := func() time.Time { return time.Unix(0, 0) }

	r := gin.New()
	r.Use(V1AuthFailLimit(l, now, 3), APIKeyAuth(svc))
	r.POST("/v1/x", func(c *gin.Context) {
		c.String(http.StatusUnauthorized, "upstream 401") // 上游透传的 401 不得计入鉴权失败
	})
	do := func(remote, key string) int {
		req := httptest.NewRequest(http.MethodPost, "/v1/x", nil)
		req.RemoteAddr = remote
		if key != "" {
			req.Header.Set("Authorization", "Bearer "+key)
		}
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
		return w.Code
	}

	// 合法 key 的请求（即便 handler 回 401）不计数
	for i := 0; i < 5; i++ {
		require.Equal(t, http.StatusUnauthorized, do("1.1.1.1:1", plain))
	}
	require.Equal(t, http.StatusUnauthorized, do("1.1.1.1:1", "sk-saig-bad"))
	require.Equal(t, http.StatusUnauthorized, do("1.1.1.1:1", ""))
	require.Equal(t, http.StatusUnauthorized, do("1.1.1.1:1", "sk-saig-bad"))
	// 已失败 3 次：本窗口直接 429（合法 key 也挡）
	require.Equal(t, http.StatusTooManyRequests, do("1.1.1.1:1", "sk-saig-bad"))
	require.Equal(t, http.StatusTooManyRequests, do("1.1.1.1:1", plain))
	// 其他 IP 不受影响
	require.Equal(t, http.StatusUnauthorized, do("2.2.2.2:1", "sk-saig-bad"))
}

// limit<=0 关闭该防护。
func TestV1AuthFailLimitDisabled(t *testing.T) {
	gin.SetMode(gin.TestMode)
	l := ratelimit.NewMemoryLimiter()
	r := gin.New()
	r.Use(V1AuthFailLimit(l, time.Now, 0))
	r.POST("/v1/x", func(c *gin.Context) {
		c.Set(CtxAPIKeyAuthFailed, true)
		c.AbortWithStatus(http.StatusUnauthorized)
	})
	for i := 0; i < 50; i++ {
		w := httptest.NewRecorder()
		r.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/v1/x", nil))
		require.Equal(t, http.StatusUnauthorized, w.Code)
	}
}
