package account

import (
	"time"

	"github.com/ruanun/simple-ai-gateway/internal/model"
	"github.com/ruanun/simple-ai-gateway/internal/writegate"
)

type Service struct {
	repo  *Repo
	hGate *writegate.Gate // 健康展示字段回写节流（TouchUsed/IncrFailCount 配对不变量在此维护）
}

func NewService(repo *Repo) *Service {
	return &Service{repo: repo, hGate: writegate.New(writegate.HealthWriteInterval)}
}

// Create 落库一个账号。weight<1 兜底为 1；status 直接用传入值（不把 0 改写为 1），
// 以便调用方显式停用(0)或启用(1)账号。
func (s *Service) Create(a model.Account) (*model.Account, error) {
	if a.Weight < 1 {
		a.Weight = 1
	}
	if err := s.repo.Create(&a); err != nil {
		return nil, err
	}
	return &a, nil
}

func (s *Service) ListByChannel(channelID uint64) ([]model.Account, error) {
	return s.repo.ListByChannel(channelID)
}

func (s *Service) ListEnabledByChannel(channelID uint64) ([]model.Account, error) {
	return s.repo.ListEnabledByChannel(channelID)
}

func (s *Service) Delete(id uint64) error { return s.repo.Delete(id) }

func (s *Service) SetStatus(id uint64, status int8) error { return s.repo.UpdateStatus(id, status) }

// SetProxyGroup 更新账号绑定的代理组（空=直连）。
func (s *Service) SetProxyGroup(id uint64, group string) error {
	return s.repo.UpdateProxyGroup(id, group)
}

// UpdateFields 更新账号调度字段（并发/速率/过期/冷却），map 语义允许显式写 0/null。
func (s *Service) UpdateFields(id uint64, fields map[string]any) error {
	return s.repo.UpdateFields(id, fields)
}

// Get 按主键取账号。
func (s *Service) Get(id uint64) (*model.Account, error) { return s.repo.Get(id) }

// FirstEnabledByChannel 取渠道下第一个启用账号（按主键序）。
func (s *Service) FirstEnabledByChannel(channelID uint64) (*model.Account, error) {
	return s.repo.FirstEnabledByChannel(channelID)
}

// TouchUsed 成功回写：last_used_at + fail_count 清零。60s 窗口节流（窗口内跳过重复 UPDATE，
// last_used_at 展示滞后上限即窗口）；失败（IncrFailCount）会撤销记账，保证失败后的下一次成功立即回写清零。
func (s *Service) TouchUsed(id uint64, at time.Time) error {
	if !s.hGate.Allow(id, at) {
		return nil
	}
	return s.repo.TouchUsed(id, at)
}

// IncrFailCount 失败回写：fail_count 自增（每次即写），并撤销 TouchUsed 的节流记账。
func (s *Service) IncrFailCount(id uint64) error {
	s.hGate.MarkDirty(id)
	return s.repo.IncrFailCount(id)
}

// ListAll 列出所有账号（跨渠道）。
func (s *Service) ListAll() ([]model.Account, error) { return s.repo.ListAll() }
