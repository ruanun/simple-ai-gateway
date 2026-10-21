package apikey

import (
	"testing"

	"github.com/ruanun/simple-ai-gateway/internal/dbtest"
	"github.com/ruanun/simple-ai-gateway/internal/model"
	"github.com/stretchr/testify/require"
)

func TestAuthCachePositiveAndInvalidate(t *testing.T) {
	g := dbtest.OpenInMemory(t)
	require.NoError(t, g.AutoMigrate(&model.APIKey{}))
	svc := NewService(NewRepo(g), "sk-saig")
	plain, rec, err := svc.Generate("k", nil)
	require.NoError(t, err)

	_, err = svc.Authenticate(plain)
	require.NoError(t, err)

	// 直接改 DB（绕过管理端失效）：5s 缓存内仍命中旧快照。
	require.NoError(t, g.Model(&model.APIKey{}).Where("id = ?", rec.ID).Update("status", 0).Error)
	_, err = svc.Authenticate(plain)
	require.NoError(t, err, "缓存内应仍命中；这是性能换来的有界陈旧")

	// 管理端失效后立即生效。
	svc.InvalidateAuthCacheByID(rec.ID)
	_, err = svc.Authenticate(plain)
	require.ErrorIs(t, err, ErrUnauthorized)
}

func TestAuthCacheBypassesDailyLimitKeys(t *testing.T) {
	g := dbtest.OpenInMemory(t)
	require.NoError(t, g.AutoMigrate(&model.APIKey{}))
	svc := NewService(NewRepo(g), "sk-saig")
	plain, rec, err := svc.Generate("k", nil)
	require.NoError(t, err)

	// 设日限额会触发失效；之后鉴权必须回表，拿到最新 daily_cost_used。
	require.NoError(t, svc.UpdateGovernance(rec.ID, map[string]any{"daily_cost_limit": int64(100)}))
	require.NoError(t, g.Model(&model.APIKey{}).Where("id = ?", rec.ID).
		Updates(map[string]any{"daily_cost_used": int64(150), "daily_cost_date": "2026-10-21"}).Error)
	got, err := svc.Authenticate(plain)
	require.NoError(t, err)
	require.Equal(t, int64(150), got.DailyCostUsed, "有日限额的 key 不得命中旧缓存")
}

func TestAuthCacheNegativeAndClear(t *testing.T) {
	g := dbtest.OpenInMemory(t)
	require.NoError(t, g.AutoMigrate(&model.APIKey{}))
	svc := NewService(NewRepo(g), "sk-saig")
	const plain = "sk-saig-negative"
	_, err := svc.Authenticate(plain)
	require.ErrorIs(t, err, ErrUnauthorized)

	// 负缓存期间补建同 hash 的 key，仍拒绝；清空缓存后放行。
	require.NoError(t, g.Create(&model.APIKey{Name: "late", KeyHash: hashKey(plain), Status: 1}).Error)
	_, err = svc.Authenticate(plain)
	require.ErrorIs(t, err, ErrUnauthorized, "负缓存应阻止无效 key 反复打 DB")
	svc.InvalidateAllAuthCache()
	got, err := svc.Authenticate(plain)
	require.NoError(t, err)
	require.Equal(t, "late", got.Name)
}

func TestAuthCacheDisabled(t *testing.T) {
	g := dbtest.OpenInMemory(t)
	require.NoError(t, g.AutoMigrate(&model.APIKey{}))
	svc := NewService(NewRepo(g), "sk-saig")
	svc.SetAuthCache(0, 0)
	plain, rec, err := svc.Generate("k", nil)
	require.NoError(t, err)
	_, err = svc.Authenticate(plain)
	require.NoError(t, err)
	require.NoError(t, g.Model(&model.APIKey{}).Where("id = ?", rec.ID).Update("status", 0).Error)
	_, err = svc.Authenticate(plain)
	require.ErrorIs(t, err, ErrUnauthorized, "关闭缓存后必须每次回表")
}
