package gateway

import "net/http"

// Adapter 把出站请求体构造为对应上游协议的 HTTP 请求。
// 只负责 URL 拼接与协议名；请求头统一委托 BuildOutboundHeaders(spec)。
type Adapter interface {
	BuildRequest(spec HeaderSpec, body []byte) (*http.Request, error)
	Protocol() string
}

func For(providerType string) Adapter {
	switch providerType {
	case "anthropic":
		return AnthropicAdapter{}
	default:
		return OpenAIAdapter{}
	}
}
