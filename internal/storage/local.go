package storage

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
)

// Local 本地文件系统实现。key 映射为 root 下的相对路径；写入走「临时文件 + rename」，
// 同目录 rename 原子，读方不会看到半截文件。不 fsync：审计为旁路数据，与 SQLite WAL 默认持久性同级。
type Local struct{ root string }

// NewLocal 构造本地后端。root 为空拒绝（防止零值配置把审计写进进程当前目录）；
// 非空则立即建目录（0700），权限问题在启动期而非首条审计时暴露。
func NewLocal(root string) (*Local, error) {
	if root == "" {
		return nil, errors.New("storage.local.root 不能为空")
	}
	if err := os.MkdirAll(root, 0o700); err != nil {
		return nil, fmt.Errorf("创建审计存储目录 %s 失败: %w", root, err)
	}
	return &Local{root: root}, nil
}

func (l *Local) path(key string) string { return filepath.Join(l.root, filepath.FromSlash(key)) }

// Health 就绪探活：根目录必须仍存在且是目录（权限问题在 NewLocal 启动期已暴露）。
func (l *Local) Health(_ context.Context) error {
	info, err := os.Stat(l.root)
	if err != nil {
		return fmt.Errorf("审计存储目录不可访问: %w", err)
	}
	if !info.IsDir() {
		return fmt.Errorf("审计存储根路径不是目录: %s", l.root)
	}
	return nil
}

// Put 原子写：同目录临时文件（CreateTemp 缺省 0600）写满后 rename 覆盖目标。
func (l *Local) Put(_ context.Context, key string, data []byte) error {
	if err := validateKey(key); err != nil {
		return err
	}
	dst := l.path(key)
	dir := filepath.Dir(dst)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("创建目录 %s 失败: %w", dir, err)
	}
	tmp, err := os.CreateTemp(dir, filepath.Base(dst)+".tmp.*")
	if err != nil {
		return fmt.Errorf("创建临时文件失败: %w", err)
	}
	tmpName := tmp.Name()
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		_ = os.Remove(tmpName)
		return fmt.Errorf("写入 %s 失败: %w", tmpName, err)
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmpName)
		return fmt.Errorf("关闭 %s 失败: %w", tmpName, err)
	}
	if err := os.Rename(tmpName, dst); err != nil {
		_ = os.Remove(tmpName)
		return fmt.Errorf("落盘 %s 失败: %w", dst, err)
	}
	return nil
}

// Get 读整个文件；不存在映射为 ErrNotFound。
func (l *Local) Get(_ context.Context, key string) ([]byte, error) {
	if err := validateKey(key); err != nil {
		return nil, err
	}
	b, err := os.ReadFile(l.path(key))
	if errors.Is(err, fs.ErrNotExist) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return b, nil
}

// Delete 逐个删除；不存在忽略，其余错误聚合返回（调用方据此保留 DB 行下轮重试）。
// 顺手回收叶子空目录：key 布局是每个请求一个目录（audit/YYYY/MM/DD/<request_id>/），
// 只删文件会让空的 request 目录永久残留、inode 无界增长。
func (l *Local) Delete(_ context.Context, keys []string) error {
	var errs []error
	for _, k := range keys {
		if err := validateKey(k); err != nil {
			errs = append(errs, err)
			continue
		}
		p := l.path(k)
		if err := os.Remove(p); err != nil && !errors.Is(err, fs.ErrNotExist) {
			errs = append(errs, fmt.Errorf("删除 %s 失败: %w", k, err))
			continue
		}
		// best-effort 收掉这一层叶子目录：非空（同请求另一个对象尚在）时 os.Remove 报错，直接忽略。
		// 不向上递归——日期目录可能正被同日写入使用，递归删会与写入竞态。
		_ = os.Remove(filepath.Dir(p))
	}
	return errors.Join(errs...)
}
