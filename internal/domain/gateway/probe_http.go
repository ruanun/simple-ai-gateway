package gateway

import "net/http"

// ProbeModels 对上游模型列表端点发一次 GET（用给定 client），返回状态码与响应体。
// 出站头走统一治理（BuildOutboundHeaders：按 protocol/auth_style 分流鉴权 + custom_headers）。
// 供主动探测器（连通/鉴权探测）使用。
//
// 端点取 BuildModelsURLCandidates 的**首条候选**：探测只关心「渠道通不通」，
// 保持一次探测=一个请求的判定语义与出站请求量；需要真的把模型列表捞全时用 FetchModels。
// 旧实现对所有渠道死拼 {baseURL}/models，anthropic 渠道（base_url 不含 /v1）因此恒 404 →
// 熔断永远等不到探测闭合，只能靠真实流量裁决。openai 渠道的首候选仍是 {baseURL}/models，
// 探测行为与改造前一致。
func ProbeModels(cli *http.Client, baseURL, key, protocol, authStyle string, customHeaders map[string]string) (int, []byte, error) {
	candidates, err := BuildModelsURLCandidates(baseURL, protocol)
	if err != nil {
		return 0, nil, err
	}
	return getModels(cli, candidates[0], modelsHeader(protocol, baseURL, key, authStyle, customHeaders))
}
