package portal

import (
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/ruanun/simple-ai-gateway/internal/dbtest"
	"github.com/ruanun/simple-ai-gateway/internal/middleware"
	"github.com/ruanun/simple-ai-gateway/internal/model"
	"github.com/stretchr/testify/require"
)

// ctxWithUID 造一个已注入 uid 的 gin.Context；键与 uid(c) 实际读取的
// middleware.CtxPortalUserID（值为 "portal_user_id"）保持一致。
func ctxWithUID(userID uint64) (*gin.Context, *httptest.ResponseRecorder) {
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Set(middleware.CtxPortalUserID, userID)
	return c, w
}

func TestAccessibleAppIDsMemberOnly(t *testing.T) {
	gin.SetMode(gin.TestMode)
	g := dbtest.OpenInMemory(t)
	require.NoError(t, g.AutoMigrate(&model.App{}, &model.AppMember{}, &model.APIKey{}))
	require.NoError(t, g.Create(&model.App{Name: "app1", OwnerUserID: 1}).Error)
	require.NoError(t, g.Create(&model.App{Name: "app2", OwnerUserID: 2}).Error)
	require.NoError(t, g.Create(&model.AppMember{AppID: 1, PortalUserID: 1}).Error)
	require.NoError(t, g.Create(&model.AppMember{AppID: 2, PortalUserID: 2}).Error)
	h := &Handler{g: g}

	c, _ := ctxWithUID(1)
	ids := h.accessibleAppIDs(c)
	require.Equal(t, []uint64{1}, ids)

	c2, _ := ctxWithUID(1)
	_, ok := h.ownApp(c2, 2) // App2 非成员
	require.False(t, ok)
}
