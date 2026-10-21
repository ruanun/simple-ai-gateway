package gateway

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// TestVendorOf 验证按模型名前缀推断归属厂商（/v1/models 的 owned_by 分组用）。
func TestVendorOf(t *testing.T) {
	cases := map[string]string{
		"gpt-4o":              "openai",
		"o3-mini":             "openai",
		"chatgpt-4o-latest":   "openai",
		"claude-3-5-sonnet":   "anthropic",
		"gemini-2.5-pro":      "google",
		"glm-4.6":             "zhipu",
		"deepseek-chat":       "deepseek",
		"qwen-max":            "alibaba",
		"kimi-k2":             "moonshot",
		"moonshot-v1-8k":      "moonshot",
		"doubao-pro-32k":      "bytedance",
		"grok-4":              "xai",
		"llama-3.3-70b":       "meta",
		"mistral-large":       "mistral",
		"GLM-4.6":             "zhipu",             // 大小写不敏感
		" gpt-4o ":            "openai",            // 前后空白容错
		"my-private-model-v2": "simple-ai-gateway", // 未识别回退网关名
	}
	for model, want := range cases {
		require.Equal(t, want, vendorOf(model), "model=%q", model)
	}
}
