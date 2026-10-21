package admin

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/ruanun/simple-ai-gateway/internal/captcha"
	"github.com/ruanun/simple-ai-gateway/internal/dbtest"
	"github.com/ruanun/simple-ai-gateway/internal/domain/setting"
	"github.com/ruanun/simple-ai-gateway/internal/model"
)

func TestSettingHandlerGetUpdate(t *testing.T) {
	gin.SetMode(gin.TestMode)
	g := dbtest.OpenInMemory(t)
	if err := g.AutoMigrate(&model.SystemSetting{}); err != nil {
		t.Fatal(err)
	}
	svc := setting.NewService(setting.NewRepo(g))
	h := NewSettingHandler(svc, nil, nil) // cleanup 端点不在本用例覆盖

	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodGet, "/admin/settings", nil)
	h.Get(c)
	if w.Code != http.StatusOK {
		t.Fatalf("GET status=%d", w.Code)
	}

	w2 := httptest.NewRecorder()
	c2, _ := gin.CreateTestContext(w2)
	c2.Request = httptest.NewRequest(http.MethodPost, "/admin/settings",
		strings.NewReader(`{"audit_cleanup_enabled":true,"audit_retention_days":15,"request_log_cleanup_enabled":false,"request_log_retention_days":90}`))
	c2.Request.Header.Set("Content-Type", "application/json")
	h.Update(c2)
	if w2.Code != http.StatusOK {
		t.Fatalf("POST status=%d", w2.Code)
	}
	var resp map[string]any
	json.Unmarshal(w2.Body.Bytes(), &resp)
	if resp["audit_retention_days"] != float64(15) || resp["audit_cleanup_enabled"] != true {
		t.Fatalf("更新结果不符: %v", resp)
	}
}

// 人机验证设置：非法取值、选 Turnstile 却缺密钥都须 400；合法组合可保存并回读。
func TestSettingHandlerCaptchaValidation(t *testing.T) {
	gin.SetMode(gin.TestMode)
	g := dbtest.OpenInMemory(t)
	if err := g.AutoMigrate(&model.SystemSetting{}); err != nil {
		t.Fatal(err)
	}
	h := NewSettingHandler(setting.NewService(setting.NewRepo(g)), nil, nil)
	post := func(body string) (int, map[string]any) {
		w := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(w)
		c.Request = httptest.NewRequest(http.MethodPost, "/admin/settings", strings.NewReader(body))
		c.Request.Header.Set("Content-Type", "application/json")
		h.Update(c)
		var resp map[string]any
		_ = json.Unmarshal(w.Body.Bytes(), &resp)
		return w.Code, resp
	}
	if code, _ := post(`{"captcha_type":"sms"}`); code != http.StatusBadRequest {
		t.Fatalf("非法验证方式应 400, got=%d", code)
	}
	if code, _ := post(`{"captcha_type":"turnstile","turnstile_site_key":"site"}`); code != http.StatusBadRequest {
		t.Fatalf("Turnstile 缺 secret 应 400, got=%d", code)
	}
	code, resp := post(`{"captcha_type":"turnstile","turnstile_site_key":" site ","turnstile_secret_key":"sec"}`)
	if code != http.StatusOK || resp["captcha_type"] != "turnstile" || resp["turnstile_site_key"] != "site" {
		t.Fatalf("合法 Turnstile 设置应保存并去空白: code=%d resp=%v", code, resp)
	}
	code, resp = post(`{"captcha_type":""}`)
	if code != http.StatusOK || resp["captcha_type"] != "" {
		t.Fatalf("关闭人机验证应能写回空值: code=%d resp=%v", code, resp)
	}
}

// TestTurnstileConfigTest 桩模拟 siteverify：secret=good 有效，token=tok-ok 通过，其余 token 无效。
func TestTurnstileConfigTest(t *testing.T) {
	gin.SetMode(gin.TestMode)
	g := dbtest.OpenInMemory(t)
	if err := g.AutoMigrate(&model.SystemSetting{}); err != nil {
		t.Fatal(err)
	}
	svc := setting.NewService(setting.NewRepo(g))
	stub := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		switch {
		case r.PostForm.Get("secret") != "good":
			_, _ = w.Write([]byte(`{"success":false,"error-codes":["invalid-input-secret"]}`))
		case r.PostForm.Get("response") == "tok-ok":
			_, _ = w.Write([]byte(`{"success":true,"hostname":"gw.example.com"}`))
		default:
			_, _ = w.Write([]byte(`{"success":false,"error-codes":["invalid-input-response"]}`))
		}
	}))
	defer stub.Close()
	cs := captcha.New(nil)
	cs.TurnstileURL = stub.URL
	h := NewSettingHandler(svc, nil, cs)

	post := func(body string) (int, map[string]any) {
		w := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(w)
		c.Request = httptest.NewRequest(http.MethodPost, "/admin/settings/turnstile-test", strings.NewReader(body))
		c.Request.Header.Set("Content-Type", "application/json")
		h.TestTurnstile(c)
		var resp map[string]any
		_ = json.Unmarshal(w.Body.Bytes(), &resp)
		return w.Code, resp
	}

	code, resp := post(`{"secret":"good","token":"tok-ok"}`)
	if code != http.StatusOK || resp["ok"] != true || resp["hostname"] != "gw.example.com" {
		t.Fatalf("完整测试应通过并回显域名: %d %v", code, resp)
	}
	code, resp = post(`{"secret":"good"}`)
	if code != http.StatusOK || resp["ok"] != true || resp["secret_only"] != true {
		t.Fatalf("不带 token 时仅校验 Secret，有效应通过: %d %v", code, resp)
	}
	code, resp = post(`{"secret":"bad"}`)
	if code != http.StatusOK || resp["ok"] != false || resp["message"] != "Secret Key 无效" {
		t.Fatalf("无效 Secret 应判失败: %d %v", code, resp)
	}
	code, resp = post(`{"secret":"good","token":"tok-other-site"}`)
	if code != http.StatusOK || resp["ok"] != false || !strings.Contains(resp["message"].(string), "不属于同一站点") {
		t.Fatalf("token 与 Secret 不配对应给出中文说明: %d %v", code, resp)
	}

	// Secret 留空：无已保存值时 400；保存后回退使用已保存值
	if code, _ = post(`{"token":"tok-ok"}`); code != http.StatusBadRequest {
		t.Fatalf("无可用 Secret 应 400, got=%d", code)
	}
	if err := svc.Update(&model.SystemSetting{CaptchaType: "turnstile", TurnstileSiteKey: "site", TurnstileSecretKey: "good"}); err != nil {
		t.Fatal(err)
	}
	if code, resp = post(`{"token":"tok-ok"}`); code != http.StatusOK || resp["ok"] != true {
		t.Fatalf("Secret 留空应回退已保存值: %d %v", code, resp)
	}

	stub.Close()
	if code, _ = post(`{"secret":"good","token":"tok-ok"}`); code != http.StatusBadGateway {
		t.Fatalf("校验服务不可达应 502, got=%d", code)
	}
}
