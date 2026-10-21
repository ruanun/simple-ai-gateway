package v1proto

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

// TestRespondDetailOpenAI openai 信封在原有 message/type 之外带上 code 与 request_id；
// 官方 SDK 忽略未知字段，故加字段是安全的。
func TestRespondDetailOpenAI(t *testing.T) {
	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	RespondDetail(c, http.StatusServiceUnavailable, "openai", "所有上游候选均不可用：2 个熔断中", "all_candidates_unavailable", "req-abc")

	require.Equal(t, http.StatusServiceUnavailable, w.Code)
	var got struct {
		Error struct {
			Message   string `json:"message"`
			Type      string `json:"type"`
			Code      string `json:"code"`
			RequestID string `json:"request_id"`
		} `json:"error"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &got))
	require.Equal(t, "所有上游候选均不可用：2 个熔断中", got.Error.Message)
	require.Equal(t, "api_error", got.Error.Type)
	require.Equal(t, "all_candidates_unavailable", got.Error.Code)
	require.Equal(t, "req-abc", got.Error.RequestID)
}

// TestRespondDetailAnthropic anthropic 信封保持 {"type":"error","error":{...}} 外层形状，
// 扩展字段挂在内层 error 对象上。
func TestRespondDetailAnthropic(t *testing.T) {
	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	RespondDetail(c, http.StatusBadGateway, "anthropic", "所有上游候选均不可用：1 个连接失败", "all_candidates_failed", "req-xyz")

	var got struct {
		Type  string `json:"type"`
		Error struct {
			Type      string `json:"type"`
			Message   string `json:"message"`
			Code      string `json:"code"`
			RequestID string `json:"request_id"`
		} `json:"error"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &got))
	require.Equal(t, "error", got.Type)
	require.Equal(t, "api_error", got.Error.Type)
	require.Equal(t, "all_candidates_failed", got.Error.Code)
	require.Equal(t, "req-xyz", got.Error.RequestID)
}

// TestRespondDetailOmitsEmptyExtras code/requestID 为空时不得输出空字段，
// 保证 Respond 的既有输出形状逐字不变（现有调用点零回归）。
func TestRespondDetailOmitsEmptyExtras(t *testing.T) {
	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	RespondDetail(c, http.StatusBadRequest, "openai", "model required", "", "")

	require.JSONEq(t, `{"error":{"message":"model required","type":"invalid_request_error"}}`, w.Body.String())
}

// TestRespondUnchanged 原 Respond 的输出形状不因新增 RespondDetail 而改变。
func TestRespondUnchanged(t *testing.T) {
	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	Respond(c, http.StatusTooManyRequests, "openai", "channel rate limited")

	require.JSONEq(t, `{"error":{"message":"channel rate limited","type":"rate_limit_error"}}`, w.Body.String())
}
