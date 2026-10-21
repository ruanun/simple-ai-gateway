package admin

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/ruanun/simple-ai-gateway/internal/audit"
	"github.com/ruanun/simple-ai-gateway/internal/dbtest"
	"github.com/ruanun/simple-ai-gateway/internal/middleware"
	"github.com/ruanun/simple-ai-gateway/internal/model"
	"github.com/ruanun/simple-ai-gateway/internal/storage"
	"gorm.io/gorm"
)

// gz 测试用压缩（与 audit 包内实现等价，避免导出私有 helper）。
func gz(s string) []byte {
	var buf bytes.Buffer
	zw := gzip.NewWriter(&buf)
	_, _ = zw.Write([]byte(s))
	_ = zw.Close()
	return buf.Bytes()
}

// seedAuditWithBodies 预置一条审计记录，并把 gzip 正文放进内存存储（key 按记录 request_id 生成）。
func seedAuditWithBodies(t *testing.T, g *gorm.DB, mem *storage.Memory, rec model.AuditRecord, reqBody, respBody string) {
	t.Helper()
	rec.RequestKey = "audit/2026/09/03/" + rec.RequestID + "/request.gz"
	rec.ResponseKey = "audit/2026/09/03/" + rec.RequestID + "/response.gz"
	if err := mem.Put(context.Background(), rec.RequestKey, gz(reqBody)); err != nil {
		t.Fatal(err)
	}
	if err := mem.Put(context.Background(), rec.ResponseKey, gz(respBody)); err != nil {
		t.Fatal(err)
	}
	if err := g.Create(&rec).Error; err != nil {
		t.Fatal(err)
	}
}

// TestGetRequestLogWithAudit 验证详情端点在存在关联审计记录时，
// 返回请求日志本体 + has_audit=true + 请求/响应正文与取证元数据（截断/原始大小/SHA256，来自 AuditRecord + 存储层）。
func TestGetRequestLogWithAudit(t *testing.T) {
	gin.SetMode(gin.TestMode)
	g := dbtest.OpenInMemory(t)
	if err := g.AutoMigrate(&model.RequestLog{}, &model.AuditRecord{}); err != nil {
		t.Fatal(err)
	}

	mem := storage.NewMemory()
	seedAuditWithBodies(t, g, mem, model.AuditRecord{
		RequestID: "abc", RequestOrigSize: 100, RequestTruncated: true, RequestSHA256: "req-hash",
		ResponseOrigSize: 11, ResponseSHA256: "resp-hash",
	}, `{"model":"x"}`, `{"ok":true}`)
	if err := g.Create(&model.RequestLog{RequestID: "abc", Model: "x", Status: 200}).Error; err != nil {
		t.Fatal(err)
	}
	var lg model.RequestLog
	if err := g.Order("id desc").First(&lg).Error; err != nil {
		t.Fatal(err)
	}

	h := &CRUDHandler{g: g, audit: audit.NewService(g, mem)}
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	idStr := strconv.FormatUint(lg.ID, 10)
	c.Params = gin.Params{{Key: "id", Value: idStr}}
	c.Request = httptest.NewRequest(http.MethodGet, "/admin/request-logs/"+idStr, nil)
	c.Set(middleware.CtxAdminRole, model.RoleSuper) // super：可见审计正文

	h.GetRequestLog(c)

	if w.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
	var resp map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}
	if resp["has_audit"] != true {
		t.Fatalf("has_audit 应为 true: %v", resp)
	}
	if resp["request_body"] != `{"model":"x"}` {
		t.Fatalf("request_body 不符: %v", resp["request_body"])
	}
	if resp["response_body"] != `{"ok":true}` {
		t.Fatalf("response_body 不符: %v", resp["response_body"])
	}
	if resp["request_truncated"] != true || resp["request_orig_size"] != float64(100) {
		t.Fatalf("请求体截断元数据不符: %v", resp)
	}
	if resp["response_truncated"] != false || resp["response_orig_size"] != float64(11) {
		t.Fatalf("响应体截断元数据不符: %v", resp)
	}
	if resp["request_sha256"] != "req-hash" || resp["response_sha256"] != "resp-hash" {
		t.Fatalf("SHA256 取证哈希不符: %v", resp)
	}
}

// TestGetRequestLogWithoutAudit 验证无关联审计记录时，has_audit 为 false 且正文为空字符串。
func TestGetRequestLogWithoutAudit(t *testing.T) {
	gin.SetMode(gin.TestMode)
	g := dbtest.OpenInMemory(t)
	if err := g.AutoMigrate(&model.RequestLog{}, &model.AuditRecord{}); err != nil {
		t.Fatal(err)
	}
	if err := g.Create(&model.RequestLog{RequestID: "no-audit", Model: "y", Status: 200}).Error; err != nil {
		t.Fatal(err)
	}
	var lg model.RequestLog
	if err := g.Order("id desc").First(&lg).Error; err != nil {
		t.Fatal(err)
	}

	h := &CRUDHandler{g: g}
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	idStr := strconv.FormatUint(lg.ID, 10)
	c.Params = gin.Params{{Key: "id", Value: idStr}}
	c.Request = httptest.NewRequest(http.MethodGet, "/admin/request-logs/"+idStr, nil)

	h.GetRequestLog(c)

	if w.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
	var resp map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}
	if resp["has_audit"] != false {
		t.Fatalf("has_audit 应为 false: %v", resp)
	}
	if resp["request_body"] != "" || resp["response_body"] != "" {
		t.Fatalf("无审计时正文应为空: %v", resp)
	}
}

// TestGetRequestLogUserAgentFromAudit 验证存在关联审计记录时，
// 详情端点优先返回审计记录里的 UserAgent。
func TestGetRequestLogUserAgentFromAudit(t *testing.T) {
	gin.SetMode(gin.TestMode)
	g := dbtest.OpenInMemory(t)
	if err := g.AutoMigrate(&model.RequestLog{}, &model.AuditRecord{}); err != nil {
		t.Fatal(err)
	}

	mem := storage.NewMemory()
	seedAuditWithBodies(t, g, mem, model.AuditRecord{RequestID: "ua1", UserAgent: "curl/8.0"}, `{"model":"x"}`, `{"ok":true}`)
	if err := g.Create(&model.RequestLog{RequestID: "ua1", Model: "x", Status: 200}).Error; err != nil {
		t.Fatal(err)
	}
	var lg model.RequestLog
	if err := g.Order("id desc").First(&lg).Error; err != nil {
		t.Fatal(err)
	}

	h := &CRUDHandler{g: g, audit: audit.NewService(g, mem)}
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	idStr := strconv.FormatUint(lg.ID, 10)
	c.Params = gin.Params{{Key: "id", Value: idStr}}
	c.Request = httptest.NewRequest(http.MethodGet, "/admin/request-logs/"+idStr, nil)

	h.GetRequestLog(c)

	if w.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
	var resp map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}
	if resp["user_agent"] != "curl/8.0" {
		t.Fatalf("user_agent 应来自审计记录: %v", resp["user_agent"])
	}
}

// TestGetRequestLogUserAgentFromHeaders 验证无关联审计记录时，
// 详情端点从脱敏请求头 JSON 中回退解析 User-Agent。
func TestGetRequestLogUserAgentFromHeaders(t *testing.T) {
	gin.SetMode(gin.TestMode)
	g := dbtest.OpenInMemory(t)
	if err := g.AutoMigrate(&model.RequestLog{}, &model.AuditRecord{}); err != nil {
		t.Fatal(err)
	}
	if err := g.Create(&model.RequestLog{RequestID: "", Model: "y", Status: 200, Headers: `{"User-Agent":"Mozilla/5.0 test"}`}).Error; err != nil {
		t.Fatal(err)
	}
	var lg model.RequestLog
	if err := g.Order("id desc").First(&lg).Error; err != nil {
		t.Fatal(err)
	}

	h := &CRUDHandler{g: g}
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	idStr := strconv.FormatUint(lg.ID, 10)
	c.Params = gin.Params{{Key: "id", Value: idStr}}
	c.Request = httptest.NewRequest(http.MethodGet, "/admin/request-logs/"+idStr, nil)

	h.GetRequestLog(c)

	if w.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
	var resp map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}
	if resp["has_audit"] != false {
		t.Fatalf("has_audit 应为 false: %v", resp)
	}
	if resp["user_agent"] != "Mozilla/5.0 test" {
		t.Fatalf("user_agent 应从请求头回退解析: %v", resp["user_agent"])
	}
}

// TestGetRequestLogInvalidID 验证非数字 id 返回 400。
func TestGetRequestLogInvalidID(t *testing.T) {
	gin.SetMode(gin.TestMode)
	g := dbtest.OpenInMemory(t)
	if err := g.AutoMigrate(&model.RequestLog{}, &model.AuditRecord{}); err != nil {
		t.Fatal(err)
	}

	h := &CRUDHandler{g: g}
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Params = gin.Params{{Key: "id", Value: "abc"}}
	c.Request = httptest.NewRequest(http.MethodGet, "/admin/request-logs/abc", nil)

	h.GetRequestLog(c)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
}

// TestGetRequestLogNotFound 验证不存在的 id 返回 404。
func TestGetRequestLogNotFound(t *testing.T) {
	gin.SetMode(gin.TestMode)
	g := dbtest.OpenInMemory(t)
	if err := g.AutoMigrate(&model.RequestLog{}, &model.AuditRecord{}); err != nil {
		t.Fatal(err)
	}

	h := &CRUDHandler{g: g}
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Params = gin.Params{{Key: "id", Value: "999"}}
	c.Request = httptest.NewRequest(http.MethodGet, "/admin/request-logs/999", nil)

	h.GetRequestLog(c)

	if w.Code != http.StatusNotFound {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
}

// TestCanSeeSecrets 锁定敏感数据可见性基线：super/ops 可见，viewer/未知/空角色不可见。
func TestCanSeeSecrets(t *testing.T) {
	gin.SetMode(gin.TestMode)
	cases := []struct {
		role string
		want bool
	}{
		{model.RoleSuper, true},
		{model.RoleOps, true},
		{model.RoleViewer, false},
		{"unknown", false},
		{"", false},
	}
	for _, tc := range cases {
		c, _ := gin.CreateTestContext(httptest.NewRecorder())
		if tc.role != "" {
			c.Set(middleware.CtxAdminRole, tc.role)
		}
		if got := canSeeSecrets(c); got != tc.want {
			t.Errorf("canSeeSecrets(role=%q)=%v want %v", tc.role, got, tc.want)
		}
	}
}

// TestGetRequestLogViewerSkipsStorage viewer 无权看正文：元数据照常、正文为空、且**不触发存储层读取**。
func TestGetRequestLogViewerSkipsStorage(t *testing.T) {
	gin.SetMode(gin.TestMode)
	g := dbtest.OpenInMemory(t)
	if err := g.AutoMigrate(&model.RequestLog{}, &model.AuditRecord{}); err != nil {
		t.Fatal(err)
	}
	mem := storage.NewMemory()
	seedAuditWithBodies(t, g, mem, model.AuditRecord{RequestID: "v1", RequestSHA256: "h1", ResponseSHA256: "h2"}, `{"secret":"x"}`, `{"ok":true}`)
	if err := g.Create(&model.RequestLog{RequestID: "v1", Model: "x", Status: 200}).Error; err != nil {
		t.Fatal(err)
	}
	var lg model.RequestLog
	if err := g.Order("id desc").First(&lg).Error; err != nil {
		t.Fatal(err)
	}

	h := &CRUDHandler{g: g, audit: audit.NewService(g, mem)}
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	idStr := strconv.FormatUint(lg.ID, 10)
	c.Params = gin.Params{{Key: "id", Value: idStr}}
	c.Request = httptest.NewRequest(http.MethodGet, "/admin/request-logs/"+idStr, nil)
	c.Set(middleware.CtxAdminRole, model.RoleViewer)

	h.GetRequestLog(c)

	if w.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
	var resp map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if resp["has_audit"] != true || resp["body_visible"] != false {
		t.Fatalf("viewer 应有 has_audit=true 且 body_visible=false: %v", resp)
	}
	if resp["request_body"] != "" || resp["response_body"] != "" {
		t.Fatalf("viewer 不应拿到正文: %v", resp)
	}
	if resp["request_sha256"] != "h1" || resp["response_sha256"] != "h2" {
		t.Fatalf("元数据应照常返回: %v", resp)
	}
	if mem.Gets() != 0 {
		t.Fatalf("无权限角色不应触发存储层读取，实际 Gets=%d", mem.Gets())
	}
}

// TestGetRequestLogBodyMissing 对象已不在存储层：详情仍 200，正文空、元数据照常。
func TestGetRequestLogBodyMissing(t *testing.T) {
	gin.SetMode(gin.TestMode)
	g := dbtest.OpenInMemory(t)
	if err := g.AutoMigrate(&model.RequestLog{}, &model.AuditRecord{}); err != nil {
		t.Fatal(err)
	}
	mem := storage.NewMemory()
	if err := g.Create(&model.AuditRecord{RequestID: "gone", RequestKey: "audit/2026/09/03/gone/request.gz", ResponseKey: "audit/2026/09/03/gone/response.gz", RequestSHA256: "hh"}).Error; err != nil {
		t.Fatal(err)
	}
	if err := g.Create(&model.RequestLog{RequestID: "gone", Model: "x", Status: 200}).Error; err != nil {
		t.Fatal(err)
	}
	var lg model.RequestLog
	if err := g.Order("id desc").First(&lg).Error; err != nil {
		t.Fatal(err)
	}

	h := &CRUDHandler{g: g, audit: audit.NewService(g, mem)}
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	idStr := strconv.FormatUint(lg.ID, 10)
	c.Params = gin.Params{{Key: "id", Value: idStr}}
	c.Request = httptest.NewRequest(http.MethodGet, "/admin/request-logs/"+idStr, nil)
	c.Set(middleware.CtxAdminRole, model.RoleSuper)

	h.GetRequestLog(c)

	if w.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
	var resp map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if resp["has_audit"] != true || resp["request_body"] != "" || resp["request_sha256"] != "hh" {
		t.Fatalf("对象缺失时应 has_audit=true、正文空、元数据照常: %v", resp)
	}
}
