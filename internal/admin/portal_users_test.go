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
	"github.com/stretchr/testify/require"
)

// setupPortalUserTest 建一个只含成员相关表的内存库 + Handler。
func setupPortalUserTest(t *testing.T) (*PortalUserHandler, *gin.Engine) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	g := dbtest.OpenInMemory(t)
	require.NoError(t, g.AutoMigrate(&model.App{}, &model.PortalUser{}, &model.AppMember{}))
	return NewPortalUserHandler(g), gin.New()
}

// TestAppMemberAddIdempotent AddAppMember 幂等：重复加同一用户仅保留一行。
func TestAppMemberAddIdempotent(t *testing.T) {
	h, _ := setupPortalUserTest(t)
	require.NoError(t, h.g.Create(&model.App{Name: "app1", OwnerUserID: 1}).Error)
	u := model.PortalUser{Username: "u2", PasswordHash: "x"}
	require.NoError(t, h.g.Create(&u).Error)

	body := `{"user_id":` + strconv.FormatUint(u.ID, 10) + `}`
	for i := 0; i < 2; i++ {
		w := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(w)
		c.Params = gin.Params{{Key: "id", Value: "1"}}
		c.Request = httptest.NewRequest(http.MethodPost, "/admin/apps/1/members", strings.NewReader(body))
		c.Request.Header.Set("Content-Type", "application/json")
		h.AddAppMember(c)
		require.Equal(t, http.StatusOK, w.Code)
	}

	var n int64
	h.g.Model(&model.AppMember{}).Where("app_id = ? AND portal_user_id = ?", 1, u.ID).Count(&n)
	require.Equal(t, int64(1), n, "重复添加应保持幂等，只有一行")
}

// TestAppMemberRemoveRejectsOwner RemoveAppMember 拒绝移除 owner；可正常移除普通成员。
func TestAppMemberRemoveRejectsOwner(t *testing.T) {
	h, _ := setupPortalUserTest(t)
	require.NoError(t, h.g.Create(&model.App{Name: "app1", OwnerUserID: 1}).Error)
	require.NoError(t, h.g.Create(&model.AppMember{AppID: 1, PortalUserID: 1}).Error)
	require.NoError(t, h.g.Create(&model.AppMember{AppID: 1, PortalUserID: 2}).Error)

	// 拒删 owner（userId=1）→ 400
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Params = gin.Params{{Key: "id", Value: "1"}, {Key: "userId", Value: "1"}}
	c.Request = httptest.NewRequest(http.MethodDelete, "/admin/apps/1/members/1", nil)
	h.RemoveAppMember(c)
	require.Equal(t, http.StatusBadRequest, w.Code)
	var n1 int64
	h.g.Model(&model.AppMember{}).Where("app_id = ? AND portal_user_id = ?", 1, 1).Count(&n1)
	require.Equal(t, int64(1), n1, "owner 成员行不应被删除")

	// 正常删除非 owner 成员（userId=2）→ 200
	w2 := httptest.NewRecorder()
	c2, _ := gin.CreateTestContext(w2)
	c2.Params = gin.Params{{Key: "id", Value: "1"}, {Key: "userId", Value: "2"}}
	c2.Request = httptest.NewRequest(http.MethodDelete, "/admin/apps/1/members/2", nil)
	h.RemoveAppMember(c2)
	require.Equal(t, http.StatusOK, w2.Code)
	var n2 int64
	h.g.Model(&model.AppMember{}).Where("app_id = ? AND portal_user_id = ?", 1, 2).Count(&n2)
	require.Equal(t, int64(0), n2, "非 owner 成员应被成功移除")
}

// TestListAppMembers 列出成员：owner 行标 is_owner=true。
func TestListAppMembers(t *testing.T) {
	h, _ := setupPortalUserTest(t)
	require.NoError(t, h.g.Create(&model.App{Name: "app1", OwnerUserID: 1}).Error)
	require.NoError(t, h.g.Create(&model.PortalUser{Username: "owner1", PasswordHash: "x"}).Error)
	require.NoError(t, h.g.Create(&model.PortalUser{Username: "member2", PasswordHash: "x"}).Error)
	require.NoError(t, h.g.Create(&model.AppMember{AppID: 1, PortalUserID: 1}).Error)
	require.NoError(t, h.g.Create(&model.AppMember{AppID: 1, PortalUserID: 2}).Error)

	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Params = gin.Params{{Key: "id", Value: "1"}}
	c.Request = httptest.NewRequest(http.MethodGet, "/admin/apps/1/members", nil)
	h.ListAppMembers(c)
	require.Equal(t, http.StatusOK, w.Code)
	require.Contains(t, w.Body.String(), `"is_owner":true`)
	require.Contains(t, w.Body.String(), `"username":"owner1"`)
	require.Contains(t, w.Body.String(), `"username":"member2"`)
}

// TestSetAppOwnerEnsuresMember SetAppOwner 分配 owner 后应确保存在对应成员行。
func TestSetAppOwnerEnsuresMember(t *testing.T) {
	h, _ := setupPortalUserTest(t)
	require.NoError(t, h.g.Create(&model.App{Name: "app1"}).Error)
	require.NoError(t, h.g.Create(&model.PortalUser{Username: "u1", PasswordHash: "x"}).Error)

	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Params = gin.Params{{Key: "id", Value: "1"}}
	c.Request = httptest.NewRequest(http.MethodPost, "/admin/apps/1/owner", strings.NewReader(`{"user_id":1}`))
	c.Request.Header.Set("Content-Type", "application/json")
	h.SetAppOwner(c)
	require.Equal(t, http.StatusOK, w.Code)

	var n int64
	h.g.Model(&model.AppMember{}).Where("app_id = ? AND portal_user_id = ?", 1, 1).Count(&n)
	require.Equal(t, int64(1), n, "分配 owner 后应自动确保成员行")
}

// TestSetAppOwnerRevokesOldOwner 改派/收回 owner 时应删除旧 owner 的成员行：
// AppMember 是门户访问权唯一真相源，漏清会让被收回的用户仍能管理该 App 的 key/日志/配额。
func TestSetAppOwnerRevokesOldOwner(t *testing.T) {
	h, _ := setupPortalUserTest(t)
	require.NoError(t, h.g.Create(&model.App{Name: "app1", OwnerUserID: 1}).Error)
	require.NoError(t, h.g.Create(&model.PortalUser{Username: "old", PasswordHash: "x"}).Error)
	require.NoError(t, h.g.Create(&model.PortalUser{Username: "new", PasswordHash: "x"}).Error)
	require.NoError(t, h.g.Create(&model.AppMember{AppID: 1, PortalUserID: 1}).Error)

	// 改派给用户 2：旧 owner(1) 成员行应被删除，新 owner(2) 成员行补建
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Params = gin.Params{{Key: "id", Value: "1"}}
	c.Request = httptest.NewRequest(http.MethodPost, "/admin/apps/1/owner", strings.NewReader(`{"user_id":2}`))
	c.Request.Header.Set("Content-Type", "application/json")
	h.SetAppOwner(c)
	require.Equal(t, http.StatusOK, w.Code)

	var oldN, newN int64
	h.g.Model(&model.AppMember{}).Where("app_id = ? AND portal_user_id = ?", 1, 1).Count(&oldN)
	h.g.Model(&model.AppMember{}).Where("app_id = ? AND portal_user_id = ?", 1, 2).Count(&newN)
	require.Equal(t, int64(0), oldN, "旧 owner 的成员行应被删除（吊销访问权）")
	require.Equal(t, int64(1), newN, "新 owner 应有成员行")

	// 收回为无主（user_id=0）：当前 owner(2) 成员行同样应被删除
	w2 := httptest.NewRecorder()
	c2, _ := gin.CreateTestContext(w2)
	c2.Params = gin.Params{{Key: "id", Value: "1"}}
	c2.Request = httptest.NewRequest(http.MethodPost, "/admin/apps/1/owner", strings.NewReader(`{"user_id":0}`))
	c2.Request.Header.Set("Content-Type", "application/json")
	h.SetAppOwner(c2)
	require.Equal(t, http.StatusOK, w2.Code)

	var afterN int64
	h.g.Model(&model.AppMember{}).Where("app_id = ? AND portal_user_id = ?", 1, 2).Count(&afterN)
	require.Equal(t, int64(0), afterN, "收回为无主后旧 owner 的成员行应被删除")
}

// TestSetAppOwnerSameOwnerKeepsMember 重复设置同一 owner 幂等：成员行保留不误删。
func TestSetAppOwnerSameOwnerKeepsMember(t *testing.T) {
	h, _ := setupPortalUserTest(t)
	require.NoError(t, h.g.Create(&model.App{Name: "app1", OwnerUserID: 1}).Error)
	require.NoError(t, h.g.Create(&model.PortalUser{Username: "u1", PasswordHash: "x"}).Error)
	require.NoError(t, h.g.Create(&model.AppMember{AppID: 1, PortalUserID: 1}).Error)

	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Params = gin.Params{{Key: "id", Value: "1"}}
	c.Request = httptest.NewRequest(http.MethodPost, "/admin/apps/1/owner", strings.NewReader(`{"user_id":1}`))
	c.Request.Header.Set("Content-Type", "application/json")
	h.SetAppOwner(c)
	require.Equal(t, http.StatusOK, w.Code)

	var n int64
	h.g.Model(&model.AppMember{}).Where("app_id = ? AND portal_user_id = ?", 1, 1).Count(&n)
	require.Equal(t, int64(1), n, "同一 owner 重设不应删除其成员行")
}

// TestDeletePortalUserCleansMembers Delete 删除用户后应清空其全部成员行。
func TestDeletePortalUserCleansMembers(t *testing.T) {
	h, _ := setupPortalUserTest(t)
	require.NoError(t, h.g.Create(&model.App{Name: "app1", OwnerUserID: 1}).Error)
	require.NoError(t, h.g.Create(&model.PortalUser{Username: "u1", PasswordHash: "x"}).Error)
	require.NoError(t, h.g.Create(&model.AppMember{AppID: 1, PortalUserID: 1}).Error)

	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Params = gin.Params{{Key: "id", Value: "1"}}
	c.Request = httptest.NewRequest(http.MethodDelete, "/admin/portal-users/1", nil)
	h.Delete(c)
	require.Equal(t, http.StatusOK, w.Code)

	var n int64
	h.g.Model(&model.AppMember{}).Where("portal_user_id = ?", 1).Count(&n)
	require.Equal(t, int64(0), n, "删除用户后其成员行应清空")
}
