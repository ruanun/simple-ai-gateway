package alias

import (
	"github.com/ruanun/simple-ai-gateway/internal/model"
	"gorm.io/gorm"
)

type Repo struct{ g *gorm.DB }

func NewRepo(g *gorm.DB) *Repo { return &Repo{g: g} }

func (r *Repo) CreateAlias(a *model.ModelAlias) error { return r.g.Create(a).Error }

// AliasExists 按名精确查重（新建前预检，给出友好 400 而非撞唯一索引的 500）。
func (r *Repo) AliasExists(name string) (bool, error) {
	var n int64
	err := r.g.Model(&model.ModelAlias{}).Where("alias = ?", name).Count(&n).Error
	return n > 0, err
}

// UpdateAlias 更新别名可变字段（说明/启停），fields 由 handler 组装。
func (r *Repo) UpdateAlias(id uint64, fields map[string]any) error {
	return r.g.Model(&model.ModelAlias{}).Where("id = ?", id).Updates(fields).Error
}

func (r *Repo) ListAliases() ([]model.ModelAlias, error) {
	var as []model.ModelAlias
	return as, r.g.Order("id desc").Find(&as).Error
}

func (r *Repo) DeleteAlias(id uint64) error {
	return r.g.Transaction(func(tx *gorm.DB) error {
		if err := tx.Where("alias_id = ?", id).Delete(&model.AliasRoute{}).Error; err != nil {
			return err
		}
		return tx.Delete(&model.ModelAlias{}, id).Error
	})
}

func (r *Repo) CreateRoute(rt *model.AliasRoute) error { return r.g.Create(rt).Error }

func (r *Repo) ListRoutes(aliasID uint64) ([]model.AliasRoute, error) {
	var rs []model.AliasRoute
	return rs, r.g.Where("alias_id = ?", aliasID).Order("priority desc, id asc").Find(&rs).Error
}

func (r *Repo) DeleteRoute(id uint64) error { return r.g.Delete(&model.AliasRoute{}, id).Error }

// UpdateRoute 更新路由边的权重/优先级（渠道/模型不可改，需换目标请删边重建）。
func (r *Repo) UpdateRoute(id uint64, weight, priority int) error {
	return r.g.Model(&model.AliasRoute{}).Where("id = ?", id).
		Updates(map[string]any{"weight": weight, "priority": priority}).Error
}
