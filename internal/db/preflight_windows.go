//go:build windows

package db

import (
	"fmt"
	"os"
)

// ownerHint 是 Windows 下的诊断信息：无 uid/gid 概念，只报权限位并指向 ACL 与 dsn 配置。
func ownerHint(dir string) string {
	target := nearestExisting(dir)
	fi, err := os.Stat(target)
	if err != nil {
		return "请确认当前用户对该目录有写权限，或把 database.dsn 指向可写路径"
	}
	return fmt.Sprintf("路径 %s mode=%04o%s；请确认当前用户对该目录有写权限（检查 ACL），或把 database.dsn 指向可写路径",
		target, fi.Mode().Perm(), notDirNote(fi))
}
