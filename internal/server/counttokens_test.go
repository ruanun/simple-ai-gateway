package server

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/ruanun/simple-ai-gateway/internal/config"
	"github.com/ruanun/simple-ai-gateway/internal/db"
	"github.com/ruanun/simple-ai-gateway/internal/dbtest"
	"github.com/ruanun/simple-ai-gateway/internal/model"
	"github.com/stretchr/testify/require"
)

func TestCountTokensEndpoints(t *testing.T) {
	gin.SetMode(gin.TestMode)

	var mu sync.Mutex
	var paths []string
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		paths = append(paths, r.URL.Path)
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"input_tokens":42}`))
	}))
	defer up.Close()

	cfg := config.Config{}
	cfg.Storage.Local.Root = t.TempDir()
	cfg.Security.APIKeyPrefix = "sk-saig"
	cfg.Security.ChannelKeyAESKey = "0123456789abcdef0123456789abcdef"
	cfg.JWT.Secret = "count-tokens-secret"
	cfg.JWT.ExpireHours = 1

	g := dbtest.OpenInMemory(t)
	require.NoError(t, db.Migrate(g, model.Migrations("admin", "sag12345")))
	resetRouting(g)

	r, shutdown, err := NewWithShutdown(cfg, g, nil)
	require.NoError(t, err)
	t.Cleanup(shutdown)

	token := doJSON(t, r, http.MethodPost, "/admin/login", "",
		`{"username":"admin","password":"sag12345"}`, http.StatusOK)["token"].(string)

	// Anthropic 渠道：base_url 不带 /v1，count_tokens 走 /v1/messages/count_tokens。
	chA := doJSON(t, r, http.MethodPost, "/admin/channels", token,
		`{"type":"anthropic","name":"a1","base_url":"`+up.URL+`","api_key":"sk-a"}`, http.StatusOK)
	doJSON(t, r, http.MethodPost, "/admin/channel-models", token,
		`{"channel_id":`+itoa(chA["id"].(float64))+`,"model":"claude-3","enabled":true}`, http.StatusOK)

	// OpenAI 渠道：按项目约定 base_url 含 /v1，input_tokens 走 /responses/input_tokens。
	chO := doJSON(t, r, http.MethodPost, "/admin/channels", token,
		`{"type":"openai","name":"o1","base_url":"`+up.URL+`/v1","api_key":"sk-o"}`, http.StatusOK)
	doJSON(t, r, http.MethodPost, "/admin/channel-models", token,
		`{"channel_id":`+itoa(chO["id"].(float64))+`,"model":"gpt-4o","enabled":true}`, http.StatusOK)

	app := doJSON(t, r, http.MethodPost, "/admin/apps", token, `{"name":"team","quota":0}`, http.StatusOK)
	key := doJSON(t, r, http.MethodPost, "/admin/api-keys", token,
		`{"name":"k1","app_id":`+itoa(app["id"].(float64))+`}`, http.StatusOK)["key"].(string)

	// 故意不配价格：count_tokens 不应被价格前置校验拦截。
	doJSON(t, r, http.MethodPost, "/v1/messages/count_tokens", key,
		`{"model":"claude-3","messages":[{"role":"user","content":"hi"}]}`, http.StatusOK)
	doJSON(t, r, http.MethodPost, "/v1/responses/input_tokens", key,
		`{"model":"gpt-4o","input":"hi"}`, http.StatusOK)

	mu.Lock()
	got := append([]string(nil), paths...)
	mu.Unlock()
	require.Contains(t, got, "/v1/messages/count_tokens")
	require.Contains(t, got, "/v1/responses/input_tokens")
}

// 用 raw HTTP 再确认一次：count_tokens 失败时错误体走对应协议的渲染，不 panic。
func TestCountTokensNoCandidates(t *testing.T) {
	gin.SetMode(gin.TestMode)
	cfg := config.Config{}
	cfg.Storage.Local.Root = t.TempDir()
	cfg.Security.APIKeyPrefix = "sk-saig"
	cfg.Security.ChannelKeyAESKey = "0123456789abcdef0123456789abcdef"
	cfg.JWT.Secret = "count-tokens-secret"
	cfg.JWT.ExpireHours = 1

	g := dbtest.OpenInMemory(t)
	require.NoError(t, db.Migrate(g, model.Migrations("admin", "sag12345")))
	resetRouting(g)
	r, shutdown, err := NewWithShutdown(cfg, g, nil)
	require.NoError(t, err)
	t.Cleanup(shutdown)

	token := doJSON(t, r, http.MethodPost, "/admin/login", "",
		`{"username":"admin","password":"sag12345"}`, http.StatusOK)["token"].(string)
	app := doJSON(t, r, http.MethodPost, "/admin/apps", token, `{"name":"team","quota":0}`, http.StatusOK)
	key := doJSON(t, r, http.MethodPost, "/admin/api-keys", token,
		`{"name":"k1","app_id":`+itoa(app["id"].(float64))+`}`, http.StatusOK)["key"].(string)

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/messages/count_tokens",
		strings.NewReader(`{"model":"claude-3","messages":[]}`))
	req.Header.Set("Authorization", "Bearer "+key)
	r.ServeHTTP(w, req)
	require.Equal(t, http.StatusNotFound, w.Code)
}
