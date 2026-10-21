package setting

import "github.com/ruanun/simple-ai-gateway/internal/model"

type Service struct{ repo *Repo }

func NewService(repo *Repo) *Service { return &Service{repo: repo} }

func (s *Service) Get() (*model.SystemSetting, error) { return s.repo.Get() }

// Update 更新设置;保留天数下限归一为 1,防止误配 0 天清空数据。
func (s *Service) Update(m *model.SystemSetting) error {
	if m.AuditRetentionDays < 1 {
		m.AuditRetentionDays = 1
	}
	if m.RequestLogRetentionDays < 1 {
		m.RequestLogRetentionDays = 1
	}
	return s.repo.Update(m)
}
