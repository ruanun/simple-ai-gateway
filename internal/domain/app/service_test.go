package app

import (
	"testing"

	"github.com/ruanun/simple-ai-gateway/internal/dbtest"
	"github.com/ruanun/simple-ai-gateway/internal/model"
	"github.com/stretchr/testify/require"
)

func TestQuotaCheckAndDeduct(t *testing.T) {
	g := dbtest.OpenInMemory(t)
	require.NoError(t, g.AutoMigrate(&model.App{}))
	svc := NewService(NewRepo(g))

	a, err := svc.Create("team-a", 1_000_000) // 1 USD
	require.NoError(t, err)
	require.True(t, svc.HasQuota(a.ID))

	require.NoError(t, svc.Deduct(a.ID, 600_000))
	require.True(t, svc.HasQuota(a.ID)) // 用了 0.6，还有余
	require.NoError(t, svc.Deduct(a.ID, 600_000))
	require.False(t, svc.HasQuota(a.ID)) // 已超额
}

func TestUnlimitedQuota(t *testing.T) {
	g := dbtest.OpenInMemory(t)
	require.NoError(t, g.AutoMigrate(&model.App{}))
	svc := NewService(NewRepo(g))
	a, _ := svc.Create("free", 0) // 0=不限
	require.NoError(t, svc.Deduct(a.ID, 9_999_999))
	require.True(t, svc.HasQuota(a.ID))
}

// TestDeleteCascadesAppMembers 删 App 时应同事务清理其 app_members 行，
// 避免残留孤儿成员导致门户 Me()/管理端用户列表按 count(AppMember) 算出的「名下应用数」虚高。
func TestDeleteCascadesAppMembers(t *testing.T) {
	g := dbtest.OpenInMemory(t)
	require.NoError(t, g.AutoMigrate(&model.App{}, &model.AppMember{}))
	svc := NewService(NewRepo(g))

	a, err := svc.Create("team-b", 0)
	require.NoError(t, err)
	require.NoError(t, g.Create(&model.AppMember{AppID: a.ID, PortalUserID: 1}).Error)
	require.NoError(t, g.Create(&model.AppMember{AppID: a.ID, PortalUserID: 2}).Error)

	var before int64
	require.NoError(t, g.Model(&model.AppMember{}).Where("app_id = ?", a.ID).Count(&before).Error)
	require.EqualValues(t, 2, before)

	require.NoError(t, svc.Delete(a.ID))

	var after int64
	require.NoError(t, g.Model(&model.AppMember{}).Where("app_id = ?", a.ID).Count(&after).Error)
	require.EqualValues(t, 0, after)
}
