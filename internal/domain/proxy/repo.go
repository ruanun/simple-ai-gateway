package proxy

import (
	"time"

	"github.com/ruanun/simple-ai-gateway/internal/model"
	"gorm.io/gorm"
)

type Repo struct{ g *gorm.DB }

func NewRepo(g *gorm.DB) *Repo { return &Repo{g: g} }

// Create 落库代理。用 map 写入以绕过 gorm `default:1` 标签：否则传入的零值(如 status=0)
// 会被默认值覆盖成 1，无法显式创建停用代理。写入后把生成的主键回填到 p。
func (r *Repo) Create(p *model.Proxy) error {
	now := time.Now()
	m := map[string]any{
		"group_name":   p.Group,
		"name":         p.Name,
		"protocol":     p.Protocol,
		"host":         p.Host,
		"port":         p.Port,
		"username":     p.Username,
		"password":     p.Password,
		"weight":       p.Weight,
		"priority":     p.Priority,
		"status":       p.Status,
		"fail_count":   p.FailCount,
		"last_used_at": p.LastUsedAt,
		"created_at":   now,
		"updated_at":   now,
	}
	if err := r.g.Model(&model.Proxy{}).Create(m).Error; err != nil {
		return err
	}
	p.CreatedAt, p.UpdatedAt = now, now
	if id, ok := m["id"]; ok {
		switch v := id.(type) {
		case int64:
			p.ID = uint64(v)
		case uint64:
			p.ID = v
		}
	}
	return nil
}

func (r *Repo) Delete(id uint64) error { return r.g.Delete(&model.Proxy{}, id).Error }

func (r *Repo) List() ([]model.Proxy, error) {
	var ps []model.Proxy
	return ps, r.g.Order("group_name asc, id desc").Find(&ps).Error
}

func (r *Repo) ListEnabledByGroup(group string) ([]model.Proxy, error) {
	var ps []model.Proxy
	return ps, r.g.Where("group_name = ? AND status = ?", group, 1).Order("priority desc, id asc").Find(&ps).Error
}

func (r *Repo) UpdateStatus(id uint64, status int8) error {
	return r.g.Model(&model.Proxy{}).Where("id = ?", id).Update("status", status).Error
}

// TouchUsed 成功回写健康字段：last_used_at=at 且 fail_count 清零（Updates 会一并刷新 updated_at）。
func (r *Repo) TouchUsed(id uint64, at time.Time) error {
	return r.g.Model(&model.Proxy{}).Where("id = ?", id).
		Updates(map[string]any{"last_used_at": at, "fail_count": 0}).Error
}

// IncrFailCount 失败回写：fail_count 原地自增（UpdateColumn 不刷新 updated_at，与原 ProxyResolver 直写行为一致）。
func (r *Repo) IncrFailCount(id uint64) error {
	return r.g.Model(&model.Proxy{}).Where("id = ?", id).
		UpdateColumn("fail_count", gorm.Expr("fail_count + 1")).Error
}
