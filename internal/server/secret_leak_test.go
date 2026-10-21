package server

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/ruanun/simple-ai-gateway/internal/model"
	"github.com/stretchr/testify/require"
)

// 上游在错误响应里回显本次使用的凭证（非 sk- 前缀的上游 key、渠道自定义头的值）时，回给客户端的副本
// 须按原文替换掉——通用正则只认 sk-/Bearer，中转站/Azure/Gemini 风格的 key 会原样泄露给调用方。
// chat 透传（Relay）与 embeddings（DispatchRaw）两条错误回写路径都验；请求日志里客户端误带的凭证类头须脱敏。
func TestUpstreamErrorDoesNotLeakCredentials(t *testing.T) {
	gin.SetMode(gin.TestMode)
	const upKey = "AZ0123456789abcdefXYZ"
	const relaySecret = "relay-shared-secret-value"
	echo := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"error":{"message":"invalid key ` + upKey + ` (header X-Relay-Secret=` +
			r.Header.Get("X-Relay-Secret") + `, auth=` + r.Header.Get("Authorization") + `)"}}`))
	}))
	defer echo.Close()
	up := multiProtoUpstream()
	defer up.Close()
	r, key, g := setupMultiProto(t, up.URL)
	admin := doJSON(t, r, http.MethodPost, "/admin/login", "",
		`{"username":"admin","password":"sag12345"}`, http.StatusOK)["token"].(string)

	// 停掉默认 openai 渠道，换成会回显凭证的渠道
	var c1 model.Channel
	require.NoError(t, g.Where("name = ?", "c1").First(&c1).Error)
	doJSON(t, r, http.MethodPost, "/admin/channels/"+itoa(float64(c1.ID)), admin, `{"status":0}`, http.StatusOK)
	ch := doJSON(t, r, http.MethodPost, "/admin/channels", admin,
		`{"type":"openai","name":"leaky","base_url":"`+echo.URL+`","api_key":"`+upKey+`","custom_headers":"{\"X-Relay-Secret\":\"`+relaySecret+`\"}"}`, http.StatusOK)
	doJSON(t, r, http.MethodPost, "/admin/channel-models", admin,
		`{"channel_id":`+itoa(ch["id"].(float64))+`,"model":"gpt-4o","enabled":true}`, http.StatusOK)

	assertNoLeak := func(path, body string) {
		w := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
		req.Header.Set("Authorization", "Bearer "+key)
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Api-Key", "client-third-party-key")
		r.ServeHTTP(w, req)
		require.Equal(t, http.StatusUnauthorized, w.Code, "%s resp=%s", path, w.Body.String())
		require.NotContains(t, w.Body.String(), upKey, "%s 上游 key 不得回显给客户端", path)
		require.NotContains(t, w.Body.String(), relaySecret, "%s 自定义头的值不得回显给客户端", path)
		require.Contains(t, w.Body.String(), "[REDACTED]", path)

		var row model.RequestLog
		require.NoError(t, g.Order("id desc").First(&row).Error)
		require.NotContains(t, row.Headers, "client-third-party-key", "%s 请求日志里的凭证类头须脱敏", path)
	}
	assertNoLeak("/v1/chat/completions", `{"model":"gpt-4o","messages":[{"role":"user","content":"hi"}]}`)
	assertNoLeak("/v1/embeddings", `{"model":"gpt-4o","input":"hi"}`)
}
