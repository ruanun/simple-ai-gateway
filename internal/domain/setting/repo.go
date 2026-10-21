package setting

import (
	"errors"

	"github.com/ruanun/simple-ai-gateway/internal/model"
	"gorm.io/gorm"
)

type Repo struct{ g *gorm.DB }

func NewRepo(g *gorm.DB) *Repo { return &Repo{g: g} }

// Get 取单例设置(id=1);不存在则返回带默认值的行。
func (r *Repo) Get() (*model.SystemSetting, error) {
	var s model.SystemSetting
	if err := r.g.First(&s, 1).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return &model.SystemSetting{AuditRetentionDays: 30, RequestLogRetentionDays: 90}, nil
		}
		return nil, err
	}
	return &s, nil
}

// Update upsert 单例设置到 id=1。
func (r *Repo) Update(s *model.SystemSetting) error {
	s.ID = 1
	// 保留已存在记录的 CreatedAt:Save 是全字段覆盖,零值 CreatedAt 会破坏原值。
	var existing model.SystemSetting
	if err := r.g.First(&existing, 1).Error; err == nil {
		s.CreatedAt = existing.CreatedAt
	}
	return r.g.Save(s).Error
}
