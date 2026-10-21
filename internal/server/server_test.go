package server

import (
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/ruanun/simple-ai-gateway/internal/config"
	"github.com/ruanun/simple-ai-gateway/internal/crypto"
	"github.com/ruanun/simple-ai-gateway/internal/db"
	"github.com/ruanun/simple-ai-gateway/internal/dbtest"
	"github.com/ruanun/simple-ai-gateway/internal/domain/apikey"
	"github.com/ruanun/simple-ai-gateway/internal/domain/usage"
	"github.com/ruanun/simple-ai-gateway/internal/model"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

// 计划原有用例：锁住装配 + 鉴权失败路径（无 key -> 401）。
func TestChatCompletionsEndToEnd(t *testing.T) {
	gin.SetMode(gin.TestMode)
	// mock 上游
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"ok","choices":[]}`))
	}))
	defer up.Close()

	cfg := config.Config{}
	cfg.Storage.Local.Root = t.TempDir() // 审计正文本地存储落临时目录，避免测试残留
	cfg.Security.APIKeyPrefix = "sk-saig"
	cfg.Security.ChannelKeyAESKey = "0123456789abcdef0123456789abcdef"
	cfg.JWT.Secret = "s"
	cfg.JWT.ExpireHours = 1

	g := dbtest.OpenInMemory(t)
	require.NoError(t, db.Migrate(g, model.Migrations("admin", "sag12345")))

	// 预置渠道 + 模型
	ch := model.Channel{Name: "c", BaseURL: up.URL, Status: 1, Timeout: 30}
	require.NoError(t, g.Create(&ch).Error)
	require.NoError(t, g.Create(&model.ChannelModel{ChannelID: ch.ID, Model: "gpt-4o", Enabled: true}).Error)

	r, shutdown, err := NewWithShutdown(cfg, g, nil)
	require.NoError(t, err)
	t.Cleanup(shutdown) // 先停审计 worker 等后台 goroutine，再让 TempDir 清理（Cleanup 为 LIFO）

	// 鉴权失败：无 key -> 401
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"gpt-4o"}`))
	r.ServeHTTP(w, req)
	require.Equal(t, 401, w.Code)
}

// 完整 happy-path 端到端集成测试，覆盖 P0 验收 a~e。
// 全程走真实 HTTP（httptest.NewRecorder + r.ServeHTTP），mock 上游，不需要真实 key。
func TestFullHappyPathEndToEnd(t *testing.T) {
	gin.SetMode(gin.TestMode)

	// 1) mock 上游 OpenAI：非流式返回固定 JSON；请求体含 "stream":true 时返回 SSE。
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := readAll(r)
		if strings.Contains(string(body), `"stream":true`) {
			w.Header().Set("Content-Type", "text/event-stream")
			w.WriteHeader(http.StatusOK)
			fl, _ := w.(http.Flusher)
			_, _ = w.Write([]byte("data: {\"id\":\"ok\",\"choices\":[]}\n\n"))
			if fl != nil {
				fl.Flush()
			}
			_, _ = w.Write([]byte("data: [DONE]\n\n"))
			if fl != nil {
				fl.Flush()
			}
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"id":"ok","choices":[]}`))
	}))
	defer up.Close()

	cfg := config.Config{}
	cfg.Storage.Local.Root = t.TempDir() // 审计正文本地存储落临时目录，避免测试残留
	cfg.Security.APIKeyPrefix = "sk-saig"
	cfg.Security.ChannelKeyAESKey = "0123456789abcdef0123456789abcdef"
	cfg.JWT.Secret = "integration-secret"
	cfg.JWT.ExpireHours = 1

	// 2) 迁移用 model.Migrations("admin","sag12345") 种子管理员
	g := dbtest.OpenInMemory(t)
	require.NoError(t, db.Migrate(g, model.Migrations("admin", "sag12345")))
	resetRouting(g) // 兼容 -count=N 重跑：清空唯一索引相关表（app/price 等）

	r, shutdown, err := NewWithShutdown(cfg, g, nil)
	require.NoError(t, err)
	t.Cleanup(shutdown) // 先停审计 worker 等后台 goroutine，再让 TempDir 清理（Cleanup 为 LIFO）

	// a) POST /admin/login admin/sag12345 -> 拿 JWT
	token := doJSON(t, r, http.MethodPost, "/admin/login", "",
		`{"username":"admin","password":"sag12345"}`, http.StatusOK)["token"].(string)
	require.NotEmpty(t, token)

	// b) 带 JWT 建 channel（base_url 指向 mock 上游，直接传 type）/ channel-model / api-key
	chResp := doJSON(t, r, http.MethodPost, "/admin/channels", token,
		`{"type":"openai","name":"c1","base_url":"`+up.URL+`","api_key":"sk-up"}`,
		http.StatusOK)
	channelID := chResp["id"].(float64)
	require.NotZero(t, channelID)

	doJSON(t, r, http.MethodPost, "/admin/channel-models", token,
		`{"channel_id":`+itoa(channelID)+`,"model":"gpt-4o","enabled":true}`, http.StatusOK)

	// 配价 gpt-4o：1/1 额度够触发计费。
	doJSON(t, r, http.MethodPost, "/admin/prices", token,
		`{"model":"gpt-4o","input_per_1m":1,"output_per_1m":1,"enabled":true}`, http.StatusOK)

	// 建配额主体 App（不限额），key 必须绑定 App。
	appResp := doJSON(t, r, http.MethodPost, "/admin/apps", token,
		`{"name":"team-1","quota":0}`, http.StatusOK)
	appID := appResp["id"].(float64)
	require.NotZero(t, appID)

	keyResp := doJSON(t, r, http.MethodPost, "/admin/api-keys", token,
		`{"name":"k1","app_id":`+itoa(appID)+`}`, http.StatusOK)
	plainKey := keyResp["key"].(string)
	require.Contains(t, plainKey, "sk-saig-")

	// c) 带明文 key 打非流式 chat -> 200 且含 "id":"ok"
	wNon := httptest.NewRecorder()
	reqNon := httptest.NewRequest(http.MethodPost, "/v1/chat/completions",
		strings.NewReader(`{"model":"gpt-4o","messages":[{"role":"user","content":"hi"}]}`))
	reqNon.Header.Set("Authorization", "Bearer "+plainKey)
	reqNon.Header.Set("Content-Type", "application/json")
	r.ServeHTTP(wNon, reqNon)
	require.Equal(t, http.StatusOK, wNon.Code)
	require.Contains(t, wNon.Body.String(), `"id":"ok"`)

	// d) 带明文 key 打流式 chat -> 200 且响应体含 [DONE]
	wStream := httptest.NewRecorder()
	reqStream := httptest.NewRequest(http.MethodPost, "/v1/chat/completions",
		strings.NewReader(`{"model":"gpt-4o","stream":true,"messages":[{"role":"user","content":"hi"}]}`))
	reqStream.Header.Set("Authorization", "Bearer "+plainKey)
	reqStream.Header.Set("Content-Type", "application/json")
	r.ServeHTTP(wStream, reqStream)
	require.Equal(t, http.StatusOK, wStream.Code)
	require.Contains(t, wStream.Body.String(), "[DONE]")

	// e) 带 JWT 看请求日志 -> total >= 2
	logs := doJSON(t, r, http.MethodGet, "/admin/request-logs?limit=10", token, "", http.StatusOK)
	require.GreaterOrEqual(t, logs["total"].(float64), float64(2))
}

// TestAliasWeightedRoutingE2E 端到端验证别名加权路由：
// 别名 smart 两条同优先级边(权重 70/30) 指向两个 mock 上游，发 N 次 model:"smart"，
// 断言两个上游都被命中且高权重那个命中明显更多。
func TestAliasWeightedRoutingE2E(t *testing.T) {
	gin.SetMode(gin.TestMode)

	// 两个 mock 上游，各自统计命中次数（按 stream 分流返回）。
	var hitsA, hitsB int
	mkUpstream := func(counter *int) *httptest.Server {
		return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			body, _ := readAll(r)
			*counter++
			if strings.Contains(string(body), `"stream":true`) {
				w.Header().Set("Content-Type", "text/event-stream")
				w.WriteHeader(http.StatusOK)
				fl, _ := w.(http.Flusher)
				_, _ = w.Write([]byte("data: {\"id\":\"ok\"}\n\n"))
				if fl != nil {
					fl.Flush()
				}
				_, _ = w.Write([]byte("data: [DONE]\n\n"))
				if fl != nil {
					fl.Flush()
				}
				return
			}
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"id":"ok","choices":[]}`))
		}))
	}
	upA := mkUpstream(&hitsA)
	defer upA.Close()
	upB := mkUpstream(&hitsB)
	defer upB.Close()

	cfg := config.Config{}
	cfg.Storage.Local.Root = t.TempDir() // 审计正文本地存储落临时目录，避免测试残留
	cfg.Security.APIKeyPrefix = "sk-saig"
	cfg.Security.ChannelKeyAESKey = "0123456789abcdef0123456789abcdef"
	cfg.JWT.Secret = "s"
	cfg.JWT.ExpireHours = 1

	g := dbtest.OpenInMemory(t)
	require.NoError(t, db.Migrate(g, model.Migrations("admin", "sag12345")))
	resetRouting(g) // 兼容 -count 重跑：清空别名/边（shared-cache 内存库跨重跑保留数据）

	// 两个渠道（都声明 gpt-4o），一个别名 smart + 两条边(weight 70/30、同优先级)。
	encA, _ := crypto.Encrypt(cfg.Security.ChannelKeyAESKey, "sk-upA")
	encB, _ := crypto.Encrypt(cfg.Security.ChannelKeyAESKey, "sk-upB")
	chA := model.Channel{Name: "cA", BaseURL: upA.URL, Status: 1, Timeout: 30, Weight: 70}
	chB := model.Channel{Name: "cB", BaseURL: upB.URL, Status: 1, Timeout: 30, Weight: 30}
	require.NoError(t, g.Create(&chA).Error)
	require.NoError(t, g.Create(&chB).Error)
	// 密钥池：每个直建渠道补一个默认账号（搬运同一凭证），否则 Lookup 找不到账号 → 502。
	require.NoError(t, g.Create(&model.Account{ChannelID: chA.ID, APIKeyEnc: encA, Weight: 1, Status: 1}).Error)
	require.NoError(t, g.Create(&model.Account{ChannelID: chB.ID, APIKeyEnc: encB, Weight: 1, Status: 1}).Error)
	// 别名候选需对应启用的 ChannelModel（#29 后不再绕过渠道模型级控制）。
	require.NoError(t, g.Create(&model.ChannelModel{ChannelID: chA.ID, Model: "gpt-4o", Enabled: true}).Error)
	require.NoError(t, g.Create(&model.ChannelModel{ChannelID: chB.ID, Model: "gpt-4o", Enabled: true}).Error)

	al := model.ModelAlias{Alias: "smart", Status: 1}
	require.NoError(t, g.Create(&al).Error)
	require.NoError(t, g.Create(&model.AliasRoute{AliasID: al.ID, ChannelID: chA.ID, Model: "gpt-4o", Weight: 70, Priority: 1}).Error)
	require.NoError(t, g.Create(&model.AliasRoute{AliasID: al.ID, ChannelID: chB.ID, Model: "gpt-4o", Weight: 30, Priority: 1}).Error)
	// 配价 gpt-4o（别名 smart 路由目标，价格校验用候选真实模型名检查）。
	require.NoError(t, g.Create(&model.ModelPrice{Model: "gpt-4o", InputPer1M: 1, OutputPer1M: 1, Enabled: true}).Error)

	// server.NewWithShutdown 内部会 rt.Build(g)，此时别名/边/渠道已落库。
	r, shutdown, err := NewWithShutdown(cfg, g, nil)
	require.NoError(t, err)
	t.Cleanup(shutdown) // 先停审计 worker 等后台 goroutine，再让 TempDir 清理（Cleanup 为 LIFO）

	// 直接生成一把 key（避免再走 admin 流程）。
	keySvc := apikey.NewService(apikey.NewRepo(g), cfg.Security.APIKeyPrefix)
	plainKey, _, err := keySvc.Generate("k", nil)
	require.NoError(t, err)

	const N = 200
	for i := 0; i < N; i++ {
		w := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions",
			strings.NewReader(`{"model":"smart","messages":[]}`))
		req.Header.Set("Authorization", "Bearer "+plainKey)
		req.Header.Set("Content-Type", "application/json")
		r.ServeHTTP(w, req)
		require.Equal(t, http.StatusOK, w.Code, "resp=%s", w.Body.String())
	}

	require.Equal(t, N, hitsA+hitsB, "每次请求应恰好命中一个上游")
	require.Greater(t, hitsA, 0, "高权重渠道应被命中")
	require.Greater(t, hitsB, 0, "低权重渠道也应被命中（权重 30 非零）")
	require.Greater(t, hitsA, hitsB, "高权重(70)命中应明显多于低权重(30)，命中分布 A=%d B=%d", hitsA, hitsB)
	t.Logf("加权路由命中分布: A(w70)=%d B(w30)=%d", hitsA, hitsB)
}

// TestAliasFailoverE2E 验证降级：高权重渠道指向 500 上游，应自动改用另一个成功上游。
func TestAliasFailoverE2E(t *testing.T) {
	gin.SetMode(gin.TestMode)

	bad := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer bad.Close()
	var goodHits int
	good := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		goodHits++
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"id":"ok"}`))
	}))
	defer good.Close()

	cfg := config.Config{}
	cfg.Storage.Local.Root = t.TempDir() // 审计正文本地存储落临时目录，避免测试残留
	cfg.Security.APIKeyPrefix = "sk-saig"
	cfg.Security.ChannelKeyAESKey = "0123456789abcdef0123456789abcdef"
	cfg.JWT.Secret = "s"
	cfg.JWT.ExpireHours = 1

	g := dbtest.OpenInMemory(t)
	require.NoError(t, db.Migrate(g, model.Migrations("admin", "sag12345")))
	resetRouting(g)

	encBad, _ := crypto.Encrypt(cfg.Security.ChannelKeyAESKey, "sk-bad")
	encGood, _ := crypto.Encrypt(cfg.Security.ChannelKeyAESKey, "sk-good")
	// 高权重指向 bad(500)，低权重指向 good(200)。
	chBad := model.Channel{Name: "bad", BaseURL: bad.URL, Status: 1, Timeout: 30, Weight: 99}
	chGood := model.Channel{Name: "good", BaseURL: good.URL, Status: 1, Timeout: 30, Weight: 1}
	require.NoError(t, g.Create(&chBad).Error)
	require.NoError(t, g.Create(&chGood).Error)
	// 密钥池：每个直建渠道补一个默认账号，否则 Lookup 找不到账号 → 502。
	require.NoError(t, g.Create(&model.Account{ChannelID: chBad.ID, APIKeyEnc: encBad, Weight: 1, Status: 1}).Error)
	require.NoError(t, g.Create(&model.Account{ChannelID: chGood.ID, APIKeyEnc: encGood, Weight: 1, Status: 1}).Error)
	// 别名候选需对应启用的 ChannelModel（#29 后不再绕过渠道模型级控制）。
	require.NoError(t, g.Create(&model.ChannelModel{ChannelID: chBad.ID, Model: "gpt-4o", Enabled: true}).Error)
	require.NoError(t, g.Create(&model.ChannelModel{ChannelID: chGood.ID, Model: "gpt-4o", Enabled: true}).Error)

	al := model.ModelAlias{Alias: "smart", Status: 1}
	require.NoError(t, g.Create(&al).Error)
	require.NoError(t, g.Create(&model.AliasRoute{AliasID: al.ID, ChannelID: chBad.ID, Model: "gpt-4o", Weight: 99, Priority: 1}).Error)
	require.NoError(t, g.Create(&model.AliasRoute{AliasID: al.ID, ChannelID: chGood.ID, Model: "gpt-4o", Weight: 1, Priority: 1}).Error)
	// 配价 gpt-4o（别名 smart 路由目标，价格校验用候选真实模型名检查）。
	require.NoError(t, g.Create(&model.ModelPrice{Model: "gpt-4o", InputPer1M: 1, OutputPer1M: 1, Enabled: true}).Error)

	r, shutdown, err := NewWithShutdown(cfg, g, nil)
	require.NoError(t, err)
	t.Cleanup(shutdown) // 先停审计 worker 等后台 goroutine，再让 TempDir 清理（Cleanup 为 LIFO）
	keySvc := apikey.NewService(apikey.NewRepo(g), cfg.Security.APIKeyPrefix)
	plainKey, _, err := keySvc.Generate("k", nil)
	require.NoError(t, err)

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions",
		strings.NewReader(`{"model":"smart","messages":[]}`))
	req.Header.Set("Authorization", "Bearer "+plainKey)
	req.Header.Set("Content-Type", "application/json")
	r.ServeHTTP(w, req)

	require.Equal(t, http.StatusOK, w.Code, "应降级到 good 渠道成功，resp=%s", w.Body.String())
	require.Contains(t, w.Body.String(), `"id":"ok"`)
	require.Greater(t, goodHits, 0, "good 渠道应被命中")
}

// TestAliasCRUDHotReloadE2E 验证 admin 别名/路由 CRUD 写后热加载：
// 先无别名→404，建别名+边后立即可路由→200，删别名后→404。
func TestAliasCRUDHotReloadE2E(t *testing.T) {
	gin.SetMode(gin.TestMode)

	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"id":"ok"}`))
	}))
	defer up.Close()

	cfg := config.Config{}
	cfg.Storage.Local.Root = t.TempDir() // 审计正文本地存储落临时目录，避免测试残留
	cfg.Security.APIKeyPrefix = "sk-saig"
	cfg.Security.ChannelKeyAESKey = "0123456789abcdef0123456789abcdef"
	cfg.JWT.Secret = "s"
	cfg.JWT.ExpireHours = 1

	g := dbtest.OpenInMemory(t)
	require.NoError(t, db.Migrate(g, model.Migrations("admin", "sag12345")))
	resetRouting(g)
	r, shutdown, err := NewWithShutdown(cfg, g, nil)
	require.NoError(t, err)
	t.Cleanup(shutdown) // 先停审计 worker 等后台 goroutine，再让 TempDir 清理（Cleanup 为 LIFO）

	token := doJSON(t, r, http.MethodPost, "/admin/login", "",
		`{"username":"admin","password":"sag12345"}`, http.StatusOK)["token"].(string)

	chResp := doJSON(t, r, http.MethodPost, "/admin/channels", token,
		`{"type":"openai","name":"c1","base_url":"`+up.URL+`","api_key":"sk-up","weight":1}`, http.StatusOK)
	channelID := chResp["id"].(float64)
	// #29 后别名候选需对应启用的 ChannelModel；先给渠道建好 gpt-4o 模型。
	doJSON(t, r, http.MethodPost, "/admin/channel-models", token,
		`{"channel_id":`+itoa(channelID)+`,"model":"gpt-4o","enabled":true}`, http.StatusOK)

	keySvc := apikey.NewService(apikey.NewRepo(g), cfg.Security.APIKeyPrefix)
	plainKey, _, err := keySvc.Generate("k", nil)
	require.NoError(t, err)

	chat := func() int {
		w := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions",
			strings.NewReader(`{"model":"smart","messages":[]}`))
		req.Header.Set("Authorization", "Bearer "+plainKey)
		req.Header.Set("Content-Type", "application/json")
		r.ServeHTTP(w, req)
		return w.Code
	}

	// 未建别名 → 404
	require.Equal(t, http.StatusNotFound, chat())

	// 建别名 + 边（写后热加载）
	aliasResp := doJSON(t, r, http.MethodPost, "/admin/aliases", token, `{"alias":"smart"}`, http.StatusOK)
	aliasID := aliasResp["id"].(float64)
	doJSON(t, r, http.MethodPost, "/admin/alias-routes", token,
		`{"alias_id":`+itoa(aliasID)+`,"channel_id":`+itoa(channelID)+`,"model":"gpt-4o","weight":1,"priority":1}`, http.StatusOK)
	// 配价 gpt-4o（别名 smart 路由到此真实模型，价格校验用路由后的 Model 检查）。
	doJSON(t, r, http.MethodPost, "/admin/prices", token,
		`{"model":"gpt-4o","input_per_1m":1,"output_per_1m":1,"enabled":true}`, http.StatusOK)

	// 现在可路由 → 200
	require.Equal(t, http.StatusOK, chat())

	// 列表能看到别名与边
	aliases := doJSONArray(t, r, http.MethodGet, "/admin/aliases", token, "", http.StatusOK)
	require.Len(t, aliases, 1)
	routes := doJSONArray(t, r, http.MethodGet, "/admin/aliases/"+itoa(aliasID)+"/routes", token, "", http.StatusOK)
	require.Len(t, routes, 1)

	// 删别名（含级联删边）→ 再次热加载 → 404
	doJSON(t, r, http.MethodDelete, "/admin/aliases/"+itoa(aliasID), token, "", http.StatusOK)
	require.Equal(t, http.StatusNotFound, chat())
}

// TestBillingE2E 端到端验证计费链路：建 App(配额 1 USD) + key(绑 App) + 价格(gpt-4o) +
// 渠道/模型 + mock 上游返回带 usage 的响应 → 发 chat → 断言：
//
//	① 请求日志 cost_micros>0 且 tokens 正确
//	② App.used_quota 增加
//	③ UsageDaily 有行
//	④ 把 used 顶满后再请求得 402
func TestBillingE2E(t *testing.T) {
	gin.SetMode(gin.TestMode)

	// mock 上游：固定返回带 usage(prompt=100,completion=50) 的 OpenAI 响应。
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"id":"ok","choices":[],"usage":{"prompt_tokens":100,"completion_tokens":50,"total_tokens":150}}`))
	}))
	defer up.Close()

	cfg := config.Config{}
	cfg.Storage.Local.Root = t.TempDir() // 审计正文本地存储落临时目录，避免测试残留
	cfg.Security.APIKeyPrefix = "sk-saig"
	cfg.Security.ChannelKeyAESKey = "0123456789abcdef0123456789abcdef"
	cfg.JWT.Secret = "billing-secret"
	cfg.JWT.ExpireHours = 1

	g := dbtest.OpenInMemory(t)
	require.NoError(t, db.Migrate(g, model.Migrations("admin", "sag12345")))
	resetRouting(g)

	r, shutdown, err := NewWithShutdown(cfg, g, nil)
	require.NoError(t, err)
	t.Cleanup(shutdown) // 先停审计 worker 等后台 goroutine，再让 TempDir 清理（Cleanup 为 LIFO）
	token := doJSON(t, r, http.MethodPost, "/admin/login", "",
		`{"username":"admin","password":"sag12345"}`, http.StatusOK)["token"].(string)

	// 渠道 + 模型（直调）。
	chResp := doJSON(t, r, http.MethodPost, "/admin/channels", token,
		`{"type":"openai","name":"c1","base_url":"`+up.URL+`","api_key":"sk-up"}`, http.StatusOK)
	channelID := chResp["id"].(float64)
	doJSON(t, r, http.MethodPost, "/admin/channel-models", token,
		`{"channel_id":`+itoa(channelID)+`,"model":"gpt-4o","enabled":true}`, http.StatusOK)

	// 配价 gpt-4o：input 2.5 / output 10（USD/1M token）。cost = 100*2.5 + 50*10 = 750 micros。
	doJSON(t, r, http.MethodPost, "/admin/prices", token,
		`{"model":"gpt-4o","input_per_1m":2.5,"output_per_1m":10,"enabled":true}`, http.StatusOK)

	// 配额 1 USD = 1e6 micros 的 App，key 绑定它。
	appResp := doJSON(t, r, http.MethodPost, "/admin/apps", token,
		`{"name":"team-bill","quota":1000000}`, http.StatusOK)
	appID := uint64(appResp["id"].(float64))
	keyResp := doJSON(t, r, http.MethodPost, "/admin/api-keys", token,
		`{"name":"k1","app_id":`+strconv.FormatUint(appID, 10)+`}`, http.StatusOK)
	plainKey := keyResp["key"].(string)

	// 发一次 chat。
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions",
		strings.NewReader(`{"model":"gpt-4o","messages":[{"role":"user","content":"hi"}]}`))
	req.Header.Set("Authorization", "Bearer "+plainKey)
	req.Header.Set("Content-Type", "application/json")
	r.ServeHTTP(w, req)
	require.Equal(t, http.StatusOK, w.Code, "resp=%s", w.Body.String())
	require.Contains(t, w.Body.String(), `"id":"ok"`)

	// ① 请求日志：cost_micros>0 且 tokens 正确。
	var logRow model.RequestLog
	require.NoError(t, g.Order("id desc").First(&logRow).Error)
	require.Equal(t, 100, logRow.PromptTokens)
	require.Equal(t, 50, logRow.CompletionTokens)
	require.Equal(t, int64(750), logRow.CostMicros, "100*2.5 + 50*10 = 750 micros")
	require.False(t, logRow.UsageMissing)
	require.Equal(t, appID, logRow.AppID)

	// ② App.used_quota 增加。
	var appRow model.App
	require.NoError(t, g.First(&appRow, appID).Error)
	require.Equal(t, int64(750), appRow.UsedQuota)

	// ③ UsageDaily 有行（用量改为定时聚合，测试中手动触发一次确保数据落入统计表）。
	usage.NewAggregatorStatic(g).Aggregate()
	var dailyCnt int64
	require.NoError(t, g.Model(&model.UsageDaily{}).Where("app_id = ? AND model = ?", appID, "gpt-4o").Count(&dailyCnt).Error)
	require.Equal(t, int64(1), dailyCnt)
	var daily model.UsageDaily
	require.NoError(t, g.Where("app_id = ? AND model = ?", appID, "gpt-4o").First(&daily).Error)
	require.Equal(t, int64(1), daily.Requests)
	require.Equal(t, int64(100), daily.PromptTokens)
	require.Equal(t, int64(750), daily.CostMicros)

	// ④ 把 used 顶满后再请求 → 402。
	require.NoError(t, g.Model(&model.App{}).Where("id = ?", appID).Update("used_quota", int64(1000000)).Error)
	w2 := httptest.NewRecorder()
	req2 := httptest.NewRequest(http.MethodPost, "/v1/chat/completions",
		strings.NewReader(`{"model":"gpt-4o","messages":[{"role":"user","content":"hi"}]}`))
	req2.Header.Set("Authorization", "Bearer "+plainKey)
	req2.Header.Set("Content-Type", "application/json")
	r.ServeHTTP(w2, req2)
	require.Equal(t, http.StatusPaymentRequired, w2.Code, "配额耗尽应 402，resp=%s", w2.Body.String())
}

// multiProtoUpstream 起一个按 path 分流的 mock 上游：
//   - /chat/completions：非流式返回带 usage 的 OpenAI 响应；stream:true 返回 SSE（含 delta + usage + [DONE]）。
//   - /embeddings：返回带 usage 的 embeddings 响应。
//   - /images/generations：返回图片 URL 列表。
//   - /messages：返回 Anthropic 格式响应（非流式 JSON 或流式 SSE）。
func multiProtoUpstream() *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := readAll(r)
		switch r.URL.Path {
		case "/chat/completions":
			if strings.Contains(string(body), `"stream":true`) {
				w.Header().Set("Content-Type", "text/event-stream")
				w.WriteHeader(http.StatusOK)
				fl, _ := w.(http.Flusher)
				_, _ = w.Write([]byte("data: {\"choices\":[{\"delta\":{\"content\":\"Hel\"}}]}\n\n"))
				if fl != nil {
					fl.Flush()
				}
				_, _ = w.Write([]byte("data: {\"choices\":[{\"delta\":{\"content\":\"lo\"},\"finish_reason\":\"stop\"}]}\n\n"))
				if fl != nil {
					fl.Flush()
				}
				_, _ = w.Write([]byte("data: {\"choices\":[],\"usage\":{\"prompt_tokens\":10,\"completion_tokens\":5,\"total_tokens\":15}}\n\n"))
				if fl != nil {
					fl.Flush()
				}
				_, _ = w.Write([]byte("data: [DONE]\n\n"))
				if fl != nil {
					fl.Flush()
				}
				return
			}
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"id":"chatok","choices":[{"message":{"content":"hello"},"finish_reason":"stop"}],"usage":{"prompt_tokens":10,"completion_tokens":5,"total_tokens":15}}`))
		case "/embeddings":
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"object":"list","data":[{"embedding":[0.1,0.2]}],"usage":{"prompt_tokens":8,"total_tokens":8}}`))
		case "/images/generations":
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"created":1,"data":[{"url":"http://img/x.png"}]}`))
		case "/v1/messages":
			// Anthropic 透传 mock：直接返回 Anthropic 格式（非流式 JSON 或流式 SSE）。
			if strings.Contains(string(body), `"stream":true`) {
				w.Header().Set("Content-Type", "text/event-stream")
				w.WriteHeader(http.StatusOK)
				fl, _ := w.(http.Flusher)
				_, _ = w.Write([]byte("event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"id\":\"msg_1\",\"type\":\"message\",\"role\":\"assistant\",\"model\":\"gpt-4o\",\"content\":[],\"usage\":{\"input_tokens\":10,\"output_tokens\":0}}}\n\n"))
				if fl != nil {
					fl.Flush()
				}
				_, _ = w.Write([]byte("event: content_block_start\ndata: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"text\",\"text\":\"\"}}\n\n"))
				if fl != nil {
					fl.Flush()
				}
				_, _ = w.Write([]byte("event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"Hel\"}}\n\n"))
				if fl != nil {
					fl.Flush()
				}
				_, _ = w.Write([]byte("event: content_block_stop\ndata: {\"type\":\"content_block_stop\",\"index\":0}\n\n"))
				if fl != nil {
					fl.Flush()
				}
				_, _ = w.Write([]byte("event: message_delta\ndata: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"end_turn\"},\"usage\":{\"output_tokens\":5}}\n\n"))
				if fl != nil {
					fl.Flush()
				}
				_, _ = w.Write([]byte("event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n"))
				if fl != nil {
					fl.Flush()
				}
				return
			}
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"id":"msg_1","type":"message","role":"assistant","content":[{"type":"text","text":"hello"}],"model":"gpt-4o","stop_reason":"end_turn","usage":{"input_tokens":10,"output_tokens":5}}`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
}

// setupMultiProto 装配一套带 mock 上游 + App + key(绑 App) + 渠道/模型 的环境，返回 (engine, plainKey, db)。
// 同时建一个 anthropic 渠道（指向同一 mock 上游）供 /v1/messages 透传测试使用。
func setupMultiProto(t *testing.T, upURL string) (*gin.Engine, string, *gorm.DB) {
	t.Helper()
	cfg := config.Config{}
	cfg.Storage.Local.Root = t.TempDir() // 审计正文本地存储落临时目录，避免测试残留
	cfg.Security.APIKeyPrefix = "sk-saig"
	cfg.Security.ChannelKeyAESKey = "0123456789abcdef0123456789abcdef"
	cfg.JWT.Secret = "p3-secret"
	cfg.JWT.ExpireHours = 1

	g := dbtest.OpenInMemory(t)
	require.NoError(t, db.Migrate(g, model.Migrations("admin", "sag12345")))
	resetRouting(g)

	r, shutdown, err := NewWithShutdown(cfg, g, nil)
	require.NoError(t, err)
	t.Cleanup(shutdown) // 先停审计 worker 等后台 goroutine，再让 TempDir 清理（Cleanup 为 LIFO）
	token := doJSON(t, r, http.MethodPost, "/admin/login", "",
		`{"username":"admin","password":"sag12345"}`, http.StatusOK)["token"].(string)

	// OpenAI 渠道：处理 /v1/chat/completions、/v1/embeddings、/v1/images/generations。
	chResp := doJSON(t, r, http.MethodPost, "/admin/channels", token,
		`{"type":"openai","name":"c1","base_url":"`+upURL+`","api_key":"sk-up"}`, http.StatusOK)
	channelID := chResp["id"].(float64)
	doJSON(t, r, http.MethodPost, "/admin/channel-models", token,
		`{"channel_id":`+itoa(channelID)+`,"model":"gpt-4o","enabled":true}`, http.StatusOK)

	// Anthropic 渠道：指向同一 mock 上游（支持 /messages），供 /v1/messages 透传使用。
	anthCh := doJSON(t, r, http.MethodPost, "/admin/channels", token,
		`{"type":"anthropic","name":"ant-c1","base_url":"`+upURL+`","api_key":"sk-ant-test"}`, http.StatusOK)
	doJSON(t, r, http.MethodPost, "/admin/channel-models", token,
		`{"channel_id":`+itoa(anthCh["id"].(float64))+`,"model":"gpt-4o","enabled":true}`, http.StatusOK)

	// 配价 gpt-4o（各协议端点均用此模型）：1/1 额度够触发计费不影响测试逻辑。
	doJSON(t, r, http.MethodPost, "/admin/prices", token,
		`{"model":"gpt-4o","input_per_1m":1,"output_per_1m":1,"enabled":true}`, http.StatusOK)

	appResp := doJSON(t, r, http.MethodPost, "/admin/apps", token, `{"name":"team-p3","quota":0}`, http.StatusOK)
	appID := appResp["id"].(float64)
	keyResp := doJSON(t, r, http.MethodPost, "/admin/api-keys", token,
		`{"name":"k1","app_id":`+itoa(appID)+`}`, http.StatusOK)
	plainKey := keyResp["key"].(string)
	require.Contains(t, plainKey, "sk-saig-")
	return r, plainKey, g
}

// TestMessagesE2E：用 x-api-key 头打 /v1/messages(Anthropic 透传) → 上游直接返回 Anthropic 格式；
// 计费落库 request_type=messages。
func TestMessagesE2E(t *testing.T) {
	gin.SetMode(gin.TestMode)
	up := multiProtoUpstream()
	defer up.Close()
	r, plainKey, g := setupMultiProto(t, up.URL)

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/messages",
		strings.NewReader(`{"model":"gpt-4o","max_tokens":100,"messages":[{"role":"user","content":"hi"}]}`))
	// 关键：用 x-api-key 头（而非 Bearer）鉴权。
	req.Header.Set("x-api-key", plainKey)
	req.Header.Set("Content-Type", "application/json")
	r.ServeHTTP(w, req)
	require.Equal(t, http.StatusOK, w.Code, "resp=%s", w.Body.String())

	// 透传模式：上游直接返回 Anthropic 格式，response body 原样透传。
	var resp map[string]any
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	require.Equal(t, "message", resp["type"])
	content := resp["content"].([]any)
	require.Equal(t, "hello", content[0].(map[string]any)["text"])

	// 计费落库 request_type=messages。
	var logRow model.RequestLog
	require.NoError(t, g.Order("id desc").First(&logRow).Error)
	require.Equal(t, "messages", logRow.RequestType)
	require.Equal(t, 10, logRow.PromptTokens)
	require.Equal(t, 5, logRow.CompletionTokens)
}

// TestMessagesStreamE2E：stream:true → 透传 Anthropic SSE，响应含原始 Anthropic SSE 事件。
func TestMessagesStreamE2E(t *testing.T) {
	gin.SetMode(gin.TestMode)
	up := multiProtoUpstream()
	defer up.Close()
	r, plainKey, _ := setupMultiProto(t, up.URL)

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/messages",
		strings.NewReader(`{"model":"gpt-4o","stream":true,"max_tokens":100,"messages":[{"role":"user","content":"hi"}]}`))
	req.Header.Set("x-api-key", plainKey)
	req.Header.Set("Content-Type", "application/json")
	r.ServeHTTP(w, req)
	require.Equal(t, http.StatusOK, w.Code)
	out := w.Body.String()
	// 透传模式：直接转发 mock 上游的 Anthropic SSE 事件（RelayAnthropic 原样透传）。
	require.Contains(t, out, "event: message_start")
	require.Contains(t, out, "event: message_stop")
	require.Contains(t, out, `"text":"Hel"`)
}

// TestTwoChannelsPerProtocolE2E 双协议上游按「一渠道一协议」建两条渠道：
// /v1/chat/completions 命中 openai 渠道、/v1/messages 命中 anthropic 渠道，各自同协议透传；
// 日志记录出入站协议。
func TestTwoChannelsPerProtocolE2E(t *testing.T) {
	gin.SetMode(gin.TestMode)
	up := multiProtoUpstream()
	defer up.Close()

	cfg := config.Config{}
	cfg.Storage.Local.Root = t.TempDir() // 审计正文本地存储落临时目录，避免测试残留
	cfg.Security.APIKeyPrefix = "sk-saig"
	cfg.Security.ChannelKeyAESKey = "0123456789abcdef0123456789abcdef"
	cfg.JWT.Secret = "p12b-secret"
	cfg.JWT.ExpireHours = 1
	g := dbtest.OpenInMemory(t)
	require.NoError(t, db.Migrate(g, model.Migrations("admin", "sag12345")))
	resetRouting(g)
	r, shutdown, err := NewWithShutdown(cfg, g, nil)
	require.NoError(t, err)
	t.Cleanup(shutdown) // 先停审计 worker 等后台 goroutine，再让 TempDir 清理（Cleanup 为 LIFO）
	token := doJSON(t, r, http.MethodPost, "/admin/login", "",
		`{"username":"admin","password":"sag12345"}`, http.StatusOK)["token"].(string)

	// 同一 mock 上游建两条渠道，分别声明 openai / anthropic 协议。
	oaResp := doJSON(t, r, http.MethodPost, "/admin/channels", token,
		`{"type":"openai","name":"dual-oa","base_url":"`+up.URL+`","api_key":"sk-up"}`, http.StatusOK)
	anResp := doJSON(t, r, http.MethodPost, "/admin/channels", token,
		`{"type":"anthropic","name":"dual-an","base_url":"`+up.URL+`","api_key":"sk-up"}`, http.StatusOK)
	doJSON(t, r, http.MethodPost, "/admin/channel-models", token,
		`{"channel_id":`+itoa(oaResp["id"].(float64))+`,"model":"gpt-4o","enabled":true}`, http.StatusOK)
	doJSON(t, r, http.MethodPost, "/admin/channel-models", token,
		`{"channel_id":`+itoa(anResp["id"].(float64))+`,"model":"gpt-4o","enabled":true}`, http.StatusOK)
	doJSON(t, r, http.MethodPost, "/admin/prices", token,
		`{"model":"gpt-4o","input_per_1m":1,"output_per_1m":1,"enabled":true}`, http.StatusOK)
	appResp := doJSON(t, r, http.MethodPost, "/admin/apps", token, `{"name":"team-dual","quota":0}`, http.StatusOK)
	keyResp := doJSON(t, r, http.MethodPost, "/admin/api-keys", token,
		`{"name":"k1","app_id":`+itoa(appResp["id"].(float64))+`}`, http.StatusOK)
	plainKey := keyResp["key"].(string)

	// openai 入站 → openai 渠道
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions",
		strings.NewReader(`{"model":"gpt-4o","messages":[{"role":"user","content":"hi"}]}`))
	req.Header.Set("Authorization", "Bearer "+plainKey)
	req.Header.Set("Content-Type", "application/json")
	r.ServeHTTP(w, req)
	require.Equal(t, http.StatusOK, w.Code, "chat resp=%s", w.Body.String())

	// anthropic 入站 → anthropic 渠道
	w2 := httptest.NewRecorder()
	req2 := httptest.NewRequest(http.MethodPost, "/v1/messages",
		strings.NewReader(`{"model":"gpt-4o","max_tokens":100,"messages":[{"role":"user","content":"hi"}]}`))
	req2.Header.Set("x-api-key", plainKey)
	req2.Header.Set("Content-Type", "application/json")
	r.ServeHTTP(w2, req2)
	require.Equal(t, http.StatusOK, w2.Code, "messages resp=%s", w2.Body.String())

	// 日志协议列：chat=openai/openai 命中 openai 渠道，messages=anthropic/anthropic 命中 anthropic 渠道。
	var logs []model.RequestLog
	require.NoError(t, g.Order("id asc").Find(&logs).Error)
	require.Len(t, logs, 2)
	require.Equal(t, "openai", logs[0].Protocol)
	require.Equal(t, "openai", logs[0].UpstreamProtocol)
	require.Equal(t, uint64(oaResp["id"].(float64)), logs[0].ChannelID)
	require.Equal(t, "anthropic", logs[1].Protocol)
	require.Equal(t, "anthropic", logs[1].UpstreamProtocol)
	require.Equal(t, uint64(anResp["id"].(float64)), logs[1].ChannelID)
}

// TestChannelTypeValidationE2E 渠道协议(type)只接受 openai/anthropic，非法值 400；
// 未传 type 默认 openai；type 可经更新修改。
func TestChannelTypeValidationE2E(t *testing.T) {
	gin.SetMode(gin.TestMode)
	cfg := config.Config{}
	cfg.Storage.Local.Root = t.TempDir() // 审计正文本地存储落临时目录，避免测试残留
	cfg.Security.APIKeyPrefix = "sk-saig"
	cfg.Security.ChannelKeyAESKey = "0123456789abcdef0123456789abcdef"
	cfg.JWT.Secret = "p12b-secret"
	cfg.JWT.ExpireHours = 1
	g := dbtest.OpenInMemory(t)
	require.NoError(t, db.Migrate(g, model.Migrations("admin", "sag12345")))
	resetRouting(g)
	r, shutdown, err := NewWithShutdown(cfg, g, nil)
	require.NoError(t, err)
	t.Cleanup(shutdown) // 先停审计 worker 等后台 goroutine，再让 TempDir 清理（Cleanup 为 LIFO）
	token := doJSON(t, r, http.MethodPost, "/admin/login", "",
		`{"username":"admin","password":"sag12345"}`, http.StatusOK)["token"].(string)

	// 创建时非法 type → 400
	doJSON(t, r, http.MethodPost, "/admin/channels", token,
		`{"type":"gemini","name":"bad-type","base_url":"http://u","api_key":"k"}`, http.StatusBadRequest)

	// 未传 type → 默认 openai
	resp := doJSON(t, r, http.MethodPost, "/admin/channels", token,
		`{"name":"oa","base_url":"http://u","api_key":"k"}`, http.StatusOK)
	require.Equal(t, "openai", resp["type"])
	id := itoa(resp["id"].(float64))

	// 更新 type → 生效
	doJSON(t, r, http.MethodPost, "/admin/channels/"+id, token, `{"type":"anthropic"}`, http.StatusOK)
	var ch model.Channel
	require.NoError(t, g.First(&ch, resp["id"]).Error)
	require.Equal(t, "anthropic", ch.Type)

	// 更新时非法 type → 400
	doJSON(t, r, http.MethodPost, "/admin/channels/"+id, token, `{"type":"gemini"}`, http.StatusBadRequest)
}

// TestBatchAliasRoutesE2E 批量建规范名别名：新建别名+边；别名已存在则复用（追加边）；
// 重复边跳过；ListAliases 附带 route_count。
func TestBatchAliasRoutesE2E(t *testing.T) {
	gin.SetMode(gin.TestMode)
	cfg := config.Config{}
	cfg.Storage.Local.Root = t.TempDir() // 审计正文本地存储落临时目录，避免测试残留
	cfg.Security.APIKeyPrefix = "sk-saig"
	cfg.Security.ChannelKeyAESKey = "0123456789abcdef0123456789abcdef"
	cfg.JWT.Secret = "batch-secret"
	cfg.JWT.ExpireHours = 1
	g := dbtest.OpenInMemory(t)
	require.NoError(t, db.Migrate(g, model.Migrations("admin", "sag12345")))
	resetRouting(g)
	r, shutdown, err := NewWithShutdown(cfg, g, nil)
	require.NoError(t, err)
	t.Cleanup(shutdown) // 先停审计 worker 等后台 goroutine，再让 TempDir 清理（Cleanup 为 LIFO）
	token := doJSON(t, r, http.MethodPost, "/admin/login", "",
		`{"username":"admin","password":"sag12345"}`, http.StatusOK)["token"].(string)

	ch1 := doJSON(t, r, http.MethodPost, "/admin/channels", token,
		`{"type":"openai","name":"c1","base_url":"http://u1","api_key":"k"}`, http.StatusOK)
	ch2 := doJSON(t, r, http.MethodPost, "/admin/channels", token,
		`{"type":"openai","name":"c2","base_url":"http://u2","api_key":"k"}`, http.StatusOK)
	id1, id2 := itoa(ch1["id"].(float64)), itoa(ch2["id"].(float64))

	// 第一批：渠道1 两个版本名 → 新建 2 别名 + 2 边
	res := doJSON(t, r, http.MethodPost, "/admin/alias-routes/batch", token,
		`{"items":[{"alias":"glm-5.2","channel_id":`+id1+`,"model":"glm-5.2-20260505"},{"alias":"qwen-max","channel_id":`+id1+`,"model":"qwen-max-0125"}]}`, http.StatusOK)
	require.Equal(t, float64(2), res["created"])
	require.Equal(t, float64(0), res["reused_alias"])
	require.Equal(t, float64(0), res["skipped"])

	// 第二批：同别名换渠道 → 复用别名加边；完全重复项 → 跳过
	res = doJSON(t, r, http.MethodPost, "/admin/alias-routes/batch", token,
		`{"items":[{"alias":"glm-5.2","channel_id":`+id2+`,"model":"glm-5.2-latest"},{"alias":"glm-5.2","channel_id":`+id1+`,"model":"glm-5.2-20260505"}]}`, http.StatusOK)
	require.Equal(t, float64(1), res["created"])
	require.Equal(t, float64(2), res["reused_alias"], "两项都命中已有别名")
	require.Equal(t, float64(1), res["skipped"], "重复边应跳过")

	// 别名唯一：glm-5.2 只有一条记录、两条边；qwen-max 一条边（route_count 随 ListAliases 返回）
	list := doJSONArray(t, r, http.MethodGet, "/admin/aliases", token, "", http.StatusOK)
	byName := map[string]map[string]any{}
	for _, it := range list {
		m := it.(map[string]any)
		byName[m["alias"].(string)] = m
	}
	require.Len(t, byName, 2)
	require.Equal(t, float64(2), byName["glm-5.2"]["route_count"])
	require.Equal(t, float64(1), byName["qwen-max"]["route_count"])

	// 空 items → 400
	doJSON(t, r, http.MethodPost, "/admin/alias-routes/batch", token, `{"items":[]}`, http.StatusBadRequest)

	// —— 别名/路由边编辑端点 ——
	// 重名别名 → 409 冲突带友好提示（而非撞唯一索引 500）
	dup := doJSON(t, r, http.MethodPost, "/admin/aliases", token, `{"alias":"glm-5.2"}`, http.StatusConflict)
	require.Equal(t, "别名已存在", dup["error"].(map[string]any)["message"])

	// 更新别名说明与启停
	var al model.ModelAlias
	require.NoError(t, g.Where("alias = ?", "glm-5.2").First(&al).Error)
	doJSON(t, r, http.MethodPost, "/admin/aliases/"+itoa(float64(al.ID)), token,
		`{"description":"规范名","status":0}`, http.StatusOK)
	require.NoError(t, g.First(&al, al.ID).Error)
	require.Equal(t, "规范名", al.Description)
	require.Equal(t, int8(0), al.Status)

	// 更新路由边权重/优先级（weight<1 钳到 1）
	var rt model.AliasRoute
	require.NoError(t, g.Where("alias_id = ?", al.ID).First(&rt).Error)
	doJSON(t, r, http.MethodPost, "/admin/alias-routes/"+itoa(float64(rt.ID)), token,
		`{"weight":5,"priority":2}`, http.StatusOK)
	require.NoError(t, g.First(&rt, rt.ID).Error)
	require.Equal(t, 5, rt.Weight)
	require.Equal(t, 2, rt.Priority)
	doJSON(t, r, http.MethodPost, "/admin/alias-routes/"+itoa(float64(rt.ID)), token,
		`{"weight":0,"priority":2}`, http.StatusOK)
	require.NoError(t, g.First(&rt, rt.ID).Error)
	require.Equal(t, 1, rt.Weight, "weight<1 应钳到 1")
}

// TestEmbeddingsE2E：打 /v1/embeddings → 按 prompt_tokens 计费，request_type=embeddings。
func TestEmbeddingsE2E(t *testing.T) {
	gin.SetMode(gin.TestMode)
	up := multiProtoUpstream()
	defer up.Close()
	r, plainKey, g := setupMultiProto(t, up.URL)

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/embeddings",
		strings.NewReader(`{"model":"gpt-4o","input":"hi"}`))
	req.Header.Set("Authorization", "Bearer "+plainKey)
	req.Header.Set("Content-Type", "application/json")
	r.ServeHTTP(w, req)
	require.Equal(t, http.StatusOK, w.Code, "resp=%s", w.Body.String())
	require.Contains(t, w.Body.String(), `"embedding"`)

	var logRow model.RequestLog
	require.NoError(t, g.Order("id desc").First(&logRow).Error)
	require.Equal(t, "embeddings", logRow.RequestType)
	require.Equal(t, 8, logRow.PromptTokens)
}

// TestImagesE2E：price type=times per_call=40000 → /v1/images/generations → App.used_quota += 40000，log request_type=images。
func TestImagesE2E(t *testing.T) {
	gin.SetMode(gin.TestMode)
	up := multiProtoUpstream()
	defer up.Close()

	cfg := config.Config{}
	cfg.Storage.Local.Root = t.TempDir() // 审计正文本地存储落临时目录，避免测试残留
	cfg.Security.APIKeyPrefix = "sk-saig"
	cfg.Security.ChannelKeyAESKey = "0123456789abcdef0123456789abcdef"
	cfg.JWT.Secret = "p3-img-secret"
	cfg.JWT.ExpireHours = 1

	g := dbtest.OpenInMemory(t)
	require.NoError(t, db.Migrate(g, model.Migrations("admin", "sag12345")))
	resetRouting(g)
	r, shutdown, err := NewWithShutdown(cfg, g, nil)
	require.NoError(t, err)
	t.Cleanup(shutdown) // 先停审计 worker 等后台 goroutine，再让 TempDir 清理（Cleanup 为 LIFO）
	token := doJSON(t, r, http.MethodPost, "/admin/login", "",
		`{"username":"admin","password":"sag12345"}`, http.StatusOK)["token"].(string)

	chResp := doJSON(t, r, http.MethodPost, "/admin/channels", token,
		`{"type":"openai","name":"c1","base_url":"`+up.URL+`","api_key":"sk-up"}`, http.StatusOK)
	channelID := chResp["id"].(float64)
	doJSON(t, r, http.MethodPost, "/admin/channel-models", token,
		`{"channel_id":`+itoa(channelID)+`,"model":"dall-e","enabled":true}`, http.StatusOK)

	// 按次计费价格
	doJSON(t, r, http.MethodPost, "/admin/prices", token, `{"model":"dall-e","type":"times","per_call_micros":40000,"enabled":true}`, http.StatusOK)

	appResp := doJSON(t, r, http.MethodPost, "/admin/apps", token, `{"name":"team-img","quota":0}`, http.StatusOK)
	appID := uint64(appResp["id"].(float64))
	keyResp := doJSON(t, r, http.MethodPost, "/admin/api-keys", token,
		`{"name":"k1","app_id":`+strconv.FormatUint(appID, 10)+`}`, http.StatusOK)
	plainKey := keyResp["key"].(string)

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/images/generations",
		strings.NewReader(`{"model":"dall-e","prompt":"a cat"}`))
	req.Header.Set("Authorization", "Bearer "+plainKey)
	req.Header.Set("Content-Type", "application/json")
	r.ServeHTTP(w, req)
	require.Equal(t, http.StatusOK, w.Code, "resp=%s", w.Body.String())

	// App.used_quota += 40000。
	var appRow model.App
	require.NoError(t, g.First(&appRow, appID).Error)
	require.Equal(t, int64(40000), appRow.UsedQuota)

	var logRow model.RequestLog
	require.NoError(t, g.Order("id desc").First(&logRow).Error)
	require.Equal(t, "images", logRow.RequestType)
	require.Equal(t, int64(40000), logRow.CostMicros)
}

// TestModelsE2E：GET /v1/models 返回含别名 + 渠道模型 id。
func TestModelsE2E(t *testing.T) {
	gin.SetMode(gin.TestMode)

	cfg := config.Config{}
	cfg.Storage.Local.Root = t.TempDir() // 审计正文本地存储落临时目录，避免测试残留
	cfg.Security.APIKeyPrefix = "sk-saig"
	cfg.Security.ChannelKeyAESKey = "0123456789abcdef0123456789abcdef"
	cfg.JWT.Secret = "p3-models-secret"
	cfg.JWT.ExpireHours = 1

	g := dbtest.OpenInMemory(t)
	require.NoError(t, db.Migrate(g, model.Migrations("admin", "sag12345")))
	resetRouting(g)

	// 渠道模型 gpt-4o + 别名 smart（需加路由边才能通过路由表过滤）。
	ch := model.Channel{Name: "c", BaseURL: "http://x", Status: 1, Timeout: 30}
	require.NoError(t, g.Create(&ch).Error)
	require.NoError(t, g.Create(&model.ChannelModel{ChannelID: ch.ID, Model: "gpt-4o", Enabled: true}).Error)
	al := model.ModelAlias{Alias: "smart", Status: 1}
	require.NoError(t, g.Create(&al).Error)
	// 别名必须有路由边才能出现在 /v1/models 列表（无边的别名路由无意义）
	require.NoError(t, g.Create(&model.AliasRoute{AliasID: al.ID, ChannelID: ch.ID, Model: "gpt-4o", Weight: 1, Priority: 1}).Error)

	r, shutdown, err := NewWithShutdown(cfg, g, nil)
	require.NoError(t, err)
	t.Cleanup(shutdown) // 先停审计 worker 等后台 goroutine，再让 TempDir 清理（Cleanup 为 LIFO）
	keySvc := apikey.NewService(apikey.NewRepo(g), cfg.Security.APIKeyPrefix)
	plainKey, _, err := keySvc.Generate("k", nil)
	require.NoError(t, err)

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/v1/models", nil)
	req.Header.Set("Authorization", "Bearer "+plainKey)
	r.ServeHTTP(w, req)
	require.Equal(t, http.StatusOK, w.Code, "resp=%s", w.Body.String())

	var resp struct {
		Object string `json:"object"`
		Data   []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	require.Equal(t, "list", resp.Object)
	ids := map[string]bool{}
	for _, d := range resp.Data {
		ids[d.ID] = true
	}
	require.True(t, ids["gpt-4o"], "应含渠道真实模型 gpt-4o，got=%v", ids)
	require.True(t, ids["smart"], "应含别名 smart，got=%v", ids)
}

// TestAccountPoolWeightedE2E 端到端验证密钥池加权轮换：同一渠道下两个账号(weight 70/30)，
// 各账号 base_url 覆盖指向不同 mock 上游；发 N 次 chat，断言两账号都被命中、高权重明显更多，
// 且请求日志落了 account_id。
func TestAccountPoolWeightedE2E(t *testing.T) {
	gin.SetMode(gin.TestMode)

	var hitsA, hitsB int
	mkUpstream := func(counter *int) *httptest.Server {
		return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			*counter++
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"id":"ok","choices":[]}`))
		}))
	}
	upA := mkUpstream(&hitsA)
	defer upA.Close()
	upB := mkUpstream(&hitsB)
	defer upB.Close()

	cfg := config.Config{}
	cfg.Storage.Local.Root = t.TempDir() // 审计正文本地存储落临时目录，避免测试残留
	cfg.Security.APIKeyPrefix = "sk-saig"
	cfg.Security.ChannelKeyAESKey = "0123456789abcdef0123456789abcdef"
	cfg.JWT.Secret = "p5-weight"
	cfg.JWT.ExpireHours = 1

	g := dbtest.OpenInMemory(t)
	require.NoError(t, db.Migrate(g, model.Migrations("admin", "sag12345")))
	resetRouting(g)

	// 单渠道直调 gpt-4o；渠道 base_url 留空，由两个账号各自覆盖到不同上游。
	enc, _ := crypto.Encrypt(cfg.Security.ChannelKeyAESKey, "sk-up")
	ch := model.Channel{Name: "c", BaseURL: "http://placeholder", Status: 1, Timeout: 30}
	require.NoError(t, g.Create(&ch).Error)
	require.NoError(t, g.Create(&model.ChannelModel{ChannelID: ch.ID, Model: "gpt-4o", Enabled: true}).Error)
	// 两个账号：weight 70 → upA、weight 30 → upB（base_url 覆盖）。
	require.NoError(t, g.Create(&model.Account{ChannelID: ch.ID, Name: "acc-a", APIKeyEnc: enc, BaseURL: upA.URL, Weight: 70, Status: 1}).Error)
	require.NoError(t, g.Create(&model.Account{ChannelID: ch.ID, Name: "acc-b", APIKeyEnc: enc, BaseURL: upB.URL, Weight: 30, Status: 1}).Error)
	// 配价 gpt-4o：1/1 额度够触发价格前置校验通过。
	require.NoError(t, g.Create(&model.ModelPrice{Model: "gpt-4o", InputPer1M: 1, OutputPer1M: 1, Enabled: true}).Error)

	r, shutdown, err := NewWithShutdown(cfg, g, nil)
	require.NoError(t, err)
	t.Cleanup(shutdown) // 先停审计 worker 等后台 goroutine，再让 TempDir 清理（Cleanup 为 LIFO）
	keySvc := apikey.NewService(apikey.NewRepo(g), cfg.Security.APIKeyPrefix)
	plainKey, _, err := keySvc.Generate("k", nil)
	require.NoError(t, err)

	const N = 300
	for i := 0; i < N; i++ {
		w := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions",
			strings.NewReader(`{"model":"gpt-4o","messages":[]}`))
		req.Header.Set("Authorization", "Bearer "+plainKey)
		req.Header.Set("Content-Type", "application/json")
		r.ServeHTTP(w, req)
		require.Equal(t, http.StatusOK, w.Code, "resp=%s", w.Body.String())
	}

	require.Equal(t, N, hitsA+hitsB, "每次请求应恰好命中一个账号上游")
	require.Greater(t, hitsA, 0, "高权重账号应被命中")
	require.Greater(t, hitsB, 0, "低权重账号也应被命中")
	require.Greater(t, hitsA, hitsB, "高权重(70)命中应明显多于低权重(30)，A=%d B=%d", hitsA, hitsB)
	t.Logf("密钥池加权命中分布: A(w70)=%d B(w30)=%d", hitsA, hitsB)

	// 请求日志落了 account_id（应同时出现两个账号 id）。
	var accIDs []uint64
	require.NoError(t, g.Model(&model.RequestLog{}).Distinct().Order("account_id asc").Pluck("account_id", &accIDs).Error)
	nonZero := 0
	for _, id := range accIDs {
		if id != 0 {
			nonZero++
		}
	}
	require.GreaterOrEqual(t, nonZero, 2, "请求日志应记录到两个不同账号 id，got=%v", accIDs)
}

// TestAccountFailoverE2E 端到端验证账号级换号：同一渠道两个账号，A 指向 429 上游、B 指向好上游。
// 渠道 MaxRetries=1（=渠道内换账号次数），发请求 → A 失败触发账号熔断/换号 → 自动用 B 成功。
func TestAccountFailoverE2E(t *testing.T) {
	gin.SetMode(gin.TestMode)

	// A：始终 429（触发账号级立即熔断）；B：好上游。
	bad := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusTooManyRequests)
	}))
	defer bad.Close()
	var goodHits int
	good := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		goodHits++
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"id":"ok","choices":[]}`))
	}))
	defer good.Close()

	cfg := config.Config{}
	cfg.Storage.Local.Root = t.TempDir() // 审计正文本地存储落临时目录，避免测试残留
	cfg.Security.APIKeyPrefix = "sk-saig"
	cfg.Security.ChannelKeyAESKey = "0123456789abcdef0123456789abcdef"
	cfg.JWT.Secret = "p5-failover"
	cfg.JWT.ExpireHours = 1

	g := dbtest.OpenInMemory(t)
	require.NoError(t, db.Migrate(g, model.Migrations("admin", "sag12345")))
	resetRouting(g)

	enc, _ := crypto.Encrypt(cfg.Security.ChannelKeyAESKey, "sk-up")
	// 关键：渠道 MaxRetries=1，才会在渠道内换账号重试。
	ch := model.Channel{Name: "c", BaseURL: "http://placeholder", Status: 1, Timeout: 30, MaxRetries: 1}
	require.NoError(t, g.Create(&ch).Error)
	require.NoError(t, g.Create(&model.ChannelModel{ChannelID: ch.ID, Model: "gpt-4o", Enabled: true}).Error)
	require.NoError(t, g.Create(&model.Account{ChannelID: ch.ID, Name: "acc-bad", APIKeyEnc: enc, BaseURL: bad.URL, Weight: 1, Status: 1}).Error)
	require.NoError(t, g.Create(&model.Account{ChannelID: ch.ID, Name: "acc-good", APIKeyEnc: enc, BaseURL: good.URL, Weight: 1, Status: 1}).Error)
	// 配价 gpt-4o：1/1 额度够触发价格前置校验通过。
	require.NoError(t, g.Create(&model.ModelPrice{Model: "gpt-4o", InputPer1M: 1, OutputPer1M: 1, Enabled: true}).Error)

	r, shutdown, err := NewWithShutdown(cfg, g, nil)
	require.NoError(t, err)
	t.Cleanup(shutdown) // 先停审计 worker 等后台 goroutine，再让 TempDir 清理（Cleanup 为 LIFO）
	keySvc := apikey.NewService(apikey.NewRepo(g), cfg.Security.APIKeyPrefix)
	plainKey, _, err := keySvc.Generate("k", nil)
	require.NoError(t, err)

	// 多发几次：无论首选 A 还是 B，A 一旦 429 立即熔断，后续/重试都换到 B 成功。
	for i := 0; i < 5; i++ {
		w := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions",
			strings.NewReader(`{"model":"gpt-4o","messages":[]}`))
		req.Header.Set("Authorization", "Bearer "+plainKey)
		req.Header.Set("Content-Type", "application/json")
		r.ServeHTTP(w, req)
		require.Equal(t, http.StatusOK, w.Code, "应换号到 good 账号成功，resp=%s", w.Body.String())
		require.Contains(t, w.Body.String(), `"id":"ok"`)
	}
	require.Greater(t, goodHits, 0, "good 账号应被命中")
}

// TestProtocolAccount429FailoverE2E 覆盖 C1：协议端点(embeddings/走 DispatchRaw)遇账号 429 应换号。
// 同一渠道两账号：A 指向恒 429 上游、B 指向好 embeddings 上游，渠道 MaxRetries=1。
// 打 /v1/embeddings → 断言最终 200 且只命中 B（A 的 429 被换号、不透传客户端）。
func TestProtocolAccount429FailoverE2E(t *testing.T) {
	gin.SetMode(gin.TestMode)

	// A：embeddings 始终 429；B：好 embeddings 上游。
	bad := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusTooManyRequests)
	}))
	defer bad.Close()
	var goodHits int
	good := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		goodHits++
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"object":"list","data":[{"embedding":[0.1,0.2]}],"usage":{"prompt_tokens":8,"total_tokens":8}}`))
	}))
	defer good.Close()

	cfg := config.Config{}
	cfg.Storage.Local.Root = t.TempDir() // 审计正文本地存储落临时目录，避免测试残留
	cfg.Security.APIKeyPrefix = "sk-saig"
	cfg.Security.ChannelKeyAESKey = "0123456789abcdef0123456789abcdef"
	cfg.JWT.Secret = "p5-proto-429"
	cfg.JWT.ExpireHours = 1

	g := dbtest.OpenInMemory(t)
	require.NoError(t, db.Migrate(g, model.Migrations("admin", "sag12345")))
	resetRouting(g)

	enc, _ := crypto.Encrypt(cfg.Security.ChannelKeyAESKey, "sk-up")
	// 渠道 MaxRetries=1，才会在渠道内换账号重试。
	ch := model.Channel{Name: "c", BaseURL: "http://placeholder", Status: 1, Timeout: 30, MaxRetries: 1}
	require.NoError(t, g.Create(&ch).Error)
	require.NoError(t, g.Create(&model.ChannelModel{ChannelID: ch.ID, Model: "gpt-4o", Enabled: true}).Error)
	accBad := model.Account{ChannelID: ch.ID, Name: "acc-bad", APIKeyEnc: enc, BaseURL: bad.URL, Weight: 1, Status: 1}
	accGood := model.Account{ChannelID: ch.ID, Name: "acc-good", APIKeyEnc: enc, BaseURL: good.URL, Weight: 1, Status: 1}
	require.NoError(t, g.Create(&accBad).Error)
	require.NoError(t, g.Create(&accGood).Error)
	// 配价 gpt-4o：1/1 额度够触发价格前置校验通过。
	require.NoError(t, g.Create(&model.ModelPrice{Model: "gpt-4o", InputPer1M: 1, OutputPer1M: 1, Enabled: true}).Error)

	r, shutdown, err := NewWithShutdown(cfg, g, nil)
	require.NoError(t, err)
	t.Cleanup(shutdown) // 先停审计 worker 等后台 goroutine，再让 TempDir 清理（Cleanup 为 LIFO）
	keySvc := apikey.NewService(apikey.NewRepo(g), cfg.Security.APIKeyPrefix)
	plainKey, _, err := keySvc.Generate("k", nil)
	require.NoError(t, err)

	// 多发几次：无论首选 A 还是 B，A 一旦 429 立即熔断/被排除，重试换到 B 成功。
	for i := 0; i < 5; i++ {
		w := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, "/v1/embeddings",
			strings.NewReader(`{"model":"gpt-4o","input":"hi"}`))
		req.Header.Set("Authorization", "Bearer "+plainKey)
		req.Header.Set("Content-Type", "application/json")
		r.ServeHTTP(w, req)
		require.Equal(t, http.StatusOK, w.Code, "协议端点遇账号 429 应换号到 good，resp=%s", w.Body.String())
		require.Contains(t, w.Body.String(), `"embedding"`)
	}
	require.Greater(t, goodHits, 0, "good 账号应被命中")

	// 请求日志应只落 good 账号 id（A 全程失败、不作为成功账号落库）。
	var logRow model.RequestLog
	require.NoError(t, g.Order("id desc").First(&logRow).Error)
	require.Equal(t, accGood.ID, logRow.AccountID, "成功账号应为 good")
}

// TestAccountNon429FailoverE2E 覆盖 I2：账号普通失败(500，非 429)时也应换号、且不重选回坏账号。
// 同一渠道两账号：A 指向恒 500 上游、B 指向好上游，渠道 MaxRetries=1。
// 打 chat → 断言成功且成功账号是 B（重试排除了刚失败的 A，不会再选回它）。
func TestAccountNon429FailoverE2E(t *testing.T) {
	gin.SetMode(gin.TestMode)

	// A：始终 500（普通失败，不触发账号立即熔断）；B：好上游。
	bad := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer bad.Close()
	var goodHits int
	good := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		goodHits++
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"id":"ok","choices":[]}`))
	}))
	defer good.Close()

	cfg := config.Config{}
	cfg.Storage.Local.Root = t.TempDir() // 审计正文本地存储落临时目录，避免测试残留
	cfg.Security.APIKeyPrefix = "sk-saig"
	cfg.Security.ChannelKeyAESKey = "0123456789abcdef0123456789abcdef"
	cfg.JWT.Secret = "p5-non429"
	cfg.JWT.ExpireHours = 1

	g := dbtest.OpenInMemory(t)
	require.NoError(t, db.Migrate(g, model.Migrations("admin", "sag12345")))
	resetRouting(g)

	enc, _ := crypto.Encrypt(cfg.Security.ChannelKeyAESKey, "sk-up")
	// 渠道 MaxRetries=1，单次请求内换账号一次。
	ch := model.Channel{Name: "c", BaseURL: "http://placeholder", Status: 1, Timeout: 30, MaxRetries: 1}
	require.NoError(t, g.Create(&ch).Error)
	require.NoError(t, g.Create(&model.ChannelModel{ChannelID: ch.ID, Model: "gpt-4o", Enabled: true}).Error)
	accBad := model.Account{ChannelID: ch.ID, Name: "acc-bad", APIKeyEnc: enc, BaseURL: bad.URL, Weight: 1, Status: 1}
	accGood := model.Account{ChannelID: ch.ID, Name: "acc-good", APIKeyEnc: enc, BaseURL: good.URL, Weight: 1, Status: 1}
	require.NoError(t, g.Create(&accBad).Error)
	require.NoError(t, g.Create(&accGood).Error)
	// 配价 gpt-4o：1/1 额度够触发价格前置校验通过。
	require.NoError(t, g.Create(&model.ModelPrice{Model: "gpt-4o", InputPer1M: 1, OutputPer1M: 1, Enabled: true}).Error)

	r, shutdown, err := NewWithShutdown(cfg, g, nil)
	require.NoError(t, err)
	t.Cleanup(shutdown) // 先停审计 worker 等后台 goroutine，再让 TempDir 清理（Cleanup 为 LIFO）
	keySvc := apikey.NewService(apikey.NewRepo(g), cfg.Security.APIKeyPrefix)
	plainKey, _, err := keySvc.Generate("k", nil)
	require.NoError(t, err)

	// 500 不会让 A 立即熔断，故关键在于重试时排除 A（LookupExcluding）。
	// 多发几次：每次无论首选谁，重试都不会选回刚失败的 A → 落到 B 成功。
	for i := 0; i < 5; i++ {
		w := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions",
			strings.NewReader(`{"model":"gpt-4o","messages":[]}`))
		req.Header.Set("Authorization", "Bearer "+plainKey)
		req.Header.Set("Content-Type", "application/json")
		r.ServeHTTP(w, req)
		require.Equal(t, http.StatusOK, w.Code, "非 429 失败也应换号到 good，resp=%s", w.Body.String())
		require.Contains(t, w.Body.String(), `"id":"ok"`)
	}
	require.Greater(t, goodHits, 0, "good 账号应被命中")

	// 成功落库账号应为 good（重试未选回坏账号 A）。
	var logRow model.RequestLog
	require.NoError(t, g.Order("id desc").First(&logRow).Error)
	require.Equal(t, accGood.ID, logRow.AccountID, "成功账号应为 good，不是被排除的 bad")
}

// TestAccountAdminCRUDE2E 端到端验证 admin 账号(密钥池) CRUD：
// 登录→建平台/渠道(联动建默认账号)→GET 列默认账号(1 个)→POST 加第二账号→列表 2 个→
// POST status 停用→DELETE 删第二账号→列表回 1。
func TestAccountAdminCRUDE2E(t *testing.T) {
	gin.SetMode(gin.TestMode)

	cfg := config.Config{}
	cfg.Storage.Local.Root = t.TempDir() // 审计正文本地存储落临时目录，避免测试残留
	cfg.Security.APIKeyPrefix = "sk-saig"
	cfg.Security.ChannelKeyAESKey = "0123456789abcdef0123456789abcdef"
	cfg.JWT.Secret = "p5b-account-crud"
	cfg.JWT.ExpireHours = 1

	g := dbtest.OpenInMemory(t)
	require.NoError(t, db.Migrate(g, model.Migrations("admin", "sag12345")))
	resetRouting(g)
	r, shutdown, err := NewWithShutdown(cfg, g, nil)
	require.NoError(t, err)
	t.Cleanup(shutdown) // 先停审计 worker 等后台 goroutine，再让 TempDir 清理（Cleanup 为 LIFO）

	token := doJSON(t, r, http.MethodPost, "/admin/login", "",
		`{"username":"admin","password":"sag12345"}`, http.StatusOK)["token"].(string)
	require.NotEmpty(t, token)

	// 建渠道（CreateChannel 联动建一个默认账号）。
	chResp := doJSON(t, r, http.MethodPost, "/admin/channels", token,
		`{"type":"openai","name":"c1","base_url":"http://x","api_key":"sk-up"}`, http.StatusOK)
	channelID := chResp["id"].(float64)
	require.NotZero(t, channelID)

	// GET /admin/accounts?channel_id= → 含默认账号（1 个）。
	accs := doJSONArray(t, r, http.MethodGet, "/admin/accounts?channel_id="+itoa(channelID), token, "", http.StatusOK)
	require.Len(t, accs, 1, "建渠道应联动 1 个默认账号")

	// channel_id 缺失 → 400。
	doJSON(t, r, http.MethodGet, "/admin/accounts", token, "", http.StatusBadRequest)

	// POST /admin/accounts 加第二账号。
	acc2 := doJSON(t, r, http.MethodPost, "/admin/accounts", token,
		`{"channel_id":`+itoa(channelID)+`,"name":"账号2","api_key":"sk-up-2","base_url":"http://y","weight":3,"priority":5}`,
		http.StatusOK)
	acc2ID := acc2["id"].(float64)
	require.NotZero(t, acc2ID)
	require.Equal(t, float64(3), acc2["weight"])
	require.Equal(t, float64(5), acc2["priority"])
	require.Equal(t, float64(1), acc2["status"], "新账号默认启用")

	// 列表应有 2 个。
	accs = doJSONArray(t, r, http.MethodGet, "/admin/accounts?channel_id="+itoa(channelID), token, "", http.StatusOK)
	require.Len(t, accs, 2)

	// 凭证已加密落库且可解密回明文（密文不出 JSON，直接查库验证）。
	var acc2Row model.Account
	require.NoError(t, g.First(&acc2Row, uint64(acc2ID)).Error)
	require.NotEmpty(t, acc2Row.APIKeyEnc)
	plain, err := crypto.Decrypt(cfg.Security.ChannelKeyAESKey, acc2Row.APIKeyEnc)
	require.NoError(t, err)
	require.Equal(t, "sk-up-2", plain)

	// POST status 停用第二账号。
	doJSON(t, r, http.MethodPost, "/admin/accounts/"+itoa(acc2ID)+"/status", token, `{"status":0}`, http.StatusOK)
	require.NoError(t, g.First(&acc2Row, uint64(acc2ID)).Error)
	require.Equal(t, int8(0), acc2Row.Status, "停用后 status=0")

	// DELETE 第二账号 → 列表回 1。
	doJSON(t, r, http.MethodDelete, "/admin/accounts/"+itoa(acc2ID), token, "", http.StatusOK)
	accs = doJSONArray(t, r, http.MethodGet, "/admin/accounts?channel_id="+itoa(channelID), token, "", http.StatusOK)
	require.Len(t, accs, 1, "删除第二账号后应回 1 个")
}

// TestSetAccountProxyE2E 端到端验证 admin 账号改绑代理组端点：
// 建渠道(联动默认账号)→默认 proxy_group 为空(直连)→POST /admin/accounts/:id/proxy 置 "pool-a"→
// 查库 ProxyGroup=="pool-a"→再置 "" 恢复直连。
func TestSetAccountProxyE2E(t *testing.T) {
	gin.SetMode(gin.TestMode)

	cfg := config.Config{}
	cfg.Storage.Local.Root = t.TempDir() // 审计正文本地存储落临时目录，避免测试残留
	cfg.Security.APIKeyPrefix = "sk-saig"
	cfg.Security.ChannelKeyAESKey = "0123456789abcdef0123456789abcdef"
	cfg.JWT.Secret = "p6b-account-proxy"
	cfg.JWT.ExpireHours = 1

	g := dbtest.OpenInMemory(t)
	require.NoError(t, db.Migrate(g, model.Migrations("admin", "sag12345")))
	resetRouting(g)
	r, shutdown, err := NewWithShutdown(cfg, g, nil)
	require.NoError(t, err)
	t.Cleanup(shutdown) // 先停审计 worker 等后台 goroutine，再让 TempDir 清理（Cleanup 为 LIFO）

	token := doJSON(t, r, http.MethodPost, "/admin/login", "",
		`{"username":"admin","password":"sag12345"}`, http.StatusOK)["token"].(string)
	require.NotEmpty(t, token)

	// 建渠道（联动建一个默认账号）。
	chResp := doJSON(t, r, http.MethodPost, "/admin/channels", token,
		`{"type":"openai","name":"c1","base_url":"http://x","api_key":"sk-up"}`, http.StatusOK)
	channelID := chResp["id"].(float64)
	require.NotZero(t, channelID)

	// 取默认账号 id；默认 proxy_group 为空（直连）。
	accs := doJSONArray(t, r, http.MethodGet, "/admin/accounts?channel_id="+itoa(channelID), token, "", http.StatusOK)
	require.Len(t, accs, 1)
	accID := accs[0].(map[string]any)["id"].(float64)
	require.Equal(t, "", accs[0].(map[string]any)["proxy_group"], "默认账号应直连（proxy_group 空）")

	proxyPath := "/admin/accounts/" + itoa(accID) + "/proxy"

	// 置 pool-a：查库确认绑定。
	doJSON(t, r, http.MethodPost, proxyPath, token, `{"proxy_group":"pool-a"}`, http.StatusOK)
	var accRow model.Account
	require.NoError(t, g.First(&accRow, uint64(accID)).Error)
	require.Equal(t, "pool-a", accRow.ProxyGroup, "改绑后 proxy_group 应为 pool-a")

	// 置空：恢复直连。
	doJSON(t, r, http.MethodPost, proxyPath, token, `{"proxy_group":""}`, http.StatusOK)
	require.NoError(t, g.First(&accRow, uint64(accID)).Error)
	require.Equal(t, "", accRow.ProxyGroup, "置空后应恢复直连")
}

// appIDFromKey 由明文 key 反查所属 App id（测试辅助）。
func appIDFromKey(t *testing.T, g *gorm.DB, plainKey string) uint64 {
	t.Helper()
	// 明文 key 形如 sk-saig-<id>-<secret>；直接取库里唯一一条 api_key 的 app_id 更稳妥。
	var key model.APIKey
	require.NoError(t, g.Order("id desc").First(&key).Error)
	return key.AppID
}

// pollAuditCount 轮询 audit_records 表里指定 app 的记录数，直到 >=want 或超时。
// 审计是异步落库的，需要轮询等待而非立即断言。
func pollAuditCount(g *gorm.DB, appID uint64, want int64) int64 {
	var n int64
	for i := 0; i < 50; i++ { // 最多等约 2.5s
		g.Model(&model.AuditRecord{}).Where("app_id = ?", appID).Count(&n)
		if n >= want {
			return n
		}
		time.Sleep(50 * time.Millisecond)
	}
	return n
}

// TestAuditChatE2E：App audit_enabled=true → chat 非流式 → 轮询审计异步落库后，
// GET /admin/request-logs 列出日志，再 GET /admin/request-logs/:id 断言 has_audit=true
// 且 request_body 含原始请求、response_body 含上游响应（审计正文经请求日志详情回放）。
func TestAuditChatE2E(t *testing.T) {
	gin.SetMode(gin.TestMode)
	up := multiProtoUpstream()
	defer up.Close()
	r, plainKey, g := setupMultiProto(t, up.URL)

	appID := appIDFromKey(t, g, plainKey)
	require.NoError(t, g.Model(&model.App{}).Where("id = ?", appID).Update("audit_enabled", true).Error)

	token := doJSON(t, r, http.MethodPost, "/admin/login", "",
		`{"username":"admin","password":"sag12345"}`, http.StatusOK)["token"].(string)

	// 打一条带可识别 marker 的非流式 chat。
	reqBody := `{"model":"gpt-4o","messages":[{"role":"user","content":"audit-marker-hi"}]}`
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(reqBody))
	req.Header.Set("Authorization", "Bearer "+plainKey)
	req.Header.Set("Content-Type", "application/json")
	r.ServeHTTP(w, req)
	require.Equal(t, http.StatusOK, w.Code, "resp=%s", w.Body.String())

	// 异步落库：轮询等待审计记录出现。
	require.GreaterOrEqual(t, pollAuditCount(g, appID, 1), int64(1), "审计记录应异步落库")

	// 经请求日志端点列出该 App 的日志。
	list := doJSON(t, r, http.MethodGet, "/admin/request-logs?app_id="+strconv.FormatUint(appID, 10), token, "", http.StatusOK)
	require.GreaterOrEqual(t, list["total"].(float64), float64(1))
	items := list["items"].([]any)
	require.NotEmpty(t, items)
	first := items[0].(map[string]any)
	require.Equal(t, "chat", first["request_type"])
	logID := uint64(first["id"].(float64))

	// 回放断言：详情关联审计正文，request_body 含原始请求、response_body 含上游响应。
	detail := doJSON(t, r, http.MethodGet, "/admin/request-logs/"+strconv.FormatUint(logID, 10), token, "", http.StatusOK)
	require.Equal(t, true, detail["has_audit"], "开审计的请求详情应关联审计正文")
	require.Contains(t, detail["request_body"].(string), "audit-marker-hi", "应回放原始请求体")
	require.Contains(t, detail["response_body"].(string), "chatok", "应回放上游响应体")
}

// TestAuditMessagesRawBodyE2E：/v1/messages(Anthropic) → 审计 request_body 是原始 Anthropic body
// （含 "system"/Anthropic 结构），request_type=messages。
func TestAuditMessagesRawBodyE2E(t *testing.T) {
	gin.SetMode(gin.TestMode)
	up := multiProtoUpstream()
	defer up.Close()
	r, plainKey, g := setupMultiProto(t, up.URL)

	appID := appIDFromKey(t, g, plainKey)
	require.NoError(t, g.Model(&model.App{}).Where("id = ?", appID).Update("audit_enabled", true).Error)

	token := doJSON(t, r, http.MethodPost, "/admin/login", "",
		`{"username":"admin","password":"sag12345"}`, http.StatusOK)["token"].(string)

	// 原始 Anthropic body：含 system 字段（OpenAI 转换后会被拆进 messages，不再有顶层 "system"）。
	anthBody := `{"model":"gpt-4o","max_tokens":100,"system":"you are a tester","messages":[{"role":"user","content":"hi"}]}`
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(anthBody))
	req.Header.Set("x-api-key", plainKey)
	req.Header.Set("Content-Type", "application/json")
	r.ServeHTTP(w, req)
	require.Equal(t, http.StatusOK, w.Code, "resp=%s", w.Body.String())

	require.GreaterOrEqual(t, pollAuditCount(g, appID, 1), int64(1), "审计记录应异步落库")

	list := doJSON(t, r, http.MethodGet,
		"/admin/request-logs?app_id="+strconv.FormatUint(appID, 10)+"&request_type=messages", token, "", http.StatusOK)
	require.GreaterOrEqual(t, list["total"].(float64), float64(1))
	items := list["items"].([]any)
	first := items[0].(map[string]any)
	require.Equal(t, "messages", first["request_type"])
	logID := uint64(first["id"].(float64))

	detail := doJSON(t, r, http.MethodGet, "/admin/request-logs/"+strconv.FormatUint(logID, 10), token, "", http.StatusOK)
	require.Equal(t, true, detail["has_audit"], "开审计的请求详情应关联审计正文")
	reqStored := detail["request_body"].(string)
	require.Contains(t, reqStored, `"system"`, "审计 request_body 应是转换前的原始 Anthropic body（含顶层 system）")
	require.Contains(t, reqStored, "you are a tester")
}

// TestAuditDisabledE2E：App audit_enabled=false → chat → 轮询确认无审计记录。
func TestAuditDisabledE2E(t *testing.T) {
	gin.SetMode(gin.TestMode)
	up := multiProtoUpstream()
	defer up.Close()
	r, plainKey, g := setupMultiProto(t, up.URL)

	appID := appIDFromKey(t, g, plainKey)
	// 关键：关闭该 App 的审计。
	require.NoError(t, g.Model(&model.App{}).Where("id = ?", appID).Update("audit_enabled", false).Error)

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions",
		strings.NewReader(`{"model":"gpt-4o","messages":[{"role":"user","content":"hi"}]}`))
	req.Header.Set("Authorization", "Bearer "+plainKey)
	req.Header.Set("Content-Type", "application/json")
	r.ServeHTTP(w, req)
	require.Equal(t, http.StatusOK, w.Code, "resp=%s", w.Body.String())

	// 等待一段时间确认审计未落库（异步写若发生会在此窗口出现）。
	var n int64
	for i := 0; i < 6; i++ {
		time.Sleep(50 * time.Millisecond)
		g.Model(&model.AuditRecord{}).Where("app_id = ?", appID).Count(&n)
		require.Equal(t, int64(0), n, "audit_enabled=false 不应产生审计记录")
	}
}

// TestRequestIDLinksLogAndAuditE2E 验证同一次请求的 RequestLog 与 AuditRecord 共享同一个
// request_id（由 RequestID 中间件生成，经 govern/finish/auditSubmit 落库）：
// 打一条非流式 chat（App 默认开审计）→ RequestLog.RequestID 非空 → 用它反查到对应 AuditRecord。
func TestRequestIDLinksLogAndAuditE2E(t *testing.T) {
	gin.SetMode(gin.TestMode)
	up := multiProtoUpstream()
	defer up.Close()
	r, plainKey, g := setupMultiProto(t, up.URL)

	appID := appIDFromKey(t, g, plainKey)
	require.NoError(t, g.Model(&model.App{}).Where("id = ?", appID).Update("audit_enabled", true).Error)

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions",
		strings.NewReader(`{"model":"gpt-4o","messages":[{"role":"user","content":"hi"}]}`))
	req.Header.Set("Authorization", "Bearer "+plainKey)
	req.Header.Set("Content-Type", "application/json")
	r.ServeHTTP(w, req)
	require.Equal(t, http.StatusOK, w.Code, "resp=%s", w.Body.String())

	// 审计是异步落库，先轮询等待出现。
	require.GreaterOrEqual(t, pollAuditCount(g, appID, 1), int64(1), "审计记录应异步落库")

	var lg model.RequestLog
	require.NoError(t, g.Order("id desc").First(&lg).Error)
	require.NotEmpty(t, lg.RequestID, "RequestLog.RequestID 不应为空")

	var ar model.AuditRecord
	require.NoError(t, g.Where("request_id = ?", lg.RequestID).First(&ar).Error,
		"应能按 request_id 找到关联的审计记录")
	require.Equal(t, appID, ar.AppID)
}

// TestAuditStreamChatE2E：流式 chat + 审计开 → 审计正文应含上游 SSE 帧原文。
// 防 Deps.CaptureBody 接线被静默回归：接线断成恒 false 时流式审计会退化成空
// response_body 而非报错，仅此正向断言能抓住（roadmap 2.9 终审遗留项）。
func TestAuditStreamChatE2E(t *testing.T) {
	gin.SetMode(gin.TestMode)
	up := multiProtoUpstream()
	defer up.Close()
	r, plainKey, g := setupMultiProto(t, up.URL)

	appID := appIDFromKey(t, g, plainKey)
	require.NoError(t, g.Model(&model.App{}).Where("id = ?", appID).Update("audit_enabled", true).Error)

	token := doJSON(t, r, http.MethodPost, "/admin/login", "",
		`{"username":"admin","password":"sag12345"}`, http.StatusOK)["token"].(string)

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions",
		strings.NewReader(`{"model":"gpt-4o","stream":true,"messages":[{"role":"user","content":"stream-audit-marker"}]}`))
	req.Header.Set("Authorization", "Bearer "+plainKey)
	req.Header.Set("Content-Type", "application/json")
	r.ServeHTTP(w, req)
	require.Equal(t, http.StatusOK, w.Code, "resp=%s", w.Body.String())
	require.Contains(t, w.Body.String(), "[DONE]")

	require.GreaterOrEqual(t, pollAuditCount(g, appID, 1), int64(1), "审计记录应异步落库")

	// 库层：审计记录应标记 stream=true。
	var ar model.AuditRecord
	require.NoError(t, g.Where("app_id = ?", appID).Order("id desc").First(&ar).Error)
	require.True(t, ar.Stream, "流式请求的审计记录应标记 stream=true")

	// 端点层：详情回放 response_body 应含 SSE 帧原文（delta 内容 + 结束帧）。
	list := doJSON(t, r, http.MethodGet, "/admin/request-logs?app_id="+strconv.FormatUint(appID, 10), token, "", http.StatusOK)
	items := list["items"].([]any)
	require.NotEmpty(t, items)
	logID := uint64(items[0].(map[string]any)["id"].(float64))
	detail := doJSON(t, r, http.MethodGet, "/admin/request-logs/"+strconv.FormatUint(logID, 10), token, "", http.StatusOK)
	require.Equal(t, true, detail["has_audit"], "开审计的流式请求详情应关联审计正文")
	require.Contains(t, detail["request_body"].(string), "stream-audit-marker", "应回放原始请求体")
	respStored := detail["response_body"].(string)
	require.Contains(t, respStored, "data:", "流式审计正文应为 SSE 帧原文")
	require.Contains(t, respStored, "Hel", "应包含上游 delta 帧内容")
	require.Contains(t, respStored, "[DONE]", "应包含流结束帧")
}

// TestAuditStreamMessagesE2E：流式 /v1/messages + 审计开 → 审计正文应含 Anthropic SSE 事件帧。
// RelayAnthropic 是 CaptureBody 的独立消费点，与 Relay(openai) 分开断言防单侧回归。
func TestAuditStreamMessagesE2E(t *testing.T) {
	gin.SetMode(gin.TestMode)
	up := multiProtoUpstream()
	defer up.Close()
	r, plainKey, g := setupMultiProto(t, up.URL)

	appID := appIDFromKey(t, g, plainKey)
	require.NoError(t, g.Model(&model.App{}).Where("id = ?", appID).Update("audit_enabled", true).Error)

	token := doJSON(t, r, http.MethodPost, "/admin/login", "",
		`{"username":"admin","password":"sag12345"}`, http.StatusOK)["token"].(string)

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/messages",
		strings.NewReader(`{"model":"gpt-4o","stream":true,"max_tokens":100,"messages":[{"role":"user","content":"hi"}]}`))
	req.Header.Set("x-api-key", plainKey)
	req.Header.Set("Content-Type", "application/json")
	r.ServeHTTP(w, req)
	require.Equal(t, http.StatusOK, w.Code, "resp=%s", w.Body.String())

	require.GreaterOrEqual(t, pollAuditCount(g, appID, 1), int64(1), "审计记录应异步落库")

	list := doJSON(t, r, http.MethodGet,
		"/admin/request-logs?app_id="+strconv.FormatUint(appID, 10)+"&request_type=messages", token, "", http.StatusOK)
	items := list["items"].([]any)
	require.NotEmpty(t, items)
	logID := uint64(items[0].(map[string]any)["id"].(float64))
	detail := doJSON(t, r, http.MethodGet, "/admin/request-logs/"+strconv.FormatUint(logID, 10), token, "", http.StatusOK)
	require.Equal(t, true, detail["has_audit"], "开审计的流式 messages 详情应关联审计正文")
	respStored := detail["response_body"].(string)
	require.Contains(t, respStored, "message_start", "应包含 Anthropic SSE 起始事件帧")
	require.Contains(t, respStored, "content_block_delta", "应包含 Anthropic SSE 增量事件帧")
}

// TestAuditStreamDisabledE2E：流式 + 审计关 → 确认无审计落库（反向断言，
// 与非流式 TestAuditDisabledE2E 对称；CaptureBody=false 时流式透传不攒捕获体）。
func TestAuditStreamDisabledE2E(t *testing.T) {
	gin.SetMode(gin.TestMode)
	up := multiProtoUpstream()
	defer up.Close()
	r, plainKey, g := setupMultiProto(t, up.URL)

	appID := appIDFromKey(t, g, plainKey)
	require.NoError(t, g.Model(&model.App{}).Where("id = ?", appID).Update("audit_enabled", false).Error)

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions",
		strings.NewReader(`{"model":"gpt-4o","stream":true,"messages":[{"role":"user","content":"hi"}]}`))
	req.Header.Set("Authorization", "Bearer "+plainKey)
	req.Header.Set("Content-Type", "application/json")
	r.ServeHTTP(w, req)
	require.Equal(t, http.StatusOK, w.Code, "resp=%s", w.Body.String())
	require.Contains(t, w.Body.String(), "[DONE]", "审计关闭不应影响流式转发本身")

	// 等待一段时间确认审计未落库（异步写若发生会在此窗口出现）。
	var n int64
	for i := 0; i < 6; i++ {
		time.Sleep(50 * time.Millisecond)
		g.Model(&model.AuditRecord{}).Where("app_id = ?", appID).Count(&n)
		require.Equal(t, int64(0), n, "audit_enabled=false 的流式请求不应产生审计记录")
	}
}

// TestSetAppAuditE2E：建 App（默认 audit_enabled=true）→ POST /admin/apps/:id/audit 置 false →
// 查库确认关闭 → 再置 true 恢复。锁住 App 审计开关端点。
func TestSetAppAuditE2E(t *testing.T) {
	gin.SetMode(gin.TestMode)

	cfg := config.Config{}
	cfg.Storage.Local.Root = t.TempDir() // 审计正文本地存储落临时目录，避免测试残留
	cfg.Security.APIKeyPrefix = "sk-saig"
	cfg.Security.ChannelKeyAESKey = "0123456789abcdef0123456789abcdef"
	cfg.JWT.Secret = "audit-toggle-secret"
	cfg.JWT.ExpireHours = 1

	g := dbtest.OpenInMemory(t)
	require.NoError(t, db.Migrate(g, model.Migrations("admin", "sag12345")))
	resetRouting(g) // 兼容 -count=N 重跑：清空唯一索引相关表

	r, shutdown, err := NewWithShutdown(cfg, g, nil)
	require.NoError(t, err)
	t.Cleanup(shutdown) // 先停审计 worker 等后台 goroutine，再让 TempDir 清理（Cleanup 为 LIFO）
	token := doJSON(t, r, http.MethodPost, "/admin/login", "",
		`{"username":"admin","password":"sag12345"}`, http.StatusOK)["token"].(string)

	// 建 App：默认 audit_enabled=true（gorm default）。
	appResp := doJSON(t, r, http.MethodPost, "/admin/apps", token,
		`{"name":"team-audit-toggle","quota":0}`, http.StatusOK)
	appID := uint64(appResp["id"].(float64))
	var appRow model.App
	require.NoError(t, g.First(&appRow, appID).Error)
	require.True(t, appRow.AuditEnabled, "新建 App 默认应开启审计")

	// 置 false：查库确认关闭。
	auditPath := "/admin/apps/" + strconv.FormatUint(appID, 10) + "/audit"
	doJSON(t, r, http.MethodPost, auditPath, token, `{"audit_enabled":false}`, http.StatusOK)
	require.NoError(t, g.First(&appRow, appID).Error)
	require.False(t, appRow.AuditEnabled, "置 false 后审计应关闭")

	// 再置 true：恢复。
	doJSON(t, r, http.MethodPost, auditPath, token, `{"audit_enabled":true}`, http.StatusOK)
	require.NoError(t, g.First(&appRow, appID).Error)
	require.True(t, appRow.AuditEnabled, "置 true 后审计应恢复")
}

// newForwardProxyServer 起一个最小 http 正向代理：把收到的（含绝对 URL 的）请求转发到目标上游，
// 每次转发对 *counter 计数。仅用于测试经代理出站。
func newForwardProxyServer(counter *int) *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		*counter++
		outReq, err := http.NewRequest(r.Method, r.URL.String(), r.Body)
		if err != nil {
			w.WriteHeader(http.StatusBadGateway)
			return
		}
		for k, vs := range r.Header {
			for _, v := range vs {
				outReq.Header.Add(k, v)
			}
		}
		resp, err := http.DefaultTransport.RoundTrip(outReq)
		if err != nil {
			w.WriteHeader(http.StatusBadGateway)
			return
		}
		defer resp.Body.Close()
		for k, vs := range resp.Header {
			for _, v := range vs {
				w.Header().Add(k, v)
			}
		}
		w.WriteHeader(resp.StatusCode)
		_, _ = io.Copy(w, resp.Body)
	}))
}

// hostPort 从 httptest server URL 解析出 host 与 int 端口。
func hostPort(t *testing.T, rawURL string) (string, int) {
	t.Helper()
	u, err := url.Parse(rawURL)
	require.NoError(t, err)
	host, portStr, err := net.SplitHostPort(u.Host)
	require.NoError(t, err)
	port, err := strconv.Atoi(portStr)
	require.NoError(t, err)
	return host, port
}

// TestProxyAdminCRUDE2E 端到端验证 admin 代理池 CRUD：登录→建代理→列表/按组列→停用→删除。
func TestProxyAdminCRUDE2E(t *testing.T) {
	gin.SetMode(gin.TestMode)

	cfg := config.Config{}
	cfg.Storage.Local.Root = t.TempDir() // 审计正文本地存储落临时目录，避免测试残留
	cfg.Security.APIKeyPrefix = "sk-saig"
	cfg.Security.ChannelKeyAESKey = "0123456789abcdef0123456789abcdef"
	cfg.JWT.Secret = "p6-proxy-crud"
	cfg.JWT.ExpireHours = 1

	g := dbtest.OpenInMemory(t)
	require.NoError(t, db.Migrate(g, model.Migrations("admin", "sag12345")))
	resetRouting(g)
	r, shutdown, err := NewWithShutdown(cfg, g, nil)
	require.NoError(t, err)
	t.Cleanup(shutdown) // 先停审计 worker 等后台 goroutine，再让 TempDir 清理（Cleanup 为 LIFO）

	token := doJSON(t, r, http.MethodPost, "/admin/login", "",
		`{"username":"admin","password":"sag12345"}`, http.StatusOK)["token"].(string)
	require.NotEmpty(t, token)

	// 建两个代理：p1(启用)、p2(同组，将被停用)。
	p1 := doJSON(t, r, http.MethodPost, "/admin/proxies", token,
		`{"group":"pool-a","name":"p1","protocol":"http","host":"127.0.0.1","port":1080,"weight":70}`, http.StatusOK)
	p1ID := p1["id"].(float64)
	require.NotZero(t, p1ID)
	require.Equal(t, "pool-a", p1["group"])
	require.Equal(t, float64(1), p1["status"], "新建代理默认启用")

	p2 := doJSON(t, r, http.MethodPost, "/admin/proxies", token,
		`{"group":"pool-a","name":"p2","protocol":"socks5","host":"127.0.0.1","port":1081,"weight":30}`, http.StatusOK)
	p2ID := p2["id"].(float64)

	// host 缺失 → 400。
	doJSON(t, r, http.MethodPost, "/admin/proxies", token, `{"group":"pool-a","name":"bad"}`, http.StatusBadRequest)

	// 列表（全部）→ 2 个。
	all := doJSONArray(t, r, http.MethodGet, "/admin/proxies", token, "", http.StatusOK)
	require.Len(t, all, 2)

	// 按组（仅启用）→ 2 个。
	enabled := doJSONArray(t, r, http.MethodGet, "/admin/proxies?group=pool-a", token, "", http.StatusOK)
	require.Len(t, enabled, 2)

	// 停用 p2 → 按组（仅启用）回 1 个。
	doJSON(t, r, http.MethodPost, "/admin/proxies/"+itoa(p2ID)+"/status", token, `{"status":0}`, http.StatusOK)
	enabled = doJSONArray(t, r, http.MethodGet, "/admin/proxies?group=pool-a", token, "", http.StatusOK)
	require.Len(t, enabled, 1, "停用后按组仅启用应回 1 个")
	require.Equal(t, "p1", enabled[0].(map[string]any)["name"])

	// 删除 p2 → 全部列表回 1 个。
	doJSON(t, r, http.MethodDelete, "/admin/proxies/"+itoa(p2ID), token, "", http.StatusOK)
	all = doJSONArray(t, r, http.MethodGet, "/admin/proxies", token, "", http.StatusOK)
	require.Len(t, all, 1)
}

// TestAlertTestEndpointDisabledE2E 验证 alert.enabled=false（默认，webhookN=nil）时
// POST /admin/alert/test 不 panic，直接回 ok:false + 未启用提示。
func TestAlertTestEndpointDisabledE2E(t *testing.T) {
	gin.SetMode(gin.TestMode)

	cfg := config.Config{}
	cfg.Storage.Local.Root = t.TempDir() // 审计正文本地存储落临时目录，避免测试残留
	cfg.Security.APIKeyPrefix = "sk-saig"
	cfg.Security.ChannelKeyAESKey = "0123456789abcdef0123456789abcdef"
	cfg.JWT.Secret = "p3b-alert-test"
	cfg.JWT.ExpireHours = 1
	// cfg.Alert 留零值：Enabled=false，等价 webhookN=nil。

	g := dbtest.OpenInMemory(t)
	require.NoError(t, db.Migrate(g, model.Migrations("admin", "sag12345")))
	resetRouting(g)
	r, shutdown, err := NewWithShutdown(cfg, g, nil)
	require.NoError(t, err)
	t.Cleanup(shutdown) // 先停审计 worker 等后台 goroutine，再让 TempDir 清理（Cleanup 为 LIFO）

	token := doJSON(t, r, http.MethodPost, "/admin/login", "",
		`{"username":"admin","password":"sag12345"}`, http.StatusOK)["token"].(string)
	require.NotEmpty(t, token)

	resp := doJSON(t, r, http.MethodPost, "/admin/alert/test", token, "", http.StatusOK)
	require.Equal(t, false, resp["ok"])
	require.NotEmpty(t, resp["error"])
}

// TestRequestViaProxyE2E 端到端验证经代理出站：起一个记录请求的 http 正向代理，建代理组指向它、
// 账号绑该代理组、发 chat → 断言代理 server 收到请求（经代理出站）且请求成功。
func TestRequestViaProxyE2E(t *testing.T) {
	gin.SetMode(gin.TestMode)

	// mock 上游。
	var upHits int
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		upHits++
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"id":"ok","choices":[]}`))
	}))
	defer up.Close()

	// 记录请求的正向代理。
	var viaProxy int
	px := newForwardProxyServer(&viaProxy)
	defer px.Close()

	cfg := config.Config{}
	cfg.Storage.Local.Root = t.TempDir() // 审计正文本地存储落临时目录，避免测试残留
	cfg.Security.APIKeyPrefix = "sk-saig"
	cfg.Security.ChannelKeyAESKey = "0123456789abcdef0123456789abcdef"
	cfg.JWT.Secret = "p6-via-proxy"
	cfg.JWT.ExpireHours = 1

	g := dbtest.OpenInMemory(t)
	require.NoError(t, db.Migrate(g, model.Migrations("admin", "sag12345")))
	resetRouting(g)

	// 渠道直调 gpt-4o，base_url 指向 mock 上游。
	enc, _ := crypto.Encrypt(cfg.Security.ChannelKeyAESKey, "sk-up")
	ch := model.Channel{Name: "c", BaseURL: up.URL, Status: 1, Timeout: 30}
	require.NoError(t, g.Create(&ch).Error)
	require.NoError(t, g.Create(&model.ChannelModel{ChannelID: ch.ID, Model: "gpt-4o", Enabled: true}).Error)
	// 配价 gpt-4o：1/1 额度够触发价格前置校验通过。
	require.NoError(t, g.Create(&model.ModelPrice{Model: "gpt-4o", InputPer1M: 1, OutputPer1M: 1, Enabled: true}).Error)

	// 代理：组 pool-a 指向记录请求的正向代理。
	pxHost, pxPort := hostPort(t, px.URL)
	require.NoError(t, g.Create(&model.Proxy{Group: "pool-a", Name: "p1", Protocol: "http",
		Host: pxHost, Port: pxPort, Weight: 1, Status: 1}).Error)
	// 账号绑该代理组。
	require.NoError(t, g.Create(&model.Account{ChannelID: ch.ID, Name: "acc", APIKeyEnc: enc,
		Weight: 1, Status: 1, ProxyGroup: "pool-a"}).Error)

	r, shutdown, err := NewWithShutdown(cfg, g, nil)
	require.NoError(t, err)
	t.Cleanup(shutdown) // 先停审计 worker 等后台 goroutine，再让 TempDir 清理（Cleanup 为 LIFO）
	keySvc := apikey.NewService(apikey.NewRepo(g), cfg.Security.APIKeyPrefix)
	plainKey, _, err := keySvc.Generate("k", nil)
	require.NoError(t, err)

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions",
		strings.NewReader(`{"model":"gpt-4o","messages":[]}`))
	req.Header.Set("Authorization", "Bearer "+plainKey)
	req.Header.Set("Content-Type", "application/json")
	r.ServeHTTP(w, req)

	require.Equal(t, http.StatusOK, w.Code, "经代理应成功，resp=%s", w.Body.String())
	require.Contains(t, w.Body.String(), `"id":"ok"`)
	require.Greater(t, viaProxy, 0, "出站请求应经正向代理 server")
	require.Greater(t, upHits, 0, "上游应被命中（经代理转发可达）")
}

// TestAccountNoProxyDirectE2E 回归：账号 proxy_group 为空 → 直连出站成功（不走代理）。
func TestAccountNoProxyDirectE2E(t *testing.T) {
	gin.SetMode(gin.TestMode)

	var upHits int
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		upHits++
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"id":"ok","choices":[]}`))
	}))
	defer up.Close()

	// 起一个正向代理但不绑定任何账号，断言它不被命中。
	var viaProxy int
	px := newForwardProxyServer(&viaProxy)
	defer px.Close()

	cfg := config.Config{}
	cfg.Storage.Local.Root = t.TempDir() // 审计正文本地存储落临时目录，避免测试残留
	cfg.Security.APIKeyPrefix = "sk-saig"
	cfg.Security.ChannelKeyAESKey = "0123456789abcdef0123456789abcdef"
	cfg.JWT.Secret = "p6-direct"
	cfg.JWT.ExpireHours = 1

	g := dbtest.OpenInMemory(t)
	require.NoError(t, db.Migrate(g, model.Migrations("admin", "sag12345")))
	resetRouting(g)

	enc, _ := crypto.Encrypt(cfg.Security.ChannelKeyAESKey, "sk-up")
	ch := model.Channel{Name: "c", BaseURL: up.URL, Status: 1, Timeout: 30}
	require.NoError(t, g.Create(&ch).Error)
	require.NoError(t, g.Create(&model.ChannelModel{ChannelID: ch.ID, Model: "gpt-4o", Enabled: true}).Error)
	// 配价 gpt-4o：1/1 额度够触发价格前置校验通过。
	require.NoError(t, g.Create(&model.ModelPrice{Model: "gpt-4o", InputPer1M: 1, OutputPer1M: 1, Enabled: true}).Error)
	// 账号 proxy_group 为空 → 直连。
	require.NoError(t, g.Create(&model.Account{ChannelID: ch.ID, Name: "acc", APIKeyEnc: enc, Weight: 1, Status: 1}).Error)

	r, shutdown, err := NewWithShutdown(cfg, g, nil)
	require.NoError(t, err)
	t.Cleanup(shutdown) // 先停审计 worker 等后台 goroutine，再让 TempDir 清理（Cleanup 为 LIFO）
	keySvc := apikey.NewService(apikey.NewRepo(g), cfg.Security.APIKeyPrefix)
	plainKey, _, err := keySvc.Generate("k", nil)
	require.NoError(t, err)

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions",
		strings.NewReader(`{"model":"gpt-4o","messages":[]}`))
	req.Header.Set("Authorization", "Bearer "+plainKey)
	req.Header.Set("Content-Type", "application/json")
	r.ServeHTTP(w, req)

	require.Equal(t, http.StatusOK, w.Code, "直连应成功，resp=%s", w.Body.String())
	require.Contains(t, w.Body.String(), `"id":"ok"`)
	require.Greater(t, upHits, 0, "上游应被直连命中")
	require.Equal(t, 0, viaProxy, "proxy_group 为空时不应经代理")
}

// lastKeyID 取库里最新一把 api_key 的 id（测试辅助：setup 后只有一把 key）。
func lastKeyID(t *testing.T, g *gorm.DB) uint64 {
	t.Helper()
	var key model.APIKey
	require.NoError(t, g.Order("id desc").First(&key).Error)
	return key.ID
}

// chatReq 发一次 chat 请求，返回状态码与响应体（测试辅助）。
func chatReq(r http.Handler, plainKey, model string) (int, string) {
	return chatWith(r, plainKey, model, "hi")
}

// TestKeyDailyCostLimitE2E：key 设小额 daily_cost_limit → 累计成本超限后下次请求 402；
// 再把 daily_cost_date 改为昨天验证跨日重置后可继续。
func TestKeyDailyCostLimitE2E(t *testing.T) {
	gin.SetMode(gin.TestMode)
	up := multiProtoUpstream()
	defer up.Close()
	r, plainKey, g := setupMultiProto(t, up.URL)

	token := doJSON(t, r, http.MethodPost, "/admin/login", "",
		`{"username":"admin","password":"sag12345"}`, http.StatusOK)["token"].(string)

	// 配价 gpt-4o：input 2.5 / output 10（USD/1M）。multiProtoUpstream usage=10/5 → cost = 10*2.5 + 5*10 = 75 micros/次。
	doJSON(t, r, http.MethodPost, "/admin/prices", token,
		`{"model":"gpt-4o","input_per_1m":2.5,"output_per_1m":10,"enabled":true}`, http.StatusOK)

	keyID := lastKeyID(t, g)
	// 经 UpdateAPIKey 端点设日成本上限 100 micros（仅够 1 次 75，第 2 次累计 150 超限）。
	doJSON(t, r, http.MethodPost, "/admin/api-keys/"+strconv.FormatUint(keyID, 10), token,
		`{"daily_cost_limit":100}`, http.StatusOK)

	// 第 1 次：成功（累计 75 < 100）。
	code, body := chatReq(r, plainKey, "gpt-4o")
	require.Equal(t, http.StatusOK, code, "首次应成功，resp=%s", body)

	// 确认日成本已累加到 75。
	var k model.APIKey
	require.NoError(t, g.First(&k, keyID).Error)
	require.Equal(t, int64(75), k.DailyCostUsed, "日成本应累加 75 micros")

	// 第 2 次：当日已用 75 >= 100? 否（75<100，仍允许进入），但本次会再 +75 → 之后超限。
	// 为确定性：直接把已用顶到上限，验证下次 402。
	require.NoError(t, g.Model(&model.APIKey{}).Where("id = ?", keyID).
		Update("daily_cost_used", int64(100)).Error)
	code, body = chatReq(r, plainKey, "gpt-4o")
	require.Equal(t, http.StatusPaymentRequired, code, "日成本超限应 402，resp=%s", body)

	// 跨日重置：把 daily_cost_date 改为昨天 → 下次请求应放行（重置后当日未用）。
	yesterday := time.Now().AddDate(0, 0, -1).Format("2006-01-02")
	require.NoError(t, g.Model(&model.APIKey{}).Where("id = ?", keyID).
		Update("daily_cost_date", yesterday).Error)
	code, body = chatReq(r, plainKey, "gpt-4o")
	require.Equal(t, http.StatusOK, code, "跨日重置后应放行，resp=%s", body)
}

// TestKeyModelWhitelistE2E：key 设 model_whitelist="gpt-4o" → 调 gpt-4o 成功、调未授权模型 403。
func TestKeyModelWhitelistE2E(t *testing.T) {
	gin.SetMode(gin.TestMode)
	up := multiProtoUpstream()
	defer up.Close()
	r, plainKey, g := setupMultiProto(t, up.URL)

	token := doJSON(t, r, http.MethodPost, "/admin/login", "",
		`{"username":"admin","password":"sag12345"}`, http.StatusOK)["token"].(string)

	keyID := lastKeyID(t, g)
	doJSON(t, r, http.MethodPost, "/admin/api-keys/"+strconv.FormatUint(keyID, 10), token,
		`{"model_whitelist":"gpt-4o"}`, http.StatusOK)

	// 白名单内模型 → 200。
	code, body := chatReq(r, plainKey, "gpt-4o")
	require.Equal(t, http.StatusOK, code, "白名单内模型应放行，resp=%s", body)

	// 未授权模型 → 403（在 Resolve 之前被治理拦截，与“无渠道 404”区分）。
	code, body = chatReq(r, plainKey, "o1")
	require.Equal(t, http.StatusForbidden, code, "白名单外模型应 403，resp=%s", body)
}

// TestKeyModelMappingE2E：key 设 model_mappings={"gpt-4":"gpt-4o"} → 发 model=gpt-4 →
// 路由命中 gpt-4o 渠道、请求日志 model=gpt-4o（映射生效贯穿计费/日志）。
func TestKeyModelMappingE2E(t *testing.T) {
	gin.SetMode(gin.TestMode)
	up := multiProtoUpstream()
	defer up.Close()
	r, plainKey, g := setupMultiProto(t, up.URL)

	token := doJSON(t, r, http.MethodPost, "/admin/login", "",
		`{"username":"admin","password":"sag12345"}`, http.StatusOK)["token"].(string)

	keyID := lastKeyID(t, g)
	doJSON(t, r, http.MethodPost, "/admin/api-keys/"+strconv.FormatUint(keyID, 10), token,
		`{"model_mappings":"{\"gpt-4\":\"gpt-4o\"}"}`, http.StatusOK)

	// 客户端发 gpt-4（渠道未声明 gpt-4），映射到 gpt-4o（渠道已声明）→ 200。
	code, body := chatReq(r, plainKey, "gpt-4")
	require.Equal(t, http.StatusOK, code, "映射后应命中 gpt-4o 渠道，resp=%s", body)

	// 请求日志 model 应为映射后 gpt-4o（贯穿计费/日志/审计）。
	var logRow model.RequestLog
	require.NoError(t, g.Order("id desc").First(&logRow).Error)
	require.Equal(t, "gpt-4o", logRow.Model, "日志 model 应为映射后模型")
}

// TestKeyRPMValueE2E 令牌 RPM 取值：-1（不限）/0（按全局默认）/正整数可写入，<-1 拒绝；建与改两条路径都验。
func TestKeyRPMValueE2E(t *testing.T) {
	gin.SetMode(gin.TestMode)
	up := multiProtoUpstream()
	defer up.Close()
	r, _, g := setupMultiProto(t, up.URL)
	token := doJSON(t, r, http.MethodPost, "/admin/login", "",
		`{"username":"admin","password":"sag12345"}`, http.StatusOK)["token"].(string)
	app := strconv.FormatUint(appIDFromKey(t, g, ""), 10)

	doJSON(t, r, http.MethodPost, "/admin/api-keys", token, `{"name":"bad","app_id":`+app+`,"rpm":-2}`, http.StatusBadRequest)
	doJSON(t, r, http.MethodPost, "/admin/api-keys", token, `{"name":"unl","app_id":`+app+`,"rpm":-1}`, http.StatusOK)
	keyID := lastKeyID(t, g)
	rpmOf := func() int {
		var k model.APIKey
		require.NoError(t, g.First(&k, keyID).Error)
		return k.RPM
	}
	require.Equal(t, model.LimitUnlimited, rpmOf(), "建 key 时 -1 须原样落库")

	path := "/admin/api-keys/" + strconv.FormatUint(keyID, 10)
	doJSON(t, r, http.MethodPost, path, token, `{"rpm":-5}`, http.StatusBadRequest)
	require.Equal(t, model.LimitUnlimited, rpmOf(), "非法取值不得改动原值")
	doJSON(t, r, http.MethodPost, path, token, `{"rpm":0}`, http.StatusOK)
	require.Equal(t, 0, rpmOf(), "改回 0（按全局默认）须生效")
	doJSON(t, r, http.MethodPost, path, token, `{"rpm":-1}`, http.StatusOK)
	require.Equal(t, model.LimitUnlimited, rpmOf(), "改为 -1 须生效")
}

// TestKeyTagFilterE2E：建 tag → 给 key 绑定 → GET /admin/api-keys?tag_id= 只返回该 key。
func TestKeyTagFilterE2E(t *testing.T) {
	gin.SetMode(gin.TestMode)
	up := multiProtoUpstream()
	defer up.Close()
	r, _, g := setupMultiProto(t, up.URL)

	token := doJSON(t, r, http.MethodPost, "/admin/login", "",
		`{"username":"admin","password":"sag12345"}`, http.StatusOK)["token"].(string)

	// 第一把 key 由 setupMultiProto 创建；再建一把无标签 key 用于对照。
	appID := appIDFromKey(t, g, "")
	doJSON(t, r, http.MethodPost, "/admin/api-keys", token,
		`{"name":"k2","app_id":`+strconv.FormatUint(appID, 10)+`}`, http.StatusOK)

	keyID := lastKeyID(t, g) // k2 是最新；但我们要给“第一把”打标签
	// 取两把 key 的 id。
	var keys []model.APIKey
	require.NoError(t, g.Order("id asc").Find(&keys).Error)
	require.Len(t, keys, 2)
	firstKeyID := keys[0].ID
	require.NotEqual(t, firstKeyID, keyID) // 确认 k2 是后建那把

	// 建标签 prod。
	tag := doJSON(t, r, http.MethodPost, "/admin/key-tags", token, `{"name":"prod"}`, http.StatusOK)
	tagID := uint64(tag["id"].(float64))
	require.NotZero(t, tagID)

	// 给第一把 key 绑定 prod。
	doJSON(t, r, http.MethodPost, "/admin/api-keys/"+strconv.FormatUint(firstKeyID, 10)+"/tags", token,
		`{"tag_id":`+strconv.FormatUint(tagID, 10)+`}`, http.StatusOK)

	// 按标签筛选 → 只返回第一把 key。
	filtered := doJSONArray(t, r, http.MethodGet,
		"/admin/api-keys?tag_id="+strconv.FormatUint(tagID, 10), token, "", http.StatusOK)
	require.Len(t, filtered, 1, "按标签筛选应只返回 1 把 key")
	item := filtered[0].(map[string]any)
	keyObj := item["key"].(map[string]any)
	require.Equal(t, float64(firstKeyID), keyObj["id"], "返回的应是绑定标签的那把 key")
	tags := item["tags"].([]any)
	require.Len(t, tags, 1)
	require.Equal(t, "prod", tags[0].(map[string]any)["name"])

	// 解绑后再筛选 → 空。
	doJSON(t, r, http.MethodDelete,
		"/admin/api-keys/"+strconv.FormatUint(firstKeyID, 10)+"/tags/"+strconv.FormatUint(tagID, 10),
		token, "", http.StatusOK)
	filtered = doJSONArray(t, r, http.MethodGet,
		"/admin/api-keys?tag_id="+strconv.FormatUint(tagID, 10), token, "", http.StatusOK)
	require.Len(t, filtered, 0, "解绑后按标签筛选应为空")
}

// doJSON 发起一次请求并断言状态码，返回解析后的 JSON 对象。
func doJSON(t *testing.T, r http.Handler, method, path, token, body string, wantCode int) map[string]any {
	t.Helper()
	var reqBody *strings.Reader
	if body == "" {
		reqBody = strings.NewReader("")
	} else {
		reqBody = strings.NewReader(body)
	}
	req := httptest.NewRequest(method, path, reqBody)
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	require.Equal(t, wantCode, w.Code, "path=%s body=%s resp=%s", path, body, w.Body.String())
	out := map[string]any{}
	if w.Body.Len() > 0 {
		require.NoError(t, json.Unmarshal(w.Body.Bytes(), &out), "path=%s resp=%s", path, w.Body.String())
	}
	return out
}

// doJSONArray 发起一次请求并断言状态码，返回解析后的 JSON 数组。
func doJSONArray(t *testing.T, r http.Handler, method, path, token, body string, wantCode int) []any {
	t.Helper()
	var reqBody *strings.Reader
	if body == "" {
		reqBody = strings.NewReader("")
	} else {
		reqBody = strings.NewReader(body)
	}
	req := httptest.NewRequest(method, path, reqBody)
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	require.Equal(t, wantCode, w.Code, "path=%s resp=%s", path, w.Body.String())
	var out []any
	if w.Body.Len() > 0 {
		require.NoError(t, json.Unmarshal(w.Body.Bytes(), &out), "path=%s resp=%s", path, w.Body.String())
	}
	return out
}

// resetRouting 清空别名/路由边/渠道相关表，使依赖唯一索引（如 model_aliases.alias）的
// 集成测试在 `go test -count=N` 同进程重跑时保持幂等（dbtest 的 shared-cache 内存库跨重跑保留数据）。
func resetRouting(g *gorm.DB) {
	g.Exec("DELETE FROM alias_routes")
	g.Exec("DELETE FROM model_aliases")
	g.Exec("DELETE FROM channel_models")
	g.Exec("DELETE FROM channels")
	g.Exec("DELETE FROM accounts")
	// 计费相关表：apps.name / model_prices.model 有唯一索引，跨 -count=N 重跑需清空避免冲突。
	g.Exec("DELETE FROM apps")
	g.Exec("DELETE FROM model_prices")
	g.Exec("DELETE FROM usage_daily")
	g.Exec("DELETE FROM usage_hourly")
	g.Exec("DELETE FROM api_keys")
	g.Exec("DELETE FROM request_logs")
	g.Exec("DELETE FROM proxies")
	// 令牌标签：key_tags.name 有唯一索引，跨 -count=N 重跑需清空避免冲突。
	g.Exec("DELETE FROM key_tags")
	g.Exec("DELETE FROM key_tag_relations")
}

func itoa(f float64) string {
	return strconv.FormatUint(uint64(f), 10)
}

func readAll(r *http.Request) ([]byte, error) {
	return io.ReadAll(r.Body)
}

func TestChannelUpdateRateMultiplierE2E(t *testing.T) {
	gin.SetMode(gin.TestMode)
	cfg := config.Config{}
	cfg.Storage.Local.Root = t.TempDir() // 审计正文本地存储落临时目录，避免测试残留
	cfg.Security.APIKeyPrefix = "sk-saig"
	cfg.Security.ChannelKeyAESKey = "0123456789abcdef0123456789abcdef"
	cfg.JWT.Secret = "chup-secret"
	cfg.JWT.ExpireHours = 1

	g := dbtest.OpenInMemory(t)
	require.NoError(t, db.Migrate(g, model.Migrations("admin", "sag12345")))
	resetRouting(g)
	r, shutdown, err := NewWithShutdown(cfg, g, nil)
	require.NoError(t, err)
	t.Cleanup(shutdown) // 先停审计 worker 等后台 goroutine，再让 TempDir 清理（Cleanup 为 LIFO）
	token := doJSON(t, r, http.MethodPost, "/admin/login", "",
		`{"username":"admin","password":"sag12345"}`, http.StatusOK)["token"].(string)

	// 建渠道时带倍率 1.5。
	chResp := doJSON(t, r, http.MethodPost, "/admin/channels", token,
		`{"type":"openai","name":"c1","base_url":"http://up","api_key":"k","rate_multiplier":1.5}`, http.StatusOK)
	channelID := uint64(chResp["id"].(float64))
	var ch model.Channel
	require.NoError(t, g.First(&ch, channelID).Error)
	require.Equal(t, 1.5, ch.RateMultiplier)

	// 更新倍率为 3。
	doJSON(t, r, http.MethodPost, "/admin/channels/"+strconv.FormatUint(channelID, 10), token,
		`{"rate_multiplier":3}`, http.StatusOK)
	require.NoError(t, g.First(&ch, channelID).Error)
	require.Equal(t, 3.0, ch.RateMultiplier)
}

func TestChannelPriceMultiplierE2E(t *testing.T) {
	gin.SetMode(gin.TestMode)

	// mock 上游:usage prompt=100,completion=50。
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"ok","choices":[],"usage":{"prompt_tokens":100,"completion_tokens":50,"total_tokens":150}}`))
	}))
	defer up.Close()

	cfg := config.Config{}
	cfg.Storage.Local.Root = t.TempDir() // 审计正文本地存储落临时目录，避免测试残留
	cfg.Security.APIKeyPrefix = "sk-saig"
	cfg.Security.ChannelKeyAESKey = "0123456789abcdef0123456789abcdef"
	cfg.JWT.Secret = "mult-secret"
	cfg.JWT.ExpireHours = 1

	g := dbtest.OpenInMemory(t)
	require.NoError(t, db.Migrate(g, model.Migrations("admin", "sag12345")))
	resetRouting(g)

	r, shutdown, err := NewWithShutdown(cfg, g, nil)
	require.NoError(t, err)
	t.Cleanup(shutdown) // 先停审计 worker 等后台 goroutine，再让 TempDir 清理（Cleanup 为 LIFO）
	token := doJSON(t, r, http.MethodPost, "/admin/login", "",
		`{"username":"admin","password":"sag12345"}`, http.StatusOK)["token"].(string)

	chResp := doJSON(t, r, http.MethodPost, "/admin/channels", token,
		`{"type":"openai","name":"c1","base_url":"`+up.URL+`","api_key":"sk-up"}`, http.StatusOK)
	channelID := uint64(chResp["id"].(float64))
	doJSON(t, r, http.MethodPost, "/admin/channel-models", token,
		`{"channel_id":`+strconv.FormatUint(channelID, 10)+`,"model":"gpt-4o","enabled":true}`, http.StatusOK)

	// 全局官方价 2.5/10(base=750);渠道覆盖价 2/8(base=100*2+50*8=600);渠道倍率 2 → cost=600*2=1200。
	doJSON(t, r, http.MethodPost, "/admin/prices", token,
		`{"model":"gpt-4o","input_per_1m":2.5,"output_per_1m":10,"enabled":true}`, http.StatusOK)
	doJSON(t, r, http.MethodPost, "/admin/prices", token,
		`{"channel_id":`+strconv.FormatUint(channelID, 10)+`,"model":"gpt-4o","input_per_1m":2,"output_per_1m":8,"enabled":true}`, http.StatusOK)
	require.NoError(t, g.Model(&model.Channel{}).Where("id = ?", channelID).Update("rate_multiplier", 2.0).Error)

	appResp := doJSON(t, r, http.MethodPost, "/admin/apps", token, `{"name":"team","quota":1000000}`, http.StatusOK)
	appID := uint64(appResp["id"].(float64))
	keyResp := doJSON(t, r, http.MethodPost, "/admin/api-keys", token,
		`{"name":"k1","app_id":`+strconv.FormatUint(appID, 10)+`}`, http.StatusOK)
	plainKey := keyResp["key"].(string)

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions",
		strings.NewReader(`{"model":"gpt-4o","messages":[{"role":"user","content":"hi"}]}`))
	req.Header.Set("Authorization", "Bearer "+plainKey)
	req.Header.Set("Content-Type", "application/json")
	r.ServeHTTP(w, req)
	require.Equal(t, http.StatusOK, w.Code, "resp=%s", w.Body.String())

	var logRow model.RequestLog
	require.NoError(t, g.Order("id desc").First(&logRow).Error)
	require.Equal(t, int64(1200), logRow.CostMicros, "覆盖价 600 × 渠道倍率 2 = 1200")
}

func TestAliasBillsByServedModelE2E(t *testing.T) {
	gin.SetMode(gin.TestMode)
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"ok","choices":[],"usage":{"prompt_tokens":100,"completion_tokens":50,"total_tokens":150}}`))
	}))
	defer up.Close()

	cfg := config.Config{}
	cfg.Storage.Local.Root = t.TempDir() // 审计正文本地存储落临时目录，避免测试残留
	cfg.Security.APIKeyPrefix = "sk-saig"
	cfg.Security.ChannelKeyAESKey = "0123456789abcdef0123456789abcdef"
	cfg.JWT.Secret = "alias-bill-secret"
	cfg.JWT.ExpireHours = 1

	g := dbtest.OpenInMemory(t)
	require.NoError(t, db.Migrate(g, model.Migrations("admin", "sag12345")))
	resetRouting(g)
	r, shutdown, err := NewWithShutdown(cfg, g, nil)
	require.NoError(t, err)
	t.Cleanup(shutdown) // 先停审计 worker 等后台 goroutine，再让 TempDir 清理（Cleanup 为 LIFO）
	token := doJSON(t, r, http.MethodPost, "/admin/login", "",
		`{"username":"admin","password":"sag12345"}`, http.StatusOK)["token"].(string)

	chResp := doJSON(t, r, http.MethodPost, "/admin/channels", token,
		`{"type":"openai","name":"c1","base_url":"`+up.URL+`","api_key":"k"}`, http.StatusOK)
	channelID := uint64(chResp["id"].(float64))
	doJSON(t, r, http.MethodPost, "/admin/channel-models", token,
		`{"channel_id":`+strconv.FormatUint(channelID, 10)+`,"model":"real-model","enabled":true}`, http.StatusOK)
	// 价格只登记在目标模型 real-model 下(2.5/10 → 100*2.5+50*10=750)。
	doJSON(t, r, http.MethodPost, "/admin/prices", token,
		`{"model":"real-model","input_per_1m":2.5,"output_per_1m":10,"enabled":true}`, http.StatusOK)
	// 别名 myalias → real-model。
	al := doJSON(t, r, http.MethodPost, "/admin/aliases", token, `{"alias":"myalias"}`, http.StatusOK)
	doJSON(t, r, http.MethodPost, "/admin/alias-routes", token,
		`{"alias_id":`+itoa(al["id"].(float64))+`,"channel_id":`+strconv.FormatUint(channelID, 10)+`,"model":"real-model","weight":1,"priority":0}`, http.StatusOK)

	appResp := doJSON(t, r, http.MethodPost, "/admin/apps", token, `{"name":"team","quota":1000000}`, http.StatusOK)
	appID := uint64(appResp["id"].(float64))
	keyResp := doJSON(t, r, http.MethodPost, "/admin/api-keys", token,
		`{"name":"k1","app_id":`+strconv.FormatUint(appID, 10)+`}`, http.StatusOK)
	plainKey := keyResp["key"].(string)

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions",
		strings.NewReader(`{"model":"myalias","messages":[{"role":"user","content":"hi"}]}`))
	req.Header.Set("Authorization", "Bearer "+plainKey)
	req.Header.Set("Content-Type", "application/json")
	r.ServeHTTP(w, req)
	require.Equal(t, http.StatusOK, w.Code, "resp=%s", w.Body.String())

	var logRow model.RequestLog
	require.NoError(t, g.Order("id desc").First(&logRow).Error)
	require.Equal(t, "real-model", logRow.Model, "日志应按命中模型 real-model(而非别名 myalias)")
	require.Equal(t, int64(750), logRow.CostMicros, "应按 real-model 价计费 750(别名名查不到价会是 0)")
}

func TestUpdateChannelTypeE2E(t *testing.T) {
	gin.SetMode(gin.TestMode)
	cfg := config.Config{}
	cfg.Storage.Local.Root = t.TempDir() // 审计正文本地存储落临时目录，避免测试残留
	cfg.Security.APIKeyPrefix = "sk-saig"
	cfg.Security.ChannelKeyAESKey = "0123456789abcdef0123456789abcdef"
	cfg.JWT.Secret = "ch-type-upd"
	cfg.JWT.ExpireHours = 1
	g := dbtest.OpenInMemory(t)
	require.NoError(t, db.Migrate(g, model.Migrations("admin", "sag12345")))
	resetRouting(g)
	r, shutdown, err := NewWithShutdown(cfg, g, nil)
	require.NoError(t, err)
	t.Cleanup(shutdown) // 先停审计 worker 等后台 goroutine，再让 TempDir 清理（Cleanup 为 LIFO）
	token := doJSON(t, r, http.MethodPost, "/admin/login", "", `{"username":"admin","password":"sag12345"}`, http.StatusOK)["token"].(string)
	// 建 openai 渠道
	ch := doJSON(t, r, http.MethodPost, "/admin/channels", token, `{"type":"openai","name":"C1","base_url":"http://a","api_key":"k"}`, http.StatusOK)
	id := uint64(ch["id"].(float64))
	// 更新 name + base_url
	doJSON(t, r, http.MethodPost, "/admin/channels/"+strconv.FormatUint(id, 10), token, `{"name":"C2","base_url":"http://b"}`, http.StatusOK)
	var c model.Channel
	require.NoError(t, g.First(&c, id).Error)
	require.Equal(t, "C2", c.Name)
	require.Equal(t, "http://b", c.BaseURL)
	require.Equal(t, "openai", c.Type)
}

func TestUpdateAppE2E(t *testing.T) {
	gin.SetMode(gin.TestMode)
	cfg := config.Config{}
	cfg.Storage.Local.Root = t.TempDir() // 审计正文本地存储落临时目录，避免测试残留
	cfg.Security.APIKeyPrefix = "sk-saig"
	cfg.Security.ChannelKeyAESKey = "0123456789abcdef0123456789abcdef"
	cfg.JWT.Secret = "app-upd"
	cfg.JWT.ExpireHours = 1
	g := dbtest.OpenInMemory(t)
	require.NoError(t, db.Migrate(g, model.Migrations("admin", "sag12345")))
	resetRouting(g)
	r, shutdown, err := NewWithShutdown(cfg, g, nil)
	require.NoError(t, err)
	t.Cleanup(shutdown) // 先停审计 worker 等后台 goroutine，再让 TempDir 清理（Cleanup 为 LIFO）
	token := doJSON(t, r, http.MethodPost, "/admin/login", "", `{"username":"admin","password":"sag12345"}`, http.StatusOK)["token"].(string)
	app := doJSON(t, r, http.MethodPost, "/admin/apps", token, `{"name":"A1","quota":1000000}`, http.StatusOK)
	id := uint64(app["id"].(float64))
	require.NoError(t, g.Model(&model.App{}).Where("id=?", id).Update("used_quota", int64(500)).Error)
	doJSON(t, r, http.MethodPost, "/admin/apps/"+strconv.FormatUint(id, 10), token, `{"name":"A2","quota":2000000}`, http.StatusOK)
	var a model.App
	require.NoError(t, g.First(&a, id).Error)
	require.Equal(t, "A2", a.Name)
	require.Equal(t, int64(2000000), a.Quota)
	require.Equal(t, int64(500), a.UsedQuota)
}

func TestUpdateChannelBaseURLE2E(t *testing.T) {
	gin.SetMode(gin.TestMode)
	cfg := config.Config{}
	cfg.Storage.Local.Root = t.TempDir() // 审计正文本地存储落临时目录，避免测试残留
	cfg.Security.APIKeyPrefix = "sk-saig"
	cfg.Security.ChannelKeyAESKey = "0123456789abcdef0123456789abcdef"
	cfg.JWT.Secret = "ch-base"
	cfg.JWT.ExpireHours = 1
	g := dbtest.OpenInMemory(t)
	require.NoError(t, db.Migrate(g, model.Migrations("admin", "sag12345")))
	resetRouting(g)
	r, shutdown, err := NewWithShutdown(cfg, g, nil)
	require.NoError(t, err)
	t.Cleanup(shutdown) // 先停审计 worker 等后台 goroutine，再让 TempDir 清理（Cleanup 为 LIFO）
	token := doJSON(t, r, http.MethodPost, "/admin/login", "", `{"username":"admin","password":"sag12345"}`, http.StatusOK)["token"].(string)
	ch := doJSON(t, r, http.MethodPost, "/admin/channels", token,
		`{"type":"openai","name":"c1","base_url":"http://old","api_key":"k"}`, http.StatusOK)
	id := uint64(ch["id"].(float64))
	doJSON(t, r, http.MethodPost, "/admin/channels/"+strconv.FormatUint(id, 10), token, `{"base_url":"http://new","weight":5}`, http.StatusOK)
	var c model.Channel
	require.NoError(t, g.First(&c, id).Error)
	require.Equal(t, "http://new", c.BaseURL)
	require.Equal(t, 5, c.Weight)
	require.Equal(t, "c1", c.Name)
}

// TestAnthropicPassthroughE2E 端到端验证 Anthropic 透传链路：
// 建 anthropic provider/channel → 发 POST /v1/messages → mock 上游收到 /v1/messages + x-api-key →
// 响应原样透传客户端 → 请求日志 request_type=messages + cost=180。
func TestAnthropicPassthroughE2E(t *testing.T) {
	gin.SetMode(gin.TestMode)
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "/v1/messages", r.URL.Path)
		require.NotEmpty(t, r.Header.Get("x-api-key"))
		raw, _ := io.ReadAll(r.Body)
		var b map[string]any
		json.Unmarshal(raw, &b)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"msg_1","type":"message","role":"assistant","content":[{"type":"text","text":"I am Claude"}],"model":"` + b["model"].(string) + `","stop_reason":"end_turn","usage":{"input_tokens":20,"output_tokens":8}}`))
	}))
	defer up.Close()

	cfg := config.Config{}
	cfg.Storage.Local.Root = t.TempDir() // 审计正文本地存储落临时目录，避免测试残留
	cfg.Security.APIKeyPrefix = "sk-saig"
	cfg.Security.ChannelKeyAESKey = "0123456789abcdef0123456789abcdef"
	cfg.JWT.Secret = "anth-e2e"
	cfg.JWT.ExpireHours = 1

	g := dbtest.OpenInMemory(t)
	require.NoError(t, db.Migrate(g, model.Migrations("admin", "sag12345")))
	resetRouting(g)
	r, shutdown, err := NewWithShutdown(cfg, g, nil)
	require.NoError(t, err)
	t.Cleanup(shutdown) // 先停审计 worker 等后台 goroutine，再让 TempDir 清理（Cleanup 为 LIFO）
	token := doJSON(t, r, http.MethodPost, "/admin/login", "", `{"username":"admin","password":"sag12345"}`, http.StatusOK)["token"].(string)

	ch := doJSON(t, r, http.MethodPost, "/admin/channels", token,
		`{"type":"anthropic","name":"ant-ch","base_url":"`+up.URL+`","api_key":"sk-ant-test"}`, http.StatusOK)
	channelID := uint64(ch["id"].(float64))
	doJSON(t, r, http.MethodPost, "/admin/channel-models", token,
		`{"channel_id":`+strconv.FormatUint(channelID, 10)+`,"model":"claude-3-5-sonnet","enabled":true}`, http.StatusOK)
	doJSON(t, r, http.MethodPost, "/admin/prices", token,
		`{"model":"claude-3-5-sonnet","input_per_1m":3,"output_per_1m":15,"enabled":true}`, http.StatusOK)
	appResp := doJSON(t, r, http.MethodPost, "/admin/apps", token, `{"name":"team","quota":1000000}`, http.StatusOK)
	appID := uint64(appResp["id"].(float64))
	keyResp := doJSON(t, r, http.MethodPost, "/admin/api-keys", token,
		`{"name":"k1","app_id":`+strconv.FormatUint(appID, 10)+`}`, http.StatusOK)
	plainKey := keyResp["key"].(string)

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/messages",
		strings.NewReader(`{"model":"claude-3-5-sonnet","max_tokens":100,"messages":[{"role":"user","content":"hi"}]}`))
	req.Header.Set("x-api-key", plainKey)
	req.Header.Set("Content-Type", "application/json")
	r.ServeHTTP(w, req)
	require.Equal(t, http.StatusOK, w.Code, "resp=%s", w.Body.String())
	require.Contains(t, w.Body.String(), `"I am Claude"`)

	var logRow model.RequestLog
	require.NoError(t, g.Order("id desc").First(&logRow).Error)
	require.Equal(t, "claude-3-5-sonnet", logRow.Model)
	require.Equal(t, "messages", logRow.RequestType)
	require.Equal(t, int64(180), logRow.CostMicros) // 20*3 + 8*15 = 180
}

func TestResponsesPassthroughE2E(t *testing.T) {
	gin.SetMode(gin.TestMode)
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "/v1/responses", r.URL.Path)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"resp_1","object":"response","model":"gpt-4o","output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"hello from responses"}]}],"usage":{"input_tokens":15,"output_tokens":6,"total_tokens":21}}`))
	}))
	defer up.Close()

	cfg := config.Config{}
	cfg.Storage.Local.Root = t.TempDir() // 审计正文本地存储落临时目录，避免测试残留
	cfg.Security.APIKeyPrefix = "sk-saig"
	cfg.Security.ChannelKeyAESKey = "0123456789abcdef0123456789abcdef"
	cfg.JWT.Secret = "resp-e2e"
	cfg.JWT.ExpireHours = 1

	g := dbtest.OpenInMemory(t)
	require.NoError(t, db.Migrate(g, model.Migrations("admin", "sag12345")))
	resetRouting(g)
	r, shutdown, err := NewWithShutdown(cfg, g, nil)
	require.NoError(t, err)
	t.Cleanup(shutdown) // 先停审计 worker 等后台 goroutine，再让 TempDir 清理（Cleanup 为 LIFO）
	token := doJSON(t, r, http.MethodPost, "/admin/login", "", `{"username":"admin","password":"sag12345"}`, http.StatusOK)["token"].(string)

	ch := doJSON(t, r, http.MethodPost, "/admin/channels", token,
		`{"type":"openai","name":"c1","base_url":"`+up.URL+`/v1","api_key":"sk-test"}`, http.StatusOK)
	channelID := uint64(ch["id"].(float64))
	doJSON(t, r, http.MethodPost, "/admin/channel-models", token,
		`{"channel_id":`+strconv.FormatUint(channelID, 10)+`,"model":"gpt-4o","enabled":true}`, http.StatusOK)
	doJSON(t, r, http.MethodPost, "/admin/prices", token,
		`{"model":"gpt-4o","input_per_1m":2.5,"output_per_1m":10,"enabled":true}`, http.StatusOK)
	appResp := doJSON(t, r, http.MethodPost, "/admin/apps", token, `{"name":"team","quota":1000000}`, http.StatusOK)
	appID := uint64(appResp["id"].(float64))
	keyResp := doJSON(t, r, http.MethodPost, "/admin/api-keys", token,
		`{"name":"k1","app_id":`+strconv.FormatUint(appID, 10)+`}`, http.StatusOK)
	plainKey := keyResp["key"].(string)

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/responses",
		strings.NewReader(`{"model":"gpt-4o","input":"hi"}`))
	req.Header.Set("Authorization", "Bearer "+plainKey)
	req.Header.Set("Content-Type", "application/json")
	r.ServeHTTP(w, req)
	require.Equal(t, http.StatusOK, w.Code, "resp=%s", w.Body.String())
	require.Contains(t, w.Body.String(), `"hello from responses"`)

	var logRow model.RequestLog
	require.NoError(t, g.Order("id desc").First(&logRow).Error)
	require.Equal(t, "gpt-4o", logRow.Model)
	require.Equal(t, "responses", logRow.RequestType)
	// cost = 15*2.5 + 6*10 = 37.5+60 = 97.5 → Round = 98
	require.Equal(t, int64(98), logRow.CostMicros)
}

// TestRequestIDHeaderAndLogQueryE2E 贯通验证 P11 三件事：
// a) 创建令牌返回体 record.key 含明文（可回查）；
// b) /v1 响应头带 X-SAIG-Request-Id；
// c) /admin/request-logs?request_id= 能精确查到该条日志。
func TestRequestIDHeaderAndLogQueryE2E(t *testing.T) {
	gin.SetMode(gin.TestMode)

	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"id":"ok","choices":[]}`))
	}))
	defer up.Close()

	cfg := config.Config{}
	cfg.Storage.Local.Root = t.TempDir() // 审计正文本地存储落临时目录，避免测试残留
	cfg.Security.APIKeyPrefix = "sk-saig"
	cfg.Security.ChannelKeyAESKey = "0123456789abcdef0123456789abcdef"
	cfg.JWT.Secret = "s"
	cfg.JWT.ExpireHours = 1

	g := dbtest.OpenInMemory(t)
	require.NoError(t, db.Migrate(g, model.Migrations("admin", "sag12345")))
	resetRouting(g)

	r, shutdown, err := NewWithShutdown(cfg, g, nil)
	require.NoError(t, err)
	t.Cleanup(shutdown) // 先停审计 worker 等后台 goroutine，再让 TempDir 清理（Cleanup 为 LIFO）

	token := doJSON(t, r, http.MethodPost, "/admin/login", "",
		`{"username":"admin","password":"sag12345"}`, http.StatusOK)["token"].(string)

	chResp := doJSON(t, r, http.MethodPost, "/admin/channels", token,
		`{"type":"openai","name":"c1","base_url":"`+up.URL+`","api_key":"sk-up"}`, http.StatusOK)
	channelID := chResp["id"].(float64)
	doJSON(t, r, http.MethodPost, "/admin/channel-models", token,
		`{"channel_id":`+itoa(channelID)+`,"model":"gpt-4o","enabled":true}`, http.StatusOK)
	doJSON(t, r, http.MethodPost, "/admin/prices", token,
		`{"model":"gpt-4o","input_per_1m":1,"output_per_1m":1,"enabled":true}`, http.StatusOK)
	appResp := doJSON(t, r, http.MethodPost, "/admin/apps", token,
		`{"name":"pg-app","quota":0}`, http.StatusOK)

	// a) 创建令牌：返回体 record.key == 明文
	keyResp := doJSON(t, r, http.MethodPost, "/admin/api-keys", token,
		`{"name":"k1","app_id":`+itoa(appResp["id"].(float64))+`}`, http.StatusOK)
	plainKey := keyResp["key"].(string)
	rec := keyResp["record"].(map[string]any)
	require.Equal(t, plainKey, rec["key"])

	// b) /v1 响应头带请求 ID
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions",
		strings.NewReader(`{"model":"gpt-4o","messages":[{"role":"user","content":"hi"}]}`))
	req.Header.Set("Authorization", "Bearer "+plainKey)
	req.Header.Set("Content-Type", "application/json")
	r.ServeHTTP(w, req)
	require.Equal(t, http.StatusOK, w.Code)
	reqID := w.Header().Get("X-SAIG-Request-Id")
	require.NotEmpty(t, reqID)

	// c) 按 request_id 精确查日志
	logs := doJSON(t, r, http.MethodGet, "/admin/request-logs?request_id="+reqID, token, "", http.StatusOK)
	require.Equal(t, float64(1), logs["total"])
	item := logs["items"].([]any)[0].(map[string]any)
	require.Equal(t, reqID, item["request_id"])
	// 查不存在的 ID -> 0 条
	none := doJSON(t, r, http.MethodGet, "/admin/request-logs?request_id=nope", token, "", http.StatusOK)
	require.Equal(t, float64(0), none["total"])
}

// TestNoPriceRejectedAllEndpointsE2E（编排收敛行为变更①）：未配价模型统一 400 拒绝、不派发上游。
// 此前仅 chat 有价格前置校验，messages/responses/embeddings/images 会放行并消耗上游但不计费；
// 收敛到 BillingCtx.Prepare 后五端点口径一致（产品契约见 CLAUDE.md 主链路第 5 步）。
func TestNoPriceRejectedAllEndpointsE2E(t *testing.T) {
	gin.SetMode(gin.TestMode)
	var upHits int
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		upHits++
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"ok"}`))
	}))
	defer up.Close()

	cfg := config.Config{}
	cfg.Storage.Local.Root = t.TempDir() // 审计正文本地存储落临时目录，避免测试残留
	cfg.Security.APIKeyPrefix = "sk-saig"
	cfg.Security.ChannelKeyAESKey = "0123456789abcdef0123456789abcdef"
	cfg.JWT.Secret = "no-price-secret"
	cfg.JWT.ExpireHours = 1

	g := dbtest.OpenInMemory(t)
	require.NoError(t, db.Migrate(g, model.Migrations("admin", "sag12345")))
	resetRouting(g)
	r, shutdown, err := NewWithShutdown(cfg, g, nil)
	require.NoError(t, err)
	t.Cleanup(shutdown) // 先停审计 worker 等后台 goroutine，再让 TempDir 清理（Cleanup 为 LIFO）
	token := doJSON(t, r, http.MethodPost, "/admin/login", "",
		`{"username":"admin","password":"sag12345"}`, http.StatusOK)["token"].(string)

	// openai / anthropic 渠道各一，均声明 gpt-4o；关键：全程不配任何价格。
	oa := doJSON(t, r, http.MethodPost, "/admin/channels", token,
		`{"type":"openai","name":"oa","base_url":"`+up.URL+`","api_key":"k"}`, http.StatusOK)
	doJSON(t, r, http.MethodPost, "/admin/channel-models", token,
		`{"channel_id":`+itoa(oa["id"].(float64))+`,"model":"gpt-4o","enabled":true}`, http.StatusOK)
	an := doJSON(t, r, http.MethodPost, "/admin/channels", token,
		`{"type":"anthropic","name":"an","base_url":"`+up.URL+`","api_key":"k"}`, http.StatusOK)
	doJSON(t, r, http.MethodPost, "/admin/channel-models", token,
		`{"channel_id":`+itoa(an["id"].(float64))+`,"model":"gpt-4o","enabled":true}`, http.StatusOK)

	keySvc := apikey.NewService(apikey.NewRepo(g), cfg.Security.APIKeyPrefix)
	plainKey, _, err := keySvc.Generate("k", nil)
	require.NoError(t, err)

	post := func(path, body string) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
		req.Header.Set("Authorization", "Bearer "+plainKey)
		req.Header.Set("Content-Type", "application/json")
		r.ServeHTTP(w, req)
		return w
	}

	// ① messages（此前无价格前置校验，会放行消耗上游）→ 现统一 400。
	w := post("/v1/messages", `{"model":"gpt-4o","max_tokens":100,"messages":[{"role":"user","content":"hi"}]}`)
	require.Equal(t, http.StatusBadRequest, w.Code, "messages 未配价应 400，resp=%s", w.Body.String())
	require.Contains(t, w.Body.String(), "no price configured for model")

	// ② embeddings（同上）→ 400。
	w = post("/v1/embeddings", `{"model":"gpt-4o","input":"hi"}`)
	require.Equal(t, http.StatusBadRequest, w.Code, "embeddings 未配价应 400，resp=%s", w.Body.String())
	require.Contains(t, w.Body.String(), "no price configured for model")

	// ③ responses / images / chat 同口径 400。
	w = post("/v1/responses", `{"model":"gpt-4o","input":"hi"}`)
	require.Equal(t, http.StatusBadRequest, w.Code, "responses 未配价应 400，resp=%s", w.Body.String())
	w = post("/v1/images/generations", `{"model":"gpt-4o","prompt":"a cat"}`)
	require.Equal(t, http.StatusBadRequest, w.Code, "images 未配价应 400，resp=%s", w.Body.String())
	w = post("/v1/chat/completions", `{"model":"gpt-4o","messages":[{"role":"user","content":"hi"}]}`)
	require.Equal(t, http.StatusBadRequest, w.Code, "chat 未配价应 400（既有行为回归），resp=%s", w.Body.String())

	require.Equal(t, 0, upHits, "未配价请求不应派发到上游")
}

// TestQuotaBeforeRouteE2E（编排收敛行为变更②）：校验顺序统一为 govern→配额→路由。
// chat 在「App 配额耗尽且模型无渠道」双重失败时，此前先查路由报 404，现统一先查配额报 402。
func TestQuotaBeforeRouteE2E(t *testing.T) {
	gin.SetMode(gin.TestMode)
	up := multiProtoUpstream()
	defer up.Close()
	r, plainKey, g := setupMultiProto(t, up.URL)

	// 把 App 配额顶满：quota 从 0（不限）改为 1 且 used=1 → 配额耗尽。
	appID := appIDFromKey(t, g, plainKey)
	require.NoError(t, g.Model(&model.App{}).Where("id = ?", appID).
		Updates(map[string]any{"quota": int64(1), "used_quota": int64(1)}).Error)

	// 请求一个任何渠道都没有的模型：配额检查在路由之前 → 402（而非 404）。
	code, body := chatReq(r, plainKey, "model-not-exists")
	require.Equal(t, http.StatusPaymentRequired, code, "配额耗尽应先于路由报 402，resp=%s", body)
}

// 创建渠道带 auth_style/custom_headers；非法 custom_headers JSON 应 400；非法 auth_style 回落 auto。
func TestChannelOutboundHeadersE2E(t *testing.T) {
	gin.SetMode(gin.TestMode)
	cfg := config.Config{}
	cfg.Storage.Local.Root = t.TempDir() // 审计正文本地存储落临时目录，避免测试残留
	cfg.Security.APIKeyPrefix = "sk-saig"
	cfg.Security.ChannelKeyAESKey = "0123456789abcdef0123456789abcdef"
	cfg.JWT.Secret = "ch-headers"
	cfg.JWT.ExpireHours = 1
	g := dbtest.OpenInMemory(t)
	require.NoError(t, db.Migrate(g, model.Migrations("admin", "sag12345")))
	resetRouting(g)
	r, shutdown, err := NewWithShutdown(cfg, g, nil)
	require.NoError(t, err)
	t.Cleanup(shutdown) // 先停审计 worker 等后台 goroutine，再让 TempDir 清理（Cleanup 为 LIFO）
	token := doJSON(t, r, http.MethodPost, "/admin/login", "",
		`{"username":"admin","password":"sag12345"}`, http.StatusOK)["token"].(string)

	// 合法：auth_style=bearer + 合法 JSON。
	ch1 := doJSON(t, r, http.MethodPost, "/admin/channels", token,
		`{"name":"c1","type":"anthropic","api_key":"k","auth_style":"bearer","custom_headers":"{\"HTTP-Referer\":\"https://x.test\"}"}`,
		http.StatusOK)
	id1 := uint64(ch1["id"].(float64))
	var c1 model.Channel
	require.NoError(t, g.First(&c1, id1).Error)
	require.Equal(t, "bearer", c1.AuthStyle)
	require.JSONEq(t, `{"HTTP-Referer":"https://x.test"}`, c1.CustomHeaders)

	// 非法 custom_headers（非 JSON 对象）→ 400。
	doJSON(t, r, http.MethodPost, "/admin/channels", token,
		`{"name":"c2","type":"openai","api_key":"k","custom_headers":"not-json"}`, http.StatusBadRequest)

	// 非法 auth_style → 回落 auto。
	ch3 := doJSON(t, r, http.MethodPost, "/admin/channels", token,
		`{"name":"c3","type":"anthropic","api_key":"k","auth_style":"weird"}`, http.StatusOK)
	id3 := uint64(ch3["id"].(float64))
	var c3 model.Channel
	require.NoError(t, g.First(&c3, id3).Error)
	require.Equal(t, "auto", c3.AuthStyle)

	// UpdateChannel：合法更新 auth_style/custom_headers。
	doJSON(t, r, http.MethodPost, "/admin/channels/"+strconv.FormatUint(id1, 10), token,
		`{"auth_style":"x-api-key","custom_headers":"{\"X-Foo\":\"bar\"}"}`, http.StatusOK)
	require.NoError(t, g.First(&c1, id1).Error)
	require.Equal(t, "x-api-key", c1.AuthStyle)
	require.JSONEq(t, `{"X-Foo":"bar"}`, c1.CustomHeaders)

	// UpdateChannel：非法 custom_headers → 400。
	doJSON(t, r, http.MethodPost, "/admin/channels/"+strconv.FormatUint(id1, 10), token,
		`{"custom_headers":"not-json"}`, http.StatusBadRequest)

	// UpdateChannel：非法 auth_style → 回落 auto。
	doJSON(t, r, http.MethodPost, "/admin/channels/"+strconv.FormatUint(id1, 10), token,
		`{"auth_style":"weird"}`, http.StatusOK)
	require.NoError(t, g.First(&c1, id1).Error)
	require.Equal(t, "auto", c1.AuthStyle)
}

// TestV1ErrorEnvelopeE2E 锁定 /v1 业务面网关级错误信封与官方 SDK 契约一致：
// openai 端点 error 恒为对象 {"error":{"message","type"}}；
// anthropic 端点为 {"type":"error","error":{"type","message"}}；
// message 文本保留现状，type 按 status 映射（404→not_found_error、401→authentication_error）。
func TestV1ErrorEnvelopeE2E(t *testing.T) {
	gin.SetMode(gin.TestMode)
	up := multiProtoUpstream()
	defer up.Close()
	r, plainKey, _ := setupMultiProto(t, up.URL)

	// openai（chat）：未知模型 → 404，error 为对象且 type=not_found_error。
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions",
		strings.NewReader(`{"model":"no-such-model","messages":[{"role":"user","content":"hi"}]}`))
	req.Header.Set("Authorization", "Bearer "+plainKey)
	r.ServeHTTP(w, req)
	require.Equal(t, http.StatusNotFound, w.Code, "resp=%s", w.Body.String())
	var oa struct {
		Error map[string]any `json:"error"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &oa), "resp=%s", w.Body.String())
	require.NotNil(t, oa.Error, "openai 信封 error 须为对象")
	require.Equal(t, "no channel for model", oa.Error["message"])
	require.Equal(t, "not_found_error", oa.Error["type"])

	// anthropic（messages）：未知模型 → 404，顶层 type=error 且 error.type=not_found_error。
	w = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodPost, "/v1/messages",
		strings.NewReader(`{"model":"no-such-model","max_tokens":10,"messages":[{"role":"user","content":"hi"}]}`))
	req.Header.Set("x-api-key", plainKey)
	r.ServeHTTP(w, req)
	require.Equal(t, http.StatusNotFound, w.Code, "resp=%s", w.Body.String())
	var an struct {
		Type  string         `json:"type"`
		Error map[string]any `json:"error"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &an), "resp=%s", w.Body.String())
	require.Equal(t, "error", an.Type)
	require.Equal(t, "not_found_error", an.Error["type"])
	require.Equal(t, "no channel for model", an.Error["message"])

	// 中间件 401：无 key 时按路径分流信封——chat 走 openai、messages 走 anthropic。
	w = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"gpt-4o"}`))
	r.ServeHTTP(w, req)
	require.Equal(t, http.StatusUnauthorized, w.Code)
	oa.Error = nil
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &oa), "resp=%s", w.Body.String())
	require.Equal(t, "missing api key", oa.Error["message"])
	require.Equal(t, "authentication_error", oa.Error["type"])

	w = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(`{"model":"gpt-4o"}`))
	r.ServeHTTP(w, req)
	require.Equal(t, http.StatusUnauthorized, w.Code)
	an = struct {
		Type  string         `json:"type"`
		Error map[string]any `json:"error"`
	}{}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &an), "resp=%s", w.Body.String())
	require.Equal(t, "error", an.Type)
	require.Equal(t, "authentication_error", an.Error["type"])
	require.Equal(t, "missing api key", an.Error["message"])
}
