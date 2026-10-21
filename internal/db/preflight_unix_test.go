//go:build !windows

package db

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

// 复现 Docker bind mount 场景：目录存在但当前 uid 无写权限，
// 错误必须直接给出 uid/gid、属主与 chown 修复命令（root 绕过权限位，故跳过）。
func TestPreflightSQLiteDirUnwritableGivesOwnershipHint(t *testing.T) {
	if os.Getuid() == 0 {
		t.Skip("root 不受目录权限位限制")
	}
	dir := filepath.Join(t.TempDir(), "data")
	require.NoError(t, os.Mkdir(dir, 0o555))
	t.Cleanup(func() { _ = os.Chmod(dir, 0o755) })

	err := preflightSQLiteDir(dir)
	require.Error(t, err)
	msg := err.Error()
	require.Contains(t, msg, "不可写")
	require.Contains(t, msg, "uid=")
	require.Contains(t, msg, "属主")
	require.Contains(t, msg, "chown")
}
