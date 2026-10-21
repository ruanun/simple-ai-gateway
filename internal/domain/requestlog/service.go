package requestlog

import (
	"log/slog"
	"sync"
	"sync/atomic"
	"time"

	"github.com/ruanun/simple-ai-gateway/internal/model"
)

type Service struct {
	repo   *Repo
	ch     chan *model.RequestLog // 非 nil = 异步模式（EnableAsync 开启）
	closed atomic.Bool
	wg     sync.WaitGroup
	once   sync.Once
}

func NewService(repo *Repo) *Service { return &Service{repo: repo} }

// EnableAsync 切换异步写模式（缓冲 channel + 后台 goroutine 落库）。
// 仅装配期调用一次（非并发安全）。权衡：进程崩溃时缓冲内未落库条目丢失（与审计
// AsyncWriter 同口径）；缓冲满时丢弃新条目并记警告，保请求主链路不阻塞。
func (s *Service) EnableAsync(bufSize int) {
	s.ch = make(chan *model.RequestLog, bufSize)
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		for l := range s.ch {
			s.write(l)
		}
	}()
}

// Stop 关闭异步队列并等待已入队条目落库完成；未启用异步时 no-op。幂等
// （sync.Once：优雅关闭路径可能与显式调用重入，重复 close 通道会 panic）。
func (s *Service) Stop() {
	if s.ch == nil {
		return
	}
	s.once.Do(func() {
		s.closed.Store(true)
		close(s.ch)
		s.wg.Wait()
	})
}

// Record 旁路写日志，失败不影响主链路（工程 Review E4）。
// 异步模式入队（满则丢弃+警告）；同步模式（默认）直接落库。
// Stop 后调用（优雅关闭超时后仍有在途请求补 Record 即触发）直接丢弃，避免向已关闭通道 send panic。
func (s *Service) Record(l *model.RequestLog) {
	if s.ch != nil {
		if s.closed.Load() {
			slog.Warn("请求日志缓冲已关闭，丢弃", "request_id", l.RequestID, "status", l.Status)
			return
		}
		// 兜底：closed 判定与 send 之间存在窗口，Stop() 并发 close 通道时向已关闭通道 send 会
		// panic。请求日志是旁路，绝不反噬主链路——panic 当作丢弃吞掉，与 audit.AsyncWriter.Submit 同款兜底。
		defer func() {
			if recover() != nil {
				slog.Warn("请求日志缓冲已关闭，丢弃", "request_id", l.RequestID, "status", l.Status)
			}
		}()
		select {
		case s.ch <- l:
		default:
			slog.Warn("请求日志缓冲已满，丢弃", "request_id", l.RequestID, "status", l.Status)
		}
		return
	}
	s.write(l)
}

func (s *Service) write(l *model.RequestLog) {
	if err := s.repo.Create(l); err != nil {
		slog.Error("写请求日志失败", "request_id", l.RequestID, "status", l.Status, "err", err)
	}
}

func (s *Service) List(limit, offset int) ([]model.RequestLog, int64, error) {
	return s.repo.List(limit, offset)
}

// DeleteBefore 删除 created_at 早于 t 的日志,返回删除条数。
func (s *Service) DeleteBefore(t time.Time) (int64, error) {
	return s.repo.DeleteBefore(t)
}
