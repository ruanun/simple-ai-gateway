// Channel 域数据访问层：渠道(Channel)与渠道模型(ChannelModel)的薄 GORM 包装。
package channel

import (
	"github.com/ruanun/simple-ai-gateway/internal/model"
	"gorm.io/gorm"
)

type Repo struct{ g *gorm.DB }

func NewRepo(g *gorm.DB) *Repo { return &Repo{g: g} }

// List 全量渠道列表，按 id 倒序。
func (r *Repo) List() ([]model.Channel, error) {
	var rows []model.Channel
	return rows, r.g.Order("id desc").Find(&rows).Error
}

// Get 按主键取渠道。
func (r *Repo) Get(id uint64) (*model.Channel, error) {
	var ch model.Channel
	if err := r.g.First(&ch, id).Error; err != nil {
		return nil, err
	}
	return &ch, nil
}

// CreateWithAccount 事务内创建「渠道 + 默认账号」：渠道主键回填后写进账号的 channel_id，
// 任一失败整体回滚，保证不出现无账号的孤儿渠道。
func (r *Repo) CreateWithAccount(ch *model.Channel, acc *model.Account) error {
	return r.g.Transaction(func(tx *gorm.DB) error {
		if err := tx.Create(ch).Error; err != nil {
			return err
		}
		acc.ChannelID = ch.ID
		return tx.Create(acc).Error
	})
}

// UpdateFields 按主键局部更新渠道字段（map 写入，零值可显式落库）。
func (r *Repo) UpdateFields(id uint64, fields map[string]any) error {
	return r.g.Model(&model.Channel{}).Where("id = ?", id).Updates(fields).Error
}

// Delete 按主键删渠道。
func (r *Repo) Delete(id uint64) error { return r.g.Delete(&model.Channel{}, id).Error }

// ---- ChannelModel ----

// ListModels 渠道模型列表，按 id 倒序；channelID 非空时按渠道过滤（保持与 HTTP query 相同的字符串语义）。
func (r *Repo) ListModels(channelID string) ([]model.ChannelModel, error) {
	var rows []model.ChannelModel
	q := r.g.Order("id desc")
	if channelID != "" {
		q = q.Where("channel_id = ?", channelID)
	}
	return rows, q.Find(&rows).Error
}

// CreateModel 创建渠道模型（主键回填到 m）。
func (r *Repo) CreateModel(m *model.ChannelModel) error { return r.g.Create(m).Error }

// DeleteModel 按主键删渠道模型。
func (r *Repo) DeleteModel(id uint64) error { return r.g.Delete(&model.ChannelModel{}, id).Error }

// SetModelStatus 按主键启停渠道模型（enabled=false 时路由表不再包含该模型）。
func (r *Repo) SetModelStatus(id uint64, enabled bool) error {
	return r.g.Model(&model.ChannelModel{}).Where("id = ?", id).Update("enabled", enabled).Error
}
