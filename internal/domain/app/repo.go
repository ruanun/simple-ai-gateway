// Package app 提供配额主体 App 的 CRUD、配额校验与原子扣减。
package app

import (
	"errors"

	"github.com/ruanun/simple-ai-gateway/internal/model"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

var errReserveRetry = errors.New("reserve retry")

// ErrQuotaExceeded 请求前额度预留失败：App 不存在/停用或剩余额度不足以覆盖本次预留。
var ErrQuotaExceeded = errors.New("app quota exceeded")

type Repo struct{ g *gorm.DB }

func NewRepo(g *gorm.DB) *Repo { return &Repo{g: g} }

func (r *Repo) Create(a *model.App) error { return r.g.Create(a).Error }

func (r *Repo) Get(id uint64) (*model.App, error) {
	var a model.App
	return &a, r.g.First(&a, id).Error
}

func (r *Repo) List() ([]model.App, error) {
	var as []model.App
	return as, r.g.Order("id desc").Find(&as).Error
}

// Delete 同事务删除 App 及其全部 AppMember 行，避免残留孤儿成员导致「名下应用数」计数虚高。
func (r *Repo) Delete(id uint64) error {
	return r.g.Transaction(func(tx *gorm.DB) error {
		if err := tx.Where("app_id = ?", id).Delete(&model.AppMember{}).Error; err != nil {
			return err
		}
		return tx.Delete(&model.App{}, id).Error
	})
}

// AddUsed 原子累加已用额度（micros）。
func (r *Repo) AddUsed(id uint64, micros int64) error {
	return r.g.Model(&model.App{}).Where("id = ?", id).
		UpdateColumn("used_quota", gorm.Expr("used_quota + ?", micros)).Error
}

// Reserve 请求前预留额度，返回实际预留额（可能小于 max：只预留到剩余额度为止）。
// 只预留到剩余额度而不是要求 max 全额可容纳，避免「实际成本远小于最大估算」时把
// 临界点的正常请求误判为超额；并发请求会因剩余额度已被占而依次串行，结算后退差。
// 返回 ErrQuotaExceeded 表示额度不足/App 停用，调用方回 402。
func (r *Repo) Reserve(id uint64, max int64) (int64, error) {
	if max <= 0 {
		return 0, nil
	}
	for attempt := 0; attempt < 3; attempt++ {
		var reserved int64
		err := r.g.Transaction(func(tx *gorm.DB) error {
			var a model.App
			q := tx.Select("id", "quota", "used_quota", "status")
			if tx.Dialector.Name() != "sqlite" {
				q = q.Clauses(clause.Locking{Strength: "UPDATE"})
			}
			if err := q.First(&a, id).Error; err != nil {
				return err
			}
			if a.Status != 1 {
				return ErrQuotaExceeded
			}
			if a.Quota <= 0 {
				reserved = 0 // 不限额度：不占预留，结算时直接累加实际成本
				return nil
			}
			remaining := a.Quota - a.UsedQuota
			if remaining <= 0 {
				return ErrQuotaExceeded
			}
			reserved = max
			if reserved > remaining {
				reserved = remaining
			}
			// 条件写（带旧值）在 SQLite 无 FOR UPDATE 时也能防并发覆盖；冲突则重试。
			res := tx.Model(&model.App{}).
				Where("id = ? AND used_quota = ?", id, a.UsedQuota).
				UpdateColumn("used_quota", a.UsedQuota+reserved)
			if res.Error != nil {
				return res.Error
			}
			if res.RowsAffected == 0 {
				return errReserveRetry
			}
			return nil
		})
		if errors.Is(err, errReserveRetry) {
			continue
		}
		return reserved, err
	}
	return 0, ErrQuotaExceeded
}

// AdjustUsed 结算调整：delta 可为负（退还多预留），used_quota 下限为 0。
func (r *Repo) AdjustUsed(id uint64, delta int64) error {
	if delta == 0 {
		return nil
	}
	return r.g.Model(&model.App{}).Where("id = ?", id).
		UpdateColumn("used_quota", gorm.Expr("CASE WHEN used_quota + ? < 0 THEN 0 ELSE used_quota + ? END", delta, delta)).Error
}
