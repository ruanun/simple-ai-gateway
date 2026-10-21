package account

import (
	"time"

	"github.com/ruanun/simple-ai-gateway/internal/model"
	"gorm.io/gorm"
)

type Repo struct{ g *gorm.DB }

func NewRepo(g *gorm.DB) *Repo { return &Repo{g: g} }

// Create 落库账号。用 map 写入以绕过 gorm `default:1` 标签：否则传入的零值(如 status=0)
// 会被默认值覆盖成 1，无法显式创建停用账号。写入后把生成的主键回填到 a。
func (r *Repo) Create(a *model.Account) error {
	now := time.Now()
	m := map[string]any{
		"channel_id":                a.ChannelID,
		"name":                      a.Name,
		"api_key_enc":               a.APIKeyEnc,
		"base_url":                  a.BaseURL,
		"weight":                    a.Weight,
		"priority":                  a.Priority,
		"status":                    a.Status,
		"fail_count":                a.FailCount,
		"last_used_at":              a.LastUsedAt,
		"proxy_group":               a.ProxyGroup,
		"max_concurrency":           a.MaxConcurrency,
		"rpm_limit":                 a.RPMLimit,
		"expires_at":                a.ExpiresAt,
		"temp_unschedulable_until":  a.TempUnschedulableUntil,
		"temp_unschedulable_reason": a.TempUnschedulableReason,
		"rate_limit_reset_at":       a.RateLimitResetAt,
		"overload_until":            a.OverloadUntil,
		"created_at":                now,
		"updated_at":                now,
	}
	if err := r.g.Model(&model.Account{}).Create(m).Error; err != nil {
		return err
	}
	a.CreatedAt, a.UpdatedAt = now, now
	if id, ok := m["id"]; ok {
		switch v := id.(type) {
		case int64:
			a.ID = uint64(v)
		case uint64:
			a.ID = v
		}
	}
	return nil
}

func (r *Repo) Delete(id uint64) error { return r.g.Delete(&model.Account{}, id).Error }

// Get 按主键取账号。
func (r *Repo) Get(id uint64) (*model.Account, error) {
	var a model.Account
	if err := r.g.First(&a, id).Error; err != nil {
		return nil, err
	}
	return &a, nil
}

// FirstEnabledByChannel 取渠道下第一个启用账号（按主键序），无则返回 gorm.ErrRecordNotFound。
func (r *Repo) FirstEnabledByChannel(channelID uint64) (*model.Account, error) {
	var a model.Account
	if err := r.g.Where("channel_id = ? AND status = 1", channelID).First(&a).Error; err != nil {
		return nil, err
	}
	return &a, nil
}

func (r *Repo) ListByChannel(channelID uint64) ([]model.Account, error) {
	var as []model.Account
	return as, r.g.Where("channel_id = ?", channelID).Order("priority desc, id asc").Find(&as).Error
}

func (r *Repo) ListEnabledByChannel(channelID uint64) ([]model.Account, error) {
	var as []model.Account
	return as, r.g.Where("channel_id = ? AND status = ?", channelID, 1).Order("priority desc, id asc").Find(&as).Error
}

func (r *Repo) UpdateStatus(id uint64, status int8) error {
	return r.g.Model(&model.Account{}).Where("id = ?", id).Update("status", status).Error
}

// UpdateProxyGroup 更新账号绑定的代理组（空字符串=恢复直连出站）。
func (r *Repo) UpdateProxyGroup(id uint64, group string) error {
	return r.g.Model(&model.Account{}).Where("id = ?", id).Update("proxy_group", group).Error
}

// UpdateFields 按 map 更新账号调度字段（map 绕过 GORM default 零值陷阱，允许写 0/NULL）。
func (r *Repo) UpdateFields(id uint64, fields map[string]any) error {
	if len(fields) == 0 {
		return nil
	}
	return r.g.Model(&model.Account{}).Where("id = ?", id).Updates(fields).Error
}

// TouchUsed 成功回写健康字段：last_used_at=at 且 fail_count 清零（Updates 会一并刷新 updated_at）。
func (r *Repo) TouchUsed(id uint64, at time.Time) error {
	return r.g.Model(&model.Account{}).Where("id = ?", id).
		Updates(map[string]any{"last_used_at": at, "fail_count": 0}).Error
}

// IncrFailCount 失败回写：fail_count 原地自增（UpdateColumn 不刷新 updated_at，与原选号器行为一致）。
func (r *Repo) IncrFailCount(id uint64) error {
	return r.g.Model(&model.Account{}).Where("id = ?", id).
		UpdateColumn("fail_count", gorm.Expr("fail_count + 1")).Error
}

// ListAll 列出所有账号（跨渠道），用于熔断状态聚合等全局视图。
func (r *Repo) ListAll() ([]model.Account, error) {
	var as []model.Account
	return as, r.g.Order("id asc").Find(&as).Error
}
