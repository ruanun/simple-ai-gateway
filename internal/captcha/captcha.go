// Package captcha 门户注册的人机验证：图形验证码（本地生成）与 Cloudflare Turnstile（远端校验）。
// 用哪种由系统设置 captcha_type 决定，本包只提供两种能力，不读设置。
package captcha

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/mojocn/base64Captcha"
	"github.com/redis/go-redis/v9"
)

// 验证方式取值（与 SystemSetting.CaptchaType 对应，空串=不启用）。
const (
	TypeNone      = ""
	TypeImage     = "image"
	TypeTurnstile = "turnstile"
)

// ValidType 校验设置里的验证方式取值。
func ValidType(t string) bool { return t == TypeNone || t == TypeImage || t == TypeTurnstile }

const (
	// imageTTL 图形验证码有效期：够用户看图输入，又不至于让答案长期可用。
	imageTTL = 5 * time.Minute
	// memoryMaxEntries 内存存储容量上限：生成端点虽有按 IP 限流，分布式来源仍可能刷满，
	// 达上限后先删过期、仍超则任意淘汰——被淘汰者只需刷新验证码。
	memoryMaxEntries = 10000
	// DefaultTurnstileVerifyURL Cloudflare Turnstile 服务端校验地址。
	DefaultTurnstileVerifyURL = "https://challenges.cloudflare.com/turnstile/v0/siteverify"
)

// Service 人机验证服务。
type Service struct {
	image *base64Captcha.Captcha
	store base64Captcha.Store
	// TurnstileURL 服务端校验地址，测试可替换为本地桩。
	TurnstileURL string
	httpClient   *http.Client
}

// New 构造服务；redisCli 非 nil 时验证码答案存 Redis（多实例共享），否则存进程内存。
func New(redisCli *redis.Client) *Service {
	var store base64Captcha.Store
	if redisCli != nil {
		store = &redisStore{cli: redisCli}
	} else {
		store = newMemoryStore()
	}
	// 4 位数字、适度扭曲与噪点：挡脚本批量注册足够，不追求对抗专业打码平台。
	driver := base64Captcha.NewDriverDigit(60, 130, 4, 0.5, 30)
	return &Service{
		image:        base64Captcha.NewCaptcha(driver, store),
		store:        store,
		TurnstileURL: DefaultTurnstileVerifyURL,
		httpClient:   &http.Client{Timeout: 10 * time.Second},
	}
}

// NewImage 生成一张图形验证码，返回 id 与 data URI 形式的 PNG。
func (s *Service) NewImage() (id, image string, err error) {
	id, image, _, err = s.image.Generate()
	return id, image, err
}

// VerifyImage 校验图形验证码。无论对错都作废该 id，防止对同一张图反复试答案。
func (s *Service) VerifyImage(id, answer string) bool {
	if id == "" || answer == "" {
		return false
	}
	return s.store.Verify(id, strings.TrimSpace(answer), true)
}

// TurnstileResult Cloudflare siteverify 的原始判定。
type TurnstileResult struct {
	Success    bool     `json:"success"`
	ErrorCodes []string `json:"error-codes"`
	Hostname   string   `json:"hostname"` // 组件渲染所在域名，便于核对域名白名单
}

// SecretInvalid 是否为密钥配置错误（而非用户没通过验证）。
func (r *TurnstileResult) SecretInvalid() bool {
	for _, code := range r.ErrorCodes {
		if code == "missing-input-secret" || code == "invalid-input-secret" {
			return true
		}
	}
	return false
}

// CheckTurnstile 调 siteverify 并返回完整判定，供管理端「测试配置」展示错误码。
// 返回 error 仅表示校验服务不可达/响应异常这类系统性失败。
func (s *Service) CheckTurnstile(ctx context.Context, secret, token, remoteIP string) (*TurnstileResult, error) {
	form := url.Values{"secret": {secret}, "response": {token}}
	if remoteIP != "" {
		form.Set("remoteip", remoteIP)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, s.TurnstileURL, strings.NewReader(form.Encode()))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := s.httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	// Cloudflare 对 invalid-input-secret 等请求类错误回 HTTP 400 且带 JSON 判定体，
	// 故不能按状态码一刀切：能解析出判定（200，或非 200 但带错误码）就交给调用方区分。
	var out TurnstileResult
	decErr := json.NewDecoder(io.LimitReader(resp.Body, 64<<10)).Decode(&out)
	if decErr == nil && (resp.StatusCode == http.StatusOK || len(out.ErrorCodes) > 0) {
		return &out, nil
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("turnstile siteverify 返回 HTTP %d", resp.StatusCode)
	}
	return nil, fmt.Errorf("解析 turnstile 响应失败: %w", decErr)
}

// VerifyTurnstile 向 Cloudflare 校验前端拿到的 token。
// 返回 error 表示校验服务不可达、密钥无效等系统性失败（调用方应 fail-closed，拒绝注册）。
func (s *Service) VerifyTurnstile(ctx context.Context, secret, token, remoteIP string) (bool, error) {
	if token == "" {
		return false, nil
	}
	r, err := s.CheckTurnstile(ctx, secret, token, remoteIP)
	if err != nil {
		return false, err
	}
	// 密钥配错属于配置问题而非用户没通过验证，单独报出便于管理员排查。
	if r.SecretInvalid() {
		return false, fmt.Errorf("turnstile 密钥无效: %v", r.ErrorCodes)
	}
	return r.Success, nil
}

// ---- 存储 ----

// memoryStore 进程内验证码存储，带 TTL 与容量上限。
type memoryStore struct {
	mu sync.Mutex
	m  map[string]memoryEntry
}

type memoryEntry struct {
	answer string
	exp    time.Time
}

func newMemoryStore() *memoryStore { return &memoryStore{m: map[string]memoryEntry{}} }

func (s *memoryStore) Set(id, value string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Now()
	if len(s.m) >= memoryMaxEntries {
		for k, e := range s.m {
			if now.After(e.exp) {
				delete(s.m, k)
			}
		}
		for k := range s.m { // 仍超限：借 map 随机遍历任意删到 3/4
			if len(s.m) < memoryMaxEntries*3/4 {
				break
			}
			delete(s.m, k)
		}
	}
	s.m[id] = memoryEntry{answer: value, exp: now.Add(imageTTL)}
	return nil
}

func (s *memoryStore) Get(id string, clear bool) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	e, ok := s.m[id]
	if !ok {
		return ""
	}
	if clear || time.Now().After(e.exp) {
		delete(s.m, id)
	}
	if time.Now().After(e.exp) {
		return ""
	}
	return e.answer
}

func (s *memoryStore) Verify(id, answer string, clear bool) bool {
	v := s.Get(id, clear)
	return v != "" && v == answer
}

// redisStore Redis 验证码存储，多实例共享；读取即删在事务内完成，保证一次性。
type redisStore struct{ cli *redis.Client }

func redisCaptchaKey(id string) string { return "captcha:" + id }

func (s *redisStore) Set(id, value string) error {
	return s.cli.Set(context.Background(), redisCaptchaKey(id), value, imageTTL).Err()
}

func (s *redisStore) Get(id string, clear bool) string {
	ctx := context.Background()
	var (
		v   string
		err error
	)
	if clear {
		// MULTI 内 GET + DEL 而非 GETDEL：后者要求 Redis 6.2+，这里兼容更老的实例。
		var get *redis.StringCmd
		_, err = s.cli.TxPipelined(ctx, func(p redis.Pipeliner) error {
			get = p.Get(ctx, redisCaptchaKey(id))
			p.Del(ctx, redisCaptchaKey(id))
			return nil
		})
		if err == nil {
			v, err = get.Result()
		}
	} else {
		v, err = s.cli.Get(ctx, redisCaptchaKey(id)).Result()
	}
	if err != nil {
		return ""
	}
	return v
}

func (s *redisStore) Verify(id, answer string, clear bool) bool {
	v := s.Get(id, clear)
	return v != "" && v == answer
}
