package audit

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/ruanun/simple-ai-gateway/internal/dbtest"
	"github.com/ruanun/simple-ai-gateway/internal/model"
	"github.com/ruanun/simple-ai-gateway/internal/storage"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func newTestService(t *testing.T, store storage.Store) (*gorm.DB, *Service) {
	t.Helper()
	g := dbtest.OpenInMemory(t)
	require.NoError(t, g.AutoMigrate(&model.AuditRecord{}))
	return g, NewService(g, store)
}

// seedAudit 造一条 days 天前的审计记录并在 store 里放好两个对象。
func seedAudit(t *testing.T, g *gorm.DB, store storage.Store, reqID string, days int) model.AuditRecord {
	t.Helper()
	reqKey, respKey := objectKeys(reqID, time.Now().AddDate(0, 0, -days))
	require.NoError(t, store.Put(context.Background(), reqKey, gzipBytes([]byte("req-"+reqID))))
	require.NoError(t, store.Put(context.Background(), respKey, gzipBytes([]byte("resp-"+reqID))))
	rec := model.AuditRecord{RequestID: reqID, RequestKey: reqKey, ResponseKey: respKey}
	require.NoError(t, g.Create(&rec).Error)
	require.NoError(t, g.Model(&rec).Update("created_at", time.Now().AddDate(0, 0, -days)).Error)
	return rec
}

// TestFetchBody 取回并解压；空 key 与不存在 key 都返回 storage.ErrNotFound。
func TestFetchBody(t *testing.T) {
	mem := storage.NewMemory()
	g, svc := newTestService(t, mem)
	rec := seedAudit(t, g, mem, "fb1", 0)

	b, err := svc.FetchBody(context.Background(), rec.RequestKey)
	require.NoError(t, err)
	require.Equal(t, "req-fb1", string(b))

	_, err = svc.FetchBody(context.Background(), "")
	require.ErrorIs(t, err, storage.ErrNotFound)
	_, err = svc.FetchBody(context.Background(), "audit/2026/01/01/none/request.gz")
	require.ErrorIs(t, err, storage.ErrNotFound)
}

// TestCleanupDeletesObjectsAndRows 只删过期记录及其对象，未过期原样保留。
func TestCleanupDeletesObjectsAndRows(t *testing.T) {
	mem := storage.NewMemory()
	g, svc := newTestService(t, mem)
	old := seedAudit(t, g, mem, "old", 100)
	fresh := seedAudit(t, g, mem, "fresh", 1)

	n, err := svc.Cleanup(time.Now().AddDate(0, 0, -30))
	require.NoError(t, err)
	require.Equal(t, int64(1), n)

	var cnt int64
	g.Model(&model.AuditRecord{}).Count(&cnt)
	require.Equal(t, int64(1), cnt)
	_, err = mem.Get(context.Background(), old.RequestKey)
	require.ErrorIs(t, err, storage.ErrNotFound)
	_, err = mem.Get(context.Background(), old.ResponseKey)
	require.ErrorIs(t, err, storage.ErrNotFound)
	_, err = mem.Get(context.Background(), fresh.RequestKey)
	require.NoError(t, err, "未过期对象应保留")
}

// TestCleanupBatches 超过一批（200）的过期记录分多批全部删除。
func TestCleanupBatches(t *testing.T) {
	mem := storage.NewMemory()
	g, svc := newTestService(t, mem)
	for i := 0; i < cleanupBatch+5; i++ {
		seedAudit(t, g, mem, fmt.Sprintf("b-%03d", i), 100)
	}
	n, err := svc.Cleanup(time.Now().AddDate(0, 0, -30))
	require.NoError(t, err)
	require.Equal(t, int64(cleanupBatch+5), n)
	require.Equal(t, 0, mem.Len())
}

// deleteFailStore Delete 恒失败。
type deleteFailStore struct{ *storage.Memory }

func (d *deleteFailStore) Delete(context.Context, []string) error {
	return errors.New("模拟删除失败")
}

// TestCleanupKeepsRowsWhenObjectDeleteFails 对象删除失败 → 返回错误、行保留（下轮重试，杜绝孤儿对象）。
func TestCleanupKeepsRowsWhenObjectDeleteFails(t *testing.T) {
	fs := &deleteFailStore{Memory: storage.NewMemory()}
	g, svc := newTestService(t, fs)
	seedAudit(t, g, fs, "keep", 100)

	n, err := svc.Cleanup(time.Now().AddDate(0, 0, -30))
	require.Error(t, err)
	require.Zero(t, n)
	var cnt int64
	g.Model(&model.AuditRecord{}).Count(&cnt)
	require.Equal(t, int64(1), cnt, "对象未删成功时行必须保留")
	require.Equal(t, 2, fs.Len())
}

// blockingStore Get 阻塞直到 ctx 结束，并记录收到的 ctx 是否带 deadline。
type blockingStore struct {
	*storage.Memory
	hadDeadline bool
}

func (b *blockingStore) Get(ctx context.Context, _ string) ([]byte, error) {
	_, b.hadDeadline = ctx.Deadline()
	<-ctx.Done()
	return nil, ctx.Err()
}

// TestFetchBodyRespectsContext 存储层停滞时 FetchBody 不会把管理面请求挂死：
// 内部套了 storeOpTimeout 上限（断言 Get 收到带 deadline 的 ctx），且调用方 ctx 取消时立即返回。
func TestFetchBodyRespectsContext(t *testing.T) {
	bs := &blockingStore{Memory: storage.NewMemory()}
	_, svc := newTestService(t, bs)

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // 已取消：Get 里的 <-ctx.Done() 立即返回
	done := make(chan error, 1)
	go func() { _, err := svc.FetchBody(ctx, "audit/2026/09/03/x/request.gz"); done <- err }()
	select {
	case err := <-done:
		require.ErrorIs(t, err, context.Canceled)
	case <-time.After(3 * time.Second):
		t.Fatal("FetchBody 未在 ctx 取消后及时返回")
	}
	require.True(t, bs.hadDeadline, "FetchBody 应给存储层调用套 storeOpTimeout 上限")
}
