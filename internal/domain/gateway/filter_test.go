package gateway

import (
	"testing"

	"github.com/ruanun/simple-ai-gateway/internal/router"
	"github.com/stretchr/testify/require"
)

// TestFilterByProtocol 一渠道一协议：同协议候选保留且 Outbound=入站协议；异协议剔除；
// ProviderType 为空视为 openai。
func TestFilterByProtocol(t *testing.T) {
	cands := []router.Candidate{
		{ChannelID: 1, Model: "m", ProviderType: "openai"},    // 纯 openai
		{ChannelID: 2, Model: "m", ProviderType: "anthropic"}, // 纯 anthropic
		{ChannelID: 3, Model: "m", ProviderType: ""},          // 空 type=openai
	}
	ids := func(cs []router.Candidate) []uint64 {
		var r []uint64
		for _, c := range cs {
			r = append(r, c.ChannelID)
		}
		return r
	}

	oa := filterByProtocol(cands, "openai")
	require.Equal(t, []uint64{1, 3}, ids(oa))
	for _, c := range oa {
		require.Equal(t, "openai", c.Outbound)
	}

	an := filterByProtocol(cands, "anthropic")
	require.Equal(t, []uint64{2}, ids(an))
	for _, c := range an {
		require.Equal(t, "anthropic", c.Outbound)
	}
}

// TestFilterWithConvertPartition 透传候选永远排在转换候选之前，两区内保持原定序。
func TestFilterWithConvertPartition(t *testing.T) {
	cands := []router.Candidate{
		{ChannelID: 1, Model: "m", ProviderType: "anthropic", AllowConvert: true}, // 转换候选
		{ChannelID: 2, Model: "m", ProviderType: "openai"},                        // 透传
		{ChannelID: 3, Model: "m", ProviderType: "anthropic", AllowConvert: true}, // 转换候选
		{ChannelID: 4, Model: "m", ProviderType: "openai"},                        // 透传
	}
	out := filterWithConvert(cands, "openai")
	require.Len(t, out, 4)
	require.Equal(t, []uint64{2, 4, 1, 3}, []uint64{out[0].ChannelID, out[1].ChannelID, out[2].ChannelID, out[3].ChannelID})
	// 透传候选：出站=入站，零转换
	require.Equal(t, "openai", out[0].Outbound)
	require.Equal(t, "openai", out[0].Inbound)
	require.False(t, out[0].NeedsConvert)
	// 转换候选：出站=渠道协议，标记 NeedsConvert
	require.Equal(t, "anthropic", out[2].Outbound)
	require.Equal(t, "openai", out[2].Inbound)
	require.True(t, out[2].NeedsConvert)
}

// TestFilterWithConvertRejects 开关关/转换器不支持 → 剔除。
func TestFilterWithConvertRejects(t *testing.T) {
	cands := []router.Candidate{
		{ChannelID: 1, ProviderType: "anthropic", AllowConvert: false}, // 开关关
		{ChannelID: 2, ProviderType: "anthropic", AllowConvert: true},
	}
	out := filterWithConvert(cands, "openai")
	require.Len(t, out, 1)
	require.Equal(t, uint64(2), out[0].ChannelID)

	// messages 入站(anthropic)打到 openai 渠道同理
	out = filterWithConvert([]router.Candidate{
		{ChannelID: 3, ProviderType: "openai", AllowConvert: true},
	}, "anthropic")
	require.Len(t, out, 1)
	require.Equal(t, "openai", out[0].Outbound)
	require.True(t, out[0].NeedsConvert)
}

// TestFilterWithConvertEmptyTypeFallback 空 ProviderType 视为 openai 判断透传。
func TestFilterWithConvertEmptyTypeFallback(t *testing.T) {
	out := filterWithConvert([]router.Candidate{
		{ChannelID: 1, ProviderType: ""},
	}, "openai")
	require.Len(t, out, 1)
	require.False(t, out[0].NeedsConvert)
}

// TestFilterWithConvertPassthroughWins 渠道既支持入站协议又开了 AllowConvert → 走透传，不重复产出转换候选。
func TestFilterWithConvertPassthroughWins(t *testing.T) {
	out := filterWithConvert([]router.Candidate{
		{ChannelID: 1, ProviderType: "openai", AllowConvert: true},
	}, "openai")
	require.Len(t, out, 1)
	require.False(t, out[0].NeedsConvert)
	require.Equal(t, "openai", out[0].Outbound)
}

func TestHasConvertCandidate(t *testing.T) {
	require.False(t, hasConvertCandidate([]router.Candidate{{ChannelID: 1}}))
	require.True(t, hasConvertCandidate([]router.Candidate{{ChannelID: 1}, {ChannelID: 2, NeedsConvert: true}}))
}

// TestHasOpenAICandidateConvert allow_convert 的 anthropic 渠道也计入 openai 可服务清单。
func TestHasOpenAICandidateConvert(t *testing.T) {
	require.False(t, hasOpenAICandidate([]router.Candidate{
		{ChannelID: 1, ProviderType: "anthropic"},
	}))
	require.True(t, hasOpenAICandidate([]router.Candidate{
		{ChannelID: 1, ProviderType: "anthropic", AllowConvert: true},
	}))
}
