// Channel 域业务层：渠道 CRUD、渠道模型管理，以及协议/出站头等配置校验。
// 校验函数与哨兵错误在此导出，admin 层只做 HTTP 绑定与状态码/文案映射。
package channel

import (
	"encoding/json"
	"errors"
	"log/slog"
	"strings"

	"github.com/ruanun/simple-ai-gateway/internal/crypto"
	"github.com/ruanun/simple-ai-gateway/internal/model"
	"golang.org/x/net/http/httpguts"
)

// 业务校验/加密失败的哨兵错误，供上层按 errors.Is 映射状态码与文案。
var (
	// ErrInvalidProtocol 渠道协议不在白名单（openai/anthropic）。
	ErrInvalidProtocol = errors.New("channel: type 仅支持 openai/anthropic")
	// ErrInvalidCustomHeaders custom_headers 不是合法的 JSON 对象（或含非法 header 名）。
	ErrInvalidCustomHeaders = errors.New("channel: custom_headers 需为 JSON 对象")
	// ErrEncryptKey 上游 api_key 加密失败。
	ErrEncryptKey = errors.New("channel: encrypt channel key failed")
)

// IsValidProtocol 渠道协议(type)白名单：仅 openai/anthropic，防止非法值导致渠道静默不可路由。
func IsValidProtocol(p string) bool {
	return p == "openai" || p == "anthropic"
}

// NormalizeAuthStyle 归一出站鉴权风格：空或非法回落 auto。
func NormalizeAuthStyle(s string) string {
	switch s {
	case "auto", "x-api-key", "bearer", "both":
		return s
	default:
		return "auto"
	}
}

// IsValidCustomHeadersJSON 校验 custom_headers 为 JSON 对象且每个 key 是合法 HTTP header 名。
// 拒 JSON null（Unmarshal 成 nil map）与含非法字符（空格等）的 header 名——后者若放行，
// 会在出站请求时被 http.Client 拒绝、导致该渠道每次 attempt 都失败，把配置期错误推迟到请求期。
func IsValidCustomHeadersJSON(s string) bool {
	var m map[string]string
	if json.Unmarshal([]byte(s), &m) != nil || m == nil {
		return false
	}
	for name := range m {
		if !httpguts.ValidHeaderFieldName(name) {
			return false
		}
	}
	return true
}

// ParseCustomHeaders 渠道自定义头「JSON→map」的唯一运行期解析入口（派发 selector / 健康探测 /
// admin 拉上游模型三处共用）。统一语义：空白串返回空 map；解析失败 warn 日志 + fail-open 返回空 map，
// 不阻断请求——配置期已由 IsValidCustomHeadersJSON 把关，此处只兜运行期脏数据。
func ParseCustomHeaders(channelID uint64, raw string) map[string]string {
	customHdr := map[string]string{}
	if strings.TrimSpace(raw) == "" {
		return customHdr
	}
	if err := json.Unmarshal([]byte(raw), &customHdr); err != nil {
		slog.Warn("channel: custom_headers 解析失败，忽略", "channel", channelID, "err", err)
		return map[string]string{}
	}
	return customHdr
}

type Service struct {
	repo   *Repo
	aesKey string // 上游凭证 AES 密钥（默认账号 api_key 加密用）
}

func NewService(repo *Repo, aesKey string) *Service { return &Service{repo: repo, aesKey: aesKey} }

// CreateInput 建渠道入参，与 admin 请求体字段一一对应。
type CreateInput struct {
	Type               string
	Name               string
	BaseURL            string
	APIKey             string // 明文上游 key，Create 内加密后写入默认账号
	Timeout            int
	Priority           int
	Weight             int
	MaxRetries         int
	RPMLimit           int
	RateMultiplier     float64
	Remark             string
	AllowConvert       bool
	StripThinking      bool
	DisableStreamUsage bool
	ErrorPassthrough   bool
	AuthStyle          string
	CustomHeaders      string
}

// Create 校验 → 加密上游 key → 事务创建「渠道 + 默认账号」。
// 校验与兜底顺序与原 admin 实现逐位一致：auth_style 归一 → custom_headers 校验 →
// api_key 加密 → type 空值兜底 openai → 协议白名单 → timeout/weight/rate_multiplier 兜底。
func (s *Service) Create(in CreateInput) (*model.Channel, error) {
	// 出站头字段校验：auth_style 白名单（空/非法回落 auto）；custom_headers 非空须为 JSON 对象。
	authStyle := NormalizeAuthStyle(in.AuthStyle)
	if in.CustomHeaders != "" && !IsValidCustomHeadersJSON(in.CustomHeaders) {
		return nil, ErrInvalidCustomHeaders
	}
	enc, err := crypto.Encrypt(s.aesKey, in.APIKey)
	if err != nil {
		return nil, ErrEncryptKey
	}
	ch := model.Channel{Type: in.Type, Name: in.Name, BaseURL: in.BaseURL,
		Status: 1, Timeout: in.Timeout,
		Priority: in.Priority, Weight: in.Weight, MaxRetries: in.MaxRetries, RPMLimit: in.RPMLimit,
		RateMultiplier: in.RateMultiplier,
		Remark:         in.Remark,
		AuthStyle:      authStyle, CustomHeaders: in.CustomHeaders}
	if ch.Type == "" {
		ch.Type = "openai"
	}
	// type = 渠道协议（一渠道一协议，双协议上游建两条渠道），须过协议白名单。
	if !IsValidProtocol(ch.Type) {
		return nil, ErrInvalidProtocol
	}
	if ch.Timeout == 0 {
		ch.Timeout = 60
	}
	if ch.Weight < 1 {
		ch.Weight = 1
	}
	if ch.RateMultiplier <= 0 {
		ch.RateMultiplier = 1
	}
	ch.AllowConvert = in.AllowConvert
	ch.StripThinking = in.StripThinking
	ch.DisableStreamUsage = in.DisableStreamUsage
	ch.ErrorPassthrough = in.ErrorPassthrough
	// 默认账号：凭证下沉到账号（密钥池模型），channel_id 由事务内回填。
	acc := model.Account{Name: "默认账号", APIKeyEnc: enc, Weight: 1, Priority: 0, Status: 1}
	if err := s.repo.CreateWithAccount(&ch, &acc); err != nil {
		return nil, err
	}
	return &ch, nil
}

// UpdateInput 渠道可更新字段（指针=未传则不改），与 admin 请求体一一对应。
type UpdateInput struct {
	Type               *string
	Name               *string
	BaseURL            *string
	Priority           *int
	Weight             *int
	Timeout            *int
	MaxRetries         *int
	RPMLimit           *int
	RateMultiplier     *float64
	Status             *int8
	Remark             *string
	AllowConvert       *bool
	StripThinking      *bool
	DisableStreamUsage *bool
	ErrorPassthrough   *bool
	AuthStyle          *string
	CustomHeaders      *string
}

// Update 按传入的非 nil 字段构建局部更新并落库。校验/兜底与原 admin 实现一致：
// type 过协议白名单、weight<1 兜底 1、rate_multiplier<=0 兜底 1、auth_style 归一、
// custom_headers 非空须合法。changed=false 表示没有任何字段需要更新（未触库）。
func (s *Service) Update(id uint64, in UpdateInput) (changed bool, err error) {
	fields := map[string]any{}
	if in.Type != nil {
		// type = 渠道协议，须过协议白名单。
		if !IsValidProtocol(*in.Type) {
			return false, ErrInvalidProtocol
		}
		fields["type"] = *in.Type
	}
	if in.Name != nil {
		fields["name"] = *in.Name
	}
	if in.BaseURL != nil {
		fields["base_url"] = *in.BaseURL
	}
	if in.Priority != nil {
		fields["priority"] = *in.Priority
	}
	if in.Weight != nil {
		w := *in.Weight
		if w < 1 {
			w = 1
		}
		fields["weight"] = w
	}
	if in.Timeout != nil {
		fields["timeout"] = *in.Timeout
	}
	if in.MaxRetries != nil {
		fields["max_retries"] = *in.MaxRetries
	}
	if in.RPMLimit != nil {
		fields["rpm_limit"] = *in.RPMLimit
	}
	if in.RateMultiplier != nil {
		m := *in.RateMultiplier
		if m <= 0 {
			m = 1
		}
		fields["rate_multiplier"] = m
	}
	if in.Status != nil {
		fields["status"] = *in.Status
	}
	if in.Remark != nil {
		fields["remark"] = *in.Remark
	}
	if in.AllowConvert != nil {
		fields["allow_convert"] = *in.AllowConvert
	}
	if in.StripThinking != nil {
		fields["strip_thinking"] = *in.StripThinking
	}
	if in.DisableStreamUsage != nil {
		fields["disable_stream_usage"] = *in.DisableStreamUsage
	}
	if in.ErrorPassthrough != nil {
		fields["error_passthrough"] = *in.ErrorPassthrough
	}
	if in.AuthStyle != nil {
		fields["auth_style"] = NormalizeAuthStyle(*in.AuthStyle)
	}
	if in.CustomHeaders != nil {
		if *in.CustomHeaders != "" && !IsValidCustomHeadersJSON(*in.CustomHeaders) {
			return false, ErrInvalidCustomHeaders
		}
		fields["custom_headers"] = *in.CustomHeaders
	}
	if len(fields) == 0 {
		return false, nil
	}
	if err := s.repo.UpdateFields(id, fields); err != nil {
		return true, err
	}
	return true, nil
}

func (s *Service) List() ([]model.Channel, error) { return s.repo.List() }

func (s *Service) Get(id uint64) (*model.Channel, error) { return s.repo.Get(id) }

func (s *Service) Delete(id uint64) error { return s.repo.Delete(id) }

// ---- ChannelModel ----

// ListModels 渠道模型列表；channelID 为空串时不过滤。
func (s *Service) ListModels(channelID string) ([]model.ChannelModel, error) {
	return s.repo.ListModels(channelID)
}

func (s *Service) CreateModel(m *model.ChannelModel) error { return s.repo.CreateModel(m) }

func (s *Service) DeleteModel(id uint64) error { return s.repo.DeleteModel(id) }

// SetModelStatus 启停渠道模型。
func (s *Service) SetModelStatus(id uint64, enabled bool) error {
	return s.repo.SetModelStatus(id, enabled)
}
