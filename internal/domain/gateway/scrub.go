package gateway

import (
	"bytes"
	"context"
	"net/http"
	"regexp"
	"unicode/utf8"
)

// minRedactSecretLen 精确脱敏的最短凭证长度。太短的串在正文里遍地都是，
// 全量替换会把内容打得面目全非，反而看不出问题。
const minRedactSecretLen = 4

var redactedMark = []byte("[REDACTED]")

// secretPatterns 匹配上游错误响应体里可能出现的凭证片段。
// 典型泄露：OpenAI 401 会回显 "Incorrect API key provided: sk-abc***xyz"（含掩码星号），
// 上游若把 Authorization 头原样带进错误详情也会命中 Bearer/Basic。
var secretPatterns = []*regexp.Regexp{
	// sk- 前缀 key（含 sk-ant-、掩码 sk-abc***xyz）；停在空白/引号/反斜杠处，避免吃掉整段 JSON。
	regexp.MustCompile(`sk-[^\s"'\\]{2,}`),
	// HTTP Bearer / Basic 授权凭证。
	regexp.MustCompile(`(?i)\b(?:bearer|basic)\s+[A-Za-z0-9._~+/=-]{6,}`),
}

// scrubSecrets 把响应体里疑似凭证的片段替换为 [REDACTED]，返回新切片（不改动入参）。
// known 是调用方已掌握的凭证明文（渠道 API key、自定义鉴权头的值等）：上游常在错误里回显它们，
// 而这些 key 未必是 sk- 前缀，正则兜不住，故先按实际值精确替换，再跑通用正则兜掩码形式。
func scrubSecrets(b []byte, known ...string) []byte {
	for _, s := range known {
		if utf8.RuneCountInString(s) >= minRedactSecretLen {
			b = bytes.ReplaceAll(b, []byte(s), redactedMark)
		}
	}
	for _, re := range secretPatterns {
		b = re.ReplaceAll(b, redactedMark)
	}
	return b
}

// scrubDetail 渠道开启错误透传时，对将要回给客户端的失败详情按本次上游凭证精确脱敏。
// 未开启透传时详情只进日志/审计（管理面取证），不做额外处理。
func scrubDetail(info ChannelInfo, detail string) string {
	if !info.ErrorPassthrough || detail == "" {
		return detail
	}
	known := make([]string, 0, 1+len(info.CustomHeaders))
	if info.UpstreamKey != "" {
		known = append(known, info.UpstreamKey)
	}
	for _, v := range info.CustomHeaders {
		known = append(known, v)
	}
	return string(scrubSecrets([]byte(detail), known...))
}

// scrubIfError 仅当上游为错误状态（>=400）时脱敏响应体，正常响应原样返回。
// 返回值仅用于回写客户端；审计/日志应保留原始 body（管理面取证需要完整上游报文）。
func scrubIfError(resp *http.Response, b []byte) []byte {
	if resp.StatusCode >= 400 {
		return scrubResp(resp, b)
	}
	return b
}

type knownSecretsKey struct{}

// withSpecSecrets 把本次出站实际使用的凭证（上游 key、渠道自定义头的值）挂到上游请求的 ctx 上。
// 中转站/Azure/Gemini 等的 key 未必是 sk- 前缀，仅靠通用正则兜不住上游在错误里回显的明文；
// 挂在请求上，回写错误时经 resp.Request 取回按原文精确替换，无需把凭证层层传进各 Relay。
func withSpecSecrets(ctx context.Context, spec HeaderSpec) context.Context {
	known := make([]string, 0, 1+len(spec.CustomHeaders))
	if spec.UpstreamKey != "" {
		known = append(known, spec.UpstreamKey)
	}
	for _, v := range spec.CustomHeaders {
		known = append(known, v)
	}
	return context.WithValue(ctx, knownSecretsKey{}, known)
}

// scrubResp 脱敏上游响应体的客户端可见副本：本次请求实际使用的凭证原文 + 通用正则。
func scrubResp(resp *http.Response, b []byte) []byte {
	var known []string
	if resp != nil && resp.Request != nil {
		known, _ = resp.Request.Context().Value(knownSecretsKey{}).([]string)
	}
	return scrubSecrets(b, known...)
}
