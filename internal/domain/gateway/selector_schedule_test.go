package gateway

import (
	"io"
	"strings"
	"testing"
	"time"

	"github.com/ruanun/simple-ai-gateway/internal/model"
	"github.com/ruanun/simple-ai-gateway/internal/ratelimit"
	"github.com/stretchr/testify/require"
)

func TestSchedulableAccountsFiltersWindows(t *testing.T) {
	now := time.Date(2026, 10, 21, 12, 0, 0, 0, time.UTC)
	past := now.Add(-time.Minute)
	future := now.Add(time.Minute)
	accounts := []model.Account{
		{BaseModel: model.BaseModel{ID: 1}, Status: 1},                                           // ok
		{BaseModel: model.BaseModel{ID: 2}, Status: 0},                                           // disabled
		{BaseModel: model.BaseModel{ID: 3}, Status: 1, ExpiresAt: &past},                         // expired
		{BaseModel: model.BaseModel{ID: 4}, Status: 1, TempUnschedulableUntil: &future},          // temp
		{BaseModel: model.BaseModel{ID: 5}, Status: 1, RateLimitResetAt: &future},                // rate limited
		{BaseModel: model.BaseModel{ID: 6}, Status: 1, OverloadUntil: &future},                   // overloaded
		{BaseModel: model.BaseModel{ID: 7}, Status: 1, ExpiresAt: &future, OverloadUntil: &past}, // 过期前/冷却已到期
	}

	got := schedulableAccounts(accounts, now)
	require.Len(t, got, 2)
	require.Equal(t, uint64(1), got[0].ID)
	require.Equal(t, uint64(7), got[1].ID)
}

func TestAccountSchedulableAtBoundary(t *testing.T) {
	now := time.Date(2026, 10, 21, 12, 0, 0, 0, time.UTC)
	a := &model.Account{Status: 1, RateLimitResetAt: &now, OverloadUntil: &now}
	require.True(t, accountSchedulableAt(a, now), "等于 until 应视为已到期")
}

func TestAccountRPMExceeded(t *testing.T) {
	now := time.Date(2026, 10, 21, 12, 0, 0, 0, time.UTC)
	d := Deps{AccountLimiter: ratelimit.NewMemoryLimiter(), Now: func() time.Time { return now }}
	info := ChannelInfo{AccountID: 7, AccountRPMLimit: 1}
	require.False(t, accountRPMExceeded(d, info))
	require.True(t, accountRPMExceeded(d, info))
	require.False(t, accountRPMExceeded(d, ChannelInfo{AccountID: 7, AccountRPMLimit: 0}), "0=不限")
}

func TestAcquireAccountSlot(t *testing.T) {
	d := Deps{AccountConcurrency: ratelimit.NewMemoryConcurrency()}
	info := ChannelInfo{AccountID: 7, AccountMaxConcurrency: 1}
	release, ok := acquireAccountSlot(d, info)
	require.True(t, ok)
	_, ok = acquireAccountSlot(d, info)
	require.False(t, ok, "上限 1 时第二次应失败")
	release()
	_, ok = acquireAccountSlot(d, info)
	require.True(t, ok, "释放后应可再次占用")
}

func TestReleaseOnCloseOnce(t *testing.T) {
	calls := 0
	rc := &releaseOnClose{ReadCloser: io.NopCloser(strings.NewReader("x")), release: func() { calls++ }}
	_, _ = io.ReadAll(rc)
	require.NoError(t, rc.Close())
	require.NoError(t, rc.Close())
	require.Equal(t, 1, calls)
}
