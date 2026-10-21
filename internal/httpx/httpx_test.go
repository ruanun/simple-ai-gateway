// internal/httpx/httpx_test.go
// 统一错误响应形状测试。作者: ruan
package httpx

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

// errBody 反序列化辅助
type errEnvelope struct {
	Error struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
}

func doReq(t *testing.T, h gin.HandlerFunc) (*httptest.ResponseRecorder, errEnvelope) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodGet, "/", nil)
	h(c)
	var env errEnvelope
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &env))
	return w, env
}

// Err 输出 {"error":{"code","message"}} 且状态码正确
func TestErrShape(t *testing.T) {
	w, env := doReq(t, func(c *gin.Context) { Err(c, http.StatusTeapot, "teapot", "我是茶壶") })
	require.Equal(t, http.StatusTeapot, w.Code)
	require.Equal(t, "teapot", env.Error.Code)
	require.Equal(t, "我是茶壶", env.Error.Message)
}

// 快捷方法逐一验证状态码与 code
func TestShortcuts(t *testing.T) {
	cases := []struct {
		name   string
		call   gin.HandlerFunc
		status int
		code   string
	}{
		{"BadRequest", func(c *gin.Context) { BadRequest(c, "参数不合法") }, http.StatusBadRequest, "bad_request"},
		{"Unauthorized", func(c *gin.Context) { Unauthorized(c, "请重新登录") }, http.StatusUnauthorized, "unauthorized"},
		{"Forbidden", func(c *gin.Context) { Forbidden(c, "无权限") }, http.StatusForbidden, "forbidden"},
		{"NotFound", func(c *gin.Context) { NotFound(c, "资源不存在") }, http.StatusNotFound, "not_found"},
		{"Conflict", func(c *gin.Context) { Conflict(c, "已存在") }, http.StatusConflict, "conflict"},
		{"Internal", func(c *gin.Context) { Internal(c, "内部错误") }, http.StatusInternalServerError, "internal"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			w, env := doReq(t, tc.call)
			require.Equal(t, tc.status, w.Code)
			require.Equal(t, tc.code, env.Error.Code)
		})
	}
}

// Err 应中止后续 handler（中间件场景）
func TestErrAborts(t *testing.T) {
	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodGet, "/", nil)
	Unauthorized(c, "请重新登录")
	require.True(t, c.IsAborted())
}
