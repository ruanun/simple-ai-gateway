package breaker

import (
	"sync"
	"testing"
	"time"
)

// TestMemoryBreakerConcurrent 分片后并发读写无竞态（须配 -race 跑）。
func TestMemoryBreakerConcurrent(t *testing.T) {
	b := NewMemoryBreaker(Params{Window: 30 * time.Second, Cooldown: time.Minute, FailThreshold: 3})
	var wg sync.WaitGroup
	for w := 0; w < 8; w++ {
		wg.Add(1)
		go func(seed uint64) {
			defer wg.Done()
			now := time.Now()
			for i := uint64(0); i < 500; i++ {
				id := (seed*31 + i) % 64
				b.Allow(id, now)
				b.RecordResult(id, now, i%3 != 0, false)
				b.Recovering(id, now)
			}
		}(uint64(w))
	}
	wg.Wait()
	b.Snapshot([]uint64{1, 2, 3}, time.Now())
	b.Reset(1, 2, 3)
}
