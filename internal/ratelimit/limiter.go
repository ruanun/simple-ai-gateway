package ratelimit

import "time"

// Limiter 限流器接口：按 key+limit 判断本次请求是否放行。
type Limiter interface {
	Allow(key string, limit int, now time.Time) bool
	// Exceeded 只读判定 key 本窗口计数是否已达 limit（不计数）；limit<=0 恒 false。
	// 供「只统计失败、达阈值后前置拦截」的场景（如 /v1 鉴权失败限流）。
	Exceeded(key string, limit int, now time.Time) bool
	// Release 退还 key 在 now 所处窗口的一次计数（计数为 0 时不动）。供「先占名额、事后按结果退还」
	// 的场景（如登录失败锁定：比对前原子占位防并发绕过，成功登录再退还，使计数只反映失败）。
	Release(key string, now time.Time)
}
