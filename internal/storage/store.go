// Package storage 审计正文等大对象的存储抽象：本地目录 / S3 兼容对象存储。
// 内容无感知（gzip 等由调用方处理），key 为 `/` 分隔的相对路径。
package storage

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/ruanun/simple-ai-gateway/internal/config"
)

// ErrNotFound key 不存在。各实现统一映射，调用方用 errors.Is 判定。
var ErrNotFound = errors.New("storage: 对象不存在")

// Store 字节对象存储。key 约定：不以 / 开头、不含空段/./..、不含反斜杠。
type Store interface {
	Put(ctx context.Context, key string, data []byte) error
	Get(ctx context.Context, key string) ([]byte, error) // 不存在返回 ErrNotFound
	Delete(ctx context.Context, keys []string) error     // 批量；不存在视为成功
	// Health 就绪探活：local 检查根目录，s3 检查 bucket 可达；供 /readyz 使用。
	Health(ctx context.Context) error
}

// New 按配置构造后端。type 空视为 local；local.root 为空、type 未知、s3 必填项缺失或桶不可达
// 时返回 error，由装配点透传致进程拒启（与安全护栏同级：配置错误启动即暴露）。
func New(cfg config.StorageConfig) (Store, error) {
	switch strings.ToLower(strings.TrimSpace(cfg.Type)) {
	case "", "local":
		return NewLocal(cfg.Local.Root)
	case "s3":
		return NewS3(cfg.S3)
	default:
		return nil, fmt.Errorf("storage.type 不支持: %q（可选 local / s3）", cfg.Type)
	}
}

// validateKey 统一 key 校验：拒绝空、绝对路径、空段、. / .. 段与反斜杠，防路径穿越。
func validateKey(key string) error {
	if key == "" || strings.HasPrefix(key, "/") || strings.ContainsRune(key, '\\') {
		return fmt.Errorf("storage: 非法 key %q", key)
	}
	for _, seg := range strings.Split(key, "/") {
		if seg == "" || seg == "." || seg == ".." {
			return fmt.Errorf("storage: 非法 key %q", key)
		}
	}
	return nil
}
