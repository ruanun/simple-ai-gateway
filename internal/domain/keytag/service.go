package keytag

import "github.com/ruanun/simple-ai-gateway/internal/model"

type Service struct{ repo *Repo }

func NewService(repo *Repo) *Service { return &Service{repo: repo} }

func (s *Service) Create(name string) (*model.KeyTag, error) {
	t := &model.KeyTag{Name: name}
	if err := s.repo.Create(t); err != nil {
		return nil, err
	}
	return t, nil
}

func (s *Service) List() ([]model.KeyTag, error)                  { return s.repo.List() }
func (s *Service) Delete(id uint64) error                         { return s.repo.Delete(id) }
func (s *Service) Bind(keyID, tagID uint64) error                 { return s.repo.Bind(keyID, tagID) }
func (s *Service) Unbind(keyID, tagID uint64) error               { return s.repo.Unbind(keyID, tagID) }
func (s *Service) KeyIDsByTag(tagID uint64) ([]uint64, error)     { return s.repo.KeyIDsByTag(tagID) }
func (s *Service) TagsByKey(keyID uint64) ([]model.KeyTag, error) { return s.repo.TagsByKey(keyID) }
func (s *Service) TagsByKeys(keyIDs []uint64) (map[uint64][]model.KeyTag, error) {
	return s.repo.TagsByKeys(keyIDs)
}
