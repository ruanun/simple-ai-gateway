package db

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/ruanun/simple-ai-gateway/internal/config"
	"github.com/stretchr/testify/require"
)

func TestSQLiteDataDir(t *testing.T) {
	cases := []struct {
		name string
		dsn  string
		want string
	}{
		{"相对路径", filepath.Join("data", "saig.db"), "data"},
		{"当前目录", "saig.db", "."},
		{"file 前缀带查询参数", "file:data/saig.db?_pragma=busy_timeout(5000)", "data"},
		{"共享内存库跳过", "file::memory:?cache=shared", ""},
		{"裸内存库跳过", ":memory:", ""},
		{"命名内存库跳过", "file:memdb_x?mode=memory&cache=shared", ""},
		{"空 DSN 跳过", "", ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			require.Equal(t, c.want, sqliteDataDir(c.dsn))
		})
	}
}

func TestPreflightSQLiteDirCreatesAndCleansProbe(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "nested", "data")
	require.NoError(t, preflightSQLiteDir(dir))

	fi, err := os.Stat(dir)
	require.NoError(t, err)
	require.True(t, fi.IsDir())
	// 探针文件必须已删除，不能在数据目录留垃圾
	_, err = os.Stat(filepath.Join(dir, probeName))
	require.True(t, os.IsNotExist(err))
}

// 数据目录路径被一个普通文件占用：跨平台都会失败，用于验证错误信息带上目录与诊断提示。
func TestPreflightSQLiteDirReportsPathConflict(t *testing.T) {
	occupied := filepath.Join(t.TempDir(), "data")
	require.NoError(t, os.WriteFile(occupied, []byte("x"), 0o600))

	err := preflightSQLiteDir(occupied)
	require.Error(t, err)
	require.Contains(t, err.Error(), "数据目录")
	require.Contains(t, err.Error(), occupied)
}

// Open 走 sqlite 分支时应把目录自检失败原样抛出，而不是让驱动报 out of memory (14)。
func TestOpenSQLitePreflightFailure(t *testing.T) {
	occupied := filepath.Join(t.TempDir(), "data")
	require.NoError(t, os.WriteFile(occupied, []byte("x"), 0o600))

	_, err := Open(config.DatabaseConfig{Driver: "sqlite", DSN: filepath.Join(occupied, "saig.db")})
	require.Error(t, err)
	require.Contains(t, err.Error(), "数据目录")
	require.NotContains(t, err.Error(), "out of memory")
}
