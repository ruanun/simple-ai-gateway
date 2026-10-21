package admin

import (
	"log/slog"
	"net/http"
	"net/url"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/ruanun/simple-ai-gateway/internal/captcha"
	"github.com/ruanun/simple-ai-gateway/internal/domain/cleanup"
	"github.com/ruanun/simple-ai-gateway/internal/domain/setting"
	"github.com/ruanun/simple-ai-gateway/internal/httpx"
	"github.com/ruanun/simple-ai-gateway/internal/model"
)

// SettingHandler 系统设置读写 + 立即清理 + 人机验证配置测试。
type SettingHandler struct {
	svc     *setting.Service
	cleaner *cleanup.Cleaner
	captcha *captcha.Service
}

func NewSettingHandler(svc *setting.Service, cleaner *cleanup.Cleaner, cap *captcha.Service) *SettingHandler {
	return &SettingHandler{svc: svc, cleaner: cleaner, captcha: cap}
}

// Get 返回当前系统设置。
func (h *SettingHandler) Get(c *gin.Context) {
	s, err := h.svc.Get()
	if err != nil {
		slog.Error("查询系统设置失败", "err", err)
		httpx.Internal(c, "查询失败")
		return
	}
	c.JSON(http.StatusOK, s)
}

// Update 保存系统设置,返回保存后的值。
func (h *SettingHandler) Update(c *gin.Context) {
	var body model.SystemSetting
	if err := c.ShouldBindJSON(&body); err != nil {
		httpx.BadRequest(c, "请求参数不合法")
		return
	}
	body.TurnstileSiteKey = strings.TrimSpace(body.TurnstileSiteKey)
	body.TurnstileSecretKey = strings.TrimSpace(body.TurnstileSecretKey)
	if !captcha.ValidType(body.CaptchaType) {
		httpx.BadRequest(c, "人机验证方式不合法")
		return
	}
	body.OIDCRedirectURL = strings.TrimSpace(body.OIDCRedirectURL)
	if body.OIDCEnabled && !validOIDCRedirectURL(body.OIDCRedirectURL) {
		httpx.BadRequest(c, "启用 OIDC 须填写回调地址，形如 https://你的域名"+oidcCallbackPath)
		return
	}
	// 选了 Turnstile 却缺密钥，注册会全部失败，保存时就拦下。
	if body.CaptchaType == captcha.TypeTurnstile && (body.TurnstileSiteKey == "" || body.TurnstileSecretKey == "") {
		httpx.BadRequest(c, "启用 Cloudflare Turnstile 须同时填写 Site Key 与 Secret Key")
		return
	}
	if err := h.svc.Update(&body); err != nil {
		slog.Error("保存系统设置失败", "err", err)
		httpx.Internal(c, "保存失败")
		return
	}
	s, err := h.svc.Get()
	if err != nil {
		slog.Error("保存后重查系统设置失败", "err", err)
		httpx.Internal(c, "查询失败")
		return
	}
	c.JSON(http.StatusOK, s)
}

// Cleanup 按当前配置立即清理一次(尊重开关),返回删除条数。
func (h *SettingHandler) Cleanup(c *gin.Context) {
	auditDeleted, logDeleted := h.cleaner.RunOnce()
	c.JSON(http.StatusOK, gin.H{"audit_deleted": auditDeleted, "request_log_deleted": logDeleted})
}

// oidcCallbackPath 门户 OIDC 回调路由（与 server 装配的 /portal/api/oidc/callback 一致）。
const oidcCallbackPath = "/portal/api/oidc/callback"

// validOIDCRedirectURL 回调地址须为 http(s) 绝对地址、带主机且路径即回调路由。
func validOIDCRedirectURL(raw string) bool {
	u, err := url.Parse(raw)
	return err == nil && (u.Scheme == "https" || u.Scheme == "http") && u.Host != "" &&
		u.Path == oidcCallbackPath && u.RawQuery == "" && u.Fragment == ""
}

// secretProbeToken 仅校验 Secret 时发给 siteverify 的占位 token：
// Secret 有效时 Cloudflare 回 invalid-input-response，无效时回 invalid-input-secret，借此区分。
const secretProbeToken = "saig-secret-probe"

// turnstileCodeMsg Cloudflare siteverify 错误码的中文说明。
var turnstileCodeMsg = map[string]string{
	"missing-input-secret":   "未填写 Secret Key",
	"invalid-input-secret":   "Secret Key 无效",
	"missing-input-response": "缺少验证 token",
	"invalid-input-response": "token 无效：Site Key 与 Secret Key 不属于同一站点，或 token 已损坏",
	"timeout-or-duplicate":   "token 已过期或已被使用，请重新完成验证",
	"bad-request":            "请求格式错误",
	"internal-error":         "Cloudflare 内部错误，请稍后重试",
}

// TestTurnstile 用表单里（可未保存）的 Secret 与前端组件拿到的 token 真实调用一次 siteverify，
// 一次覆盖 Site Key、域名白名单、Secret 及二者配对。token 为空时只校验 Secret 本身。
// Secret 为空时回退已保存的值，免得管理员为测试重新粘贴。
func (h *SettingHandler) TestTurnstile(c *gin.Context) {
	var body struct {
		Secret string `json:"secret"`
		Token  string `json:"token"`
	}
	if err := c.ShouldBindJSON(&body); err != nil {
		httpx.BadRequest(c, "请求参数不合法")
		return
	}
	secret := strings.TrimSpace(body.Secret)
	if secret == "" {
		s, err := h.svc.Get()
		if err != nil {
			slog.Error("查询系统设置失败", "err", err)
			httpx.Internal(c, "查询失败")
			return
		}
		secret = s.TurnstileSecretKey
	}
	if secret == "" {
		httpx.BadRequest(c, "请先填写 Secret Key")
		return
	}
	secretOnly := body.Token == ""
	token := body.Token
	if secretOnly {
		token = secretProbeToken
	}
	r, err := h.captcha.CheckTurnstile(c.Request.Context(), secret, token, c.ClientIP())
	if err != nil {
		slog.Error("Turnstile 配置测试：调用 siteverify 失败", "err", err)
		httpx.Err(c, http.StatusBadGateway, "bad_gateway", "无法连接 Cloudflare 校验服务，请检查服务器能否访问 challenges.cloudflare.com")
		return
	}

	resp := gin.H{"ok": false, "secret_only": secretOnly, "hostname": r.Hostname, "error_codes": r.ErrorCodes}
	switch {
	case r.SecretInvalid():
		resp["message"] = "Secret Key 无效"
	case secretOnly:
		// 占位 token 必然不通过；只要没报密钥错误，就说明 Secret 可用
		resp["ok"] = true
		resp["message"] = "Secret Key 有效（未验证 Site Key 与域名，完成验证组件可做完整测试）"
	case r.Success:
		resp["ok"] = true
		resp["message"] = "配置可用：Site Key、域名与 Secret Key 均校验通过"
	default:
		msgs := make([]string, 0, len(r.ErrorCodes))
		for _, code := range r.ErrorCodes {
			if m, ok := turnstileCodeMsg[code]; ok {
				msgs = append(msgs, m)
			} else {
				msgs = append(msgs, code)
			}
		}
		if len(msgs) == 0 {
			msgs = append(msgs, "校验未通过")
		}
		resp["message"] = strings.Join(msgs, "；")
	}
	c.JSON(http.StatusOK, resp)
}
