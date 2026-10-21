package admin

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/ruanun/simple-ai-gateway/internal/breaker"
	"github.com/ruanun/simple-ai-gateway/internal/dbtest"
	"github.com/ruanun/simple-ai-gateway/internal/domain/account"
	"github.com/ruanun/simple-ai-gateway/internal/domain/channel"
	"github.com/ruanun/simple-ai-gateway/internal/domain/proxy"
	"github.com/ruanun/simple-ai-gateway/internal/model"
	"github.com/stretchr/testify/require"
)

func TestBreakerHandlerStatusAndReset(t *testing.T) {
	gin.SetMode(gin.TestMode)
	g := dbtest.OpenInMemory(t)
	require.NoError(t, g.AutoMigrate(&model.Channel{}, &model.Account{}, &model.Proxy{}))
	ch := model.Channel{Name: "c1", Type: "openai"}
	require.NoError(t, g.Create(&ch).Error)
	acc := model.Account{ChannelID: ch.ID, Name: "a1", Status: 1}
	require.NoError(t, g.Create(&acc).Error)

	now := time.Unix(1000, 0)
	params := breaker.Params{Window: 30 * time.Second, Cooldown: 60 * time.Second, FailThreshold: 1, ErrorRate: 0.5, MinSamples: 20, ProbeBackoffMax: 8}
	chBrk := breaker.NewMemoryBreaker(params)
	accBrk := breaker.NewMemoryBreaker(params)
	accBrk.RecordResult(acc.ID, now, false, true) // 429 → 账号熔断

	// 账号×模型配额冷却：预置一条 429 冷却，验证 Status 展示与 Reset 联动清除
	keyed := breaker.NewMemoryKeyed(time.Minute, 30*time.Minute)
	keyed.RecordQuotaFailure(breaker.AccountModelKey(acc.ID, "gpt-4o"), now, 0)

	chanSvc := channel.NewService(channel.NewRepo(g), "0123456789abcdef")
	accSvc := account.NewService(account.NewRepo(g))
	proxySvc := proxy.NewService(proxy.NewRepo(g))
	proxyBrk := breaker.NewMemoryBreaker(params)
	h := NewBreakerHandler(chBrk, accBrk, keyed, chanSvc, accSvc, proxyBrk, proxySvc, func() time.Time { return now })

	// Status：账号熔断、渠道为空
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodGet, "/admin/breaker/status", nil)
	h.Status(c)
	require.Equal(t, http.StatusOK, w.Code)
	var resp struct {
		Channels      []map[string]any `json:"channels"`
		Accounts      []map[string]any `json:"accounts"`
		AccountQuotas []map[string]any `json:"account_quotas"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	require.Empty(t, resp.Channels)
	require.Len(t, resp.Accounts, 1)
	require.Equal(t, "open", resp.Accounts[0]["state"])
	require.Equal(t, float64(acc.ID), resp.Accounts[0]["id"])
	require.Equal(t, float64(ch.ID), resp.Accounts[0]["channel_id"])
	// 配额冷却应出现在状态页（此前漏报）
	require.Len(t, resp.AccountQuotas, 1)
	require.Equal(t, float64(acc.ID), resp.AccountQuotas[0]["account_id"])
	require.Equal(t, "gpt-4o", resp.AccountQuotas[0]["model"])
	require.Equal(t, float64(ch.ID), resp.AccountQuotas[0]["channel_id"])

	// Reset：重置账号熔断
	w2 := httptest.NewRecorder()
	c2, _ := gin.CreateTestContext(w2)
	c2.Request = httptest.NewRequest(http.MethodPost, "/admin/breaker/reset",
		strings.NewReader(fmt.Sprintf(`{"level":"account","id":%d}`, acc.ID)))
	c2.Request.Header.Set("Content-Type", "application/json")
	h.Reset(c2)
	require.Equal(t, http.StatusOK, w2.Code)

	// Status 再查：账号熔断与配额冷却均已清空（Reset(level=account) 同步清 keyed 冷却）
	w3 := httptest.NewRecorder()
	c3, _ := gin.CreateTestContext(w3)
	c3.Request = httptest.NewRequest(http.MethodGet, "/admin/breaker/status", nil)
	h.Status(c3)
	var resp3 struct {
		Accounts      []map[string]any `json:"accounts"`
		AccountQuotas []map[string]any `json:"account_quotas"`
	}
	require.NoError(t, json.Unmarshal(w3.Body.Bytes(), &resp3))
	require.Empty(t, resp3.Accounts)
	require.Empty(t, resp3.AccountQuotas)
	require.True(t, keyed.Allow(breaker.AccountModelKey(acc.ID, "gpt-4o"), now), "配额冷却应被重置")
}

func TestBreakerHandlerResetInvalidLevel(t *testing.T) {
	gin.SetMode(gin.TestMode)
	h := NewBreakerHandler(nil, nil, nil, nil, nil, nil, nil, time.Now)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodPost, "/admin/breaker/reset",
		strings.NewReader(`{"level":"unknown","id":1}`))
	c.Request.Header.Set("Content-Type", "application/json")
	h.Reset(c)
	require.Equal(t, http.StatusBadRequest, w.Code)
	require.Contains(t, w.Body.String(), "level 仅支持 channel/account/proxy")
}

// TestBreakerHandlerProxyStatusAndReset 代理熔断出现在 status.proxies；level=proxy 可重置。
func TestBreakerHandlerProxyStatusAndReset(t *testing.T) {
	gin.SetMode(gin.TestMode)
	g := dbtest.OpenInMemory(t)
	require.NoError(t, g.AutoMigrate(&model.Channel{}, &model.Account{}, &model.Proxy{}))
	px := model.Proxy{Host: "127.0.0.1", Port: 1080, Status: 1}
	require.NoError(t, g.Create(&px).Error)

	now := time.Unix(1000, 0)
	params := breaker.Params{Window: 30 * time.Second, Cooldown: 60 * time.Second, FailThreshold: 1, ErrorRate: 0.5, MinSamples: 20, ProbeBackoffMax: 8}
	chBrk := breaker.NewMemoryBreaker(params)
	accBrk := breaker.NewMemoryBreaker(params)
	proxyBrk := breaker.NewMemoryBreaker(params)
	proxyBrk.RecordResult(px.ID, now, false, false) // 网络层失败 → 代理熔断

	chanSvc := channel.NewService(channel.NewRepo(g), "0123456789abcdef")
	accSvc := account.NewService(account.NewRepo(g))
	proxySvc := proxy.NewService(proxy.NewRepo(g))
	h := NewBreakerHandler(chBrk, accBrk, nil, chanSvc, accSvc, proxyBrk, proxySvc, func() time.Time { return now })

	// Status：代理熔断出现在 proxies
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodGet, "/admin/breaker/status", nil)
	h.Status(c)
	require.Equal(t, http.StatusOK, w.Code)
	var resp struct {
		Proxies []map[string]any `json:"proxies"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	require.Len(t, resp.Proxies, 1)
	require.Equal(t, "open", resp.Proxies[0]["state"])
	require.Equal(t, float64(px.ID), resp.Proxies[0]["id"])

	// Reset：level=proxy 重置该代理熔断
	w2 := httptest.NewRecorder()
	c2, _ := gin.CreateTestContext(w2)
	c2.Request = httptest.NewRequest(http.MethodPost, "/admin/breaker/reset",
		strings.NewReader(fmt.Sprintf(`{"level":"proxy","id":%d}`, px.ID)))
	c2.Request.Header.Set("Content-Type", "application/json")
	h.Reset(c2)
	require.Equal(t, http.StatusOK, w2.Code)

	// Status 再查：proxies 为空
	w3 := httptest.NewRecorder()
	c3, _ := gin.CreateTestContext(w3)
	c3.Request = httptest.NewRequest(http.MethodGet, "/admin/breaker/status", nil)
	h.Status(c3)
	var resp3 struct {
		Proxies []map[string]any `json:"proxies"`
	}
	require.NoError(t, json.Unmarshal(w3.Body.Bytes(), &resp3))
	require.Empty(t, resp3.Proxies)
}
