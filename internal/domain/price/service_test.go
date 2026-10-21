package price

import (
	"testing"

	"github.com/ruanun/simple-ai-gateway/internal/dbtest"
	"github.com/ruanun/simple-ai-gateway/internal/model"
	"github.com/stretchr/testify/require"
)

func TestUpsertAndGet(t *testing.T) {
	g := dbtest.OpenInMemory(t)
	require.NoError(t, g.AutoMigrate(&model.ModelPrice{}))
	svc := NewService(NewRepo(g))

	require.NoError(t, svc.Upsert(model.ModelPrice{Model: "gpt-4o", InputPer1M: 2.5, OutputPer1M: 10, Enabled: true}))
	require.NoError(t, svc.Upsert(model.ModelPrice{Model: "gpt-4o", InputPer1M: 3, OutputPer1M: 12, Enabled: true})) // 覆盖

	p, ok := svc.Get("gpt-4o")
	require.True(t, ok)
	require.Equal(t, 3.0, p.InputPer1M)
	require.Equal(t, 12.0, p.OutputPer1M)

	// 唯一索引生效：覆盖而非新增。
	list, err := svc.List()
	require.NoError(t, err)
	require.Len(t, list, 1)
}

func TestUpsertTimes(t *testing.T) {
	g := dbtest.OpenInMemory(t)
	require.NoError(t, g.AutoMigrate(&model.ModelPrice{}))
	svc := NewService(NewRepo(g))
	require.NoError(t, svc.Upsert(model.ModelPrice{Model: "dall-e-3", Type: "times", PerCallMicros: 40000, Enabled: true}))
	p, ok := svc.Get("dall-e-3")
	require.True(t, ok)
	require.Equal(t, "times", p.Type)
	require.Equal(t, int64(40000), p.PerCallMicros)
	// 覆盖更新
	require.NoError(t, svc.Upsert(model.ModelPrice{Model: "dall-e-3", Type: "times", PerCallMicros: 80000, Enabled: true}))
	p, _ = svc.Get("dall-e-3")
	require.Equal(t, int64(80000), p.PerCallMicros)
}

func TestResolveOverrideThenFallback(t *testing.T) {
	g := dbtest.OpenInMemory(t)
	require.NoError(t, g.AutoMigrate(&model.ModelPrice{}))
	svc := NewService(NewRepo(g))

	// 全局官方价(channel_id=0)与渠道 7 的覆盖价。
	require.NoError(t, svc.Upsert(model.ModelPrice{Model: "gpt-4o", InputPer1M: 2.5, OutputPer1M: 10, Enabled: true}))
	require.NoError(t, svc.Upsert(model.ModelPrice{ChannelID: 7, Model: "gpt-4o", InputPer1M: 2, OutputPer1M: 8, Enabled: true}))

	// 渠道 7 命中覆盖价。
	p, ok := svc.Resolve(7, "gpt-4o")
	require.True(t, ok)
	require.Equal(t, 2.0, p.InputPer1M)

	// 渠道 9 无覆盖 → 回退全局官方价。
	p, ok = svc.Resolve(9, "gpt-4o")
	require.True(t, ok)
	require.Equal(t, 2.5, p.InputPer1M)

	// 完全没有的模型 → false。
	_, ok = svc.Resolve(7, "nope")
	require.False(t, ok)

	// 同 (channel,model) 覆盖而非新增。
	require.NoError(t, svc.Upsert(model.ModelPrice{ChannelID: 7, Model: "gpt-4o", InputPer1M: 1, OutputPer1M: 4, Enabled: true}))
	p, _ = svc.Resolve(7, "gpt-4o")
	require.Equal(t, 1.0, p.InputPer1M)
}
