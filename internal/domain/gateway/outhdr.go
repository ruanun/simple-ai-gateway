package gateway

import (
	"log/slog"
	"net/http"
	"net/url"
	"strings"
)

// HeaderSpec 描述一次出站请求的头治理输入。
type HeaderSpec struct {
	Protocol      string            // "openai" | "anthropic"
	BaseURL       string            // 上游 base_url，用于 auto 分流判官方域名
	UpstreamKey   string            // 上游凭证（明文）
	AuthStyle     string            // auto|x-api-key|bearer|both（仅 anthropic 有意义）
	CustomHeaders map[string]string // 渠道级静态自定义头
	ClientHeader  http.Header       // 客户端原始请求头（回填 source，可为 nil）
	Stream        bool              // 预留，暂不影响头（Accept-Encoding 恒 identity）
}

// 通用透传白名单（EnsureHeader：客户端有才透传，缺失不补默认，不伪造）。
var passthroughExactHeaders = []string{
	"User-Agent",
	"X-App",
	"X-Client-Request-Id",
	"X-Claude-Code-Session-Id",
	"Accept",
}

// X-Stainless-* 系列（Anthropic/OpenAI SDK 指纹）按前缀整体透传。
const stainlessPrefix = "x-stainless-"

// 自定义头受保护名单：不允许被 custom_headers 覆盖（鉴权/传输框架头 + 恒在定值）。
// 鉴权/传输框架头一旦放开会造成凭证外泄或请求分帧错乱；content-type/accept-encoding
// 是第 1 步写入的恒在定值（后者是 SSE 中继逐行扫描的功能安全不变量），同样不可被覆盖。
var protectedHeaderNames = map[string]bool{
	"authorization":     true,
	"x-api-key":         true,
	"host":              true,
	"content-length":    true,
	"connection":        true,
	"transfer-encoding": true,
	"content-type":      true,
	"accept-encoding":   true,
}

// specFor 用渠道运行参数组装出站 HeaderSpec，供 Dispatch/DispatchRaw 复用。
// provType 由调用方决定：Dispatch 传候选出站协议(c.Outbound)，DispatchRaw 传渠道原生协议。
func specFor(provType string, info ChannelInfo, clientHeader http.Header, stream bool) HeaderSpec {
	return HeaderSpec{
		Protocol:      provType,
		BaseURL:       info.BaseURL,
		UpstreamKey:   info.UpstreamKey,
		AuthStyle:     info.AuthStyle,
		CustomHeaders: info.CustomHeaders,
		ClientHeader:  clientHeader,
		Stream:        stream,
	}
}

// BuildOutboundHeaders 按 spec 产出出站请求头。
// 顺序：定值 → 白名单回填 → 自定义头（护栏）→ 鉴权分流（最后写，压过自定义头）。
func BuildOutboundHeaders(spec HeaderSpec) http.Header {
	h := http.Header{}

	// 1. 定值。Accept-Encoding: identity 保证上游不压缩，Relay 逐行扫 SSE 才安全。
	h.Set("Content-Type", "application/json")
	h.Set("Accept-Encoding", "identity")

	// 2. 白名单回填（客户端有才透传，不伪造）。
	src := spec.ClientHeader
	for _, name := range passthroughExactHeaders {
		ensureHeader(h, src, name, "")
	}
	if src != nil {
		for name, vals := range src {
			if strings.HasPrefix(strings.ToLower(name), stainlessPrefix) && len(vals) > 0 && strings.TrimSpace(vals[0]) != "" {
				h.Set(name, vals[0])
			}
		}
	}
	if spec.Protocol == "anthropic" {
		// anthropic-beta：透传客户端已有值（本项目不注入默认 beta，区别于冒充官方 CLI 的工具）。
		// HTTP 允许同名头多行发送（如 context-1m 与缓存 beta 分两行），
		// 若用 ensureHeader/Get 只取首行会静默丢弃第二行起的 beta 标志，
		// 因此单独特判：合并 Values 里的所有行为逗号分隔单行再 set。
		ensureHeaderMultiValue(h, src, "Anthropic-Beta")
		ensureHeader(h, src, "Anthropic-Version", "2023-06-01")
	}

	// 3. 自定义头合并（护栏：剥离受保护名 + 跳过 CRLF 名/值）。
	for name, val := range spec.CustomHeaders {
		trimmed := strings.TrimSpace(name)
		lower := strings.ToLower(trimmed)
		if lower == "" || protectedHeaderNames[lower] {
			continue
		}
		if strings.ContainsAny(name, "\r\n") || strings.ContainsAny(val, "\r\n") {
			slog.Warn("outhdr: 跳过含 CRLF 的自定义头", "header", name)
			continue
		}
		h.Set(trimmed, val)
	}

	// 4. 鉴权分流（最后写，覆盖任何同名自定义头）。
	applyAuth(h, spec)
	return h
}

// applyAuth 按 protocol + authStyle 写鉴权头。
func applyAuth(h http.Header, spec HeaderSpec) {
	if spec.Protocol != "anthropic" {
		// openai 及其余：恒 Bearer。
		h.Set("Authorization", "Bearer "+spec.UpstreamKey)
		return
	}
	style := spec.AuthStyle
	if style == "" {
		style = "auto"
	}
	if style == "auto" {
		if isAnthropicOfficialHost(spec.BaseURL) {
			style = "x-api-key"
		} else {
			style = "both"
		}
	}
	switch style {
	case "x-api-key":
		h.Del("Authorization")
		h.Set("X-Api-Key", spec.UpstreamKey)
	case "bearer":
		h.Del("X-Api-Key")
		h.Set("Authorization", "Bearer "+spec.UpstreamKey)
	default: // "both"
		h.Set("Authorization", "Bearer "+spec.UpstreamKey)
		h.Set("X-Api-Key", spec.UpstreamKey)
	}
}

// anthropicOfficialHosts auto 鉴权分流的官方域名单：命中→仅 x-api-key，未命中→both 双发。
// 缺省仅 api.anthropic.com；经 SetAnthropicOfficialHosts 由配置覆盖（如自建 anthropic 兼容网关
// 想避免 both 双发，可把其域名加进 security.anthropic_official_hosts）。
var anthropicOfficialHosts = []string{"api.anthropic.com"}

// SetAnthropicOfficialHosts 覆盖官方域名单（装配期调用一次；空切片不覆盖，保内置默认）。
func SetAnthropicOfficialHosts(hosts []string) {
	if len(hosts) > 0 {
		anthropicOfficialHosts = hosts
	}
}

// isAnthropicOfficialHost 域名单精确匹配（大小写不敏感）。
func isAnthropicOfficialHost(baseURL string) bool {
	u, err := url.Parse(baseURL)
	if err != nil {
		return false
	}
	for _, h := range anthropicOfficialHosts {
		if strings.EqualFold(u.Hostname(), h) {
			return true
		}
	}
	return false
}

// ensureHeaderMultiValue：source 中该 key 若存在（可能多行）→ 合并所有非空行为逗号分隔单行
// set 进 target；source 无值时不动 target（不伪造）。单行 "a,b" 经 Values 只返回一个元素，
// join 后仍是 "a,b"；多行 ["a","b"] join 后变 "a,b"，两种情形结果一致、语义正确。
func ensureHeaderMultiValue(target, source http.Header, key string) {
	if source == nil {
		return
	}
	vals := source.Values(key)
	if len(vals) == 0 {
		return
	}
	merged := strings.Join(vals, ",")
	if strings.TrimSpace(merged) == "" {
		return
	}
	target.Set(key, merged)
}

// ensureHeader：source 有非空值 → set；否则 target 已有 → 保留；否则有 def → set def。
func ensureHeader(target, source http.Header, key, def string) {
	if source != nil {
		if v := strings.TrimSpace(source.Get(key)); v != "" {
			target.Set(key, v)
			return
		}
	}
	if strings.TrimSpace(target.Get(key)) != "" {
		return
	}
	if def != "" {
		target.Set(key, def)
	}
}
