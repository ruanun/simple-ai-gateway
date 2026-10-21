// Package price 提供模型价格表 ModelPrice 的 Upsert/查询/删除与常见模型种子。
package price

import (
	"time"

	"github.com/ruanun/simple-ai-gateway/internal/model"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type Repo struct{ g *gorm.DB }

func NewRepo(g *gorm.DB) *Repo { return &Repo{g: g} }

// Upsert 按 (channel_id, model) 复合唯一键插入或覆盖。
//
// 覆盖分支刻意用 clause.Assignments 取 p 的实际值，而非 AssignmentColumns 的 `excluded.*`：
// Enabled 标了 `gorm:"default:true"`，GORM 的 Create 会把该字段的零值替换成默认值再绑参，
// 于是 enabled=false 进 INSERT 时已变成 true，excluded.enabled 自然也是 true——停用被静默写回启用。
// 同理 updated_at 不取 excluded：整行回传时 p.UpdatedAt 是从库里读出的旧值，GORM 的 autoUpdateTime
// 只填零值不会刷新，沿用 excluded 会让「按 updated_at 倒序」的列表排序不动。
// 注意：插入分支仍受 default:true 影响（新建即停用会存成启用）——当前无此入口（新增价格恒为启用），
// 若将来要支持，需把 Enabled 改成 *bool 或去掉 default 标签。
func (r *Repo) Upsert(p *model.ModelPrice) error {
	return r.g.Clauses(clause.OnConflict{
		Columns: []clause.Column{{Name: "channel_id"}, {Name: "model"}},
		DoUpdates: clause.Assignments(map[string]any{
			"input_per_1m":        p.InputPer1M,
			"output_per_1m":       p.OutputPer1M,
			"cached_input_per_1m": p.CachedInputPer1M,
			"cache_write_per_1m":  p.CacheWritePer1M,
			"currency":            p.Currency,
			"enabled":             p.Enabled,
			"type":                p.Type,
			"per_call_micros":     p.PerCallMicros,
			"updated_at":          time.Now(),
		}),
	}).Create(p).Error
}

// GetByChannel 取 (channelID, model) 的价;未命中返回 gorm.ErrRecordNotFound。
func (r *Repo) GetByChannel(channelID uint64, modelName string) (*model.ModelPrice, error) {
	var p model.ModelPrice
	return &p, r.g.Where("channel_id = ? AND model = ?", channelID, modelName).First(&p).Error
}

// ListByChannel 列某渠道的价(channelID=0 即全局官方价)。
// 按最近更新倒序(id 兜底稳定同 updated_at 的批量导入),刚配/刚同步的置顶。
func (r *Repo) ListByChannel(channelID uint64) ([]model.ModelPrice, error) {
	var ps []model.ModelPrice
	return ps, r.g.Where("channel_id = ?", channelID).Order("updated_at desc, id desc").Find(&ps).Error
}

func (r *Repo) List() ([]model.ModelPrice, error) {
	var ps []model.ModelPrice
	return ps, r.g.Order("updated_at desc, id desc").Find(&ps).Error
}

func (r *Repo) Delete(id uint64) error { return r.g.Delete(&model.ModelPrice{}, id).Error }
