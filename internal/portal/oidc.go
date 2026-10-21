package portal

import (
	"crypto/rand"
	"encoding/hex"
	"log/slog"
	"net/http"
	"strings"
	"sync"

	"github.com/coreos/go-oidc/v3/oidc"
	"github.com/gin-gonic/gin"
	"github.com/ruanun/simple-ai-gateway/internal/httpx"
	"github.com/ruanun/simple-ai-gateway/internal/middleware"
	"github.com/ruanun/simple-ai-gateway/internal/model"
	"golang.org/x/oauth2"
	"gorm.io/gorm"
)

const (
	// oidcStateCookie 登录发起时存 state.PKCE verifier.nonce，回调时一次性校验。
	oidcStateCookie = "saig_oidc_state"
	// oidcHandoffCookie 回调签发的门户 JWT 经此短时 HttpOnly cookie 交给前端，由前端同源 POST
	// /oidc/exchange 换取。曾放在 /portal#token= 下发：攻击者把自己的 token 拼进链接发给受害者，
	// 受害者打开即被登录成攻击者账号（登录 CSRF），之后建的令牌、发的请求都记在攻击者名下。
	// cookie 只能由本站服务端设置，外站无法替受害者植入。
	oidcHandoffCookie = "saig_oidc_handoff"
	oidcCookiePath    = "/portal/api/oidc"
)

// setOIDCCookie 写 OIDC 流程 cookie：HttpOnly、限定在 /portal/api/oidc 路径；回调地址为 https 时加 Secure。
// maxAge<0 即删除。
func setOIDCCookie(c *gin.Context, name, value string, maxAge int, secure bool, sameSite http.SameSite) {
	c.SetSameSite(sameSite)
	c.SetCookie(name, value, maxAge, oidcCookiePath, "", secure, true)
}

// oidcProviderCache 按配置缓存 discovery 结果；配置变更（issuer/client 任一不同）自动重建。
type oidcProviderCache struct {
	mu       sync.Mutex
	issuer   string
	clientID string
	secret   string
	provider *oidc.Provider
}

// Config 公开的门户配置（登录页渲染用，不含敏感字段）。
func (h *Handler) Config(c *gin.Context) {
	s, err := h.settings.Get()
	if err != nil {
		slog.Error("portal: 查询系统设置失败", "err", err)
		httpx.Internal(c, "查询失败")
		return
	}
	c.JSON(http.StatusOK, gin.H{
		"register_enabled": s.PortalRegisterEnabled,
		"oidc_enabled":     s.OIDCEnabled,
		"oidc_button_text": s.OIDCButtonText,
		// 注册人机验证：前端据此渲染图形验证码或 Turnstile 组件（site key 本就公开，secret 不下发）
		"captcha_type":       s.CaptchaType,
		"turnstile_site_key": s.TurnstileSiteKey,
	})
}

// oidcSetup 读取设置并构造 provider + oauth2 配置；未启用或配置不全返回 nil。
func (h *Handler) oidcSetup(c *gin.Context) (*oidc.Provider, *oauth2.Config, *model.SystemSetting) {
	s, err := h.settings.Get()
	if err != nil || !s.OIDCEnabled || s.OIDCIssuer == "" || s.OIDCClientID == "" {
		httpx.Forbidden(c, "OIDC 登录未启用")
		return nil, nil, nil
	}
	if s.OIDCRedirectURL == "" {
		httpx.Forbidden(c, "OIDC 回调地址未配置，请管理员在系统设置中填写")
		return nil, nil, nil
	}
	h.oidcCache.mu.Lock()
	defer h.oidcCache.mu.Unlock()
	if h.oidcCache.provider == nil || h.oidcCache.issuer != s.OIDCIssuer ||
		h.oidcCache.clientID != s.OIDCClientID || h.oidcCache.secret != s.OIDCClientSecret {
		p, err := oidc.NewProvider(c.Request.Context(), s.OIDCIssuer)
		if err != nil {
			slog.Error("portal: OIDC 提供方发现失败", "issuer", s.OIDCIssuer, "err", err)
			httpx.Err(c, http.StatusBadGateway, "bad_gateway", "OIDC 提供方发现失败")
			return nil, nil, nil
		}
		h.oidcCache.provider = p
		h.oidcCache.issuer, h.oidcCache.clientID, h.oidcCache.secret = s.OIDCIssuer, s.OIDCClientID, s.OIDCClientSecret
	}
	conf := &oauth2.Config{
		ClientID:     s.OIDCClientID,
		ClientSecret: s.OIDCClientSecret,
		Endpoint:     h.oidcCache.provider.Endpoint(),
		RedirectURL:  s.OIDCRedirectURL, // 显式配置，不从 Host / X-Forwarded-Proto 推导
		Scopes:       []string{oidc.ScopeOpenID, "profile", "email"},
	}
	return h.oidcCache.provider, conf, s
}

// secureCookie 回调地址为 https 时 cookie 加 Secure。
func secureCookie(s *model.SystemSetting) bool {
	return strings.HasPrefix(s.OIDCRedirectURL, "https://")
}

// OIDCLogin 302 到提供方授权页。state（防 CSRF）、PKCE verifier（防授权码被截获后换 token）、
// nonce（防 id_token 重放）一起存 HttpOnly cookie，回调时校验。SameSite=Lax：从提供方跳回是
// 顶层 GET 导航，Lax 下 cookie 会随回调带上。
func (h *Handler) OIDCLogin(c *gin.Context) {
	_, conf, s := h.oidcSetup(c)
	if conf == nil {
		return
	}
	state, nonce, verifier := rand.Text(), rand.Text(), oauth2.GenerateVerifier()
	setOIDCCookie(c, oidcStateCookie, state+"."+verifier+"."+nonce, 600, secureCookie(s), http.SameSiteLaxMode)
	c.Redirect(http.StatusFound, conf.AuthCodeURL(state, oauth2.S256ChallengeOption(verifier), oidc.Nonce(nonce)))
}

// OIDCExchange 前端回到登录页后同源 POST 调用：取走回调写下的 handoff cookie 中的门户 JWT 并立即删除。
func (h *Handler) OIDCExchange(c *gin.Context) {
	tok, err := c.Cookie(oidcHandoffCookie)
	setOIDCCookie(c, oidcHandoffCookie, "", -1, false, http.SameSiteStrictMode)
	if err != nil || tok == "" {
		httpx.Unauthorized(c, "第三方登录已失效，请重新登录")
		return
	}
	c.JSON(http.StatusOK, gin.H{"token": tok})
}

// OIDCCallback 授权码回调：验 state → 换 token → 验 id_token → 绑定/自动注册 → 签门户 JWT。
func (h *Handler) OIDCCallback(c *gin.Context) {
	provider, conf, s := h.oidcSetup(c)
	if conf == nil {
		return
	}
	raw, _ := c.Cookie(oidcStateCookie)
	setOIDCCookie(c, oidcStateCookie, "", -1, false, http.SameSiteLaxMode) // 一次性
	parts := strings.Split(raw, ".")
	if len(parts) != 3 || parts[0] == "" || c.Query("state") != parts[0] {
		httpx.BadRequest(c, "state 不匹配")
		return
	}
	verifier, nonce := parts[1], parts[2]

	tok, err := conf.Exchange(c.Request.Context(), c.Query("code"), oauth2.VerifierOption(verifier))
	if err != nil {
		slog.Error("portal: OIDC token 交换失败", "err", err)
		httpx.Err(c, http.StatusBadGateway, "bad_gateway", "token 交换失败")
		return
	}
	rawID, _ := tok.Extra("id_token").(string)
	if rawID == "" {
		slog.Error("portal: OIDC 提供方未返回 id_token")
		httpx.Err(c, http.StatusBadGateway, "bad_gateway", "提供方未返回 id_token")
		return
	}
	idTok, err := provider.Verifier(&oidc.Config{ClientID: conf.ClientID}).Verify(c.Request.Context(), rawID)
	if err != nil {
		slog.Error("portal: OIDC id_token 校验失败", "err", err)
		httpx.Unauthorized(c, "id_token 校验失败")
		return
	}
	if idTok.Nonce != nonce {
		slog.Warn("portal: OIDC id_token nonce 不匹配")
		httpx.Unauthorized(c, "id_token 校验失败")
		return
	}
	var claims struct {
		PreferredUsername string `json:"preferred_username"`
		Email             string `json:"email"`
	}
	_ = idTok.Claims(&claims)
	subjectKey := idTok.Issuer + "|" + idTok.Subject

	var u model.PortalUser
	err = h.g.Where("oidc_subject = ?", subjectKey).First(&u).Error
	if err == gorm.ErrRecordNotFound {
		// 未绑定：受注册开关控制自动注册（用户名取 preferred_username/email 前缀，冲突加后缀）
		if !s.PortalRegisterEnabled {
			httpx.Forbidden(c, "该第三方账号未绑定且注册未开放，请联系管理员")
			return
		}
		name := claims.PreferredUsername
		if name == "" && claims.Email != "" {
			name = strings.SplitN(claims.Email, "@", 2)[0]
		}
		if name == "" {
			name = "oidc-" + idTok.Subject
		}
		if len(name) > 56 {
			name = name[:56]
		}
		u2, cerr := createOIDCUser(h.g, name, claims.Email, subjectKey, s.PortalRegisterQuota)
		if cerr != nil {
			slog.Error("portal: OIDC 自动注册失败", "err", cerr)
			httpx.Internal(c, "自动注册失败")
			return
		}
		u = *u2
	} else if err != nil {
		slog.Error("portal: 查询 OIDC 绑定用户失败", "err", err)
		httpx.Internal(c, "查询失败")
		return
	}
	if u.Status != 1 {
		httpx.Forbidden(c, "账号已禁用")
		return
	}
	jwtTok, err := middleware.IssueJWT(h.jwtSecret, u.ID, u.Username, "user", u.TokenVersion, h.expireHours)
	if err != nil {
		slog.Error("portal: 签发 token 失败", "err", err)
		httpx.Internal(c, "登录失败，请重试")
		return
	}
	// 令牌经 60 秒有效的 handoff cookie 交给前端（见 oidcHandoffCookie），URL 里不带任何凭证。
	// SameSite=Strict：只在同站请求里发送，前端随后的同源 POST /oidc/exchange 能带上。
	setOIDCCookie(c, oidcHandoffCookie, jwtTok, 60, secureCookie(s), http.SameSiteStrictMode)
	c.Redirect(http.StatusFound, "/portal/login?oidc=1")
}

// createOIDCUser 事务内建 OIDC 用户（无本地密码）+ 同名默认 App；用户名冲突自动加后缀重试。
func createOIDCUser(g *gorm.DB, name, email, subjectKey string, quota int64) (*model.PortalUser, error) {
	u := &model.PortalUser{Username: name, Email: email, Status: 1, Source: "oidc", OIDCSubject: subjectKey}
	err := g.Transaction(func(tx *gorm.DB) error {
		if err := tx.Create(u).Error; err != nil {
			// 用户名撞车：加随机后缀重试一次
			b := make([]byte, 3)
			_, _ = rand.Read(b)
			u.Username = name + "-" + hex.EncodeToString(b)
			if err2 := tx.Create(u).Error; err2 != nil {
				return err2
			}
		}
		app := &model.App{Name: u.Username, Quota: quota, Status: 1, OwnerUserID: u.ID}
		if err := tx.Create(app).Error; err != nil {
			app.Name = u.Username + "-app"
			if err2 := tx.Create(app).Error; err2 != nil {
				return err2
			}
		}
		// owner 即首个成员
		return tx.Create(&model.AppMember{AppID: app.ID, PortalUserID: u.ID}).Error
	})
	if err != nil {
		return nil, err
	}
	return u, nil
}
