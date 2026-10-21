package gateway

import (
	"testing"
	"time"

	"github.com/ruanun/simple-ai-gateway/internal/breaker"
	"github.com/ruanun/simple-ai-gateway/internal/crypto"
	"github.com/ruanun/simple-ai-gateway/internal/dbtest"
	"github.com/ruanun/simple-ai-gateway/internal/domain/account"
	"github.com/ruanun/simple-ai-gateway/internal/domain/channel"
	"github.com/ruanun/simple-ai-gateway/internal/model"
	"github.com/ruanun/simple-ai-gateway/internal/session"
	"github.com/ruanun/simple-ai-gateway/internal/writegate"
	"github.com/stretchr/testify/require"
)

// TestRecordResultHealthWriteback 验证 RecordResult 的账号健康字段回写（M4）：
// 失败 → fail_count 自增；成功 → fail_count 清零且 last_used_at 回写。
func TestRecordResultHealthWriteback(t *testing.T) {
	g := dbtest.OpenInMemory(t)
	require.NoError(t, g.AutoMigrate(&model.Account{}))

	// 建一个账号，初始 fail_count=0、last_used_at 为空。
	acc := model.Account{ChannelID: 1, Name: "acc", APIKeyEnc: "enc", Weight: 1, Status: 1}
	require.NoError(t, g.Create(&acc).Error)

	fixed := time.Unix(1_700_000_000, 0)
	accSvc := account.NewService(account.NewRepo(g))
	s := NewAccountSelector(channel.NewService(channel.NewRepo(g), "0123456789abcdef0123456789abcdef"),
		"0123456789abcdef0123456789abcdef", accSvc,
		breaker.NewMemoryBreaker(breaker.Params{Window: time.Minute, Cooldown: time.Minute, FailThreshold: 3, ErrorRate: 0.5, MinSamples: 20, ProbeBackoffMax: 8}), nil, func() time.Time { return fixed }, nil, 1.0)

	// 两次失败 → fail_count 累加到 2。
	s.RecordResult(acc.ID, "m", false, false, 0)
	s.RecordResult(acc.ID, "m", false, false, 0)
	var row model.Account
	require.NoError(t, g.First(&row, acc.ID).Error)
	require.Equal(t, 2, row.FailCount, "两次失败后 fail_count 应为 2")

	// 一次成功 → fail_count 清零、last_used_at 回写为 now。
	s.RecordResult(acc.ID, "m", true, false, 0)
	require.NoError(t, g.First(&row, acc.ID).Error)
	require.Equal(t, 0, row.FailCount, "成功后 fail_count 应清零")
	require.NotNil(t, row.LastUsedAt, "成功后 last_used_at 应非空")
	require.Equal(t, fixed.Unix(), row.LastUsedAt.Unix(), "last_used_at 应回写为 now")

	// accountID==0 应直接跳过（不 panic、不影响其它账号）。
	s.RecordResult(0, "m", false, false, 0)
	require.NoError(t, g.First(&row, acc.ID).Error)
	require.Equal(t, 0, row.FailCount)
}

// TestRecordResultHealthThrottle 成功回写 60s 节流（节流在 account.Service 内）：
// 窗口内不重写 last_used_at；失败后（MarkDirty）下一次成功立即回写清零 fail_count。
func TestRecordResultHealthThrottle(t *testing.T) {
	g := dbtest.OpenInMemory(t)
	require.NoError(t, g.AutoMigrate(&model.Account{}))

	acc := model.Account{ChannelID: 1, Name: "acc", APIKeyEnc: "enc", Weight: 1, Status: 1}
	require.NoError(t, g.Create(&acc).Error)

	cur := time.Date(2026, 7, 20, 12, 0, 0, 0, time.UTC)
	now := func() time.Time { return cur }
	accSvc := account.NewService(account.NewRepo(g))
	s := NewAccountSelector(channel.NewService(channel.NewRepo(g), "0123456789abcdef0123456789abcdef"),
		"0123456789abcdef0123456789abcdef", accSvc,
		breaker.NewMemoryBreaker(breaker.Params{Window: time.Minute, Cooldown: time.Minute, FailThreshold: 3, ErrorRate: 0.5, MinSamples: 20, ProbeBackoffMax: 8}), nil, now, nil, 1.0)

	readAccount := func(t *testing.T) model.Account {
		t.Helper()
		var row model.Account
		require.NoError(t, g.First(&row, acc.ID).Error)
		return row
	}

	s.RecordResult(acc.ID, "m", true, false, 0) // 首次成功 → 写
	first := readAccount(t).LastUsedAt
	require.NotNil(t, first, "首次成功应回写 last_used_at")

	cur = cur.Add(10 * time.Second)
	s.RecordResult(acc.ID, "m", true, false, 0) // 窗口内 → 跳过
	require.Equal(t, *first, *readAccount(t).LastUsedAt, "窗口内不应重写 last_used_at")

	cur = cur.Add(writegate.HealthWriteInterval)
	s.RecordResult(acc.ID, "m", true, false, 0) // 超窗 → 写
	require.True(t, readAccount(t).LastUsedAt.After(*first), "超窗应重写 last_used_at")

	cur = cur.Add(time.Second)
	s.RecordResult(acc.ID, "m", false, false, 0) // 失败 → fail_count=1 + MarkDirty
	require.Equal(t, 1, readAccount(t).FailCount)

	cur = cur.Add(time.Second)
	s.RecordResult(acc.ID, "m", true, false, 0) // 失败后的成功 → 立即回写清零
	require.Equal(t, 0, readAccount(t).FailCount, "失败后的成功应立即清零 fail_count（MarkDirty）")
}

// TestQuotaCooldownSkipsAccountForModel 账号×模型配额冷却闭环（P12a）：
// (acc1, model-a) 配额失败后，model-a 的 lookup 选到 acc2、model-b 不受影响；成功后清退避。
func TestQuotaCooldownSkipsAccountForModel(t *testing.T) {
	g := dbtest.OpenInMemory(t)
	require.NoError(t, g.AutoMigrate(&model.Channel{}, &model.Account{}))

	aes := "0123456789abcdef0123456789abcdef"
	ch := model.Channel{Name: "ch", Type: "openai", BaseURL: "http://up", Status: 1, Timeout: 5}
	require.NoError(t, g.Create(&ch).Error)
	enc1, err := crypto.Encrypt(aes, "sk-1")
	require.NoError(t, err)
	enc2, err := crypto.Encrypt(aes, "sk-2")
	require.NoError(t, err)
	acc1 := model.Account{ChannelID: ch.ID, Name: "a1", APIKeyEnc: enc1, Weight: 1, Priority: 1, Status: 1}
	acc2 := model.Account{ChannelID: ch.ID, Name: "a2", APIKeyEnc: enc2, Weight: 1, Priority: 0, Status: 1}
	require.NoError(t, g.Create(&acc1).Error)
	require.NoError(t, g.Create(&acc2).Error)

	fixed := time.Unix(1_700_000_000, 0)
	accSvc := account.NewService(account.NewRepo(g))
	keyed := breaker.NewMemoryKeyed(time.Minute, 30*time.Minute)
	accBrk := breaker.NewMemoryBreaker(breaker.Params{Window: time.Minute, Cooldown: time.Minute, FailThreshold: 3, ErrorRate: 0.5, MinSamples: 20, ProbeBackoffMax: 8})
	s := NewAccountSelector(channel.NewService(channel.NewRepo(g), aes), aes, accSvc, accBrk, keyed,
		func() time.Time { return fixed }, nil, 1.0)

	// 冷却前：acc1 优先级更高，model-a 应选 acc1。
	info, ok := s.Lookup(ch.ID, "model-a")
	require.True(t, ok)
	require.Equal(t, acc1.ID, info.AccountID)

	// (acc1, model-a) 配额失败 → model-a 跳过 acc1 选 acc2。
	s.RecordResult(acc1.ID, "model-a", false, true, 0)
	info, ok = s.Lookup(ch.ID, "model-a")
	require.True(t, ok)
	require.Equal(t, acc2.ID, info.AccountID, "model-a 应跳过配额冷却中的 acc1")

	// model-b 不受影响，仍选 acc1。
	info, ok = s.Lookup(ch.ID, "model-b")
	require.True(t, ok)
	require.Equal(t, acc1.ID, info.AccountID, "同账号其他模型不受影响")

	// 账号级熔断不应被配额失败触发（配额失败只进账号×模型层，acc1 整体仍可用）。
	require.True(t, accBrk.Allow(acc1.ID, fixed), "配额失败不应触发账号级熔断")

	// 成功后清退避 → model-a 恢复选 acc1。
	s.RecordResult(acc1.ID, "model-a", true, false, 0)
	info, ok = s.Lookup(ch.ID, "model-a")
	require.True(t, ok)
	require.Equal(t, acc1.ID, info.AccountID, "成功后应恢复")

	// 两账号对 model-a 全冷却 → lookup 失败（供 Dispatch failover）。
	s.RecordResult(acc1.ID, "model-a", false, true, 0)
	s.RecordResult(acc2.ID, "model-a", false, true, 0)
	_, ok = s.Lookup(ch.ID, "model-a")
	require.False(t, ok, "全冷却时 lookup 应失败，交由派发层 failover")
}

// TestStickyEscapesModelCooledAccount 验证会话粘连遇账号×模型配额冷却时能逃逸重选（P12a）：
// 粘连账号对该模型冷却中 → LookupWithSession 应跳过它选另一账号；
// 注意粘连键是 (session, channel)→account（与模型无关），逃逸后同一 session 的映射会
// 更新为新账号，因此验证"同账号其他模型不受影响"需换一个 session（否则粘连会直接复用
// 逃逸后写入的新账号，而不是重新按优先级选择）。
func TestStickyEscapesModelCooledAccount(t *testing.T) {
	g := dbtest.OpenInMemory(t)
	require.NoError(t, g.AutoMigrate(&model.Channel{}, &model.Account{}))

	aes := "0123456789abcdef0123456789abcdef"
	ch := model.Channel{Name: "ch", Type: "openai", BaseURL: "http://up", Status: 1, Timeout: 5}
	require.NoError(t, g.Create(&ch).Error)
	enc1, err := crypto.Encrypt(aes, "sk-1")
	require.NoError(t, err)
	enc2, err := crypto.Encrypt(aes, "sk-2")
	require.NoError(t, err)
	acc1 := model.Account{ChannelID: ch.ID, Name: "a1", APIKeyEnc: enc1, Weight: 1, Priority: 1, Status: 1}
	acc2 := model.Account{ChannelID: ch.ID, Name: "a2", APIKeyEnc: enc2, Weight: 1, Priority: 0, Status: 1}
	require.NoError(t, g.Create(&acc1).Error)
	require.NoError(t, g.Create(&acc2).Error)

	fixed := time.Unix(1_700_000_000, 0)
	accSvc := account.NewService(account.NewRepo(g))
	keyed := breaker.NewMemoryKeyed(time.Minute, 30*time.Minute)
	accBrk := breaker.NewMemoryBreaker(breaker.Params{Window: time.Minute, Cooldown: time.Minute, FailThreshold: 3, ErrorRate: 0.5, MinSamples: 20, ProbeBackoffMax: 8})
	sticky := session.NewMemoryStickyStore(time.Hour)
	s := NewAccountSelector(channel.NewService(channel.NewRepo(g), aes), aes, accSvc, accBrk, keyed, func() time.Time { return fixed }, sticky, 1.0)

	// 建立粘连：model-a 首次选择应选高优先级 acc1，并写入 (sess, ch.ID) → acc1。
	info, ok := s.LookupWithSession("sess", ch.ID, "model-a")
	require.True(t, ok)
	require.Equal(t, acc1.ID, info.AccountID, "首次粘连应选高优先级 acc1")

	// (acc1, model-a) 配额失败 → 冷却。
	s.RecordResult(acc1.ID, "model-a", false, true, 0)

	// 同一 session 再查 model-a：粘连账号 acc1 对 model-a 冷却中 → 逃逸重选，落到 acc2，
	// 且 (sess, ch.ID) 的粘连映射会被更新为 acc2。
	info, ok = s.LookupWithSession("sess", ch.ID, "model-a")
	require.True(t, ok)
	require.Equal(t, acc2.ID, info.AccountID, "粘连账号对该模型冷却中应逃逸重选到 acc2")

	// model-b 不受 acc1 的 model-a 冷却影响：用新 session 建立粘连（避免复用上一步已逃逸到
	// acc2 的 sess 映射），应仍按优先级选中 acc1，证明冷却是按账号×模型隔离的。
	info, ok = s.LookupWithSession("sess2", ch.ID, "model-b")
	require.True(t, ok)
	require.Equal(t, acc1.ID, info.AccountID, "同账号 acc1 对其他模型不受影响，新会话粘连仍选回高优先级账号")
}
