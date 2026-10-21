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

func setupAccountTest(t *testing.T) *CRUDHandler {
	t.Helper()
	gin.SetMode(gin.TestMode)
	g := dbtest.OpenInMemory(t)
	require.NoError(t, g.AutoMigrate(&model.Channel{}, &model.Account{}))
	return NewCRUDHandler(g, nil, nil, nil, router.NewTable(), "0123456789abcdef0123456789abcdef",
		nil, nil, nil, nil, nil)
}

func TestCreateAndUpdateAccountScheduleFields(t *testing.T) {
	h := setupAccountTest(t)
	require.NoError(t, h.g.Create(&model.Channel{Name: "ch1", Type: "openai", BaseURL: "http://up", Status: 1}).Error)

	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodPost, "/admin/accounts", strings.NewReader(`{
		"channel_id":1,"name":"a1","api_key":"sk-a","max_concurrency":3,"rpm_limit":60,
		"expires_at":"2026-12-31T00:00:00Z","temp_unschedulable_reason":"maintenance"
	}`))
	c.Request.Header.Set("Content-Type", "application/json")
	h.CreateAccount(c)
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())

	var got model.Account
	require.NoError(t, h.g.First(&got).Error)
	require.Equal(t, 3, got.MaxConcurrency)
	require.Equal(t, 60, got.RPMLimit)
	require.NotNil(t, got.ExpiresAt)
	require.Equal(t, "maintenance", got.TempUnschedulableReason)

	// 更新：保留非零、再显式写 0 / 清空时间，验证 map 更新没有被 GORM default 零值陷阱覆盖。
	w = httptest.NewRecorder()
	c, _ = gin.CreateTestContext(w)
	c.Params = gin.Params{{Key: "id", Value: strconv.FormatUint(got.ID, 10)}}
	c.Request = httptest.NewRequest(http.MethodPost, "/admin/accounts/"+strconv.FormatUint(got.ID, 10),
		strings.NewReader(`{"max_concurrency":0,"rpm_limit":0,"expires_at":"","name":"a2"}`))
	c.Request.Header.Set("Content-Type", "application/json")
	h.UpdateAccount(c)
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())

	var updated model.Account
	require.NoError(t, h.g.First(&updated, got.ID).Error)
	require.Equal(t, 0, updated.MaxConcurrency)
	require.Equal(t, 0, updated.RPMLimit)
	require.Nil(t, updated.ExpiresAt)
	require.Equal(t, "a2", updated.Name)
}
