package portal

import (
	"log/slog"
	"net/http"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/ruanun/simple-ai-gateway/internal/captcha"
	"github.com/ruanun/simple-ai-gateway/internal/domain/apikey"
	"github.com/ruanun/simple-ai-gateway/internal/domain/setting"
	"github.com/ruanun/simple-ai-gateway/internal/domain/usage"
	"github.com/ruanun/simple-ai-gateway/internal/httpx"
	"github.com/ruanun/simple-ai-gateway/internal/middleware"
	"github.com/ruanun/simple-ai-gateway/internal/model"
	"github.com/ruanun/simple-ai-gateway/internal/paginate"
	"github.com/ruanun/simple-ai-gateway/internal/pwpolicy"
	"golang.org/x/crypto/bcrypt"
	"gorm.io/gorm"
)

// maxKeysPerApp 门户自建令牌每 App 上限（防滥建，管理端不受此限）。
const maxKeysPerApp = 20

// Handler 使用方门户（P9-B）：登录/注册 + 名下 App/令牌/用量/日志自助，全部限定 owner 范围。
type Handler struct {
	g           *gorm.DB
	keys        *apikey.Service
	usages      *usage.Service
	settings    *setting.Service
	jwtSecret   string
	expireHours int
	oidcCache   oidcProviderCache      // OIDC provider 缓存（P9-C），配置变更自动重建
	guard       *middleware.LoginGuard // 按用户名的登录失败锁定，nil=关闭
	captcha     *captcha.Service       // 注册人机验证，用哪种由系统设置决定
}

func NewHandler(g *gorm.DB, keys *apikey.Service, usages *usage.Service, settings *setting.Service, jwtSecret string, expireHours int, guard *middleware.LoginGuard, cap *captcha.Service) *Handler {
	return &Handler{g: g, keys: keys, usages: usages, settings: settings, jwtSecret: jwtSecret, expireHours: expireHours, guard: guard, captcha: cap}
}

// LookupUser 供 PortalAuth 中间件回查：账号存在且启用时返回 ID 与 token 版本(#25)。
func (h *Handler) LookupUser(username string) (uint64, int, bool) {
	var u model.PortalUser
	if err := h.g.Select("id, token_version").Where("username = ? AND status = 1", username).First(&u).Error; err != nil {
		return 0, 0, false
	}
	return u.ID, u.TokenVersion, true
}

func uid(c *gin.Context) uint64 {
	v, _ := c.Get(middleware.CtxPortalUserID)
	id, _ := v.(uint64)
	return id
}

// ---- 公开端点 ----

func (h *Handler) Login(c *gin.Context) {
	var body struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}
	if err := c.ShouldBindJSON(&body); err != nil {
		httpx.BadRequest(c, "请求参数不合法")
		return
	}
	succeed, ok := h.guard.Acquire("portal", body.Username)
	if !ok {
		httpx.Err(c, http.StatusTooManyRequests, "too_many_requests", "该账号登录失败次数过多，请稍后再试")
		return
	}
	var u model.PortalUser
	if err := h.g.Where("username = ?", body.Username).First(&u).Error; err != nil {
		pwpolicy.DummyCompare(body.Password) // 与密码错误分支耗时对齐，防按时间差枚举用户名
		httpx.Unauthorized(c, "用户名或密码错误")
		return
	}
	if u.PasswordHash == "" {
		pwpolicy.DummyCompare(body.Password) // 仅 OIDC 账号无本地密码，同样对齐耗时
		httpx.Unauthorized(c, "用户名或密码错误")
		return
	}
	if bcrypt.CompareHashAndPassword([]byte(u.PasswordHash), []byte(body.Password)) != nil {
		httpx.Unauthorized(c, "用户名或密码错误")
		return
	}
	succeed() // 密码正确即退还名额（含账号已禁用的情形：那不是猜错密码）
	if u.Status != 1 {
		httpx.Forbidden(c, "账号已禁用")
		return
	}
	tok, err := middleware.IssueJWT(h.jwtSecret, u.ID, u.Username, "user", u.TokenVersion, h.expireHours)
	if err != nil {
		slog.Error("portal: 签发 token 失败", "err", err)
		httpx.Internal(c, "登录失败，请重试")
		return
	}
	c.JSON(http.StatusOK, gin.H{"token": tok, "username": u.Username})
}

// Register 开放注册（受系统设置开关控制），注册即自动建同名默认 App。
func (h *Handler) Register(c *gin.Context) {
	s, err := h.settings.Get()
	if err != nil || !s.PortalRegisterEnabled {
		httpx.Forbidden(c, "注册未开放，请联系管理员")
		return
	}
	var body struct {
		Username  string `json:"username"`
		Password  string `json:"password"`
		CaptchaID string `json:"captcha_id"` // 图形验证码 id（captcha_type=image）
		Captcha   string `json:"captcha"`    // 图形验证码答案，或 Turnstile token
	}
	if err := c.ShouldBindJSON(&body); err != nil {
		httpx.BadRequest(c, "请求参数不合法")
		return
	}
	body.Username = strings.TrimSpace(body.Username)
	if body.Username == "" || len(body.Username) > 64 {
		httpx.BadRequest(c, "用户名不能为空且不超过 64 字符")
		return
	}
	if !pwpolicy.Valid(body.Password) {
		httpx.BadRequest(c, pwpolicy.ErrMsg)
		return
	}
	if !h.checkCaptcha(c, s, body.CaptchaID, body.Captcha) {
		return
	}
	u, err := CreateUserWithDefaultApp(h.g, body.Username, body.Password, s.PortalRegisterQuota)
	if err != nil {
		httpx.Conflict(c, "用户名已存在")
		return
	}
	tok, err := middleware.IssueJWT(h.jwtSecret, u.ID, u.Username, "user", u.TokenVersion, h.expireHours)
	if err != nil {
		slog.Error("portal: 签发 token 失败", "err", err)
		httpx.Internal(c, "登录失败，请重试")
		return
	}
	c.JSON(http.StatusOK, gin.H{"token": tok, "username": u.Username})
}

// Captcha 生成一张图形验证码（仅 captcha_type=image 时可用）。
func (h *Handler) Captcha(c *gin.Context) {
	s, err := h.settings.Get()
	if err != nil || s.CaptchaType != captcha.TypeImage {
		httpx.NotFound(c, "图形验证码未启用")
		return
	}
	id, img, err := h.captcha.NewImage()
	if err != nil {
		slog.Error("portal: 生成图形验证码失败", "err", err)
		httpx.Internal(c, "生成验证码失败，请重试")
		return
	}
	c.JSON(http.StatusOK, gin.H{"captcha_id": id, "image": img})
}

// checkCaptcha 按系统设置校验注册请求的人机验证；未通过时已写好错误响应并返回 false。
// Turnstile 校验服务不可达时 fail-closed：宁可暂时注册不了，也不放脚本批量注册。
func (h *Handler) checkCaptcha(c *gin.Context, s *model.SystemSetting, id, answer string) bool {
	switch s.CaptchaType {
	case captcha.TypeImage:
		if !h.captcha.VerifyImage(id, answer) {
			httpx.BadRequest(c, "验证码错误或已过期，请刷新后重试")
			return false
		}
	case captcha.TypeTurnstile:
		ok, err := h.captcha.VerifyTurnstile(c.Request.Context(), s.TurnstileSecretKey, answer, c.ClientIP())
		if err != nil {
			slog.Error("portal: Turnstile 校验失败", "err", err)
			httpx.Err(c, http.StatusServiceUnavailable, "captcha_unavailable", "人机验证服务暂不可用，请稍后重试")
			return false
		}
		if !ok {
			httpx.BadRequest(c, "人机验证未通过，请重试")
			return false
		}
	}
	return true
}

// CreateUserWithDefaultApp 事务内建门户用户 + 同名默认 App（App 名冲突时加 -u 后缀）。
// 供注册与管理端建号共用。
func CreateUserWithDefaultApp(g *gorm.DB, username, password string, quota int64) (*model.PortalUser, error) {
	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		return nil, err
	}
	u := &model.PortalUser{Username: username, PasswordHash: string(hash), Status: 1, Source: "local"}
	err = g.Transaction(func(tx *gorm.DB) error {
		if err := tx.Create(u).Error; err != nil {
			return err
		}
		// App 名撞存量应用时退避加后缀。必须【预查】而非 Create 失败后同事务重试：
		// PostgreSQL 里约束冲突会把当前事务置为 aborted，后续任何语句（含重试与建成员行）
		// 都报 "current transaction is aborted"，注册整体失败并误报"用户名已存在"。
		// 预查后的并发撞名窗口极小，撞上时事务失败返回 409，可接受。
		name := username
		var dup int64
		if err := tx.Model(&model.App{}).Where("name = ?", name).Count(&dup).Error; err != nil {
			return err
		}
		if dup > 0 {
			name = username + "-u" + strconv.FormatUint(u.ID, 10)
		}
		app := &model.App{Name: name, Quota: quota, Status: 1, OwnerUserID: u.ID}
		if err := tx.Create(app).Error; err != nil {
			return err
		}
		// owner 即首个成员
		return tx.Create(&model.AppMember{AppID: app.ID, PortalUserID: u.ID}).Error
	})
	if err != nil {
		return nil, err
	}
	return u, nil
}

// ---- 认证端点（PortalAuth 之后）----

func (h *Handler) Me(c *gin.Context) {
	var u model.PortalUser
	if err := h.g.First(&u, uid(c)).Error; err != nil {
		httpx.Unauthorized(c, "登录已过期，请重新登录")
		return
	}
	var appCount int64
	h.g.Model(&model.AppMember{}).Where("portal_user_id = ?", u.ID).Count(&appCount)
	c.JSON(http.StatusOK, gin.H{"username": u.Username, "source": u.Source, "app_count": appCount})
}

func (h *Handler) ChangePassword(c *gin.Context) {
	var body struct {
		OldPassword string `json:"old_password"`
		NewPassword string `json:"new_password"`
	}
	if err := c.ShouldBindJSON(&body); err != nil {
		httpx.BadRequest(c, "请求参数不合法")
		return
	}
	if !pwpolicy.Valid(body.NewPassword) {
		httpx.BadRequest(c, pwpolicy.ErrMsg)
		return
	}
	var u model.PortalUser
	if err := h.g.First(&u, uid(c)).Error; err != nil {
		httpx.Unauthorized(c, "登录已过期，请重新登录")
		return
	}
	if u.PasswordHash == "" {
		httpx.Forbidden(c, "第三方登录账号无本地密码")
		return
	}
	if bcrypt.CompareHashAndPassword([]byte(u.PasswordHash), []byte(body.OldPassword)) != nil {
		httpx.BadRequest(c, "旧密码不正确")
		return
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(body.NewPassword), bcrypt.DefaultCost)
	if err != nil {
		slog.Error("portal: 生成密码哈希失败", "err", err)
		httpx.Internal(c, "密码加密失败")
		return
	}
	// 改密同时递增 token_version，使改密前签发的 JWT 立即失效(#25)。
	if err := h.g.Model(&u).Updates(map[string]any{
		"password_hash": string(hash),
		"token_version": gorm.Expr("token_version + 1"),
	}).Error; err != nil {
		slog.Error("portal: 修改密码失败", "id", u.ID, "err", err)
		httpx.Internal(c, "更新失败")
		return
	}
	c.JSON(http.StatusOK, gin.H{"ok": true})
}

// accessibleAppIDs 当前用户作为成员可访问的全部 App ID（访问权唯一真相源，含 owner）。
func (h *Handler) accessibleAppIDs(c *gin.Context) []uint64 {
	var ids []uint64
	h.g.Model(&model.AppMember{}).Where("portal_user_id = ?", uid(c)).Pluck("app_id", &ids)
	return ids
}

func (h *Handler) ListApps(c *gin.Context) {
	rows := []model.App{}
	ids := h.accessibleAppIDs(c)
	if len(ids) > 0 {
		h.g.Where("id IN ?", ids).Order("id asc").Find(&rows)
	}
	c.JSON(http.StatusOK, rows)
}

// ownApp 校验 App 归属（当前用户为其成员），返回 App；否则按 404 处理（不泄露存在性）。
func (h *Handler) ownApp(c *gin.Context, appID uint64) (*model.App, bool) {
	var n int64
	h.g.Model(&model.AppMember{}).Where("app_id = ? AND portal_user_id = ?", appID, uid(c)).Count(&n)
	if n == 0 {
		httpx.NotFound(c, "应用不存在")
		return nil, false
	}
	var app model.App
	if err := h.g.First(&app, appID).Error; err != nil {
		httpx.NotFound(c, "应用不存在")
		return nil, false
	}
	return &app, true
}

func (h *Handler) AppUsageTrend(c *gin.Context) {
	appID, _ := strconv.ParseUint(c.Param("id"), 10, 64)
	if _, ok := h.ownApp(c, appID); !ok {
		return
	}
	days, _ := strconv.Atoi(c.DefaultQuery("days", "7"))
	if days <= 0 {
		days = 7
	}
	rows, err := h.usages.TrendByApp(appID, days)
	if err != nil {
		slog.Error("portal: 查询用量趋势失败", "app_id", appID, "err", err)
		httpx.Internal(c, "查询失败")
		return
	}
	if rows == nil {
		rows = []model.UsageDaily{}
	}
	c.JSON(http.StatusOK, rows)
}

// AppUsageAnalysis 门户版用量分析：归属校验（404 隔离）后返回 trend/models/daily_models 三块，
// 与管理端 /admin 同形；天数白名单与 nil 归一收敛在 usage.Service.AnalysisByApp。
func (h *Handler) AppUsageAnalysis(c *gin.Context) {
	appID, _ := strconv.ParseUint(c.Param("id"), 10, 64)
	if _, ok := h.ownApp(c, appID); !ok {
		return
	}
	days, _ := strconv.Atoi(c.DefaultQuery("days", "7"))
	a, err := h.usages.AnalysisByApp(appID, days)
	if err != nil {
		slog.Error("portal: 查询用量分析失败", "app_id", appID, "err", err)
		httpx.Internal(c, "查询失败")
		return
	}
	c.JSON(http.StatusOK, gin.H{"trend": a.Trend, "models": a.Models, "daily_models": a.DailyModels})
}

func (h *Handler) ListKeys(c *gin.Context) {
	appIDs := h.accessibleAppIDs(c)
	if len(appIDs) == 0 {
		c.JSON(http.StatusOK, []model.APIKey{})
		return
	}
	q := h.g.Where("app_id IN ?", appIDs).Order("id desc")
	if v := c.Query("app_id"); v != "" {
		q = q.Where("app_id = ?", v) // 与 IN 叠加,越权的 app_id 自然查空
	}
	var rows []model.APIKey
	q.Find(&rows)
	c.JSON(http.StatusOK, rows)
}

func (h *Handler) CreateKey(c *gin.Context) {
	var body struct {
		Name  string `json:"name"`
		AppID uint64 `json:"app_id"`
	}
	if err := c.ShouldBindJSON(&body); err != nil {
		httpx.BadRequest(c, "请求参数不合法")
		return
	}
	if strings.TrimSpace(body.Name) == "" {
		httpx.BadRequest(c, "名称不能为空")
		return
	}
	if _, ok := h.ownApp(c, body.AppID); !ok {
		return
	}
	var n int64
	h.g.Model(&model.APIKey{}).Where("app_id = ?", body.AppID).Count(&n)
	if n >= maxKeysPerApp {
		httpx.BadRequest(c, "该应用令牌数已达上限(20)")
		return
	}
	// 单次 Create 原子建含归属的 key，避免旧"先建后补绑 app_id"两步走留下无主 key 窗口。
	plain, rec, err := h.keys.GenerateForApp(body.Name, body.AppID, 0, nil)
	if err != nil {
		slog.Error("portal: 生成密钥失败", "app_id", body.AppID, "err", err)
		httpx.Internal(c, "生成失败")
		return
	}
	c.JSON(http.StatusOK, gin.H{"id": rec.ID, "key": plain, "name": rec.Name, "app_id": rec.AppID})
}

// ownKey 校验令牌归属当前用户名下 App。
func (h *Handler) ownKey(c *gin.Context, keyID string) (*model.APIKey, bool) {
	var k model.APIKey
	if err := h.g.First(&k, "id = ?", keyID).Error; err != nil {
		httpx.NotFound(c, "密钥不存在")
		return nil, false
	}
	var n int64
	h.g.Model(&model.AppMember{}).Where("app_id = ? AND portal_user_id = ?", k.AppID, uid(c)).Count(&n)
	if n == 0 {
		httpx.NotFound(c, "密钥不存在")
		return nil, false
	}
	return &k, true
}

// UpdateKey 改名/启停/日成本上限（治理字段白名单，不允许改 app_id 逃出归属范围）。
func (h *Handler) UpdateKey(c *gin.Context) {
	k, ok := h.ownKey(c, c.Param("id"))
	if !ok {
		return
	}
	var body struct {
		Name           *string `json:"name"`
		Status         *int8   `json:"status"`
		DailyCostLimit *int64  `json:"daily_cost_limit"`
	}
	if err := c.ShouldBindJSON(&body); err != nil {
		httpx.BadRequest(c, "请求参数不合法")
		return
	}
	updates := map[string]any{}
	if body.Name != nil && strings.TrimSpace(*body.Name) != "" {
		updates["name"] = *body.Name
	}
	if body.Status != nil {
		if *body.Status != 0 && *body.Status != 1 {
			httpx.BadRequest(c, "status 仅支持 0/1")
			return
		}
		// 管理员停用的令牌（多为泄露处置）门户不得重新启用，只能删除；停用不受限。
		if *body.Status == 1 && k.DisabledByAdmin {
			httpx.Forbidden(c, "该令牌已被管理员停用，如需恢复请联系管理员")
			return
		}
		updates["status"] = *body.Status
	}
	if body.DailyCostLimit != nil {
		updates["daily_cost_limit"] = *body.DailyCostLimit
	}
	if len(updates) == 0 {
		httpx.BadRequest(c, "未提供需要更新的字段")
		return
	}
	if err := h.g.Model(k).Updates(updates).Error; err != nil {
		slog.Error("portal: 更新密钥失败", "id", k.ID, "err", err)
		httpx.Internal(c, "更新失败")
		return
	}
	h.keys.InvalidateAuthCacheByID(k.ID)
	c.JSON(http.StatusOK, k)
}

func (h *Handler) DeleteKey(c *gin.Context) {
	k, ok := h.ownKey(c, c.Param("id"))
	if !ok {
		return
	}
	h.keys.InvalidateAuthCacheByID(k.ID)
	if err := h.g.Delete(&model.APIKey{}, k.ID).Error; err != nil {
		slog.Error("portal: 删除密钥失败", "id", k.ID, "err", err)
		httpx.Internal(c, "删除失败")
		return
	}
	c.JSON(http.StatusOK, gin.H{"ok": true})
}

// ListRequestLogs 自己名下 App 的请求日志（强制注入归属过滤，不可越权）。
func (h *Handler) ListRequestLogs(c *gin.Context) {
	appIDs := h.accessibleAppIDs(c)
	if len(appIDs) == 0 {
		c.JSON(http.StatusOK, gin.H{"total": 0, "items": []model.RequestLog{}})
		return
	}
	// 分页参数夹逼（原裸 Atoi 无默认/无上限，与 admin 侧统一走 paginate.Params）。
	limit, offset := paginate.Params(c.DefaultQuery("limit", "20"), c.DefaultQuery("offset", "0"), 20, 200)
	q := h.g.Model(&model.RequestLog{}).Where("app_id IN ?", appIDs).Order("id desc")
	if v := c.Query("app_id"); v != "" {
		q = q.Where("app_id = ?", v)
	}
	if v := c.Query("model"); v != "" {
		q = q.Where("model = ?", v)
	}
	if v := c.Query("start_time"); v != "" {
		q = q.Where("created_at >= ?", v)
	}
	if v := c.Query("end_time"); v != "" {
		q = q.Where("created_at <= ?", v)
	}
	var total int64
	q.Count(&total)
	var rows []model.RequestLog
	q.Limit(limit).Offset(offset).Find(&rows)
	c.JSON(http.StatusOK, paginate.New(rows, total, limit, offset))
}
