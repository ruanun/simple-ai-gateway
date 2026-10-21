package server

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"math/big"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/ruanun/simple-ai-gateway/internal/model"
	"github.com/stretchr/testify/require"
)

const testOIDCRedirect = "https://gw.example.com/portal/api/oidc/callback"

// fakeIssuer 极简 OIDC 提供方：discovery + JWKS + token 端点，RS256 签发 id_token。
// 行为贴近真实提供方：token 端点校验 PKCE（code_verifier 须与授权请求的 code_challenge 对应），
// id_token 回带授权请求里的 nonce。
type fakeIssuer struct {
	srv  *httptest.Server
	key  *rsa.PrivateKey
	sub  string // 下一次 /token 返回的 id_token subject
	name string // preferred_username

	mu            sync.Mutex
	challenge     string // 最近一次授权请求的 code_challenge
	nonce         string // 最近一次授权请求的 nonce
	nonceOverride string // 非空时 id_token 回带此 nonce（模拟重放/篡改）
}

func newFakeIssuer(t *testing.T) *fakeIssuer {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	require.NoError(t, err)
	f := &fakeIssuer{key: key, sub: "sub-1", name: "oidcuser"}
	mux := http.NewServeMux()
	mux.HandleFunc("/.well-known/openid-configuration", func(w http.ResponseWriter, r *http.Request) {
		base := f.srv.URL
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"issuer":%q,"authorization_endpoint":%q,"token_endpoint":%q,"jwks_uri":%q,"id_token_signing_alg_values_supported":["RS256"]}`,
			base, base+"/auth", base+"/token", base+"/jwks")
	})
	mux.HandleFunc("/jwks", func(w http.ResponseWriter, r *http.Request) {
		pub := &f.key.PublicKey
		jwk := map[string]any{
			"keys": []map[string]string{{
				"kty": "RSA", "alg": "RS256", "use": "sig", "kid": "k1",
				"n": base64.RawURLEncoding.EncodeToString(pub.N.Bytes()),
				"e": base64.RawURLEncoding.EncodeToString(big.NewInt(int64(pub.E)).Bytes()),
			}},
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(jwk)
	})
	mux.HandleFunc("/token", func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		f.mu.Lock()
		challenge, nonce := f.challenge, f.nonce
		if f.nonceOverride != "" {
			nonce = f.nonceOverride
		}
		f.mu.Unlock()
		sum := sha256.Sum256([]byte(r.PostForm.Get("code_verifier")))
		if challenge == "" || base64.RawURLEncoding.EncodeToString(sum[:]) != challenge {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"error":"invalid_grant","error_description":"PKCE verification failed"}`))
			return
		}
		claims := jwt.MapClaims{
			"iss": f.srv.URL, "sub": f.sub, "aud": "test-client", "nonce": nonce,
			"exp": time.Now().Add(time.Hour).Unix(), "iat": time.Now().Unix(),
			"preferred_username": f.name, "email": f.name + "@example.com",
		}
		tok := jwt.NewWithClaims(jwt.SigningMethodRS256, claims)
		tok.Header["kid"] = "k1"
		signed, err := tok.SignedString(f.key)
		require.NoError(t, err)
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"access_token":"at","token_type":"Bearer","id_token":%q}`, signed)
	})
	f.srv = httptest.NewServer(mux)
	t.Cleanup(f.srv.Close)
	return f
}

// oidcSettings 开启 OIDC 的设置请求体。
func oidcSettings(issuer, redirect string, register bool) string {
	return fmt.Sprintf(`{"audit_retention_days":30,"request_log_retention_days":90,"portal_register_enabled":%t,"portal_register_quota":500000,`+
		`"oidc_enabled":true,"oidc_issuer":%q,"oidc_client_id":"test-client","oidc_client_secret":"sec","oidc_button_text":"用 SSO 登录","oidc_redirect_url":%q}`,
		register, issuer, redirect)
}

func findCookie(res *http.Response, name string) *http.Cookie {
	for _, c := range res.Cookies() {
		if c.Name == name {
			return c
		}
	}
	return nil
}

// oidcStart 发起 /portal/api/oidc/login，记录授权请求里的 PKCE challenge 与 nonce 给假提供方，
// 返回 (state, state cookie)。
func oidcStart(t *testing.T, r http.Handler, f *fakeIssuer) (string, *http.Cookie) {
	t.Helper()
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/portal/api/oidc/login", nil))
	require.Equal(t, http.StatusFound, w.Code, "body=%s", w.Body.String())
	loc, err := url.Parse(w.Header().Get("Location"))
	require.NoError(t, err)
	q := loc.Query()
	require.Equal(t, testOIDCRedirect, q.Get("redirect_uri"), "回调地址须取自配置")
	require.Equal(t, "S256", q.Get("code_challenge_method"), "须启用 PKCE")
	require.NotEmpty(t, q.Get("code_challenge"))
	require.NotEmpty(t, q.Get("nonce"), "须带 nonce")
	f.mu.Lock()
	f.challenge, f.nonce = q.Get("code_challenge"), q.Get("nonce")
	f.mu.Unlock()
	state := q.Get("state")
	require.NotEmpty(t, state)
	c := findCookie(w.Result(), "saig_oidc_state")
	require.NotNil(t, c)
	return state, c
}

// oidcCallback 携带 state cookie 回调，返回 recorder。
func oidcCallback(t *testing.T, r http.Handler, state string, cookie *http.Cookie) *httptest.ResponseRecorder {
	t.Helper()
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/portal/api/oidc/callback?code=any&state="+url.QueryEscape(state), nil)
	req.AddCookie(cookie)
	r.ServeHTTP(w, req)
	return w
}

// oidcExchange 携带 handoff cookie 调 exchange，返回 recorder。
func oidcExchange(r http.Handler, handoff *http.Cookie) *httptest.ResponseRecorder {
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/portal/api/oidc/exchange", nil)
	if handoff != nil {
		req.AddCookie(handoff)
	}
	r.ServeHTTP(w, req)
	return w
}

// oidcLogin 走完整流程（login → callback → exchange），返回门户 JWT。
func oidcLogin(t *testing.T, r http.Handler, f *fakeIssuer) string {
	t.Helper()
	state, cookie := oidcStart(t, r, f)
	w := oidcCallback(t, r, state, cookie)
	require.Equal(t, http.StatusFound, w.Code, "body=%s", w.Body.String())
	require.Equal(t, "/portal/login?oidc=1", w.Header().Get("Location"), "回调跳转地址不得携带任何凭证")
	handoff := findCookie(w.Result(), "saig_oidc_handoff")
	require.NotNil(t, handoff)
	ex := oidcExchange(r, handoff)
	require.Equal(t, http.StatusOK, ex.Code, "body=%s", ex.Body.String())
	var out struct {
		Token string `json:"token"`
	}
	require.NoError(t, json.Unmarshal(ex.Body.Bytes(), &out))
	require.NotEmpty(t, out.Token)
	return out.Token
}

// TestOIDCLoginE2E 假 issuer 全流程：自动注册 → 二次登录复用绑定 → 注册关闭拒绝新绑定 → state 防护。
func TestOIDCLoginE2E(t *testing.T) {
	r, g, admin := setupPortalTest(t)
	iss := newFakeIssuer(t)

	// 开启 OIDC 但未填回调地址 / 回调路径不对 → 保存即拒
	doJSON(t, r, http.MethodPost, "/admin/settings", admin,
		strings.Replace(oidcSettings(iss.srv.URL, testOIDCRedirect, true), `"oidc_redirect_url":"`+testOIDCRedirect+`"`, `"oidc_redirect_url":""`, 1),
		http.StatusBadRequest)
	doJSON(t, r, http.MethodPost, "/admin/settings", admin,
		oidcSettings(iss.srv.URL, "https://gw.example.com/elsewhere", true), http.StatusBadRequest)
	doJSON(t, r, http.MethodPost, "/admin/settings", admin, oidcSettings(iss.srv.URL, testOIDCRedirect, true), http.StatusOK)

	// 公开配置端点透出开关
	conf := doJSON(t, r, http.MethodGet, "/portal/api/config", "", "", http.StatusOK)
	require.Equal(t, true, conf["oidc_enabled"])
	require.Equal(t, "用 SSO 登录", conf["oidc_button_text"])

	// ① 首次登录：自动注册；token 只经 handoff cookie + exchange 交付
	tok := oidcLogin(t, r, iss)
	me := doJSON(t, r, http.MethodGet, "/portal/api/me", tok, "", http.StatusOK)
	require.Equal(t, "oidcuser", me["username"])
	require.Equal(t, "oidc", me["source"])
	require.Equal(t, float64(1), me["app_count"], "自动注册应建默认 App")
	var app model.App
	var u model.PortalUser
	require.NoError(t, g.Where("username = ?", "oidcuser").First(&u).Error)
	require.NoError(t, g.Where("owner_user_id = ?", u.ID).First(&app).Error)
	require.Equal(t, int64(500000), app.Quota)

	// OIDC 用户无本地密码：改密 403
	doJSON(t, r, http.MethodPost, "/portal/api/password", tok, `{"old_password":"x","new_password":"whatever123"}`, http.StatusForbidden)

	// ② 二次登录：同 sub 复用绑定，不重复建号
	oidcLogin(t, r, iss)
	var n int64
	g.Model(&model.PortalUser{}).Where("oidc_subject LIKE ?", "%|sub-1").Count(&n)
	require.Equal(t, int64(1), n, "同一 sub 不应重复建号")

	// ③ 注册关闭后，新 sub 未绑定 → 403；已绑定的 sub-1 仍可登录
	doJSON(t, r, http.MethodPost, "/admin/settings", admin, oidcSettings(iss.srv.URL, testOIDCRedirect, false), http.StatusOK)
	iss.sub, iss.name = "sub-2", "stranger"
	state3, cookie3 := oidcStart(t, r, iss)
	require.Equal(t, http.StatusForbidden, oidcCallback(t, r, state3, cookie3).Code, "注册关闭时新第三方账号应被拒")
	iss.sub, iss.name = "sub-1", "oidcuser"
	oidcLogin(t, r, iss)

	// ④ 关闭 OIDC → 入口 403
	doJSON(t, r, http.MethodPost, "/admin/settings", admin,
		`{"audit_retention_days":30,"request_log_retention_days":90,"oidc_enabled":false}`, http.StatusOK)
	wOff := httptest.NewRecorder()
	r.ServeHTTP(wOff, httptest.NewRequest(http.MethodGet, "/portal/api/oidc/login", nil))
	require.Equal(t, http.StatusForbidden, wOff.Code)
}

// TestOIDCSecurityE2E 回调各项防护：state、PKCE、nonce、cookie 属性、exchange 必须带 handoff cookie。
func TestOIDCSecurityE2E(t *testing.T) {
	r, _, admin := setupPortalTest(t)
	iss := newFakeIssuer(t)
	doJSON(t, r, http.MethodPost, "/admin/settings", admin, oidcSettings(iss.srv.URL, testOIDCRedirect, true), http.StatusOK)

	// cookie 属性：HttpOnly、限定路径、https 回调地址下 Secure、state 为 Lax
	_, stateCookie := oidcStart(t, r, iss)
	require.True(t, stateCookie.HttpOnly)
	require.True(t, stateCookie.Secure, "https 回调地址下 state cookie 须 Secure")
	require.Equal(t, "/portal/api/oidc", stateCookie.Path)
	require.Equal(t, http.SameSiteLaxMode, stateCookie.SameSite)

	// state 不匹配 → 400
	_, c1 := oidcStart(t, r, iss)
	require.Equal(t, http.StatusBadRequest, oidcCallback(t, r, "forged-state", c1).Code)

	// PKCE：cookie 里的 verifier 被换掉（模拟截获授权码者不持有原 verifier）→ 提供方拒绝换 token
	state2, c2 := oidcStart(t, r, iss)
	parts := strings.Split(c2.Value, ".")
	require.Len(t, parts, 3)
	c2.Value = parts[0] + ".wrong-verifier-wrong-verifier-wrong-verifier-xx." + parts[2]
	require.Equal(t, http.StatusBadGateway, oidcCallback(t, r, state2, c2).Code, "PKCE 校验失败须拒绝")

	// nonce 不匹配（id_token 重放/篡改）→ 401
	state3, c3 := oidcStart(t, r, iss)
	iss.nonceOverride = "replayed-nonce"
	require.Equal(t, http.StatusUnauthorized, oidcCallback(t, r, state3, c3).Code, "nonce 不匹配须拒绝")
	iss.nonceOverride = ""

	// 成功回调：handoff cookie 为 Strict + HttpOnly + Secure，跳转地址不含 token
	state4, c4 := oidcStart(t, r, iss)
	w := oidcCallback(t, r, state4, c4)
	require.Equal(t, http.StatusFound, w.Code)
	require.NotContains(t, w.Header().Get("Location"), "token")
	handoff := findCookie(w.Result(), "saig_oidc_handoff")
	require.NotNil(t, handoff)
	require.True(t, handoff.HttpOnly)
	require.True(t, handoff.Secure)
	require.Equal(t, http.SameSiteStrictMode, handoff.SameSite)
	require.LessOrEqual(t, handoff.MaxAge, 60)

	// exchange 不带 handoff cookie → 401（攻击者无法凭链接让受害者登录成自己的账号）；带上则取到且删除 cookie
	require.Equal(t, http.StatusUnauthorized, oidcExchange(r, nil).Code)
	ex := oidcExchange(r, handoff)
	require.Equal(t, http.StatusOK, ex.Code)
	cleared := findCookie(ex.Result(), "saig_oidc_handoff")
	require.NotNil(t, cleared)
	require.Less(t, cleared.MaxAge, 0, "取走后须删除 handoff cookie")
}

// 未配置回调地址的存量启用状态（升级前已开 OIDC）：登录入口 fail-closed，不回退到按 Host 推导。
func TestOIDCRequiresRedirectURL(t *testing.T) {
	r, g, _ := setupPortalTest(t)
	iss := newFakeIssuer(t)
	require.NoError(t, g.Save(&model.SystemSetting{BaseModel: model.BaseModel{ID: 1}, AuditRetentionDays: 30, RequestLogRetentionDays: 90,
		OIDCEnabled: true, OIDCIssuer: iss.srv.URL, OIDCClientID: "test-client", OIDCClientSecret: "sec"}).Error)
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/portal/api/oidc/login", nil)
	req.Host = "evil.example"
	r.ServeHTTP(w, req)
	require.Equal(t, http.StatusForbidden, w.Code)
	require.Contains(t, w.Body.String(), "回调地址未配置")
}
