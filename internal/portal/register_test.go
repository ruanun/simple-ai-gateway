package portal

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/ruanun/simple-ai-gateway/internal/captcha"
	"github.com/ruanun/simple-ai-gateway/internal/dbtest"
	"github.com/ruanun/simple-ai-gateway/internal/domain/setting"
	"github.com/ruanun/simple-ai-gateway/internal/model"
	"github.com/stretchr/testify/require"
)

// newRegisterEnv 开放注册 + 指定人机验证方式，返回路由与 captcha 服务（供替换 Turnstile 校验地址）。
func newRegisterEnv(t *testing.T, captchaType string) (*gin.Engine, *captcha.Service) {
	gin.SetMode(gin.TestMode)
	g := dbtest.OpenInMemory(t)
	require.NoError(t, g.AutoMigrate(&model.PortalUser{}, &model.App{}, &model.AppMember{}, &model.SystemSetting{}))
	settings := setting.NewService(setting.NewRepo(g))
	require.NoError(t, settings.Update(&model.SystemSetting{
		PortalRegisterEnabled: true, CaptchaType: captchaType,
		TurnstileSiteKey: "site", TurnstileSecretKey: "sec",
	}))
	cs := captcha.New(nil)
	h := NewHandler(g, nil, nil, settings, "test-secret", 1, nil, cs)
	r := gin.New()
	r.POST("/portal/api/register", h.Register)
	r.GET("/portal/api/captcha", h.Captcha)
	return r, cs
}

func postRegister(r *gin.Engine, body map[string]string) int {
	b, _ := json.Marshal(body)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/portal/api/register", bytes.NewReader(b)))
	return w.Code
}

func TestRegisterImageCaptcha(t *testing.T) {
	r, _ := newRegisterEnv(t, captcha.TypeImage)

	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/portal/api/captcha", nil))
	require.Equal(t, http.StatusOK, w.Code)
	var cap struct {
		CaptchaID string `json:"captcha_id"`
		Image     string `json:"image"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &cap))
	require.NotEmpty(t, cap.CaptchaID)
	require.NotEmpty(t, cap.Image)

	require.Equal(t, http.StatusBadRequest, postRegister(r, map[string]string{"username": "u1", "password": "password-1"}), "缺验证码须拒绝")
	require.Equal(t, http.StatusBadRequest, postRegister(r, map[string]string{
		"username": "u1", "password": "password-1", "captcha_id": cap.CaptchaID, "captcha": "wrong",
	}), "验证码错误须拒绝")
}

func TestRegisterTurnstile(t *testing.T) {
	r, cs := newRegisterEnv(t, captcha.TypeTurnstile)
	reply := `{"success":true}`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte(reply)) }))
	defer srv.Close()
	cs.TurnstileURL = srv.URL

	require.Equal(t, http.StatusOK, postRegister(r, map[string]string{"username": "u1", "password": "password-1", "captcha": "tok"}))

	reply = `{"success":false}`
	require.Equal(t, http.StatusBadRequest, postRegister(r, map[string]string{"username": "u2", "password": "password-1", "captcha": "tok"}))

	srv.Close() // 校验服务不可达：fail-closed
	require.Equal(t, http.StatusServiceUnavailable, postRegister(r, map[string]string{"username": "u3", "password": "password-1", "captcha": "tok"}))

	// 未启用图形验证码时，生成端点不可用
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/portal/api/captcha", nil))
	require.Equal(t, http.StatusNotFound, w.Code)
}

// 不启用人机验证时注册照常。
func TestRegisterNoCaptcha(t *testing.T) {
	r, _ := newRegisterEnv(t, captcha.TypeNone)
	require.Equal(t, http.StatusOK, postRegister(r, map[string]string{"username": "u1", "password": "password-1"}))
}
