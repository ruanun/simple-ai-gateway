package storage

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestLocalRoundTrip Put 后 Get 原样读回；嵌套目录自动创建；目标文件权限 0600。
func TestLocalRoundTrip(t *testing.T) {
	root := t.TempDir()
	s, err := NewLocal(root)
	require.NoError(t, err)
	ctx := context.Background()

	require.NoError(t, s.Put(ctx, "audit/2026/09/03/abc/request.gz", []byte("hello")))
	got, err := s.Get(ctx, "audit/2026/09/03/abc/request.gz")
	require.NoError(t, err)
	require.Equal(t, []byte("hello"), got)

	// 文件落在 root 下对应路径
	_, err = os.Stat(filepath.Join(root, "audit", "2026", "09", "03", "abc", "request.gz"))
	require.NoError(t, err)
}

// TestLocalPutOverwritesAtomically 同 key 二次 Put 覆盖，且目录里不残留 .tmp 文件。
func TestLocalPutOverwritesAtomically(t *testing.T) {
	root := t.TempDir()
	s, err := NewLocal(root)
	require.NoError(t, err)
	ctx := context.Background()

	require.NoError(t, s.Put(ctx, "a/b.gz", []byte("v1")))
	require.NoError(t, s.Put(ctx, "a/b.gz", []byte("v2")))
	got, err := s.Get(ctx, "a/b.gz")
	require.NoError(t, err)
	require.Equal(t, []byte("v2"), got)

	entries, err := os.ReadDir(filepath.Join(root, "a"))
	require.NoError(t, err)
	require.Len(t, entries, 1, "不应残留临时文件: %v", entries)
	require.Equal(t, "b.gz", entries[0].Name())
}

// TestLocalGetNotFound 不存在的 key 返回 ErrNotFound。
func TestLocalGetNotFound(t *testing.T) {
	s, err := NewLocal(t.TempDir())
	require.NoError(t, err)
	_, err = s.Get(context.Background(), "nope/x.gz")
	require.ErrorIs(t, err, ErrNotFound)
}

// TestLocalDeleteIdempotent 批量删除：存在的删掉，不存在的视为成功。
func TestLocalDeleteIdempotent(t *testing.T) {
	s, err := NewLocal(t.TempDir())
	require.NoError(t, err)
	ctx := context.Background()
	require.NoError(t, s.Put(ctx, "d/1.gz", []byte("1")))
	require.NoError(t, s.Put(ctx, "d/2.gz", []byte("2")))

	require.NoError(t, s.Delete(ctx, []string{"d/1.gz", "d/2.gz", "d/missing.gz"}))
	_, err = s.Get(ctx, "d/1.gz")
	require.ErrorIs(t, err, ErrNotFound)
	_, err = s.Get(ctx, "d/2.gz")
	require.ErrorIs(t, err, ErrNotFound)
	require.NoError(t, s.Delete(ctx, nil), "空列表应直接成功")
}

// TestLocalRejectsBadKey 防路径穿越：空 key、前导 /、.. 段、反斜杠一律拒绝，且不落文件。
func TestLocalRejectsBadKey(t *testing.T) {
	root := t.TempDir()
	s, err := NewLocal(root)
	require.NoError(t, err)
	ctx := context.Background()
	for _, k := range []string{"", "/abs.gz", "../escape.gz", "a/../../escape.gz", "a\\b.gz", "a//b.gz", "./x.gz"} {
		require.Error(t, s.Put(ctx, k, []byte("x")), "key=%q 应被拒绝", k)
		_, err := s.Get(ctx, k)
		require.Error(t, err, "Get key=%q 应被拒绝", k)
		require.Error(t, s.Delete(ctx, []string{k}), "Delete key=%q 应被拒绝", k)
	}
	// 父目录未被写入任何文件
	entries, err := os.ReadDir(filepath.Dir(root))
	require.NoError(t, err)
	for _, e := range entries {
		require.NotEqual(t, "escape.gz", e.Name())
	}
}

// TestNewLocalRejectsEmptyRoot root 为空拒绝构造（防止零值配置把审计写进进程当前目录）。
func TestNewLocalRejectsEmptyRoot(t *testing.T) {
	_, err := NewLocal("")
	require.Error(t, err)
}

// TestNewLocalCreatesRoot 构造期即创建根目录，权限问题启动时暴露。
func TestNewLocalCreatesRoot(t *testing.T) {
	root := filepath.Join(t.TempDir(), "nested", "audit")
	_, err := NewLocal(root)
	require.NoError(t, err)
	st, err := os.Stat(root)
	require.NoError(t, err)
	require.True(t, st.IsDir())
}

// TestLocalDeleteRemovesEmptyLeafDir 删完一个 request 目录下的两个对象后，该叶子目录随之回收，
// 上层日期目录与其它 request 目录不受影响（key 布局每请求一目录，不回收会让 inode 无界增长）。
func TestLocalDeleteRemovesEmptyLeafDir(t *testing.T) {
	root := t.TempDir()
	s, err := NewLocal(root)
	require.NoError(t, err)
	ctx := context.Background()

	require.NoError(t, s.Put(ctx, "a/2026/09/03/req1/request.gz", []byte("q")))
	require.NoError(t, s.Put(ctx, "a/2026/09/03/req1/response.gz", []byte("p")))
	require.NoError(t, s.Delete(ctx, []string{"a/2026/09/03/req1/request.gz", "a/2026/09/03/req1/response.gz"}))

	_, err = os.Stat(filepath.Join(root, "a", "2026", "09", "03", "req1"))
	require.True(t, os.IsNotExist(err), "空的 request 叶子目录应被回收")
	st, err := os.Stat(filepath.Join(root, "a", "2026", "09", "03"))
	require.NoError(t, err, "上层日期目录不应向上递归删除")
	require.True(t, st.IsDir())

	// 同日两个 request 目录，只删其一，另一个及其文件完好
	require.NoError(t, s.Put(ctx, "a/2026/09/03/req2/request.gz", []byte("q2")))
	require.NoError(t, s.Put(ctx, "a/2026/09/03/req2/response.gz", []byte("p2")))
	require.NoError(t, s.Put(ctx, "a/2026/09/03/req3/request.gz", []byte("q3")))
	require.NoError(t, s.Put(ctx, "a/2026/09/03/req3/response.gz", []byte("p3")))
	require.NoError(t, s.Delete(ctx, []string{"a/2026/09/03/req2/request.gz", "a/2026/09/03/req2/response.gz"}))

	_, err = os.Stat(filepath.Join(root, "a", "2026", "09", "03", "req2"))
	require.True(t, os.IsNotExist(err), "被清空的 req2 目录应被回收")
	got, err := s.Get(ctx, "a/2026/09/03/req3/request.gz")
	require.NoError(t, err, "同日其它 request 目录不受影响")
	require.Equal(t, []byte("q3"), got)
	got, err = s.Get(ctx, "a/2026/09/03/req3/response.gz")
	require.NoError(t, err)
	require.Equal(t, []byte("p3"), got)
}
