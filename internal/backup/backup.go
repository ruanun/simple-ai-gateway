// Package backup 提供 SQLite 数据文件的在线备份与恢复（默认零依赖部署形态）。
// MySQL/PostgreSQL 请使用 mysqldump / pg_dump；本包不假装能跨方言做一致快照。
package backup

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/ruanun/simple-ai-gateway/internal/config"
	"github.com/ruanun/simple-ai-gateway/internal/db"
	"github.com/ruanun/simple-ai-gateway/internal/model"
)

// Create 在 outDir 下生成一致性 SQLite 快照（VACUUM INTO），并复制本地审计目录。
// 返回备份文件路径。
func Create(cfg config.Config, outDir string) (string, error) {
	if !isSQLite(cfg) {
		return "", errors.New("自动备份仅支持 sqlite；mysql/postgres 请用 mysqldump/pg_dump")
	}
	if strings.TrimSpace(outDir) == "" {
		return "", errors.New("备份目录不能为空")
	}
	if err := os.MkdirAll(outDir, 0o700); err != nil {
		return "", fmt.Errorf("创建备份目录失败: %w", err)
	}
	name := "saig-" + time.Now().Format("20060102-150405") + ".db"
	dst := filepath.Join(outDir, name)

	g, err := db.Open(cfg.Database)
	if err != nil {
		return "", fmt.Errorf("打开数据库失败: %w", err)
	}
	defer func() {
		if sqlDB, e := g.DB(); e == nil {
			_ = sqlDB.Close()
		}
	}()
	// VACUUM INTO 生成一致性快照，不需要停写；路径按 SQLite 字面量转义单引号。
	quoted := strings.ReplaceAll(dst, "'", "''")
	if err := g.Exec("VACUUM INTO '" + quoted + "'").Error; err != nil {
		return "", fmt.Errorf("生成数据库快照失败: %w", err)
	}

	// 本地审计正文目录随包备份；S3 后端由对象存储自身生命周期管理，不在此复制。
	if isLocalStorage(cfg) && strings.TrimSpace(cfg.Storage.Local.Root) != "" {
		if _, err := os.Stat(cfg.Storage.Local.Root); err == nil {
			auditDir := filepath.Join(outDir, "audit")
			if err := copyDir(cfg.Storage.Local.Root, auditDir); err != nil {
				return "", fmt.Errorf("复制审计目录失败: %w", err)
			}
		} else if !os.IsNotExist(err) {
			return "", fmt.Errorf("检查审计目录失败: %w", err)
		}
	}
	return dst, nil
}

// Restore 用备份文件/目录恢复 SQLite 数据库。调用前必须停止网关进程。
// 目录形态会取其中第一个 *.db，并在存在 audit/ 时恢复本地审计目录。
func Restore(cfg config.Config, source string) error {
	if !isSQLite(cfg) {
		return errors.New("自动恢复仅支持 sqlite；mysql/postgres 请用对应数据库工具")
	}
	dbFile := source
	auditSrc := ""
	if info, err := os.Stat(source); err == nil && info.IsDir() {
		entries, err := os.ReadDir(source)
		if err != nil {
			return fmt.Errorf("读取备份目录失败: %w", err)
		}
		for _, e := range entries {
			if !e.IsDir() && strings.HasSuffix(strings.ToLower(e.Name()), ".db") {
				dbFile = filepath.Join(source, e.Name())
				break
			}
		}
		if _, err := os.Stat(filepath.Join(source, "audit")); err == nil {
			auditSrc = filepath.Join(source, "audit")
		}
	}
	if !strings.HasSuffix(strings.ToLower(dbFile), ".db") {
		return errors.New("备份目录中未找到 .db 文件")
	}
	if _, err := os.Stat(dbFile); err != nil {
		return fmt.Errorf("备份文件不可读: %w", err)
	}

	target := cfg.Database.DSN
	if strings.TrimSpace(target) == "" {
		return errors.New("database.dsn 为空")
	}
	if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
		return fmt.Errorf("创建数据库目录失败: %w", err)
	}
	// 先备份当前库再原子替换，恢复失败时仍可回退。
	if _, err := os.Stat(target); err == nil {
		prev := target + ".before-restore-" + time.Now().Format("20060102-150405")
		if err := copyFile(target, prev); err != nil {
			return fmt.Errorf("备份当前数据库失败: %w", err)
		}
	}
	tmp := target + ".restore.tmp"
	if err := copyFile(dbFile, tmp); err != nil {
		return fmt.Errorf("写入恢复临时文件失败: %w", err)
	}
	if err := os.Rename(tmp, target); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("替换数据库文件失败: %w", err)
	}
	if auditSrc != "" && isLocalStorage(cfg) && cfg.Storage.Local.Root != "" {
		if err := copyDir(auditSrc, cfg.Storage.Local.Root); err != nil {
			return fmt.Errorf("恢复审计目录失败: %w", err)
		}
	}

	// 跑一遍迁移，兼容旧备份；失败时保留已恢复文件并返回错误。
	g, err := db.Open(cfg.Database)
	if err != nil {
		return fmt.Errorf("打开恢复后的数据库失败: %w", err)
	}
	defer func() {
		if sqlDB, e := g.DB(); e == nil {
			_ = sqlDB.Close()
		}
	}()
	if err := db.Migrate(g, model.Migrations(cfg.Admin.Username, cfg.Admin.Password)); err != nil {
		return fmt.Errorf("恢复后迁移失败: %w", err)
	}
	return nil
}

func isSQLite(cfg config.Config) bool {
	d := strings.ToLower(strings.TrimSpace(cfg.Database.Driver))
	return d == "" || d == "sqlite"
}

func isLocalStorage(cfg config.Config) bool {
	t := strings.ToLower(strings.TrimSpace(cfg.Storage.Type))
	return t == "" || t == "local"
}

func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	if err := os.MkdirAll(filepath.Dir(dst), 0o700); err != nil {
		return err
	}
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		_ = out.Close()
		return err
	}
	return out.Close()
}

func copyDir(src, dst string) error {
	return filepath.WalkDir(src, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}
		target := filepath.Join(dst, rel)
		if d.IsDir() {
			return os.MkdirAll(target, 0o700)
		}
		return copyFile(path, target)
	})
}
