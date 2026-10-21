package db

import "gorm.io/gorm"

type Migration struct {
	ID string
	Up func(tx *gorm.DB) error
}

type schemaMigration struct {
	ID string `gorm:"primaryKey;column:id"`
}

func (schemaMigration) TableName() string { return "schema_migrations" }

// Migrate 顺序执行未应用的迁移，已应用的跳过（幂等）。
func Migrate(g *gorm.DB, regs []Migration) error {
	if err := g.AutoMigrate(&schemaMigration{}); err != nil {
		return err
	}
	for _, m := range regs {
		var cnt int64
		g.Model(&schemaMigration{}).Where("id = ?", m.ID).Count(&cnt)
		if cnt > 0 {
			continue
		}
		if err := g.Transaction(func(tx *gorm.DB) error {
			if err := m.Up(tx); err != nil {
				return err
			}
			return tx.Create(&schemaMigration{ID: m.ID}).Error
		}); err != nil {
			return err
		}
	}
	return nil
}
