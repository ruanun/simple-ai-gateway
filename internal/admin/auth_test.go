package admin

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/ruanun/simple-ai-gateway/internal/dbtest"
	"github.com/ruanun/simple-ai-gateway/internal/middleware"
	"github.com/ruanun/simple-ai-gateway/internal/model"
	"github.com/ruanun/simple-ai-gateway/internal/ratelimit"
	"github.com/stretchr/testify/require"
	"golang.org/x/crypto/bcrypt"
)

// 同一用户名失败达阈值后，即使密码正确也 429；不存在的用户名同样计数（不泄露账号是否存在）；
// 成功登录不计数。
func TestLoginUserFailLock(t *testing.T) {
	gin.SetMode(gin.TestMode)
	g := dbtest.OpenInMemory(t)
	require.NoError(t, g.AutoMigrate(&model.AdminUser{}))
	hash, err := bcrypt.GenerateFromPassword([]byte("right-password"), bcrypt.MinCost)
	require.NoError(t, err)
	require.NoError(t, g.Create(&model.AdminUser{Username: "alice", PasswordHash: string(hash), Status: 1}).Error)

	now := func() time.Time { return time.Unix(0, 0) }
	guard := middleware.NewLoginGuard(ratelimit.NewMemoryLimiter(), now, 2)
	h := NewAuthHandler(g, "test-secret", 1, guard)
	r := gin.New()
	r.POST("/admin/login", h.Login)
	login := func(user, pass string) int {
		b, _ := json.Marshal(map[string]string{"username": user, "password": pass})
		w := httptest.NewRecorder()
		r.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/admin/login", bytes.NewReader(b)))
		return w.Code
	}

	require.Equal(t, http.StatusOK, login("alice", "right-password"))
	require.Equal(t, http.StatusOK, login("alice", "right-password"), "成功登录不应计入失败")
	require.Equal(t, http.StatusUnauthorized, login("alice", "wrong-1"))
	require.Equal(t, http.StatusUnauthorized, login("alice", "wrong-2"))
	require.Equal(t, http.StatusTooManyRequests, login("alice", "right-password"), "锁定期内正确密码也须拒绝")

	require.Equal(t, http.StatusUnauthorized, login("ghost", "x"))
	require.Equal(t, http.StatusUnauthorized, login("ghost", "x"))
	require.Equal(t, http.StatusTooManyRequests, login("ghost", "x"), "不存在的用户名须与真实账号同样锁定")
}
