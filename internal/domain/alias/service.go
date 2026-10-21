package alias

import "github.com/ruanun/simple-ai-gateway/internal/model"

type Service struct{ repo *Repo }

func NewService(repo *Repo) *Service { return &Service{repo: repo} }

func (s *Service) CreateAlias(name string) (*model.ModelAlias, error) {
	a := &model.ModelAlias{Alias: name, Status: 1}
	if err := s.repo.CreateAlias(a); err != nil {
		return nil, err
	}
	return a, nil
}

func (s *Service) ListAliases() ([]model.ModelAlias, error)      { return s.repo.ListAliases() }
func (s *Service) DeleteAlias(id uint64) error                   { return s.repo.DeleteAlias(id) }
func (s *Service) AliasExists(name string) (bool, error)         { return s.repo.AliasExists(name) }
func (s *Service) UpdateAlias(id uint64, f map[string]any) error { return s.repo.UpdateAlias(id, f) }

func (s *Service) AddRoute(aliasID, channelID uint64, modelName string, weight, priority int) (*model.AliasRoute, error) {
	r := &model.AliasRoute{AliasID: aliasID, ChannelID: channelID, Model: modelName, Weight: weight, Priority: priority}
	if err := s.repo.CreateRoute(r); err != nil {
		return nil, err
	}
	return r, nil
}

func (s *Service) ListRoutes(aliasID uint64) ([]model.AliasRoute, error) {
	return s.repo.ListRoutes(aliasID)
}
func (s *Service) DeleteRoute(id uint64) error { return s.repo.DeleteRoute(id) }
func (s *Service) UpdateRoute(id uint64, weight, priority int) error {
	return s.repo.UpdateRoute(id, weight, priority)
}
