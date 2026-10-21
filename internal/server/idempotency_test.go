package server

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/ruanun/simple-ai-gateway/internal/config"
	"github.com/ruanun/simple-ai-gateway/internal/db"
	"github.com/ruanun/simple-ai-gateway/internal/dbtest"
	"github.com/ruanun/simple-ai-gateway/internal/model"
	"github.com/stretchr/testify/require"
)

func TestIdempotencyKeyNonStreaming(t *testing.T) {
	gin.SetMode(gin.TestMode)
	var calls atomic.Int64
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		_, _ = io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"ok","choices":[],"usage":{"prompt_tokens":10,"completion_tokens":5,"total_tokens":15}}`))
	}))
	defer up.Close()

	cfg := config.Config{}
	cfg.Storage.Local.Root = t.TempDir()
	cfg.Security.APIKeyPrefix = "sk-saig"
	cfg.Security.ChannelKeyAESKey = "0123456789abcdef0123456789abcdef"
	cfg.JWT.Secret = "idem-secret"
	cfg.JWT.ExpireHours = 1

	g := dbtest.OpenInMemory(t)
	require.NoError(t, db.Migrate(g, model.Migrations("admin", "sag12345")))
	resetRouting(g)
	r, shutdown, err := NewWithShutdown(cfg, g, nil)
	require.NoError(t, err)
	t.Cleanup(shutdown)

	token := doJSON(t, r, http.MethodPost, "/admin/login", "",
		`{"username":"admin","password":"sag12345"}`, http.StatusOK)["token"].(string)
	ch := doJSON(t, r, http.MethodPost, "/admin/channels", token,
		`{"type":"openai","name":"c","base_url":"`+up.URL+`","api_key":"sk-up"}`, http.StatusOK)
	doJSON(t, r, http.MethodPost, "/admin/channel-models", token,
		`{"channel_id":`+itoa(ch["id"].(float64))+`,"model":"gpt-4o","enabled":true}`, http.StatusOK)
	doJSON(t, r, http.MethodPost, "/admin/prices", token,
		`{"model":"gpt-4o","input_per_1m":1,"output_per_1m":1,"enabled":true}`, http.StatusOK)
	app := doJSON(t, r, http.MethodPost, "/admin/apps", token, `{"name":"team","quota":0}`, http.StatusOK)
	key := doJSON(t, r, http.MethodPost, "/admin/api-keys", token,
		`{"name":"k","app_id":`+itoa(app["id"].(float64))+`}`, http.StatusOK)["key"].(string)

	const body = `{"model":"gpt-4o","messages":[{"role":"user","content":"hi"}]}`
	post := func(payload string, idem string) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(payload))
		req.Header.Set("Authorization", "Bearer "+key)
		req.Header.Set("Content-Type", "application/json")
		if idem != "" {
			req.Header.Set("Idempotency-Key", idem)
		}
		r.ServeHTTP(w, req)
		return w
	}

	first := post(body, "idem-1")
	require.Equal(t, http.StatusOK, first.Code, first.Body.String())
	require.Equal(t, int64(1), calls.Load())

	second := post(body, "idem-1")
	require.Equal(t, http.StatusOK, second.Code, second.Body.String())
	require.Equal(t, int64(1), calls.Load(), "同 key 同 payload 不应再次打到上游")
	require.Equal(t, "true", second.Header().Get("Idempotent-Replayed"))
	require.Equal(t, first.Body.String(), second.Body.String())

	conflict := post(`{"model":"gpt-4o","messages":[{"role":"user","content":"different"}]}`, "idem-1")
	require.Equal(t, http.StatusConflict, conflict.Code, conflict.Body.String())
	require.Equal(t, int64(1), calls.Load())

	stream := post(`{"model":"gpt-4o","stream":true,"messages":[{"role":"user","content":"hi"}]}`, "idem-2")
	require.Equal(t, http.StatusBadRequest, stream.Code, stream.Body.String())
	require.Equal(t, int64(1), calls.Load(), "流式请求不应进入幂等缓存")
}
