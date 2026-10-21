package session

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"hash/fnv"
	"regexp"
	"strconv"
	"strings"
)

// legacyUserIDRegex 匹配 Claude Code legacy 格式:
// user_{deviceID}_account_{accountUUID}_session_{sessionUUID}
var legacyUserIDRegex = regexp.MustCompile(`^user_([0-9a-f]+)_account_([^_]*)_session_([0-9a-f-]+)$`)

// ExtractAnthropicSessionID 从 Anthropic Messages 请求体的 metadata.user_id 提取 session_id。
// Claude Code 自动传此字段，无需客户端额外配置。未找到返回空串。
func ExtractAnthropicSessionID(body []byte) string {
	var req struct {
		Metadata *struct {
			UserID string `json:"user_id"`
		} `json:"metadata"`
	}
	if json.Unmarshal(body, &req) != nil || req.Metadata == nil || req.Metadata.UserID == "" {
		return ""
	}
	raw := strings.TrimSpace(req.Metadata.UserID)
	// JSON 格式: {"device_id":"...","session_id":"..."}
	if len(raw) > 0 && raw[0] == '{' {
		var j struct {
			SessionID string `json:"session_id"`
		}
		if json.Unmarshal([]byte(raw), &j) == nil && j.SessionID != "" {
			return j.SessionID
		}
		return ""
	}
	// Legacy 格式: user_{deviceID}_account_{...}_session_{sessionUUID}
	matches := legacyUserIDRegex.FindStringSubmatch(raw)
	if len(matches) >= 4 {
		return matches[3]
	}
	return ""
}

// ExtractResponsesSessionID 从 OpenAI Responses 请求体提取 session key：
// 优先客户端显式传递的 prompt_cache_key（更稳定，跨 IP/UA 不变），否则复用
// previous_response_id（Codex CLI 自动传此字段）。未找到返回空串。
func ExtractResponsesSessionID(body []byte) string {
	if key := extractPromptCacheKey(body); key != "" {
		return stableSessionKey("prompt:" + key)
	}
	var req struct {
		PreviousResponseID string `json:"previous_response_id"`
	}
	if json.Unmarshal(body, &req) != nil {
		return ""
	}
	return strings.TrimSpace(req.PreviousResponseID)
}

// ExtractChatSessionID 为 chat completions 提取 session key：
// 优先 prompt_cache_key；没有则回退到 clientIP+UA+keyID+请求内容指纹。
func ExtractChatSessionID(clientIP, userAgent string, apiKeyID uint64, body []byte) string {
	if key := extractPromptCacheKey(body); key != "" {
		return stableSessionKey("prompt:" + key)
	}
	return GenerateChatSessionHash(clientIP, userAgent, apiKeyID, body)
}

// extractPromptCacheKey 读取 OpenAI Chat/Responses 的 prompt_cache_key。
// 该字段是客户端为提示缓存显式指定的亲和键，比 IP/UA 指纹稳定。
func extractPromptCacheKey(body []byte) string {
	var req struct {
		PromptCacheKey string `json:"prompt_cache_key"`
	}
	if json.Unmarshal(body, &req) != nil {
		return ""
	}
	return strings.TrimSpace(req.PromptCacheKey)
}

// stableSessionKey 对客户端原始 key 做定长哈希，避免把任意长度的外部字符串
// 直接写进内存/Redis 的 sticky 存储；前缀区分来源，便于排查。
func stableSessionKey(raw string) string {
	sum := sha256.Sum256([]byte(raw))
	return "saig:" + hex.EncodeToString(sum[:16])
}

// GenerateChatSessionHash 对 chat completions 请求生成 session hash（fallback 方案）。
// 用 clientIP + UA + apiKeyID + model + system + tools + 首条非空消息内容做 FNV64a hash。
// 纳入 model/system/tools 是为了让同一会话在 IP/UA 变化时仍能命中、不同配置的请求
// 又不会被错误粘连到同一账号。
func GenerateChatSessionHash(clientIP, userAgent string, apiKeyID uint64, body []byte) string {
	var req struct {
		Model    string          `json:"model"`
		System   json.RawMessage `json:"system"`
		Tools    json.RawMessage `json:"tools"`
		Messages []struct {
			Role    string          `json:"role"`
			Content json.RawMessage `json:"content"`
		} `json:"messages"`
	}
	if json.Unmarshal(body, &req) != nil || len(req.Messages) == 0 {
		return ""
	}
	// 取首条非空消息内容（system 或 user）作为会话指纹。content 可能是字符串，
	// 也可能是多模态数组，统一按原始 JSON 参与哈希。
	firstContent := ""
	for _, m := range req.Messages {
		if raw := strings.TrimSpace(string(m.Content)); raw != "" && raw != "null" && raw != `""` && raw != "[]" {
			firstContent = raw
			break
		}
	}
	if firstContent == "" {
		return ""
	}
	h := fnv.New64a()
	h.Write([]byte(clientIP))
	h.Write([]byte(":"))
	h.Write([]byte(userAgent))
	h.Write([]byte(":"))
	h.Write([]byte(strconv.FormatUint(apiKeyID, 10)))
	h.Write([]byte("|"))
	h.Write([]byte(req.Model))
	h.Write([]byte("|"))
	h.Write([]byte(firstContent))
	h.Write([]byte("|"))
	h.Write(req.System)
	h.Write([]byte("|"))
	h.Write(req.Tools)
	return strconv.FormatUint(h.Sum64(), 36)
}
