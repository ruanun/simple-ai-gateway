package storage

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"
	"github.com/ruanun/simple-ai-gateway/internal/config"
	"github.com/stretchr/testify/require"
)

// S3 后端实测（环境变量门控，未设置时跳过，日常套件零依赖；沿用 dialect_migrate_test 先例）：
//
//	docker run -d --rm --name saig-test-minio -p 19000:9000 \
//	  -e MINIO_ROOT_USER=saigtest -e MINIO_ROOT_PASSWORD=saigtest123 minio/minio server /data
//	TEST_S3_ENDPOINT=127.0.0.1:19000 TEST_S3_BUCKET=saig-test \
//	TEST_S3_ACCESS_KEY=saigtest TEST_S3_SECRET_KEY=saigtest123 \
//	go test ./internal/storage/ -run TestS3 -v
//
// 桶不存在时由测试自行创建（生产代码不自动建桶）。
func s3TestConfig(t *testing.T) config.S3StorageConfig {
	t.Helper()
	ep := os.Getenv("TEST_S3_ENDPOINT")
	if ep == "" {
		t.Skip("未设置 TEST_S3_ENDPOINT，跳过 S3 实测")
	}
	cfg := config.S3StorageConfig{
		Endpoint: ep, Bucket: os.Getenv("TEST_S3_BUCKET"),
		AccessKey: os.Getenv("TEST_S3_ACCESS_KEY"), SecretKey: os.Getenv("TEST_S3_SECRET_KEY"),
		UseSSL: os.Getenv("TEST_S3_USE_SSL") == "true", PathStyle: true, Prefix: "unit",
	}
	require.NotEmpty(t, cfg.Bucket, "TEST_S3_BUCKET 必填")
	cli, err := minio.New(cfg.Endpoint, &minio.Options{
		Creds: credentials.NewStaticV4(cfg.AccessKey, cfg.SecretKey, ""), Secure: cfg.UseSSL, BucketLookup: minio.BucketLookupPath,
	})
	require.NoError(t, err)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	ok, err := cli.BucketExists(ctx, cfg.Bucket)
	require.NoError(t, err)
	if !ok {
		require.NoError(t, cli.MakeBucket(ctx, cfg.Bucket, minio.MakeBucketOptions{}))
	}
	return cfg
}

// TestS3RoundTrip Put/Get 往返、覆盖、NotFound、批量删除（含不存在 key）。
func TestS3RoundTrip(t *testing.T) {
	cfg := s3TestConfig(t)
	s, err := NewS3(cfg)
	require.NoError(t, err)
	ctx := context.Background()
	key := "audit/2026/09/03/s3test/request.gz"

	require.NoError(t, s.Put(ctx, key, []byte("v1")))
	require.NoError(t, s.Put(ctx, key, []byte("v2")))
	got, err := s.Get(ctx, key)
	require.NoError(t, err)
	require.Equal(t, []byte("v2"), got)

	require.NoError(t, s.Delete(ctx, []string{key, "audit/never/existed.gz"}))
	_, err = s.Get(ctx, key)
	require.ErrorIs(t, err, ErrNotFound)
}

// TestS3BucketMissingRejected 桶不存在时构造失败（启动期拒启语义）。
func TestS3BucketMissingRejected(t *testing.T) {
	cfg := s3TestConfig(t)
	cfg.Bucket = "saig-definitely-missing-bucket"
	_, err := NewS3(cfg)
	require.Error(t, err)
}
