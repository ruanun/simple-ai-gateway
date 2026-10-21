package gateway

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/ruanun/simple-ai-gateway/internal/breaker"
	"github.com/ruanun/simple-ai-gateway/internal/ratelimit"
	"github.com/ruanun/simple-ai-gateway/internal/router"
	"github.com/stretchr/testify/require"
)

// TestDispatchRawPathPassthrough 验证 DispatchRaw 把 path 透传到上游、返回原始响应。
func TestDispatchRawPathPassthrough(t *testing.T) {
	var gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		require.Equal(t, "Bearer up-key", r.Header.Get("Authorization"))
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"object":"list"}`))
	}))
	defer srv.Close()

	d := Deps{
		Breaker: breaker.NewMemoryBreaker(breaker.Params{Window: time.Minute, Cooldown: time.Minute, FailThreshold: 3, ErrorRate: 0.5, MinSamples: 20, ProbeBackoffMax: 8}),
		Limiter: ratelimit.NewMemoryLimiter(),
		Lookup: func(id uint64, modelName string) (ChannelInfo, bool) {
			return ChannelInfo{BaseURL: srv.URL, UpstreamKey: "up-key", Timeout: 5}, true
		},
		Now: func() time.Time { return time.Unix(1_000_000, 0) },
	}
	out := DispatchRaw([]router.Candidate{{ChannelID: 1}}, "/embeddings", []byte(`{"model":"m"}`), d, false)
	require.NotNil(t, out.Resp)
	defer out.Resp.Body.Close()
	require.Equal(t, "/embeddings", gotPath)
	require.Equal(t, uint64(1), out.ChannelID)
	body, _ := io.ReadAll(out.Resp.Body)
	require.Contains(t, string(body), `"object":"list"`)
}

// TestDispatchRawRetryThenSucceed 验证 5xx 渠道被跳过、择优到下一个 2xx 渠道。
func TestDispatchRawRetryThenSucceed(t *testing.T) {
	bad := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(500) }))
	defer bad.Close()
	good := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer good.Close()

	infos := map[uint64]ChannelInfo{
		1: {BaseURL: bad.URL, Timeout: 5},
		2: {BaseURL: good.URL, Timeout: 5},
	}
	d := Deps{
		Breaker: breaker.NewMemoryBreaker(breaker.Params{Window: time.Minute, Cooldown: time.Minute, FailThreshold: 3, ErrorRate: 0.5, MinSamples: 20, ProbeBackoffMax: 8}),
		Limiter: ratelimit.NewMemoryLimiter(),
		Lookup:  func(id uint64, modelName string) (ChannelInfo, bool) { i, ok := infos[id]; return i, ok },
		Now:     func() time.Time { return time.Unix(1_000_000, 0) },
	}
	out := DispatchRaw([]router.Candidate{{ChannelID: 1}, {ChannelID: 2}}, "/images/generations", []byte(`{"model":"m"}`), d, false)
	require.NotNil(t, out.Resp)
	defer out.Resp.Body.Close()
	require.Equal(t, uint64(2), out.ChannelID)
}

// TestDispatchRawReselectsOn429 覆盖 C1+I2：协议端点遇账号 429 应换号（之前 429 直接透传不换号），
// 且重试排除刚失败账号。Lookup 总返回 429 坏账号 1，只有 LookupExcluding({1}) 返回好账号 2。
func TestDispatchRawReselectsOn429(t *testing.T) {
	bad := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusTooManyRequests)
	}))
	defer bad.Close()
	good := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"object":"list"}`))
	}))
	defer good.Close()

	badInfo := ChannelInfo{BaseURL: bad.URL, Timeout: 5, MaxRetries: 1, AccountID: 1}
	goodInfo := ChannelInfo{BaseURL: good.URL, Timeout: 5, MaxRetries: 1, AccountID: 2}
	var accResults []struct {
		id       uint64
		ok       bool
		quotaHit bool
	}
	d := Deps{
		Breaker: breaker.NewMemoryBreaker(breaker.Params{Window: time.Minute, Cooldown: time.Minute, FailThreshold: 5, ErrorRate: 0.5, MinSamples: 20, ProbeBackoffMax: 8}), Limiter: ratelimit.NewMemoryLimiter(),
		Lookup: func(id uint64, modelName string) (ChannelInfo, bool) { return badInfo, true },
		LookupExcluding: func(id uint64, modelName string, exclude map[uint64]bool) (ChannelInfo, bool) {
			if exclude[1] {
				return goodInfo, true
			}
			return badInfo, true
		},
		RecordAccountResult: func(accountID uint64, modelName string, ok, quotaHit bool, retryAfter time.Duration) {
			accResults = append(accResults, struct {
				id       uint64
				ok       bool
				quotaHit bool
			}{accountID, ok, quotaHit})
		},
		Now: func() time.Time { return time.Unix(1_000_000, 0) },
	}
	out := DispatchRaw([]router.Candidate{{ChannelID: 1}}, "/embeddings", []byte(`{"model":"m"}`), d, false)
	require.NotNil(t, out.Resp, "账号 429 应换号成功，而非透传 429")
	defer out.Resp.Body.Close()
	require.Equal(t, http.StatusOK, out.Resp.StatusCode)
	require.Equal(t, uint64(2), out.AccountID, "重试应换到好账号 2")
	// 账号级回调：坏账号 1 记 429 失败、好账号 2 记成功。
	require.Len(t, accResults, 2)
	require.Equal(t, uint64(1), accResults[0].id)
	require.False(t, accResults[0].ok)
	require.True(t, accResults[0].quotaHit, "坏账号失败应标记 quotaHit（P12a：记账号×模型配额冷却，不再立即熔断整个账号）")
	require.Equal(t, uint64(2), accResults[1].id)
	require.True(t, accResults[1].ok)
}

// TestDispatchRawPropagatesRateMultiplier 验证 DispatchRaw 把渠道倍率穿透到 RateMultiplier 字段。
func TestDispatchRawPropagatesRateMultiplier(t *testing.T) {
	good := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"ok"}`))
	}))
	defer good.Close()
	d := Deps{
		Breaker: breaker.NewMemoryBreaker(breaker.Params{Window: time.Minute, Cooldown: time.Minute, FailThreshold: 3, ErrorRate: 0.5, MinSamples: 20, ProbeBackoffMax: 8}), Limiter: ratelimit.NewMemoryLimiter(),
		Lookup: func(id uint64, modelName string) (ChannelInfo, bool) {
			return ChannelInfo{BaseURL: good.URL, Timeout: 5, RateMultiplier: 3}, true
		},
		Now: func() time.Time { return time.Unix(1_000_000, 0) },
	}
	out := DispatchRaw([]router.Candidate{{ChannelID: 1}}, "/chat/completions", []byte(`{"model":"m"}`), d, false)
	require.NotNil(t, out.Resp)
	if out.Resp != nil {
		out.Resp.Body.Close()
	}
	require.Equal(t, 3.0, out.RateMultiplier)
}

// TestDispatchRawAllFail 全候选 5xx 返回无响应，且 502 详情带上游失败原因（与 Dispatch 对齐）。
func TestDispatchRawAllFail(t *testing.T) {
	bad := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(503) }))
	defer bad.Close()
	d := Deps{
		Breaker: breaker.NewMemoryBreaker(breaker.Params{Window: time.Minute, Cooldown: time.Minute, FailThreshold: 3, ErrorRate: 0.5, MinSamples: 20, ProbeBackoffMax: 8}), Limiter: ratelimit.NewMemoryLimiter(),
		Lookup: func(id uint64, modelName string) (ChannelInfo, bool) {
			return ChannelInfo{BaseURL: bad.URL, Timeout: 5}, true
		},
		Now: func() time.Time { return time.Unix(1_000_000, 0) },
	}
	out := DispatchRaw([]router.Candidate{{ChannelID: 1}}, "/embeddings", []byte(`{"model":"m"}`), d, false)
	require.Nil(t, out.Resp)
	require.Contains(t, out.Err, "all candidates failed")
	require.Contains(t, out.Err, "上游503", "502 详情应带上游失败原因")
}

func TestDispatchRawRewritesModelToCandidateModel(t *testing.T) {
	var gotModel string
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var b struct {
			Model string `json:"model"`
		}
		raw, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(raw, &b)
		gotModel = b.Model
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"ok"}`))
	}))
	defer up.Close()
	d := Deps{
		Breaker: breaker.NewMemoryBreaker(breaker.Params{Window: time.Minute, Cooldown: time.Minute, FailThreshold: 3, ErrorRate: 0.5, MinSamples: 20, ProbeBackoffMax: 8}), Limiter: ratelimit.NewMemoryLimiter(),
		Lookup: func(id uint64, modelName string) (ChannelInfo, bool) {
			return ChannelInfo{BaseURL: up.URL, Timeout: 5}, true
		},
		Now: func() time.Time { return time.Unix(1_000_000, 0) },
	}
	out := DispatchRaw([]router.Candidate{{ChannelID: 1, Model: "gpt-4o-real"}}, "/chat/completions", []byte(`{"model":"alias-x"}`), d, false)
	require.NotNil(t, out.Resp)
	if out.Resp != nil {
		out.Resp.Body.Close()
	}
	require.Equal(t, "gpt-4o-real", gotModel)
	require.Equal(t, "gpt-4o-real", out.Model)
}
