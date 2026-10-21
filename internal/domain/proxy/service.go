// Package proxy 提供 IP 代理池的 CRUD、按组查询启用代理与健康字段回写。
package proxy

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

func (s *Service) Create(p model.Proxy) (*model.Proxy, error) {
	if p.Weight < 1 {
		p.Weight = 1
	}
	if p.Protocol == "" {
		p.Protocol = "http"
	}
	if err := s.repo.Create(&p); err != nil {
		return nil, err
	}
	return &p, nil
}

func (s *Service) List() ([]model.Proxy, error) { return s.repo.List() }
func (s *Service) ListEnabledByGroup(g string) ([]model.Proxy, error) {
	return s.repo.ListEnabledByGroup(g)
}
func (s *Service) Delete(id uint64) error                 { return s.repo.Delete(id) }
func (s *Service) SetStatus(id uint64, status int8) error { return s.repo.UpdateStatus(id, status) }

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
