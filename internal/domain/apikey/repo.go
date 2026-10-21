package apikey

import (
	"errors"

	"github.com/ruanun/simple-ai-gateway/internal/model"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

var errDailyReserveRetry = errors.New("daily reserve retry")

// ErrDailyCostExceeded 请求前日成本预留失败：剩余日额度不足以覆盖本次预留。
var ErrDailyCostExceeded = errors.New("daily cost limit exceeded")

type Repo struct{ g *gorm.DB }

func NewRepo(g *gorm.DB) *Repo { return &Repo{g: g} }

func (r *Repo) Create(k *model.APIKey) error { return r.g.Create(k).Error }

func (r *Repo) FindByHash(hash string) (*model.APIKey, error) {
	var k model.APIKey
	if err := r.g.Where("key_hash = ?", hash).First(&k).Error; err != nil {
		return nil, err
	}
	return &k, nil
}

func (r *Repo) List() ([]model.APIKey, error) {
	var ks []model.APIKey
	return ks, r.g.Order("id desc").Find(&ks).Error
}

func (r *Repo) Delete(id uint64) error { return r.g.Delete(&model.APIKey{}, id).Error }

// Get 按 ID 取 key。
func (r *Repo) Get(id uint64) (*model.APIKey, error) {
	var k model.APIKey
	return &k, r.g.First(&k, id).Error
}

// AddDailyCost 累加当日成本（原子 + 跨日重置合一，单条 SQL）。
// 日期为今天 → 累加；非今天 → 重置为本次 cost 并把日期翻到今天。
// SET 顺序刻意 daily_cost_used 在前、daily_cost_date 在后：MySQL 按从左到右用已赋新值求值，
// date 先改会让 CASE 恒真丢失重置；SQLite/PostgreSQL 全部按旧行值求值，两序皆可。此序三方言均正确。
func (r *Repo) AddDailyCost(id uint64, cost int64, today string) error {
	return r.g.Exec(
		"UPDATE api_keys SET daily_cost_used = CASE WHEN daily_cost_date = ? THEN daily_cost_used + ? ELSE ? END, daily_cost_date = ? WHERE id = ?",
		today, cost, cost, today, id,
	).Error
}

// ReserveDaily 请求前预留日成本，返回实际预留额（只预留到当日剩余额度为止）。
// 日期不是今天时按「今天从 0 起算」处理；daily_cost_limit<=0（不限）时返回 0,nil。
// 返回 ErrDailyCostExceeded 表示当日额度已耗尽。
func (r *Repo) ReserveDaily(id uint64, max int64, today string) (int64, error) {
	if max <= 0 {
		return 0, nil
	}
	for attempt := 0; attempt < 3; attempt++ {
		var reserved int64
		err := r.g.Transaction(func(tx *gorm.DB) error {
			var k model.APIKey
			q := tx.Select("id", "daily_cost_limit", "daily_cost_used", "daily_cost_date")
			if tx.Dialector.Name() != "sqlite" {
				q = q.Clauses(clause.Locking{Strength: "UPDATE"})
			}
			if err := q.First(&k, id).Error; err != nil {
				return err
			}
			if k.DailyCostLimit <= 0 {
				reserved = 0
				return nil
			}
			used := k.DailyCostUsed
			if k.DailyCostDate != today {
				used = 0
			}
			remaining := k.DailyCostLimit - used
			if remaining <= 0 {
				return ErrDailyCostExceeded
			}
			reserved = max
			if reserved > remaining {
				reserved = remaining
			}
			res := tx.Model(&model.APIKey{}).
				Where("id = ? AND daily_cost_date = ? AND daily_cost_used = ?", id, k.DailyCostDate, k.DailyCostUsed).
				Updates(map[string]any{"daily_cost_used": used + reserved, "daily_cost_date": today})
			if res.Error != nil {
				return res.Error
			}
			if res.RowsAffected == 0 {
				return errDailyReserveRetry
			}
			return nil
		})
		if errors.Is(err, errDailyReserveRetry) {
			continue
		}
		return reserved, err
	}
	return 0, ErrDailyCostExceeded
}

// AdjustDailyCost 结算差额：正差按 AddDailyCost 累加（含跨日重置）；负差仅在同日扣减且下限为 0。
func (r *Repo) AdjustDailyCost(id uint64, delta int64, today string) error {
	if delta == 0 {
		return nil
	}
	if delta > 0 {
		return r.AddDailyCost(id, delta, today)
	}
	return r.g.Exec(
		"UPDATE api_keys SET daily_cost_used = CASE WHEN daily_cost_used + ? < 0 THEN 0 ELSE daily_cost_used + ? END WHERE id = ? AND daily_cost_date = ?",
		delta, delta, id, today,
	).Error
}

// ResetDailyCostIfStale 若 key 日期非今天则清零并设为今天。
func (r *Repo) ResetDailyCostIfStale(id uint64, today string) error {
	return r.g.Model(&model.APIKey{}).Where("id = ? AND daily_cost_date <> ?", id, today).
		Updates(map[string]any{"daily_cost_used": 0, "daily_cost_date": today}).Error
}

// UpdateGovernance 更新 key 的治理字段(白名单/映射/日限额等)。
func (r *Repo) UpdateGovernance(id uint64, fields map[string]any) error {
	return r.g.Model(&model.APIKey{}).Where("id = ?", id).Updates(fields).Error
}
