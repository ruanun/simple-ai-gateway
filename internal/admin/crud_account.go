// 密钥池（Account）管理接口（新增/删除/启停/绑定代理组）。
package admin

import (
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/ruanun/simple-ai-gateway/internal/crypto"
	"github.com/ruanun/simple-ai-gateway/internal/httpx"
	"github.com/ruanun/simple-ai-gateway/internal/model"
)

// ---- Account（密钥池）----
// ListAccounts 按 channel_id 列出该渠道下所有账号（含停用），用于后台密钥池下钻。
func (h *CRUDHandler) ListAccounts(c *gin.Context) {
	cid, _ := strconv.ParseUint(c.Query("channel_id"), 10, 64)
	if cid == 0 {
		httpx.BadRequest(c, "channel_id 不能为空")
		return
	}
	rows, err := h.accounts.ListByChannel(cid)
	if err != nil {
		httpx.Internal(c, "查询失败")
		return
	}
	c.JSON(http.StatusOK, rows)
}

// CreateAccount 新增账号：加密上游 api_key（走 ChannelKeyAESKey）后落库，默认启用。
func (h *CRUDHandler) CreateAccount(c *gin.Context) {
	var body struct {
		ChannelID               uint64  `json:"channel_id"`
		Name                    string  `json:"name"`
		APIKey                  string  `json:"api_key"`
		BaseURL                 string  `json:"base_url"`
		Weight                  int     `json:"weight"`
		Priority                int     `json:"priority"`
		ProxyGroup              string  `json:"proxy_group"` // 可选：绑定代理组（空=直连出站）
		MaxConcurrency          *int    `json:"max_concurrency"`
		RPMLimit                *int    `json:"rpm_limit"`
		ExpiresAt               *string `json:"expires_at"`
		TempUnschedulableUntil  *string `json:"temp_unschedulable_until"`
		TempUnschedulableReason *string `json:"temp_unschedulable_reason"`
		RateLimitResetAt        *string `json:"rate_limit_reset_at"`
		OverloadUntil           *string `json:"overload_until"`
	}
	if err := c.ShouldBindJSON(&body); err != nil || body.ChannelID == 0 {
		httpx.BadRequest(c, "请求参数不合法")
		return
	}
	enc, err := crypto.Encrypt(h.aesKey, body.APIKey)
	if err != nil {
		httpx.Internal(c, "加密失败")
		return
	}
	if body.Weight < 1 {
		body.Weight = 1
	}
	maxConc, rpm := validateAccountLimits(body.MaxConcurrency, body.RPMLimit)
	expires, err := parseOptionalAccountTime(body.ExpiresAt)
	if err != nil {
		httpx.BadRequest(c, "expires_at 格式不合法，需 RFC3339")
		return
	}
	tempUntil, err := parseOptionalAccountTime(body.TempUnschedulableUntil)
	if err != nil {
		httpx.BadRequest(c, "temp_unschedulable_until 格式不合法，需 RFC3339")
		return
	}
	rateReset, err := parseOptionalAccountTime(body.RateLimitResetAt)
	if err != nil {
		httpx.BadRequest(c, "rate_limit_reset_at 格式不合法，需 RFC3339")
		return
	}
	overload, err := parseOptionalAccountTime(body.OverloadUntil)
	if err != nil {
		httpx.BadRequest(c, "overload_until 格式不合法，需 RFC3339")
		return
	}
	tempReason := ""
	if body.TempUnschedulableReason != nil {
		tempReason = strings.TrimSpace(*body.TempUnschedulableReason)
	}
	acc, err := h.accounts.Create(model.Account{
		ChannelID: body.ChannelID, Name: body.Name, APIKeyEnc: enc,
		BaseURL: body.BaseURL, Weight: body.Weight, Priority: body.Priority, Status: 1,
		ProxyGroup:     body.ProxyGroup,
		MaxConcurrency: maxConc, RPMLimit: rpm, ExpiresAt: expires,
		TempUnschedulableUntil: tempUntil, TempUnschedulableReason: tempReason,
		RateLimitResetAt: rateReset, OverloadUntil: overload,
	})
	if err != nil {
		httpx.Internal(c, "创建失败")
		return
	}
	c.JSON(http.StatusOK, acc)
}

// UpdateAccount 更新账号基础信息与调度字段（map 更新，允许显式写 0/NULL）。
func (h *CRUDHandler) UpdateAccount(c *gin.Context) {
	id, _ := strconv.ParseUint(c.Param("id"), 10, 64)
	if id == 0 {
		httpx.BadRequest(c, "id 不合法")
		return
	}
	var body struct {
		Name                    *string `json:"name"`
		BaseURL                 *string `json:"base_url"`
		Weight                  *int    `json:"weight"`
		Priority                *int    `json:"priority"`
		MaxConcurrency          *int    `json:"max_concurrency"`
		RPMLimit                *int    `json:"rpm_limit"`
		ExpiresAt               *string `json:"expires_at"`
		TempUnschedulableUntil  *string `json:"temp_unschedulable_until"`
		TempUnschedulableReason *string `json:"temp_unschedulable_reason"`
		RateLimitResetAt        *string `json:"rate_limit_reset_at"`
		OverloadUntil           *string `json:"overload_until"`
	}
	if err := c.ShouldBindJSON(&body); err != nil {
		httpx.BadRequest(c, "请求参数不合法")
		return
	}
	maxConc, rpm := validateAccountLimits(body.MaxConcurrency, body.RPMLimit)
	fields := map[string]any{}
	if body.Name != nil {
		fields["name"] = *body.Name
	}
	if body.BaseURL != nil {
		fields["base_url"] = *body.BaseURL
	}
	if body.Weight != nil {
		if *body.Weight < 1 {
			*body.Weight = 1
		}
		fields["weight"] = *body.Weight
	}
	if body.Priority != nil {
		fields["priority"] = *body.Priority
	}
	if body.MaxConcurrency != nil {
		fields["max_concurrency"] = maxConc
	}
	if body.RPMLimit != nil {
		fields["rpm_limit"] = rpm
	}
	for name, raw := range map[string]*string{
		"expires_at": body.ExpiresAt, "temp_unschedulable_until": body.TempUnschedulableUntil,
		"rate_limit_reset_at": body.RateLimitResetAt, "overload_until": body.OverloadUntil,
	} {
		if raw == nil {
			continue
		}
		t, err := parseOptionalAccountTime(raw)
		if err != nil {
			httpx.BadRequest(c, name+" 格式不合法，需 RFC3339")
			return
		}
		fields[name] = t
	}
	if body.TempUnschedulableReason != nil {
		fields["temp_unschedulable_reason"] = strings.TrimSpace(*body.TempUnschedulableReason)
	}
	if err := h.accounts.UpdateFields(id, fields); err != nil {
		httpx.Internal(c, "更新失败")
		return
	}
	c.JSON(http.StatusOK, gin.H{"ok": true})
}

func validateAccountLimits(maxConc, rpm *int) (int, int) {
	mc, r := 0, 0
	if maxConc != nil {
		mc = *maxConc
	}
	if rpm != nil {
		r = *rpm
	}
	if mc < 0 {
		mc = 0
	}
	if r < 0 {
		r = 0
	}
	return mc, r
}

func parseOptionalAccountTime(raw *string) (*time.Time, error) {
	if raw == nil || strings.TrimSpace(*raw) == "" {
		return nil, nil
	}
	s := strings.TrimSpace(*raw)
	if t, err := time.Parse(time.RFC3339, s); err == nil {
		return &t, nil
	}
	if t, err := time.ParseInLocation("2006-01-02T15:04", s, time.Local); err == nil {
		return &t, nil
	}
	return nil, errors.New("时间格式不合法")
}

// DeleteAccount 删除账号。
func (h *CRUDHandler) DeleteAccount(c *gin.Context) {
	id, _ := strconv.ParseUint(c.Param("id"), 10, 64)
	if err := h.accounts.Delete(id); err != nil {
		httpx.Internal(c, "删除失败")
		return
	}
	c.JSON(http.StatusOK, gin.H{"ok": true})
}

// SetAccountStatus 启用(1)/停用(0)账号。
func (h *CRUDHandler) SetAccountStatus(c *gin.Context) {
	id, _ := strconv.ParseUint(c.Param("id"), 10, 64)
	var body struct {
		Status int8 `json:"status"`
	}
	if err := c.ShouldBindJSON(&body); err != nil {
		httpx.BadRequest(c, "请求参数不合法")
		return
	}
	if err := h.accounts.SetStatus(id, body.Status); err != nil {
		httpx.Internal(c, "更新失败")
		return
	}
	c.JSON(http.StatusOK, gin.H{"ok": true})
}

// SetAccountProxy 更新已建账号绑定的代理组（proxy_group 留空=恢复直连）。
func (h *CRUDHandler) SetAccountProxy(c *gin.Context) {
	id, _ := strconv.ParseUint(c.Param("id"), 10, 64)
	var body struct {
		ProxyGroup string `json:"proxy_group"`
	}
	if err := c.ShouldBindJSON(&body); err != nil {
		httpx.BadRequest(c, "请求参数不合法")
		return
	}
	if err := h.accounts.SetProxyGroup(id, body.ProxyGroup); err != nil {
		httpx.Internal(c, "更新失败")
		return
	}
	c.JSON(http.StatusOK, gin.H{"ok": true})
}
