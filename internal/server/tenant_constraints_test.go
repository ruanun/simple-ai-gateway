package server

import (
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/ruanun/simple-ai-gateway/internal/model"
	"github.com/stretchr/testify/require"
)

// blockingUpstream 请求内容含 "block" 时挂起直到 release 关闭（模拟长流式请求占着槽位），
// 含 "fail" 时回 500，其余正常返回带 usage 的 chat 响应。
func blockingUpstream(entered chan<- struct{}, release <-chan struct{}) *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := readAll(r)
		body := string(raw)
		switch {
		case strings.Contains(body, "block"):
			entered <- struct{}{}
			<-release
		case strings.Contains(body, "fail"):
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = w.Write([]byte(`{"error":{"message":"boom"}}`))
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"x","choices":[{"message":{"content":"hi"},"finish_reason":"stop"}],"usage":{"prompt_tokens":10,"completion_tokens":5,"total_tokens":15}}`))
	}))
}

// App 并发上限：占满后同 App 新请求 429；请求结束（成功或上游失败）都归还槽位。
func TestAppConcurrencyLimitE2E(t *testing.T) {
	gin.SetMode(gin.TestMode)
	entered := make(chan struct{}, 1)
	release := make(chan struct{})
	var releaseOnce sync.Once
	unblock := func() { releaseOnce.Do(func() { close(release) }) }
	up := blockingUpstream(entered, release)
	defer up.Close()
	defer unblock() // 先于 up.Close 执行：断言中途失败时也放行挂起的上游请求，否则 Close 等待处理结束会卡死
	r, key, g := setupMultiProto(t, up.URL)
	admin := doJSON(t, r, http.MethodPost, "/admin/login", "",
		`{"username":"admin","password":"sag12345"}`, http.StatusOK)["token"].(string)
	appPath := "/admin/apps/" + strconv.FormatUint(appIDFromKey(t, g, ""), 10)

	doJSON(t, r, http.MethodPost, appPath, admin, `{"max_concurrency":-2}`, http.StatusBadRequest)
	doJSON(t, r, http.MethodPost, appPath, admin, `{"max_concurrency":1}`, http.StatusOK)

	var wg sync.WaitGroup
	wg.Add(1)
	var firstCode int
	go func() {
		defer wg.Done()
		firstCode, _ = chatWith(r, key, "gpt-4o", "block")
	}()
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("首个请求未到达上游")
	}
	code, body := chatWith(r, key, "gpt-4o", "hi")
	require.Equal(t, http.StatusTooManyRequests, code, "槽位占满时同 App 新请求应 429: %s", body)
	require.Contains(t, body, "concurrent")

	unblock()
	wg.Wait()
	require.Equal(t, http.StatusOK, firstCode)
	code, body = chatWith(r, key, "gpt-4o", "hi")
	require.Equal(t, http.StatusOK, code, "请求结束后槽位须归还: %s", body)

	// 上游失败的请求同样归还槽位
	code, _ = chatWith(r, key, "gpt-4o", "fail")
	require.NotEqual(t, http.StatusOK, code)
	code, body = chatWith(r, key, "gpt-4o", "hi")
	require.Equal(t, http.StatusOK, code, "失败请求结束后槽位须归还: %s", body)

	// -1 不限：可正常写入
	doJSON(t, r, http.MethodPost, appPath, admin, `{"max_concurrency":-1}`, http.StatusOK)
	var a model.App
	require.NoError(t, g.First(&a, appIDFromKey(t, g, "")).Error)
	require.Equal(t, model.LimitUnlimited, a.MaxConcurrency)
}

// App 模型白名单：管理员硬约束，令牌映射与新建无白名单令牌都绕不过；/v1/models 同步过滤。
func TestAppModelWhitelistE2E(t *testing.T) {
	gin.SetMode(gin.TestMode)
	up := multiProtoUpstream()
	defer up.Close()
	r, key, g := setupMultiProto(t, up.URL)
	admin := doJSON(t, r, http.MethodPost, "/admin/login", "",
		`{"username":"admin","password":"sag12345"}`, http.StatusOK)["token"].(string)
	appPath := "/admin/apps/" + strconv.FormatUint(appIDFromKey(t, g, ""), 10)

	code, _ := chatReq(r, key, "gpt-4o")
	require.Equal(t, http.StatusOK, code, "白名单为空时不限")

	doJSON(t, r, http.MethodPost, appPath, admin, `{"model_whitelist":" gpt-4o-mini , ,other "}`, http.StatusOK)
	var a model.App
	require.NoError(t, g.First(&a, appIDFromKey(t, g, "")).Error)
	require.Equal(t, "gpt-4o-mini,other", a.ModelWhitelist, "清单应去空白、丢空项")

	code, body := chatReq(r, key, "gpt-4o")
	require.Equal(t, http.StatusForbidden, code, "不在 App 白名单的模型须 403: %s", body)
	require.Contains(t, body, "not allowed for this app")

	// 令牌映射到白名单外的模型同样被拦（App 白名单按映射后模型匹配）
	doJSON(t, r, http.MethodPost, "/admin/api-keys/"+strconv.FormatUint(lastKeyID(t, g), 10), admin,
		`{"model_mappings":"{\"gpt-4o-mini\":\"gpt-4o\"}"}`, http.StatusOK)
	code, _ = chatReq(r, key, "gpt-4o-mini")
	require.Equal(t, http.StatusForbidden, code, "映射到白名单外模型须 403")

	// 模型清单只列白名单内且可服务的模型
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/v1/models", nil)
	req.Header.Set("Authorization", "Bearer "+key)
	r.ServeHTTP(w, req)
	require.Equal(t, http.StatusOK, w.Code)
	require.NotContains(t, w.Body.String(), `"gpt-4o"`, "白名单外模型不应出现在清单里")

	// 放开白名单后恢复
	doJSON(t, r, http.MethodPost, appPath, admin, `{"model_whitelist":""}`, http.StatusOK)
	doJSON(t, r, http.MethodPost, "/admin/api-keys/"+strconv.FormatUint(lastKeyID(t, g), 10), admin,
		`{"model_mappings":""}`, http.StatusOK)
	code, _ = chatReq(r, key, "gpt-4o")
	require.Equal(t, http.StatusOK, code)
}

// 管理员停用的令牌门户不得重新启用；门户自己停用的可以；管理员启用后清除标记。
func TestAdminDisabledKeyLockedForPortalE2E(t *testing.T) {
	r, g, admin := setupPortalTest(t)
	doJSON(t, r, http.MethodPost, "/admin/portal-users", admin,
		`{"username":"tenant-u","password":"password1","with_app":true,"quota":3000000}`, http.StatusOK)
	tok := doJSON(t, r, http.MethodPost, "/portal/api/login", "", `{"username":"tenant-u","password":"password1"}`, http.StatusOK)["token"].(string)
	var a model.App
	require.NoError(t, g.Where("name = ?", "tenant-u").First(&a).Error)
	k := doJSON(t, r, http.MethodPost, "/portal/api/api-keys", tok, `{"name":"k1","app_id":`+strconv.FormatUint(a.ID, 10)+`}`, http.StatusOK)
	kid := itoa(k["id"].(float64))
	portalKey := "/portal/api/api-keys/" + kid
	flag := func() bool {
		var row model.APIKey
		require.NoError(t, g.First(&row, kid).Error)
		return row.DisabledByAdmin
	}

	// 门户自己停用/启用：不受限
	doJSON(t, r, http.MethodPost, portalKey, tok, `{"status":0}`, http.StatusOK)
	doJSON(t, r, http.MethodPost, portalKey, tok, `{"status":1}`, http.StatusOK)
	require.False(t, flag())

	// 管理员停用 → 打标记，门户启用 403，门户停用/删除仍可
	doJSON(t, r, http.MethodPost, "/admin/api-keys/"+kid, admin, `{"status":0}`, http.StatusOK)
	require.True(t, flag(), "管理员停用须打标记")
	doJSON(t, r, http.MethodPost, portalKey, tok, `{"status":1}`, http.StatusForbidden)
	doJSON(t, r, http.MethodPost, portalKey, tok, `{"status":0}`, http.StatusOK)
	var row model.APIKey
	require.NoError(t, g.First(&row, kid).Error)
	require.Equal(t, int8(0), row.Status, "门户启用被拒后令牌须仍为停用")

	// 管理员启用 → 清除标记，门户恢复自主
	doJSON(t, r, http.MethodPost, "/admin/api-keys/"+kid, admin, `{"status":1}`, http.StatusOK)
	require.False(t, flag(), "管理员启用须清除标记")
	doJSON(t, r, http.MethodPost, portalKey, tok, `{"status":0}`, http.StatusOK)
	doJSON(t, r, http.MethodPost, portalKey, tok, `{"status":1}`, http.StatusOK)
}
