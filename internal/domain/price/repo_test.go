package price

import (
	"testing"

	"github.com/ruanun/simple-ai-gateway/internal/dbtest"
	"github.com/ruanun/simple-ai-gateway/internal/model"
	"github.com/stretchr/testify/require"
)

// TestUpsertCacheWritePer1M 新列 cache_write_per_1m 经 Upsert 覆盖路径正确写入与更新（P12d）。
func TestUpsertCacheWritePer1M(t *testing.T) {
	g := dbtest.OpenInMemory(t)
	require.NoError(t, g.AutoMigrate(&model.ModelPrice{}))
	r := NewRepo(g)
	require.NoError(t, r.Upsert(&model.ModelPrice{Model: "claude-3-5-sonnet", InputPer1M: 3, OutputPer1M: 15, CacheWritePer1M: 3.75}))
	p, err := r.GetByChannel(0, "claude-3-5-sonnet")
	require.NoError(t, err)
	require.Equal(t, 3.75, p.CacheWritePer1M)
	// 二次 Upsert 覆盖（走 AssignmentColumns 白名单）
	require.NoError(t, r.Upsert(&model.ModelPrice{Model: "claude-3-5-sonnet", InputPer1M: 3, OutputPer1M: 15, CacheWritePer1M: 4}))
	p, err = r.GetByChannel(0, "claude-3-5-sonnet")
	require.NoError(t, err)
	require.Equal(t, 4.0, p.CacheWritePer1M)
}

// TestUpsertDisable 停用（enabled=false）必须能经 Upsert 落库。
// 回归：Enabled 带 gorm default:true 标签，GORM 的 Create 会跳过零值 default 字段，
// 导致 excluded.enabled 取数据库默认值 true —— 停用被静默写回启用（价格页「停用」按钮与
// 「编辑已停用价格保存后仍停用」两条链路都因此失效）。
func TestUpsertDisable(t *testing.T) {
	g := dbtest.OpenInMemory(t)
	require.NoError(t, g.AutoMigrate(&model.ModelPrice{}))
	r := NewRepo(g)
	require.NoError(t, r.Upsert(&model.ModelPrice{Model: "gpt-x", InputPer1M: 1, OutputPer1M: 2, Enabled: true}))
	p, err := r.GetByChannel(0, "gpt-x")
	require.NoError(t, err)
	require.True(t, p.Enabled, "首次插入应为启用")

	// 整行回传 + 翻转 enabled（前端 toggleM 与编辑保存的实际提交形态：带 ID 的整行）
	p.Enabled = false
	require.NoError(t, r.Upsert(p))
	got, err := r.GetByChannel(0, "gpt-x")
	require.NoError(t, err)
	require.False(t, got.Enabled, "停用应落库，不得被 default:true 覆盖回启用")

	// 反向回环：再启用
	got.Enabled = true
	require.NoError(t, r.Upsert(got))
	got, err = r.GetByChannel(0, "gpt-x")
	require.NoError(t, err)
	require.True(t, got.Enabled, "应能重新启用")
}
