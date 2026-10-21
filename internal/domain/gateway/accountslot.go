package gateway

import (
	"io"
	"strconv"
	"sync"
)

// accountRPMKey / accountConcurrencyKey 与渠道键（"ch:<id>"）区分命名空间，
// 避免和渠道 RPM/App 并发共用一个计数器键。
func accountRPMKey(id uint64) string         { return "accrpm:" + strconv.FormatUint(id, 10) }
func accountConcurrencyKey(id uint64) string { return "account:" + strconv.FormatUint(id, 10) }

// accountRPMExceeded 账号级 RPM 是否已达上限。limit<=0 / limiter=nil 恒放行。
// 命中时调用方把该账号加入 tried 并换号重试，不熔断账号。
func accountRPMExceeded(d Deps, info ChannelInfo) bool {
	if d.AccountLimiter == nil || info.AccountID == 0 || info.AccountRPMLimit <= 0 {
		return false
	}
	return !d.AccountLimiter.Allow(accountRPMKey(info.AccountID), info.AccountRPMLimit, d.Now())
}

// acquireAccountSlot 占一个账号级并发槽；limit<=0 / concurrency=nil 返回 noop。
// ok=false 时调用方换号重试；release 幂等，可安全重复调用。
func acquireAccountSlot(d Deps, info ChannelInfo) (func(), bool) {
	if d.AccountConcurrency == nil || info.AccountID == 0 || info.AccountMaxConcurrency <= 0 {
		return func() {}, true
	}
	return d.AccountConcurrency.Acquire(accountConcurrencyKey(info.AccountID), info.AccountMaxConcurrency)
}

// releaseOnClose 把账号并发槽的释放绑定到上游响应体 Close：raw 端点（DispatchRaw）
// 在把 resp.Body 交给 handler 后函数即返回，不能靠 defer 释放；由 handler 读取/关闭时归还。
// 若调用方忘记 Close，内存模式下槽位会泄漏到进程重启；Redis 模式由 15m 租约兜底。
type releaseOnClose struct {
	io.ReadCloser
	once    sync.Once
	release func()
}

func (r *releaseOnClose) Close() error {
	err := r.ReadCloser.Close()
	r.once.Do(r.release)
	return err
}
