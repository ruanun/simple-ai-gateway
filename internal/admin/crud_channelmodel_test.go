package admin

import (
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/ruanun/simple-ai-gateway/internal/dbtest"
	"github.com/ruanun/simple-ai-gateway/internal/model"
	"github.com/ruanun/simple-ai-gateway/internal/router"
	"github.com/stretchr/testify/require"
)

// setupChannelModelTest 内存库 + CRUDHandler（本测试路径仅用到 g/table/aesKey，其余依赖 nil 即可；
// routerNotify=nil 时 notifyRouterChange 跳过 Pub/Sub，只做本地 Build）。
func setupChannelModelTest(t *testing.T) *CRUDHandler {
	t.Helper()
	gin.SetMode(gin.TestMode)
	g := dbtest.OpenInMemory(t)
	// router.Build 查询的四张表必须建出，否则 notifyRouterChange 里的 Build 报错 → handler 500
	require.NoError(t, g.AutoMigrate(&model.Channel{}, &model.ChannelModel{}, &model.ModelAlias{}, &model.AliasRoute{}))
	return NewCRUDHandler(g, nil, nil, nil, router.NewTable(), "0123456789abcdef0123456789abcdef", nil, nil, nil, nil, nil)
}

// TestSetChannelModelStatus 停用→启用回环：DB 值随请求翻转；HTTP 200 同时证明路由表热刷成功。
func TestSetChannelModelStatus(t *testing.T) {
	h := setupChannelModelTest(t)
	require.NoError(t, h.g.Create(&model.Channel{Name: "ch1", Type: "openai", BaseURL: "http://up", Status: 1}).Error)
	cm := model.ChannelModel{ChannelID: 1, Model: "gpt-x", Enabled: true}
	require.NoError(t, h.g.Create(&cm).Error)

	post := func(status int) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(w)
		c.Params = gin.Params{{Key: "id", Value: strconv.FormatUint(cm.ID, 10)}}
		c.Request = httptest.NewRequest(http.MethodPost, "/admin/channel-models/"+strconv.FormatUint(cm.ID, 10)+"/status",
			strings.NewReader(`{"status":`+strconv.Itoa(status)+`}`))
		c.Request.Header.Set("Content-Type", "application/json")
		h.SetChannelModelStatus(c)
		return w
	}

	require.Equal(t, http.StatusOK, post(0).Code)
	var got model.ChannelModel
	require.NoError(t, h.g.First(&got, cm.ID).Error)
	require.False(t, got.Enabled, "status=0 后应停用")

	require.Equal(t, http.StatusOK, post(1).Code)
	require.NoError(t, h.g.First(&got, cm.ID).Error)
	require.True(t, got.Enabled, "status=1 后应回启用")
}
