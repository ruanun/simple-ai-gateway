package gateway

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/ruanun/simple-ai-gateway/internal/audit"
	"github.com/ruanun/simple-ai-gateway/internal/billing"
	"github.com/ruanun/simple-ai-gateway/internal/session"
)

// ResponsesHandler 处理 POST /v1/responses（OpenAI Responses API 入站）：
// 同协议透传——直接转发到上游 /responses，不转 chat completions。
type ResponsesHandler struct{ b BillingCtx }

func NewResponsesHandler(b BillingCtx) *ResponsesHandler { return &ResponsesHandler{b: b} }

func (h *ResponsesHandler) Handle(c *gin.Context) {
	raw, _ := io.ReadAll(c.Request.Body)
	p, ok := h.b.Prepare(c, raw, "openai", false)
	if !ok {
		return
	}
	appID, modelName := p.AppID, p.Model
	// 判断流式
	stream := p.in.stream
	// 自动从请求体 previous_response_id 提取 session，Codex CLI 自动传此字段
	sessionID := session.ExtractResponsesSessionID(raw)
	deps := h.b.requestDeps(c, p, sessionID)
	// 渠道级粘连
	cands := h.b.stickyBoost(sessionID, p.Cands)
	// 透传到上游 /responses（不转 chat completions）；流式时禁用整体超时避免截断
	out := dispatchRawInbound(cands, "/responses", p.in, deps, stream)
	if h.b.ChSticky != nil && sessionID != "" {
		if out.Resp != nil && out.Resp.StatusCode < 400 {
			h.b.ChSticky.Set(sessionID, out.ChannelID)
		} else {
			h.b.ChSticky.Delete(sessionID)
		}
	}
	servedModel := out.Model
	if servedModel == "" {
		servedModel = modelName
	}
	if out.Resp == nil {
		// nil 响应仍落日志/审计，确保失败请求可追溯（公共收尾见 finishRawFailure）。
		h.b.finishRawFailure(c, out, p, servedModel, "responses", sessionID, stream, raw)
		return
	}
	defer out.Resp.Body.Close()

	var u billing.Usage
	var usageOK bool
	var respBody []byte
	var overflow *audit.Overflow

	if out.Resp.StatusCode >= 400 {
		respBody, _ = io.ReadAll(out.Resp.Body)
		// 错误状态回写前脱敏凭证片段（审计仍存原始 respBody）。
		c.Data(out.Resp.StatusCode, "application/json", scrubResp(out.Resp, respBody))
	} else if stream {
		// 流式：逐行转发 SSE + 从 response.completed 事件提取 usage
		w := c.Writer
		if ct := out.Resp.Header.Get("Content-Type"); ct != "" {
			w.Header().Set("Content-Type", ct)
		}
		w.WriteHeader(http.StatusOK)
		var lastEventType string
		respBody, overflow = relaySSE(w, out.Resp.Body, deps.CaptureBody, func(trimmed []byte) bool {
			if bytes.HasPrefix(trimmed, []byte("event:")) {
				lastEventType = string(bytes.TrimSpace(trimmed[len("event:"):]))
			}
			if bytes.HasPrefix(trimmed, []byte("data:")) {
				payload := bytes.TrimSpace(trimmed[len("data:"):])
				if lastEventType == "response.completed" {
					// response.completed 事件含完整 response 对象，从中提取 usage
					var ev struct {
						Response *struct {
							Usage json.RawMessage `json:"usage"`
						} `json:"response"`
					}
					if json.Unmarshal(payload, &ev) == nil && ev.Response != nil && len(ev.Response.Usage) > 0 {
						if pu, ok := billing.ParseResponsesUsageJSON([]byte(`{"usage":` + string(ev.Response.Usage) + `}`)); ok {
							u = pu
							usageOK = true
						}
					}
				}
			}
			return false
		})
	} else {
		// 非流式：整体读→回写→解析 usage（input_tokens/output_tokens 格式）
		respBody, _ = io.ReadAll(out.Resp.Body)
		c.Data(out.Resp.StatusCode, "application/json", respBody)
		u, usageOK = billing.ParseResponsesUsageJSON(respBody)
	}

	// 计费/日志/审计：失败响应仍落日志/审计，cost=0；仅成功时计费并累加 key 日成本。
	var cost int64
	if out.Resp.StatusCode < 400 {
		if pr, has := h.b.Prices.Resolve(out.ChannelID, servedModel); has && usageOK {
			cost = billing.ApplyMultiplier(billing.Compute(u, pr), out.RateMultiplier)
		}
		h.b.applyKeyDailyCost(c, p.Kid, cost)
		// 成功却无 usage → 零成本结算，暴露给运维（仅非流式，流式合法无 usage 常见）。
		h.b.reportMissingUsage(reqIDOf(c), out.ChannelID, servedModel, out.Resp.StatusCode, stream, usageOK)
	}
	// 传入真实状态码，避免日志始终记录 200
	h.b.finish(c, appID, out.ChannelID, out.AccountID, servedModel, "responses", sessionID, u, usageOK, cost, out.LatencyMs, 0, stream, out.Resp.StatusCode)
	h.b.auditSubmit(c, p.AuditEnabled, appID, out.ChannelID, out.AccountID, servedModel, "responses", sessionID, out.Resp.StatusCode, u, cost, out.LatencyMs, 0, stream, raw, respBody, overflow, 0)
}
