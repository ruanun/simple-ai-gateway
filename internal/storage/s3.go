package storage

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"
	"github.com/ruanun/simple-ai-gateway/internal/config"
)

// S3 兼容对象存储实现（AWS S3 / MinIO / Cloudflare R2 / 阿里 OSS / 腾讯 COS）。
// 选用 minio-go：单模块、体积小、面向 S3 兼容生态；aws-sdk-go-v2 默认附加 CRC 校验头，
// 多家兼容存储需额外关闭且依赖树大得多。
type S3 struct {
	cli    *minio.Client
	bucket string
	prefix string // 规范化为空或以 / 结尾
}

const s3ProbeTimeout = 10 * time.Second

// NewS3 构造并探活：endpoint/bucket 必填；access_key 为空走 环境变量/凭证文件/IAM 链；
// 启动期 BucketExists 一次，桶不存在或不可达即返回 error（本项目不自动建桶）。
func NewS3(cfg config.S3StorageConfig) (*S3, error) {
	if cfg.Endpoint == "" || cfg.Bucket == "" {
		return nil, errors.New("storage.s3.endpoint 与 storage.s3.bucket 必填")
	}
	var creds *credentials.Credentials
	if cfg.AccessKey != "" {
		creds = credentials.NewStaticV4(cfg.AccessKey, cfg.SecretKey, "")
	} else {
		creds = credentials.NewChainCredentials([]credentials.Provider{
			&credentials.EnvAWS{}, &credentials.EnvMinio{}, &credentials.FileAWSCredentials{}, &credentials.IAM{},
		})
	}
	lookup := minio.BucketLookupAuto
	if cfg.PathStyle {
		lookup = minio.BucketLookupPath
	}
	cli, err := minio.New(cfg.Endpoint, &minio.Options{Creds: creds, Secure: cfg.UseSSL, Region: cfg.Region, BucketLookup: lookup})
	if err != nil {
		return nil, fmt.Errorf("初始化 S3 客户端失败: %w", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), s3ProbeTimeout)
	defer cancel()
	ok, err := cli.BucketExists(ctx, cfg.Bucket)
	if err != nil {
		return nil, fmt.Errorf("探测 S3 bucket %q 失败: %w", cfg.Bucket, err)
	}
	if !ok {
		return nil, fmt.Errorf("S3 bucket %q 不存在（本项目不自动建桶，请先创建）", cfg.Bucket)
	}
	prefix := strings.Trim(cfg.Prefix, "/")
	if prefix != "" {
		prefix += "/"
	}
	return &S3{cli: cli, bucket: cfg.Bucket, prefix: prefix}, nil
}

func (s *S3) objectName(key string) string { return s.prefix + key }

// Health 就绪探活：bucket 仍可达。使用调用方 ctx（/readyz 已限 2s），避免探活拖慢就绪判断。
func (s *S3) Health(ctx context.Context) error {
	ok, err := s.cli.BucketExists(ctx, s.bucket)
	if err != nil {
		return fmt.Errorf("探测 S3 bucket %q 失败: %w", s.bucket, err)
	}
	if !ok {
		return fmt.Errorf("S3 bucket %q 不存在", s.bucket)
	}
	return nil
}

// Put 单次上传（正文受 audit.max_body_bytes 约束，无需分片）。
func (s *S3) Put(ctx context.Context, key string, data []byte) error {
	if err := validateKey(key); err != nil {
		return err
	}
	_, err := s.cli.PutObject(ctx, s.bucket, s.objectName(key), bytes.NewReader(data), int64(len(data)),
		minio.PutObjectOptions{ContentType: "application/gzip"})
	return err
}

// Get 读整个对象。GetObject 惰性，NoSuchKey 在 Read 时才出现，故在 ReadAll 后映射。
func (s *S3) Get(ctx context.Context, key string) ([]byte, error) {
	if err := validateKey(key); err != nil {
		return nil, err
	}
	obj, err := s.cli.GetObject(ctx, s.bucket, s.objectName(key), minio.GetObjectOptions{})
	if err != nil {
		return nil, err
	}
	defer obj.Close()
	b, err := io.ReadAll(obj)
	if err != nil {
		if minio.ToErrorResponse(err).Code == "NoSuchKey" {
			return nil, ErrNotFound
		}
		return nil, err
	}
	return b, nil
}

// Delete 批量删除（SDK 内部按 1000 分批）；遍历错误通道聚合非 NoSuchKey 的失败。
func (s *S3) Delete(ctx context.Context, keys []string) error {
	if len(keys) == 0 {
		return nil
	}
	objCh := make(chan minio.ObjectInfo, len(keys))
	var errs []error
	// 非法 key 累进错误后 continue（与 Local.Delete 对齐）：一条坏 key 不该让同批合法 key 一个都删不掉，
	// 否则清理任务会被这批记录永久卡住。
	for _, k := range keys {
		if err := validateKey(k); err != nil {
			errs = append(errs, err)
			continue
		}
		objCh <- minio.ObjectInfo{Key: s.objectName(k)}
	}
	close(objCh)
	for e := range s.cli.RemoveObjects(ctx, s.bucket, objCh, minio.RemoveObjectsOptions{}) {
		if e.Err != nil && minio.ToErrorResponse(e.Err).Code != "NoSuchKey" {
			errs = append(errs, fmt.Errorf("删除 %s 失败: %w", e.ObjectName, e.Err))
		}
	}
	return errors.Join(errs...)
}
