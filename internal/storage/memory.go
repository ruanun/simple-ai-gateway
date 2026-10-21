package storage

import (
	"bytes"
	"context"
	"errors"
	"sort"
	"sync"
	"sync/atomic"
)

// Memory 进程内存实现，供测试注入；带调用计数便于断言「无权限角色不触发读取」等行为。
// 不进 config 枚举，生产不可用。
type Memory struct {
	mu   sync.RWMutex
	objs map[string][]byte
	gets atomic.Int64
	puts atomic.Int64
}

// NewMemory 构造空的内存存储。
func NewMemory() *Memory { return &Memory{objs: map[string][]byte{}} }

// Put 复制 data 存入（切断与调用方切片的共享）。
func (m *Memory) Put(_ context.Context, key string, data []byte) error {
	if err := validateKey(key); err != nil {
		return err
	}
	m.puts.Add(1)
	m.mu.Lock()
	defer m.mu.Unlock()
	m.objs[key] = bytes.Clone(data)
	return nil
}

// Get 返回副本；不存在返回 ErrNotFound。
func (m *Memory) Get(_ context.Context, key string) ([]byte, error) {
	if err := validateKey(key); err != nil {
		return nil, err
	}
	m.gets.Add(1)
	m.mu.RLock()
	defer m.mu.RUnlock()
	b, ok := m.objs[key]
	if !ok {
		return nil, ErrNotFound
	}
	return bytes.Clone(b), nil
}

// Delete 批量删除，不存在视为成功；非法 key 报错（与 Put/Get 校验对称，
// 且与 Local.Delete 语义一致：合法 key 照删，错误聚合返回不影响其它 key）。
func (m *Memory) Delete(_ context.Context, keys []string) error {
	var errs []error
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, k := range keys {
		if err := validateKey(k); err != nil {
			errs = append(errs, err)
			continue
		}
		delete(m.objs, k)
	}
	return errors.Join(errs...)
}

// Health 就绪探活：内存实现恒可用。
func (m *Memory) Health(_ context.Context) error { return nil }

// Gets 累计 Get 调用次数。
func (m *Memory) Gets() int64 { return m.gets.Load() }

// Puts 累计 Put 调用次数。
func (m *Memory) Puts() int64 { return m.puts.Load() }

// Len 当前对象数。
func (m *Memory) Len() int {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return len(m.objs)
}

// Keys 当前全部 key（升序），供断言。
func (m *Memory) Keys() []string {
	m.mu.RLock()
	defer m.mu.RUnlock()
	ks := make([]string, 0, len(m.objs))
	for k := range m.objs {
		ks = append(ks, k)
	}
	sort.Strings(ks)
	return ks
}
