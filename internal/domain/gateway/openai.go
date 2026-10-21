package gateway

import (
	"bytes"
	"net/http"
	"strings"
)

type OpenAIAdapter struct{}

func (OpenAIAdapter) Protocol() string { return "openai" }

func (OpenAIAdapter) BuildRequest(spec HeaderSpec, body []byte) (*http.Request, error) {
	url := strings.TrimRight(spec.BaseURL, "/") + "/chat/completions"
	req, err := http.NewRequest(http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header = BuildOutboundHeaders(spec)
	return req, nil
}

// buildPathRequest 构造对上游 baseURL+path 的 POST 请求，请求头委托 outhdr。
// 供 DispatchRaw 按端点路径（/embeddings、/images/generations、/chat/completions、/responses）派发。
func buildPathRequest(spec HeaderSpec, path string, body []byte) (*http.Request, error) {
	url := strings.TrimRight(spec.BaseURL, "/") + path
	req, err := http.NewRequest(http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header = BuildOutboundHeaders(spec)
	return req, nil
}
