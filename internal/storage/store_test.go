package storage

import (
	"context"
	"testing"

	"github.com/ruanun/simple-ai-gateway/internal/config"
	"github.com/stretchr/testify/require"
)

// TestNewFactoryLocal type 空或 local 都走本地实现。
func TestNewFactoryLocal(t *testing.T) {
	for _, typ := range []string{"", "local", "LOCAL"} {
		s, err := New(config.StorageConfig{Type: typ, Local: config.LocalStorageConfig{Root: t.TempDir()}})
		require.NoError(t, err, "type=%q", typ)
		require.IsType(t, &Local{}, s)
	}
}

// TestNewFactoryRejects 未知 type / 空 root 拒绝。
func TestNewFactoryRejects(t *testing.T) {
	_, err := New(config.StorageConfig{Type: "ftp"})
	require.Error(t, err)
	_, err = New(config.StorageConfig{Type: "local"})
	require.Error(t, err, "local.root 为空应拒绝")
}

// TestMemoryStore 内存实现：往返、NotFound、删除、计数。
func TestMemoryStore(t *testing.T) {
	m := NewMemory()
	ctx := context.Background()
	require.NoError(t, m.Put(ctx, "k1", []byte("v1")))
	require.NoError(t, m.Put(ctx, "k2", []byte("v2")))
	got, err := m.Get(ctx, "k1")
	require.NoError(t, err)
	require.Equal(t, []byte("v1"), got)
	_, err = m.Get(ctx, "missing")
	require.ErrorIs(t, err, ErrNotFound)
	require.Equal(t, []string{"k1", "k2"}, m.Keys())
	require.NoError(t, m.Delete(ctx, []string{"k1", "missing"}))
	require.Equal(t, 1, m.Len())
	require.Equal(t, int64(2), m.Puts())
	require.Equal(t, int64(2), m.Gets())

	// 非法 key 报错（与 Put/Get 校验对称），且不影响同批次合法 key 的删除。
	require.NoError(t, m.Put(ctx, "k3", []byte("v3")))
	require.Error(t, m.Delete(ctx, []string{"../bad"}))
	require.Error(t, m.Delete(ctx, []string{"k3", "../bad"}), "混合非法 key 时批次整体应报错")
	_, err = m.Get(ctx, "k3")
	require.ErrorIs(t, err, ErrNotFound, "合法 key 应已被删除，不受同批次非法 key 影响")
}

// TestMemoryPutClones Put 后修改原切片不影响已存内容。
func TestMemoryPutClones(t *testing.T) {
	m := NewMemory()
	src := []byte("abc")
	require.NoError(t, m.Put(context.Background(), "k", src))
	src[0] = 'z'
	got, _ := m.Get(context.Background(), "k")
	require.Equal(t, []byte("abc"), got)
}

// TestNewFactoryS3RequiresEndpointAndBucket type=s3 缺 endpoint/bucket 拒绝（不触网）。
func TestNewFactoryS3RequiresEndpointAndBucket(t *testing.T) {
	_, err := New(config.StorageConfig{Type: "s3"})
	require.Error(t, err)
	_, err = New(config.StorageConfig{Type: "s3", S3: config.S3StorageConfig{Endpoint: "127.0.0.1:1"}})
	require.Error(t, err, "缺 bucket 应拒绝")
}
