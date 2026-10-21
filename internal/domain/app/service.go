package app

import "github.com/ruanun/simple-ai-gateway/internal/model"

type Service struct{ repo *Repo }

func NewService(repo *Repo) *Service { return &Service{repo: repo} }

func (s *Service) Create(name string, quota int64) (*model.App, error) {
	a := &model.App{Name: name, Quota: quota, Status: 1}
	if err := s.repo.Create(a); err != nil {
		return nil, err
	}
	return a, nil
}

func (s *Service) Get(id uint64) (*model.App, error) { return s.repo.Get(id) }
func (s *Service) List() ([]model.App, error)        { return s.repo.List() }
func (s *Service) Delete(id uint64) error            { return s.repo.Delete(id) }

// HasQuota：status!=1（已禁用）直接拒绝；quota<=0 视为不限；否则 used<quota。App 不存在视为无配额。
func (s *Service) HasQuota(id uint64) bool {
	a, err := s.repo.Get(id)
	if err != nil {
		return false
	}
	return QuotaOK(a)
}

// QuotaOK 用已载入的 App 行判定配额（热路径复用同一次查询，避免重复回表）；语义与 HasQuota 一致。
// 请求前还会走 Reserve 做条件预留（reserve-then-commit）：并发请求各自预留最大估算成本，
// 超出剩余额度直接 402，从而把「先查后扣」的并发超额关掉；响应后 Settle 按实际成本退差。
func QuotaOK(a *model.App) bool {
	if a == nil || a.Status != 1 {
		return false
	}
	if a.Quota <= 0 {
		return true
	}
	return a.UsedQuota < a.Quota
}

// Deduct 原子累加已用额度；micros<=0 时无操作（未启用预留的旧路径仍走这里）。
func (s *Service) Deduct(id uint64, micros int64) error {
	if micros <= 0 {
		return nil
	}
	return s.repo.AddUsed(id, micros)
}

// Reserve 请求前预留额度，返回实际预留额；额度不足返回 ErrQuotaExceeded。
func (s *Service) Reserve(id uint64, micros int64) (int64, error) { return s.repo.Reserve(id, micros) }

// AdjustUsed 结算差额（actual-reserved，可为负）；失败由 gateway 记日志/告警。
func (s *Service) AdjustUsed(id uint64, delta int64) error { return s.repo.AdjustUsed(id, delta) }
