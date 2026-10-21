// 请求日志管理接口（列表筛选/详情、审计正文关联展示）。
package admin

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/ruanun/simple-ai-gateway/internal/httpx"
	"github.com/ruanun/simple-ai-gateway/internal/model"
	"github.com/ruanun/simple-ai-gateway/internal/paginate"
)

// ---- RequestLog ----
// ListRequestLogs 分页列出请求日志，支持按渠道/应用/令牌/模型/协议筛选。
func (h *CRUDHandler) ListRequestLogs(c *gin.Context) {
	// 分页参数夹逼：防负数（负 limit 在 GORM 里等于不限制、会拉全表）与超大页。
	limit, offset := paginate.Params(c.DefaultQuery("limit", "20"), c.DefaultQuery("offset", "0"), 20, 200)
	q := h.g.Model(&model.RequestLog{}).Order("id desc")
	if v := c.Query("channel_id"); v != "" {
		q = q.Where("channel_id = ?", v)
	}
	if v := c.Query("app_id"); v != "" {
		q = q.Where("app_id = ?", v)
	}
	if v := c.Query("api_key_id"); v != "" {
		q = q.Where("api_key_id = ?", v)
	}
	if v := c.Query("model"); v != "" {
		q = q.Where("model = ?", v)
	}
	if v := c.Query("request_type"); v != "" {
		q = q.Where("request_type = ?", v)
	}
	if v := c.Query("session_id"); v != "" {
		q = q.Where("session_id = ?", v)
	}
	if v := c.Query("request_id"); v != "" {
		q = q.Where("request_id = ?", v)
	}
	if v := c.Query("start_time"); v != "" {
		q = q.Where("created_at >= ?", v)
	}
	if v := c.Query("end_time"); v != "" {
		q = q.Where("created_at <= ?", v)
	}
	var total int64
	if err := q.Count(&total).Error; err != nil {
		httpx.Internal(c, "查询失败")
		return
	}
	var rows []model.RequestLog
	if err := q.Limit(limit).Offset(offset).Find(&rows).Error; err != nil {
		httpx.Internal(c, "查询失败")
		return
	}
	c.JSON(http.StatusOK, paginate.New(rows, total, limit, offset))
}

// GetRequestLog 返回单条请求日志详情；若该请求有对应审计（同 request_id），
// 一并返回请求体/响应体正文，供详情页展示。
func (h *CRUDHandler) GetRequestLog(c *gin.Context) {
	id, err := strconv.ParseUint(c.Param("id"), 10, 64)
	if err != nil {
		httpx.BadRequest(c, "id 不合法")
		return
	}
	var lg model.RequestLog
	if err := h.g.First(&lg, id).Error; err != nil {
		httpx.NotFound(c, "请求日志不存在")
		return
	}
	out := gin.H{"log": lg, "has_audit": false, "request_body": "", "response_body": "", "user_agent": "",
		"request_truncated": false, "request_orig_size": 0, "response_truncated": false, "response_orig_size": 0,
		"request_sha256": "", "response_sha256": ""}
	// viewer(只读)不得读取审计正文明文(含 prompt/响应/可能的凭证)；仅剥离正文内容，
	// 保留 has_audit/指纹/大小等元数据，前端据此显示「有审计但无权查看正文」。ops/super 正常展示。
	showBody := canSeeSecrets(c)
	if lg.RequestID != "" {
		var ar model.AuditRecord
		if err := h.g.Where("request_id = ?", lg.RequestID).First(&ar).Error; err == nil {
			out["has_audit"] = true
			out["audit_id"] = ar.ID
			out["user_agent"] = ar.UserAgent
			out["body_visible"] = showBody
			// 取证元数据直接来自记录行（正文已外置到存储层）
			out["request_truncated"] = ar.RequestTruncated
			out["request_orig_size"] = ar.RequestOrigSize
			out["request_sha256"] = ar.RequestSHA256
			out["response_truncated"] = ar.ResponseTruncated
			out["response_orig_size"] = ar.ResponseOrigSize
			out["response_sha256"] = ar.ResponseSHA256
			// 只有有权限的角色才去存储层取正文，避免为无权请求付网络/IO 代价
			if showBody {
				out["request_body"] = h.auditBody(c.Request.Context(), ar.RequestID, "request", ar.RequestKey)
				out["response_body"] = h.auditBody(c.Request.Context(), ar.RequestID, "response", ar.ResponseKey)
			}
		}
	}
	if ua, _ := out["user_agent"].(string); ua == "" {
		out["user_agent"] = userAgentFromHeaders(lg.Headers)
	}
	c.JSON(http.StatusOK, out)
}

// userAgentFromHeaders 从脱敏请求头 JSON 中取 User-Agent（大小写不敏感），
// 供无关联审计记录时的回退展示。
func userAgentFromHeaders(headers string) string {
	if headers == "" {
		return ""
	}
	var hs map[string]string
	if json.Unmarshal([]byte(headers), &hs) != nil {
		return ""
	}
	for k, v := range hs {
		if strings.EqualFold(k, "User-Agent") {
			return v
		}
	}
	return ""
}

// auditBody 从存储层取回并解压审计正文。失败只记日志并返回空串：详情整体仍 200、元数据照常，
// 正文缺失（已被清理/存储层故障）不该让整个详情页不可用。这里掌握了响应文案之外的 key/err，值得单独记一行。
func (h *CRUDHandler) auditBody(ctx context.Context, reqID, kind, key string) string {
	if h.audit == nil {
		return ""
	}
	b, err := h.audit.FetchBody(ctx, key)
	if err != nil {
		// audit_request_id 是被查审计记录的 request_id，与管理面访问日志里代表本次 admin 请求的 request_id 不是一回事
		slog.Warn("审计正文读取失败", "audit_request_id", reqID, "kind", kind, "key", key, "err", err)
		return ""
	}
	return string(b)
}
