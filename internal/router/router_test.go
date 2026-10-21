package router

import (
	"testing"

	"github.com/ruanun/simple-ai-gateway/internal/dbtest"
	"github.com/ruanun/simple-ai-gateway/internal/model"
	"github.com/stretchr/testify/require"
)

func TestResolveLayeredWeighted(t *testing.T) {
	// 直接构造候选(不走 DB)：别名 smart 两条 P1 边 70/30 + 一条 P0 降级边
	t.Cleanup(func() {})
	tab := NewTable()
	tab.SetAliasForTest("smart", []Candidate{
		{ChannelID: 1, Model: "gpt-4o", Priority: 1, Weight: 70},
		{ChannelID: 2, Model: "gpt-4o", Priority: 1, Weight: 30},
		{ChannelID: 3, Model: "gpt-4o", Priority: 0, Weight: 1},
	})
	// 注入确定性 rng：每轮不同序列，统计层内分布
	counts := map[uint64]int{}
	for i := 0; i < 1000; i++ {
		tab.rng = newSeqRng(i) // 每轮不同序列，统计层内分布
		ord := tab.Resolve("smart")
		require.Len(t, ord, 3)
		// 第一层(priority=1)的两个一定排在 priority=0 之前
		require.Contains(t, []uint64{1, 2}, ord[0].ChannelID)
		require.Equal(t, uint64(3), ord[2].ChannelID)
		counts[ord[0].ChannelID]++
	}
	// 70/30：渠道1 当选层首的次数应明显多于渠道2
	require.Greater(t, counts[1], counts[2])
}

func TestResolveDirectModel(t *testing.T) {
	tab := NewTable()
	tab.SetDirectForTest("gpt-4o", []Candidate{{ChannelID: 5, Model: "gpt-4o", Priority: 0, Weight: 1}})
	ord := tab.Resolve("gpt-4o")
	require.Len(t, ord, 1)
	require.Equal(t, uint64(5), ord[0].ChannelID)
}

func TestResolveUnknown(t *testing.T) {
	require.Empty(t, NewTable().Resolve("nope"))
}

func TestBuildSetsProviderType(t *testing.T) {
	g := dbtest.OpenInMemory(t)
	require.NoError(t, g.AutoMigrate(&model.Channel{}, &model.ChannelModel{}, &model.ModelAlias{}, &model.AliasRoute{}))
	// Channel 自带 type，不再依赖 Provider
	g.Create(&model.Channel{Name: "c1", Type: "anthropic", Status: 1, Weight: 1})
	var ch model.Channel
	g.First(&ch)
	g.Create(&model.ChannelModel{ChannelID: ch.ID, Model: "claude-3", Enabled: true})
	tb := NewTable()
	require.NoError(t, tb.Build(g))
	cs := tb.Resolve("claude-3")
	require.Len(t, cs, 1)
	require.Equal(t, "anthropic", cs[0].ProviderType)
}

// TestBuildSetsProviderTypeForAlias 验证别名路由候选也带出渠道协议(type)。
func TestBuildSetsProviderTypeForAlias(t *testing.T) {
	g := dbtest.OpenInMemory(t)
	require.NoError(t, g.AutoMigrate(&model.Channel{}, &model.ChannelModel{}, &model.ModelAlias{}, &model.AliasRoute{}))
	ch := model.Channel{Name: "anth-only", Type: "anthropic", BaseURL: "http://u", Status: 1}
	require.NoError(t, g.Create(&ch).Error)
	require.NoError(t, g.Create(&model.ChannelModel{ChannelID: ch.ID, Model: "claude-3", Enabled: true}).Error) // #29 后别名候选需对应启用的 ChannelModel

	al := model.ModelAlias{Alias: "smart", Status: 1}
	require.NoError(t, g.Create(&al).Error)
	require.NoError(t, g.Create(&model.AliasRoute{AliasID: al.ID, ChannelID: ch.ID, Model: "claude-3", Weight: 1, Priority: 1}).Error)

	tb := NewTable()
	require.NoError(t, tb.Build(g))
	cs := tb.Resolve("smart")
	require.Len(t, cs, 1)
	require.Equal(t, "anthropic", cs[0].ProviderType)
	require.Empty(t, cs[0].Outbound, "Build 阶段不填 Outbound，由候选过滤阶段填")
}

// TestBuildCarriesAllowConvert Build 应把渠道 allow_convert 带到候选上（直调与别名两路）。
func TestBuildCarriesAllowConvert(t *testing.T) {
	g := dbtest.OpenInMemory(t)
	require.NoError(t, g.AutoMigrate(&model.Channel{}, &model.ChannelModel{}, &model.ModelAlias{}, &model.AliasRoute{}))
	ch := model.Channel{Name: "an", Type: "anthropic", AllowConvert: true, Status: 1, Weight: 1}
	require.NoError(t, g.Create(&ch).Error)
	require.NoError(t, g.Create(&model.ChannelModel{ChannelID: ch.ID, Model: "claude-3-5-sonnet", Enabled: true}).Error)
	al := model.ModelAlias{Alias: "smart", Status: 1}
	require.NoError(t, g.Create(&al).Error)
	require.NoError(t, g.Create(&model.AliasRoute{AliasID: al.ID, ChannelID: ch.ID, Model: "claude-3-5-sonnet", Weight: 1}).Error)

	tb := NewTable()
	require.NoError(t, tb.Build(g))

	direct := tb.Resolve("claude-3-5-sonnet")
	require.Len(t, direct, 1)
	require.True(t, direct[0].AllowConvert)
	require.Equal(t, "anthropic", direct[0].ProviderType)

	aliased := tb.Resolve("smart")
	require.Len(t, aliased, 1)
	require.True(t, aliased[0].AllowConvert)
}

// TestBuildCarriesStripThinking 验证 Build 把渠道 strip_thinking 带出到 Candidate（direct + 别名）。
func TestBuildCarriesStripThinking(t *testing.T) {
	g := dbtest.OpenInMemory(t)
	require.NoError(t, g.AutoMigrate(&model.Channel{}, &model.ChannelModel{}, &model.ModelAlias{}, &model.AliasRoute{}))
	ch := model.Channel{Name: "c", Type: "openai", StripThinking: true, Status: 1, Weight: 1}
	require.NoError(t, g.Create(&ch).Error)
	require.NoError(t, g.Create(&model.ChannelModel{ChannelID: ch.ID, Model: "glm-4", Enabled: true}).Error)
	al := model.ModelAlias{Alias: "smart", Status: 1}
	require.NoError(t, g.Create(&al).Error)
	require.NoError(t, g.Create(&model.AliasRoute{AliasID: al.ID, ChannelID: ch.ID, Model: "glm-4", Weight: 1}).Error)

	tb := NewTable()
	require.NoError(t, tb.Build(g))

	direct := tb.Resolve("glm-4")
	require.Len(t, direct, 1)
	require.True(t, direct[0].StripThinking, "direct 候选应带出 strip_thinking")

	aliased := tb.Resolve("smart")
	require.Len(t, aliased, 1)
	require.True(t, aliased[0].StripThinking, "别名候选应带出 strip_thinking")
}

// TestBuildAliasRespectsChannelModelDisabled 验证别名候选不绕过渠道模型级禁用(#29)：
// 别名指向的 (渠道,真实模型) 若对应 ChannelModel 被禁用，该候选必须从别名路由中剔除。
func TestBuildAliasRespectsChannelModelDisabled(t *testing.T) {
	g := dbtest.OpenInMemory(t)
	require.NoError(t, g.AutoMigrate(&model.Channel{}, &model.ChannelModel{}, &model.ModelAlias{}, &model.AliasRoute{}))
	ch := model.Channel{Name: "c", Type: "openai", Status: 1, Weight: 1}
	require.NoError(t, g.Create(&ch).Error)
	cm := model.ChannelModel{ChannelID: ch.ID, Model: "gpt-4o", Enabled: true}
	require.NoError(t, g.Create(&cm).Error)
	require.NoError(t, g.Model(&cm).Update("enabled", false).Error) // 显式禁用（绕过 default:true 零值陷阱，等同生产 DisableModel）
	al := model.ModelAlias{Alias: "smart", Status: 1}
	require.NoError(t, g.Create(&al).Error)
	require.NoError(t, g.Create(&model.AliasRoute{AliasID: al.ID, ChannelID: ch.ID, Model: "gpt-4o", Weight: 1}).Error)

	tb := NewTable()
	require.NoError(t, tb.Build(g))
	require.Empty(t, tb.Resolve("smart"), "别名不得绕过被禁用的渠道模型")
}

// TestBuildAliasSkipsMissingChannelModel 验证别名指向的 ChannelModel 完全不存在(删除)时也剔除。
func TestBuildAliasSkipsMissingChannelModel(t *testing.T) {
	g := dbtest.OpenInMemory(t)
	require.NoError(t, g.AutoMigrate(&model.Channel{}, &model.ChannelModel{}, &model.ModelAlias{}, &model.AliasRoute{}))
	ch := model.Channel{Name: "c", Type: "openai", Status: 1, Weight: 1}
	require.NoError(t, g.Create(&ch).Error)
	// 不建任何 ChannelModel（模型已删除）
	al := model.ModelAlias{Alias: "smart", Status: 1}
	require.NoError(t, g.Create(&al).Error)
	require.NoError(t, g.Create(&model.AliasRoute{AliasID: al.ID, ChannelID: ch.ID, Model: "gpt-4o", Weight: 1}).Error)

	tb := NewTable()
	require.NoError(t, tb.Build(g))
	require.Empty(t, tb.Resolve("smart"), "别名指向已删除的渠道模型应剔除")
}

// TestBuildCarriesDisableStreamUsage 验证 Build 把渠道 disable_stream_usage 带出到 Candidate（direct + 别名）。
func TestBuildCarriesDisableStreamUsage(t *testing.T) {
	g := dbtest.OpenInMemory(t)
	require.NoError(t, g.AutoMigrate(&model.Channel{}, &model.ChannelModel{}, &model.ModelAlias{}, &model.AliasRoute{}))
	ch := model.Channel{Name: "c", Type: "openai", DisableStreamUsage: true, Status: 1, Weight: 1}
	require.NoError(t, g.Create(&ch).Error)
	require.NoError(t, g.Create(&model.ChannelModel{ChannelID: ch.ID, Model: "glm-4", Enabled: true}).Error)
	al := model.ModelAlias{Alias: "smart", Status: 1}
	require.NoError(t, g.Create(&al).Error)
	require.NoError(t, g.Create(&model.AliasRoute{AliasID: al.ID, ChannelID: ch.ID, Model: "glm-4", Weight: 1}).Error)

	tb := NewTable()
	require.NoError(t, tb.Build(g))

	direct := tb.Resolve("glm-4")
	require.Len(t, direct, 1)
	require.True(t, direct[0].DisableStreamUsage, "direct 候选应带出 disable_stream_usage")

	aliased := tb.Resolve("smart")
	require.Len(t, aliased, 1)
	require.True(t, aliased[0].DisableStreamUsage, "别名候选应带出 disable_stream_usage")
}
