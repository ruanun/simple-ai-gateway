package keytag

import (
	"github.com/ruanun/simple-ai-gateway/internal/model"
	"gorm.io/gorm"
)

type Repo struct{ g *gorm.DB }

func NewRepo(g *gorm.DB) *Repo { return &Repo{g: g} }

func (r *Repo) Create(t *model.KeyTag) error { return r.g.Create(t).Error }

func (r *Repo) List() ([]model.KeyTag, error) {
	var ts []model.KeyTag
	return ts, r.g.Order("id desc").Find(&ts).Error
}

func (r *Repo) Delete(id uint64) error {
	return r.g.Transaction(func(tx *gorm.DB) error {
		if err := tx.Where("tag_id = ?", id).Delete(&model.KeyTagRelation{}).Error; err != nil {
			return err
		}
		return tx.Delete(&model.KeyTag{}, id).Error
	})
}

func (r *Repo) Bind(keyID, tagID uint64) error {
	var cnt int64
	r.g.Model(&model.KeyTagRelation{}).Where("api_key_id = ? AND tag_id = ?", keyID, tagID).Count(&cnt)
	if cnt > 0 {
		return nil
	}
	return r.g.Create(&model.KeyTagRelation{APIKeyID: keyID, TagID: tagID}).Error
}

func (r *Repo) Unbind(keyID, tagID uint64) error {
	return r.g.Where("api_key_id = ? AND tag_id = ?", keyID, tagID).Delete(&model.KeyTagRelation{}).Error
}

func (r *Repo) KeyIDsByTag(tagID uint64) ([]uint64, error) {
	var ids []uint64
	return ids, r.g.Model(&model.KeyTagRelation{}).Where("tag_id = ?", tagID).Pluck("api_key_id", &ids).Error
}

func (r *Repo) TagsByKey(keyID uint64) ([]model.KeyTag, error) {
	var tags []model.KeyTag
	err := r.g.Table("key_tags").
		Joins("JOIN key_tag_relations r ON r.tag_id = key_tags.id").
		Where("r.api_key_id = ?", keyID).Find(&tags).Error
	return tags, err
}

// TagsByKeys 一次查询取多把 key 的标签并按 key 分组，替代列表逐 key 查询的 N+1(E2)。
func (r *Repo) TagsByKeys(keyIDs []uint64) (map[uint64][]model.KeyTag, error) {
	out := map[uint64][]model.KeyTag{}
	if len(keyIDs) == 0 {
		return out, nil
	}
	type row struct {
		APIKeyID uint64
		model.KeyTag
	}
	var rows []row
	err := r.g.Table("key_tags").
		Select("r.api_key_id as api_key_id, key_tags.*").
		Joins("JOIN key_tag_relations r ON r.tag_id = key_tags.id").
		Where("r.api_key_id IN ?", keyIDs).
		Scan(&rows).Error
	if err != nil {
		return nil, err
	}
	for _, rw := range rows {
		out[rw.APIKeyID] = append(out[rw.APIKeyID], rw.KeyTag)
	}
	return out, nil
}
