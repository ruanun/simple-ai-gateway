package db

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// probeName 是数据目录可写性探针文件名，创建后立即删除。
const probeName = ".saig-write-probe"

// sqliteDataDir 从 SQLite DSN 解析出数据文件所在目录。
// 内存库（`:memory:` / `mode=memory`）或无法判定时返回空串，表示跳过自检。
func sqliteDataDir(dsn string) string {
	if dsn == "" || strings.Contains(dsn, "mode=memory") {
		return ""
	}
	path := strings.TrimPrefix(dsn, "file:")
	// 去掉 `?_pragma=...` 这类查询参数，只留文件路径部分
	if i := strings.IndexByte(path, '?'); i >= 0 {
		path = path[:i]
	}
	if path == "" || strings.HasPrefix(path, ":memory:") {
		return ""
	}
	return filepath.Dir(path)
}

// preflightSQLiteDir 在打开 SQLite 前自检数据目录：不存在则创建，随后写一个探针文件验证可写。
//
// 之所以自己先探一次：直接把不可写的目录交给驱动，只会冒出 `out of memory (14)` 这类
// SQLITE_CANTOPEN 的包装错误，把排查方向带到内存/OOM 上，而真因是权限。
// Docker bind mount 沿用宿主目录属主、覆盖镜像内设置，是最常见的触发场景，
// 因此错误信息直接给出进程 uid/gid、目录属主与 mode，以及可照抄的修复命令。
func preflightSQLiteDir(dir string) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("数据目录 %s 创建失败：%w；%s", dir, err, ownerHint(dir))
	}
	probe := filepath.Join(dir, probeName)
	f, err := os.OpenFile(probe, os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return fmt.Errorf("数据目录 %s 不可写：%w；%s", dir, err, ownerHint(dir))
	}
	_ = f.Close()
	_ = os.Remove(probe)
	return nil
}

// notDirNote 在路径被同名文件占用时补一句说明（此时 MkdirAll 必然失败）。
func notDirNote(fi os.FileInfo) string {
	if fi != nil && !fi.IsDir() {
		return "（该路径被同名文件占用，不是目录）"
	}
	return ""
}

// nearestExisting 返回 path 自身或其最近的、已存在的祖先目录，
// 用于目录尚未创建时仍能报出真正卡住的那一级的属主与权限。
func nearestExisting(path string) string {
	for {
		if _, err := os.Stat(path); err == nil {
			return path
		}
		parent := filepath.Dir(path)
		if parent == path {
			return path
		}
		path = parent
	}
}
