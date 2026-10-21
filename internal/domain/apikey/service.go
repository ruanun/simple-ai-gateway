package apikey

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"time"

	"github.com/ruanun/simple-ai-gateway/internal/model"
	"golang.org/x/sync/singleflight"
	"gorm.io/gorm"
)

var ErrUnauthorized = errors.New("unauthorized api key")

type Service struct {
	repo   *Repo
	prefix string

	// 鉴权热路径缓存（默认 5s/1 万条）；SetAuthCache(0,0) 可关闭（测试/排障用）。
	auth       *authCache
	authSF     singleflight.Group
	authNotify func() // 本地失效后通知其他实例（Redis Pub/Sub，可为 nil）
}

func NewService(repo *Repo, prefix string) *Service {
	return &Service{repo: repo, prefix: prefix, auth: newAuthCache(5*time.Second, 10000)}
}

// SetAuthCache 调整鉴权缓存；ttl<=0 关闭。
func (s *Service) SetAuthCache(ttl time.Duration, max int) {
	if s == nil {
		return
	}
	s.auth = newAuthCache(ttl, max)
}

// SetAuthCacheNotify 设置本地失效后的跨实例通知回调（装配期单线程调用）。
func (s *Service) SetAuthCacheNotify(fn func()) { s.authNotify = fn }

// InvalidateAllAuthCache 清空本地鉴权缓存，供 Redis Pub/Sub 订阅端调用。
func (s *Service) InvalidateAllAuthCache() {
	if s != nil {
		s.auth.clear()
	}
}

// InvalidateAuthCacheByID 按 key id 失效本地缓存并通知其他实例。
// 行已被删除时无法取 KeyHash，退化为清空全部（key 增删是低频管理操作）。
func (s *Service) InvalidateAuthCacheByID(id uint64) {
	if s == nil || s.auth == nil || id == 0 {
		return
	}
	rec, err := s.repo.Get(id)
	if err == nil && rec != nil {
		s.auth.delete(rec.KeyHash)
	} else {
		s.auth.clear()
	}
	if s.authNotify != nil {
		s.authNotify()
	}
}

func hashKey(plain string) string {
	sum := sha256.Sum256([]byte(plain))
	return hex.EncodeToString(sum[:])
}

func randToken(n int) (string, error) {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

// Prepare 生成明文 key 和填充好的 APIKey 记录（不入库），供调用方在事务中自行创建。
func (s *Service) Prepare(name string, expireAt *time.Time) (string, *model.APIKey, error) {
	token, err := randToken(24)
	if err != nil {
		return "", nil, err
	}
	plain := s.prefix + "-" + token
	rec := &model.APIKey{
		Name:     name,
		KeyHash:  hashKey(plain),
		Key:      plain, // P11: 明文可回查
		Prefix:   plain[:min(len(plain), len(s.prefix)+1+8)],
		Status:   1,
		ExpireAt: expireAt,
	}
	return plain, rec, nil
}

// Generate 创建一把 key，返回明文与入库记录（明文同时落 Key 列，可随时回查）。
func (s *Service) Generate(name string, expireAt *time.Time) (string, *model.APIKey, error) {
	plain, rec, err := s.Prepare(name, expireAt)
	if err != nil {
		return "", nil, err
	}
	if err := s.repo.Create(rec); err != nil {
		return "", nil, err
	}
	return plain, rec, nil
}

// GenerateForApp 一次 Create 原子生成带 App 归属的 key（rpm 取值语义见 model.APIKey.RPM），
// 供 admin 与 portal 建 key 共用，避免"先建无主 key 再补绑 app_id"两步走。
func (s *Service) GenerateForApp(name string, appID uint64, rpm int, expireAt *time.Time) (string, *model.APIKey, error) {
	plain, rec, err := s.Prepare(name, expireAt)
	if err != nil {
		return "", nil, err
	}
	rec.AppID = appID
	rec.RPM = rpm
	if err := s.repo.Create(rec); err != nil {
		return "", nil, err
	}
	return plain, rec, nil
}

// Authenticate 校验明文 key：存在、启用、未过期。
// 无日成本上限的 key 走 5s 进程缓存；有日成本上限的 key 每次回表（日限额必须实时）。
func (s *Service) Authenticate(plain string) (*model.APIKey, error) {
	hash := hashKey(plain)
	if s.auth != nil {
		if rec, hit, negative := s.auth.get(hash); hit {
			if negative {
				return nil, ErrUnauthorized
			}
			if rec.DailyCostLimit <= 0 {
				return rec, nil
			}
			// 有日限额：继续回表取最新 daily_cost_used/date。
		} else {
			v, err, _ := s.authSF.Do(hash, func() (any, error) {
				rec, err := s.findByHashFresh(hash)
				if err != nil {
					return nil, err
				}
				// 日限额 key 不缓存正结果，避免陈旧用量绕过限额。
				if rec.DailyCostLimit <= 0 {
					s.auth.set(hash, rec)
				}
				return rec, nil
			})
			if err != nil {
				return nil, err
			}
			return v.(*model.APIKey), nil
		}
	}
	return s.findByHashFresh(hash)
}

// findByHashFresh 回表校验并写负缓存；正缓存由 Authenticate 按日限额决定是否写入。
func (s *Service) findByHashFresh(hash string) (*model.APIKey, error) {
	rec, err := s.repo.FindByHash(hash)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			s.auth.setNotFound(hash)
			return nil, ErrUnauthorized
		}
		return nil, err
	}
	if rec.Status != 1 || (rec.ExpireAt != nil && rec.ExpireAt.Before(time.Now())) {
		s.auth.setNotFound(hash)
		return nil, ErrUnauthorized
	}
	return rec, nil
}

// CheckDailyCostOf 用已载入的 key 行校验当日成本未超限（热路径复用调用方已查到的行，
// 避免重复回表）；limit<=0 不限。跨日时触发惰性重置（必要的写库）。
func (s *Service) CheckDailyCostOf(k *model.APIKey, now time.Time) bool {
	if k == nil || k.ID == 0 {
		return true
	}
	if k.DailyCostLimit <= 0 {
		return true
	}
	today := now.Format("2006-01-02")
	if k.DailyCostDate != today {
		_ = s.repo.ResetDailyCostIfStale(k.ID, today)
		return true // 重置后当日未用
	}
	return k.DailyCostUsed < k.DailyCostLimit
}

// ReserveDaily 请求前预留日成本，返回实际预留额；额度不足返回 ErrDailyCostExceeded。
func (s *Service) ReserveDaily(keyID uint64, micros int64, now time.Time) (int64, error) {
	if micros <= 0 {
		return 0, nil
	}
	return s.repo.ReserveDaily(keyID, micros, now.Format("2006-01-02"))
}

// AdjustDailyCost 结算差额（actual-reserved，可为负）。
func (s *Service) AdjustDailyCost(keyID uint64, delta int64, now time.Time) error {
	if delta == 0 {
		return nil
	}
	return s.repo.AdjustDailyCost(keyID, delta, now.Format("2006-01-02"))
}

// ApplyDailyCost 累加当日成本(原子+跨日重置)。
func (s *Service) ApplyDailyCost(keyID uint64, cost int64, now time.Time) error {
	if cost <= 0 {
		return nil
	}
	return s.repo.AddDailyCost(keyID, cost, now.Format("2006-01-02"))
}

// Get 按 ID 取 key。
func (s *Service) Get(keyID uint64) (*model.APIKey, error) { return s.repo.Get(keyID) }

// UpdateGovernance 更新 key 治理字段。
func (s *Service) UpdateGovernance(keyID uint64, fields map[string]any) error {
	return s.repo.UpdateGovernance(keyID, fields)
}
