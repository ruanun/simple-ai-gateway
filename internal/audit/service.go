package audit

import (
	"context"
	"fmt"
	"time"

	"github.com/ruanun/simple-ai-gateway/internal/model"
	"github.com/ruanun/simple-ai-gateway/internal/storage"
	"gorm.io/gorm"
)

// cleanupBatch 每批清理的记录数；对象批量删除与行删除按批推进，失败批次留待下轮。
const cleanupBatch = 200

// Service 审计读路径（正文取回）与超期清理。列表/详情的元数据直接读 AuditRecord，
// 只有点开正文时才经 FetchBody 去存储层取一次。
type Service struct {
	g     *gorm.DB
	store storage.Store
}

// NewService 构造审计 service。
func NewService(g *gorm.DB, store storage.Store) *Service { return &Service{g: g, store: store} }

// FetchBody 按对象 key 取回正文并解压。key 为空视为不存在（storage.ErrNotFound）。
// 内部对存储层调用套 storeOpTimeout 上限：S3 停滞时管理面详情请求最多等这么久，
// 不会被上游 hang 住整个 handler（调用方 ctx 更早取消/超时时以调用方为准）。
func (s *Service) FetchBody(ctx context.Context, key string) ([]byte, error) {
	if key == "" {
		return nil, storage.ErrNotFound
	}
	ctx, cancel := context.WithTimeout(ctx, storeOpTimeout)
	defer cancel()
	raw, err := s.store.Get(ctx, key)
	if err != nil {
		return nil, err
	}
	return gunzipBytes(raw)
}

// Cleanup 分批删除 createdBefore 之前的审计记录：先批量删存储层对象，成功后再删行。
// 对象删除失败时该批行保留并返回错误（下轮重试），避免行没了对象还在的孤儿状态。返回删除行数。
// 反向不对称：对象删成功而行删失败时，行会指向已删对象，下轮清理对不存在 key 幂等、行再删；
// 期间 FetchBody 返回 ErrNotFound 走空正文。
func (s *Service) Cleanup(createdBefore time.Time) (int64, error) {
	var n int64
	for {
		var recs []model.AuditRecord
		if err := s.g.Select("id", "request_key", "response_key").
			Where("created_at < ?", createdBefore).Order("id").Limit(cleanupBatch).Find(&recs).Error; err != nil {
			return n, err
		}
		if len(recs) == 0 {
			return n, nil
		}
		ids := make([]uint64, 0, len(recs))
		keys := make([]string, 0, 2*len(recs))
		for _, r := range recs {
			ids = append(ids, r.ID)
			if r.RequestKey != "" {
				keys = append(keys, r.RequestKey)
			}
			if r.ResponseKey != "" {
				keys = append(keys, r.ResponseKey)
			}
		}
		ctx, cancel := context.WithTimeout(context.Background(), storeOpTimeout)
		err := s.store.Delete(ctx, keys)
		cancel()
		if err != nil {
			return n, fmt.Errorf("删除审计对象失败（行保留待下轮重试）: %w", err)
		}
		res := s.g.Where("id IN ?", ids).Delete(&model.AuditRecord{})
		if res.Error != nil {
			return n, res.Error
		}
		n += res.RowsAffected
	}
}
