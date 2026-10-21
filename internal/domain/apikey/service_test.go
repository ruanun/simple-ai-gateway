package apikey

import (
	"testing"
	"time"

	"github.com/ruanun/simple-ai-gateway/internal/dbtest"
	"github.com/ruanun/simple-ai-gateway/internal/model"
	"github.com/stretchr/testify/require"
)

func TestGenerateAndAuthenticate(t *testing.T) {
	g := dbtest.OpenInMemory(t)
	require.NoError(t, g.AutoMigrate(&model.APIKey{}))
	svc := NewService(NewRepo(g), "sk-saig")

	plain, rec, err := svc.Generate("test-key", nil)
	require.NoError(t, err)
	require.Contains(t, plain, "sk-saig-")
	require.NotEmpty(t, rec.Prefix)

	got, err := svc.Authenticate(plain)
	require.NoError(t, err)
	require.Equal(t, rec.ID, got.ID)

	_, err = svc.Authenticate("sk-saig-wrong")
	require.ErrorIs(t, err, ErrUnauthorized)
}

func TestDailyCost(t *testing.T) {
	g := dbtest.OpenInMemory(t)
	require.NoError(t, g.AutoMigrate(&model.APIKey{}))
	svc := NewService(NewRepo(g), "sk-saig")
	g.Create(&model.APIKey{Name: "k", KeyHash: "h", Status: 1, DailyCostLimit: 10000}) // $0.01
	now := time.Date(2026, 6, 21, 10, 0, 0, 0, time.UTC)

	// 生产链路走 CheckDailyCostOf（热路径复用已载入的行），此处每次重查最新行再判定。
	check := func(at time.Time) bool {
		k, err := svc.Get(1)
		require.NoError(t, err)
		return svc.CheckDailyCostOf(k, at)
	}

	require.True(t, check(now)) // 未用
	require.NoError(t, svc.ApplyDailyCost(1, 6000, now))
	require.True(t, check(now)) // 0.6<1
	require.NoError(t, svc.ApplyDailyCost(1, 6000, now))
	require.False(t, check(now)) // 1.2>1 超限

	next := now.AddDate(0, 0, 1)
	require.True(t, check(next)) // 跨日重置
}

// Generate 后明文同时落库到 Key 列（P11 令牌可回查）。
func TestGenerateStoresPlainKey(t *testing.T) {
	g := dbtest.OpenInMemory(t)
	require.NoError(t, g.AutoMigrate(&model.APIKey{}))
	svc := NewService(NewRepo(g), "sk-saig")
	plain, rec, err := svc.Generate("k", nil)
	require.NoError(t, err)
	require.Equal(t, plain, rec.Key)
	var got model.APIKey
	require.NoError(t, g.First(&got, rec.ID).Error)
	require.Equal(t, plain, got.Key)
}

// 治理字段更新（列白名单 map）不得触碰 key/key_hash。
func TestUpdateGovernanceKeepsPlainKey(t *testing.T) {
	g := dbtest.OpenInMemory(t)
	require.NoError(t, g.AutoMigrate(&model.APIKey{}))
	svc := NewService(NewRepo(g), "sk-saig")
	plain, rec, err := svc.Generate("k", nil)
	require.NoError(t, err)
	require.NoError(t, svc.UpdateGovernance(rec.ID, map[string]any{"rpm": 5}))
	var got model.APIKey
	require.NoError(t, g.First(&got, rec.ID).Error)
	require.Equal(t, plain, got.Key)
	require.Equal(t, rec.KeyHash, got.KeyHash)
	require.Equal(t, 5, got.RPM)
}

// TestAddDailyCostSingleSQL 同日累加、跨日重置为本次 cost 并翻日期（合并 CASE SQL 后语义不变）。
func TestAddDailyCostSingleSQL(t *testing.T) {
	g := dbtest.OpenInMemory(t)
	require.NoError(t, g.AutoMigrate(&model.APIKey{}))
	require.NoError(t, g.Create(&model.APIKey{Name: "k", KeyHash: "hh", Status: 1,
		DailyCostUsed: 500, DailyCostDate: "2026-07-19"}).Error)
	r := NewRepo(g)

	// 跨日：清零后累加 → 30，日期翻到今天
	require.NoError(t, r.AddDailyCost(1, 30, "2026-07-20"))
	k, err := r.Get(1)
	require.NoError(t, err)
	require.Equal(t, int64(30), k.DailyCostUsed, "跨日应重置为本次 cost")
	require.Equal(t, "2026-07-20", k.DailyCostDate)

	// 同日：累加 → 50
	require.NoError(t, r.AddDailyCost(1, 20, "2026-07-20"))
	k, err = r.Get(1)
	require.NoError(t, err)
	require.Equal(t, int64(50), k.DailyCostUsed, "同日应累加")
}
