package gateway

import (
	"bytes"
	"net/http"
	"strings"
)

// AnthropicAdapter 构造 Anthropic Messages API 请求(POST /v1/messages)。
type AnthropicAdapter struct{}

func (AnthropicAdapter) Protocol() string { return "anthropic" }

func (AnthropicAdapter) BuildRequest(spec HeaderSpec, body []byte) (*http.Request, error) {
	url := strings.TrimRight(spec.BaseURL, "/") + "/v1/messages"
	req, err := http.NewRequest(http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header = BuildOutboundHeaders(spec)
	return req, nil
}
