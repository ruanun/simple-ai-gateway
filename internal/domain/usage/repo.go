// Package usage 提供用量聚合表的仪表盘查询（KPI/趋势/排行）。
package usage

import (
	"gorm.io/gorm"
)

type Repo struct{ g *gorm.DB }

func NewRepo(g *gorm.DB) *Repo { return &Repo{g: g} }
