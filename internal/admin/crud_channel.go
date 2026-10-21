// 渠道与渠道模型管理接口：HTTP 绑定 + 状态码/文案映射，业务逻辑下沉 internal/domain/channel。
package admin

import (
	"errors"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/ruanun/simple-ai-gateway/internal/crypto"
	"github.com/ruanun/simple-ai-gateway/internal/domain/channel"
	"github.com/ruanun/simple-ai-gateway/internal/domain/gateway"
	"github.com/ruanun/simple-ai-gateway/internal/httpx"
	"github.com/ruanun/simple-ai-gateway/internal/middleware"
	"github.com/ruanun/simple-ai-gateway/internal/model"
	"github.com/ruanun/simple-ai-gateway/internal/proxydial"
)

// ---- Channel ----
func (h *CRUDHandler) ListChannels(c *gin.Context) {
	rows, _ := h.channels.List()
	// viewer(只读)不得读取渠道自定义头——其值可能是中转站上游秘密（鉴权头等）。
	// ops/super 保留以支持编辑回填；APIKeyEnc 已 json:"-" 不在此列。
	if !canSeeSecrets(c) {
		for i := range rows {
			rows[i].CustomHeaders = ""
		}
	}
	c.JSON(http.StatusOK, rows)
}

func (h *CRUDHandler) CreateChannel(c *gin.Context) {
	var body struct {
		Type               string  `json:"type"`
		Name               string  `json:"name"`
		BaseURL            string  `json:"base_url"`
		APIKey             string  `json:"api_key"`
		Timeout            int     `json:"timeout"`
		Priority           int     `json:"priority"`
		Weight             int     `json:"weight"`
		MaxRetries         int     `json:"max_retries"`
		RPMLimit           int     `json:"rpm_limit"`
		RateMultiplier     float64 `json:"rate_multiplier"`
		Remark             string  `json:"remark"`
		AllowConvert       bool    `json:"allow_convert"`
		StripThinking      bool    `json:"strip_thinking"`
		DisableStreamUsage bool    `json:"disable_stream_usage"`
		ErrorPassthrough   bool    `json:"error_passthrough"`
		AuthStyle          string  `json:"auth_style"`
		CustomHeaders      string  `json:"custom_headers"`
	}
	if err := c.ShouldBindJSON(&body); err != nil {
		httpx.BadRequest(c, "请求参数不合法")
		return
	}
	ch, err := h.channels.Create(channel.CreateInput{
		Type: body.Type, Name: body.Name, BaseURL: body.BaseURL, APIKey: body.APIKey,
		Timeout: body.Timeout, Priority: body.Priority, Weight: body.Weight,
		MaxRetries: body.MaxRetries, RPMLimit: body.RPMLimit, RateMultiplier: body.RateMultiplier,
		Remark: body.Remark, AllowConvert: body.AllowConvert, StripThinking: body.StripThinking,
		DisableStreamUsage: body.DisableStreamUsage,
		ErrorPassthrough:   body.ErrorPassthrough,
		AuthStyle:          body.AuthStyle, CustomHeaders: body.CustomHeaders,
	})
	if err != nil {
		switch {
		case errors.Is(err, channel.ErrInvalidCustomHeaders):
			httpx.BadRequest(c, "custom_headers 需为 JSON 对象，如 {\"HTTP-Referer\":\"...\"}")
		case errors.Is(err, channel.ErrEncryptKey):
			httpx.Internal(c, "渠道密钥加密失败")
		case errors.Is(err, channel.ErrInvalidProtocol):
			httpx.BadRequest(c, "type 仅支持 openai/anthropic")
		default:
			httpx.Internal(c, "创建渠道失败")
		}
		return
	}
	if err := h.notifyRouterChange(); err != nil {
		httpx.Internal(c, "路由表刷新失败")
		return
	}
	c.JSON(http.StatusOK, ch)
}

// UpdateChannel 更新渠道可调字段(指针=未传则不改)。改后热加载路由表。
func (h *CRUDHandler) UpdateChannel(c *gin.Context) {
	id, _ := strconv.ParseUint(c.Param("id"), 10, 64)
	var body struct {
		Type               *string  `json:"type"`
		Name               *string  `json:"name"`
		BaseURL            *string  `json:"base_url"`
		Priority           *int     `json:"priority"`
		Weight             *int     `json:"weight"`
		Timeout            *int     `json:"timeout"`
		MaxRetries         *int     `json:"max_retries"`
		RPMLimit           *int     `json:"rpm_limit"`
		RateMultiplier     *float64 `json:"rate_multiplier"`
		Status             *int8    `json:"status"`
		Remark             *string  `json:"remark"`
		AllowConvert       *bool    `json:"allow_convert"`
		StripThinking      *bool    `json:"strip_thinking"`
		DisableStreamUsage *bool    `json:"disable_stream_usage"`
		ErrorPassthrough   *bool    `json:"error_passthrough"`
		AuthStyle          *string  `json:"auth_style"`
		CustomHeaders      *string  `json:"custom_headers"`
	}
	if err := c.ShouldBindJSON(&body); err != nil {
		httpx.BadRequest(c, "请求参数不合法")
		return
	}
	// 改已有渠道的 base_url 仅限 super：该渠道下账号的上游密钥会随之发往新地址，ops 把它改成自己的
	// 服务器再点「拉取模型」或发一次请求，就能拿到本不可见的上游明文 key。表单整体回传未改动的
	// base_url 时视为未修改，不拦。
	if body.BaseURL != nil && c.GetString(middleware.CtxAdminRole) != model.RoleSuper {
		cur, err := h.channels.Get(id)
		if err != nil || cur == nil {
			httpx.NotFound(c, "渠道不存在")
			return
		}
		if strings.TrimSpace(*body.BaseURL) != strings.TrimSpace(cur.BaseURL) {
			httpx.Forbidden(c, "仅超级管理员可修改已有渠道的 Base URL（修改后该渠道账号的上游密钥会发往新地址）")
			return
		}
	}
	changed, err := h.channels.Update(id, channel.UpdateInput{
		Type: body.Type, Name: body.Name, BaseURL: body.BaseURL,
		Priority: body.Priority, Weight: body.Weight, Timeout: body.Timeout,
		MaxRetries: body.MaxRetries, RPMLimit: body.RPMLimit, RateMultiplier: body.RateMultiplier,
		Status: body.Status, Remark: body.Remark, AllowConvert: body.AllowConvert, StripThinking: body.StripThinking,
		DisableStreamUsage: body.DisableStreamUsage,
		ErrorPassthrough:   body.ErrorPassthrough,
		AuthStyle:          body.AuthStyle, CustomHeaders: body.CustomHeaders,
	})
	if err != nil {
		switch {
		case errors.Is(err, channel.ErrInvalidProtocol):
			httpx.BadRequest(c, "type 仅支持 openai/anthropic")
		case errors.Is(err, channel.ErrInvalidCustomHeaders):
			httpx.BadRequest(c, "custom_headers 需为 JSON 对象")
		default:
			httpx.Internal(c, "更新失败")
		}
		return
	}
	if !changed {
		c.JSON(http.StatusOK, gin.H{"ok": true})
		return
	}
	if err := h.notifyRouterChange(); err != nil {
		httpx.Internal(c, "路由表刷新失败")
		return
	}
	c.JSON(http.StatusOK, gin.H{"ok": true})
}

func (h *CRUDHandler) DeleteChannel(c *gin.Context) {
	id, _ := strconv.ParseUint(c.Param("id"), 10, 64)
	if err := h.channels.Delete(id); err != nil {
		httpx.Internal(c, "删除失败")
		return
	}
	if err := h.notifyRouterChange(); err != nil {
		httpx.Internal(c, "路由表刷新失败")
		return
	}
	c.JSON(http.StatusOK, gin.H{"ok": true})
}

// ---- ChannelModel ----
func (h *CRUDHandler) ListChannelModels(c *gin.Context) {
	rows, _ := h.channels.ListModels(c.Query("channel_id"))
	c.JSON(http.StatusOK, rows)
}
func (h *CRUDHandler) CreateChannelModel(c *gin.Context) {
	var m model.ChannelModel
	if err := c.ShouldBindJSON(&m); err != nil {
		httpx.BadRequest(c, "请求参数不合法")
		return
	}
	if err := h.channels.CreateModel(&m); err != nil {
		httpx.Internal(c, "创建渠道模型失败")
		return
	}
	if err := h.notifyRouterChange(); err != nil {
		httpx.Internal(c, "路由表刷新失败")
		return
	}
	c.JSON(http.StatusOK, m)
}
func (h *CRUDHandler) DeleteChannelModel(c *gin.Context) {
	id, _ := strconv.ParseUint(c.Param("id"), 10, 64)
	if err := h.channels.DeleteModel(id); err != nil {
		httpx.Internal(c, "删除失败")
		return
	}
	if err := h.notifyRouterChange(); err != nil {
		httpx.Internal(c, "路由表刷新失败")
		return
	}
	c.JSON(http.StatusOK, gin.H{"ok": true})
}

// SetChannelModelStatus 启用(1)/停用(0)渠道模型；notifyRouterChange 热刷后路由表立即不含停用模型。
func (h *CRUDHandler) SetChannelModelStatus(c *gin.Context) {
	id, _ := strconv.ParseUint(c.Param("id"), 10, 64)
	var body struct {
		Status int8 `json:"status"`
	}
	if err := c.ShouldBindJSON(&body); err != nil {
		httpx.BadRequest(c, "请求参数不合法")
		return
	}
	if err := h.channels.SetModelStatus(id, body.Status == 1); err != nil {
		httpx.Internal(c, "更新失败")
		return
	}
	if err := h.notifyRouterChange(); err != nil {
		httpx.Internal(c, "路由表刷新失败")
		return
	}
	c.JSON(http.StatusOK, gin.H{"ok": true})
}

// FetchUpstreamModels 从上游 /models 拉取模型列表并返回，不自动入库。
// 由前端勾选后逐条调 /admin/channel-models 创建。
func (h *CRUDHandler) FetchUpstreamModels(c *gin.Context) {
	channelID, _ := strconv.ParseUint(c.Param("id"), 10, 64)
	// 取渠道
	ch, err := h.channels.Get(channelID)
	if err != nil {
		httpx.NotFound(c, "渠道不存在")
		return
	}
	// 取该渠道第一个启用账号的凭证
	acc, err := h.accounts.FirstEnabledByChannel(channelID)
	if err != nil {
		httpx.BadRequest(c, "该渠道无可用账号")
		return
	}
	// base_url 解析链与主链路 buildChannelInfo 对齐：账号覆盖渠道。
	// 不对齐会出现「管理面拉到的模型来自 A 上游、实际请求打到 B 上游」。
	baseURL := ch.BaseURL
	if acc.BaseURL != "" {
		baseURL = acc.BaseURL
	}
	if baseURL == "" {
		httpx.BadRequest(c, "渠道未配置 base_url")
		return
	}
	key, err := crypto.Decrypt(h.aesKey, acc.APIKeyEnc)
	if err != nil {
		httpx.Internal(c, "密钥解密失败")
		return
	}
	// 请求上游模型列表：出站头走统一治理（FetchModels 内部调 BuildOutboundHeaders，
	// 按渠道 auth_style 分流鉴权——anthropic 只认 x-api-key 的中转不再 401），
	// 并带上渠道自定义头，与主链路 /chat 出站行为一致。ClientHeader 无（admin 侧发起，非转发）。
	// 端点由 base_url 推导出多个候选按序尝试（各家上游拼法不一），详见 gateway.BuildModelsURLCandidates。
	customHdr := channel.ParseCustomHeaders(channelID, ch.CustomHeaders)
	// 每候选 15s 固定超时：admin 手动拉模型属低频交互操作，候选最多 4 条（最坏 60s），
	// 与 config.Probe.Timeout（探测器节奏参数）语义不同，
	// 且 CRUDHandler 不持有 config——为此穿装配层注入不值当，固定值即可。
	models, err := gateway.FetchModels(proxydial.DirectClient(15*time.Second), baseURL, key, ch.Type, ch.AuthStyle, customHdr)
	if err != nil {
		// httpx.Err 已统一记录状态码、脱敏正文与试过的候选（见 httpx.logErr），此处只补它拿不到的：
		// 渠道归属、实际出站 base_url，以及网络层原始错误（DNS/TLS/代理细节，不进响应）。
		slog.Warn("拉取上游模型失败", "channel_id", channelID, "base_url", baseURL,
			"cause", errors.Unwrap(err))
		httpx.Err(c, http.StatusBadGateway, "bad_gateway", err.Error())
		return
	}
	// 模型名列表返回，由前端选择后逐条 /admin/channel-models 创建。
	// 空列表是合法结果（账号可能一个模型都没开通），交前端提示，不当错误。
	c.JSON(http.StatusOK, gin.H{"models": models})
}
