package price

import (
	"fmt"
	"sync"

	"github.com/ruanun/simple-ai-gateway/internal/model"
)

// Service 模型价格服务,内置进程内缓存(全量加载+写后热加载)。
// notify 用于写后通知其他实例(Pub/Sub)触发 reload。
type Service struct {
	repo   *Repo
	mu     sync.RWMutex
	cache  map[string]model.ModelPrice // key = "channelID:model"
	notify func()
}

func NewService(repo *Repo) *Service {
	s := &Service{repo: repo, cache: map[string]model.ModelPrice{}}
	s.reload()
	return s
}

func cacheKey(channelID uint64, modelName string) string {
	return fmt.Sprintf("%d:%s", channelID, modelName)
}

// reload 从 DB 全量加载价格到内存缓存（仅加载 enabled=true 的价格条目）。
func (s *Service) reload() {
	all, err := s.repo.List()
	if err != nil {
		return
	}
	m := make(map[string]model.ModelPrice, len(all))
	for _, p := range all {
		if !p.Enabled {
			continue // 跳过已禁用的价格，避免误计费
		}
		m[cacheKey(p.ChannelID, p.Model)] = p
	}
	s.mu.Lock()
	s.cache = m
	s.mu.Unlock()
}

// Resolve 按 (channelID, model) 取覆盖价,无则回退 (0, model) 全局官方价;都没有返回 (零值,false)。
// 从内存缓存读,零 DB 查询。
func (s *Service) Resolve(channelID uint64, modelName string) (model.ModelPrice, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if channelID != 0 {
		if p, ok := s.cache[cacheKey(channelID, modelName)]; ok {
			return p, true
		}
	}
	if p, ok := s.cache[cacheKey(0, modelName)]; ok {
		return p, true
	}
	return model.ModelPrice{}, false
}

// Get 取全局官方价(channel_id=0)。
func (s *Service) Get(modelName string) (model.ModelPrice, bool) {
	return s.Resolve(0, modelName)
}

// SetNotify 注入写后通知回调（Pub/Sub 跨实例缓存失效）。
func (s *Service) SetNotify(fn func()) {
	s.notify = fn
}

// Reload 公开的 reload 方法，供订阅方收到通知时调用。
func (s *Service) Reload() {
	s.reload()
}

func (s *Service) reloadAndNotify() {
	s.reload()
	if s.notify != nil {
		s.notify()
	}
}

// Upsert 写入价格并刷新缓存，成功后触发通知。
func (s *Service) Upsert(p model.ModelPrice) error {
	if err := s.repo.Upsert(&p); err != nil {
		return err
	}
	s.reloadAndNotify()
	return nil
}

// Delete 删除价格并刷新缓存，成功后触发通知。
func (s *Service) Delete(id uint64) error {
	if err := s.repo.Delete(id); err != nil {
		return err
	}
	s.reloadAndNotify()
	return nil
}

// List 列出全部价格(管理页用,走 DB)。
func (s *Service) List() ([]model.ModelPrice, error) { return s.repo.List() }

// ListByChannel 列某渠道价(管理页用,走 DB)。
func (s *Service) ListByChannel(channelID uint64) ([]model.ModelPrice, error) {
	return s.repo.ListByChannel(channelID)
}
