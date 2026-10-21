package requestlog

import (
	"time"

	"github.com/ruanun/simple-ai-gateway/internal/model"
	"gorm.io/gorm"
)

type Repo struct{ g *gorm.DB }

func NewRepo(g *gorm.DB) *Repo { return &Repo{g: g} }

func (r *Repo) Create(l *model.RequestLog) error { return r.g.Create(l).Error }

func (r *Repo) List(limit, offset int) ([]model.RequestLog, int64, error) {
	var rows []model.RequestLog
	var total int64
	r.g.Model(&model.RequestLog{}).Count(&total)
	err := r.g.Order("id desc").Limit(limit).Offset(offset).Find(&rows).Error
	return rows, total, err
}

// DeleteBefore 删除 created_at 早于 t 的日志,返回删除条数。
func (r *Repo) DeleteBefore(t time.Time) (int64, error) {
	res := r.g.Where("created_at < ?", t).Delete(&model.RequestLog{})
	return res.RowsAffected, res.Error
}
