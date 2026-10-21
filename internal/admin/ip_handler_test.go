package admin

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/ruanun/simple-ai-gateway/internal/ipgeo"
)

// TestIPHandlerLookupPrivate 用私有 IP 验证 handler 主路径：
// 私有 IP 走 ipgeo 的私有短路分支，不会发起任何外部网络请求，稳定可靠。
func TestIPHandlerLookupPrivate(t *testing.T) {
	gin.SetMode(gin.TestMode)
	h := NewIPHandler(ipgeo.NewClient())
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Params = gin.Params{{Key: "ip", Value: "127.0.0.1"}}
	c.Request = httptest.NewRequest(http.MethodGet, "/admin/ip-info/127.0.0.1", nil)
	h.Lookup(c)
	if w.Code != http.StatusOK {
		t.Fatalf("status=%d", w.Code)
	}
	var res map[string]any
	json.Unmarshal(w.Body.Bytes(), &res)
	if res["private"] != true {
		t.Fatalf("私有 IP 应返回 private=true: %v", res)
	}
	if res["ip"] != "127.0.0.1" {
		t.Fatalf("ip 字段不符: %v", res["ip"])
	}
}
