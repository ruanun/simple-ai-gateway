package writegate

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// TestGate 节流窗口语义：首次放行→窗口内拒绝→超窗放行→MarkDirty 后立即放行。
func TestGate(t *testing.T) {
	g := New(HealthWriteInterval)
	t0 := time.Date(2026, 7, 20, 12, 0, 0, 0, time.UTC)

	require.True(t, g.Allow(1, t0), "首次应放行")
	require.False(t, g.Allow(1, t0.Add(10*time.Second)), "窗口内应拒绝")
	require.True(t, g.Allow(2, t0), "不同 id 互不影响")
	require.True(t, g.Allow(1, t0.Add(HealthWriteInterval)), "超窗应放行")
	require.False(t, g.Allow(1, t0.Add(HealthWriteInterval+time.Second)), "放行后重新记账")

	g.MarkDirty(1)
	require.True(t, g.Allow(1, t0.Add(HealthWriteInterval+2*time.Second)), "MarkDirty 后应立即放行")
}
