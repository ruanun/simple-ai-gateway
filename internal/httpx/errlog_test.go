// httpx 统一错误出口的日志侧测试。作者: ruan
package httpx

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/ruanun/simple-ai-gateway/internal/logger"
	"github.com/ruanun/simple-ai-gateway/internal/logtest"
	"github.com/stretchr/testify/require"
)

// errLog 在捕获全局日志的前提下跑一次 handler，返回唯一一条日志行。
// req 收完整的 *http.Request（而非 method/path）：这样注入 ctx、传 nil 请求这些场景
// 都能复用同一个 helper，不必各自手抄一遍捕获样板。
func errLog(t *testing.T, req *http.Request, h gin.HandlerFunc) map[string]any {
	t.Helper()
	gin.SetMode(gin.TestMode)
	log := logtest.Capture(t)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = req
	h(c)
	return log.Only()
}

// 5xx 是内部故障，必须 Error 级——这是「502 返回了但 docker logs 一片空白」的直接修复。
func TestErrLogs5xxAtError(t *testing.T) {
	m := errLog(t, httptest.NewRequest(http.MethodPost, "/admin/channels/3/fetch-models", nil),
		func(c *gin.Context) { Err(c, http.StatusBadGateway, "bad_gateway", "上游返回状态码 404") })

	require.Equal(t, "ERROR", m["level"])
	require.Equal(t, "POST", m["method"])
	require.Equal(t, "/admin/channels/3/fetch-models", m["path"])
	require.Equal(t, float64(502), m["status"])
	require.Equal(t, "bad_gateway", m["code"])
	require.Equal(t, "上游返回状态码 404", m["message"], "失败原因必须进日志，否则等于没记")
	require.NotEmpty(t, m["client_ip"], "带 client_ip 才能定位是谁触发的")
}

// 4xx 多为调用方问题，Warn 足够。
func TestErrLogs4xxAtWarn(t *testing.T) {
	m := errLog(t, httptest.NewRequest(http.MethodPost, "/admin/channels", nil),
		func(c *gin.Context) { BadRequest(c, "请求参数不合法") })

	require.Equal(t, "WARN", m["level"])
	require.Equal(t, float64(400), m["status"])
}

// 401/403 在管理面是高频常态（token 过期、越权探测），压到 Info 免得噪音淹没真正的故障。
func TestErrLogsAuthFailuresAtInfo(t *testing.T) {
	for _, tc := range []struct {
		name string
		call gin.HandlerFunc
	}{
		{"401", func(c *gin.Context) { Unauthorized(c, "请重新登录") }},
		{"403", func(c *gin.Context) { Forbidden(c, "当前角色无此权限") }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := errLog(t, httptest.NewRequest(http.MethodGet, "/admin/channels", nil), tc.call)
			require.Equal(t, "INFO", m["level"])
		})
	}
}

// 失败请求的查询串同样要留痕：分页/筛选参数常常正是触发失败的输入。
func TestErrLogRecordsQuery(t *testing.T) {
	m := errLog(t, httptest.NewRequest(http.MethodGet, "/admin/request-logs?page=99999&channel=3", nil),
		func(c *gin.Context) { Internal(c, "内部错误") })

	require.Equal(t, "/admin/request-logs", m["path"], "查询串不并进 path，否则按 path 聚合会被打散")
	require.Equal(t, "page=99999&channel=3", m["query"])
}

// 无查询串时不带空字段，避免每行都拖一个 query=""。
func TestErrLogOmitsEmptyQuery(t *testing.T) {
	m := errLog(t, httptest.NewRequest(http.MethodGet, "/admin/channels", nil),
		func(c *gin.Context) { Internal(c, "内部错误") })

	require.NotContains(t, m, "query")
}

// request_id 经 ctx 自动带出：同一次操作的访问日志与错误日志靠它串联。
func TestErrLogCarriesRequestID(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/admin/channels", nil)
	req = req.WithContext(logger.WithRequestID(req.Context(), "req-abc123"))

	m := errLog(t, req, func(c *gin.Context) { Internal(c, "内部错误") })
	require.Equal(t, "req-abc123", m["request_id"])
}

// Request 为 nil 的裸 Context（部分单测构造）不得 panic——日志不该比它记录的错误更危险。
func TestErrLogSurvivesNilRequest(t *testing.T) {
	gin.SetMode(gin.TestMode)
	logtest.Capture(t)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())

	require.NotPanics(t, func() { Internal(c, "内部错误") })
}

// 查询串里的敏感参数值脱敏，其余参数原样保留（OIDC 回调的 code/state 不得明文进日志）。
func TestRedactQuery(t *testing.T) {
	cases := map[string]string{
		"page=2&size=20":                      "page=2&size=20",
		"code=abc123&state=xyz":               "code=***&state=***",
		"STATE=xyz&page=1":                    "STATE=***&page=1",
		"access_token=t&q=hello%20world":      "access_token=***&q=hello%20world",
		"flag&code=1":                         "flag&code=***",
		"c%6Fde=encoded-name-still-sensitive": "c%6Fde=***",
	}
	for in, want := range cases {
		if got := redactQuery(in); got != want {
			t.Errorf("redactQuery(%q)=%q, want %q", in, got, want)
		}
	}
}
