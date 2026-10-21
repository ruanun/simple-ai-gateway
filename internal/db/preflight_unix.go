//go:build !windows

package db

import (
	"fmt"
	"os"
	"strings"
	"syscall"
)

// ownerHint 输出一行可直接照做的诊断：进程 uid/gid、卡住那一级目录的属主与 mode、修复命令。
func ownerHint(dir string) string {
	uid, gid := os.Getuid(), os.Getgid()
	var b strings.Builder
	fmt.Fprintf(&b, "当前进程 uid=%d gid=%d", uid, gid)

	target := nearestExisting(dir)
	if fi, err := os.Stat(target); err == nil {
		owner := fmt.Sprintf("mode=%04o", fi.Mode().Perm())
		if st, ok := fi.Sys().(*syscall.Stat_t); ok {
			owner = fmt.Sprintf("属主 uid=%d gid=%d mode=%04o", st.Uid, st.Gid, fi.Mode().Perm())
		}
		if target == dir {
			fmt.Fprintf(&b, "，目录%s", owner)
		} else {
			fmt.Fprintf(&b, "，最近存在的上级目录 %s %s", target, owner)
		}
		b.WriteString(notDirNote(fi))
	}

	b.WriteString("；Docker bind mount 会沿用宿主目录的属主、覆盖镜像内的属主设置，")
	fmt.Fprintf(&b, "请在宿主执行 `chown -R %d:%d <宿主目录>`，或改用 named volume（见 docker-compose.yml）", uid, gid)
	return b.String()
}
