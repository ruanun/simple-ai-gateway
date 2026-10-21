// Package pwpolicy 统一登录口令长度策略、错误文案与防用户名枚举的比对辅助，供 admin（管理端）与 portal（门户）两面共用，
// 避免"8~64 字符"校验散装多处、将来改策略漏改。
package pwpolicy

import (
	"sync"

	"golang.org/x/crypto/bcrypt"
)

// 密码长度策略：8~64 字符（按字节计，与既有行为一致）。
const (
	MinLen = 8
	MaxLen = 64
)

// ErrMsg 密码不合规时的统一错误文案。
const ErrMsg = "密码长度须为 8~64 字符"

// Valid 校验密码长度是否落在策略区间内。
func Valid(p string) bool { return len(p) >= MinLen && len(p) <= MaxLen }

var (
	dummyOnce sync.Once
	dummyHash []byte
)

// DummyCompare 对一个固定哈希跑一次 bcrypt 比对，结果丢弃。
// 登录时用户名不存在也调用它，使「用户不存在」与「密码错误」耗时一致，
// 防止按响应时间差枚举用户名。哈希首次调用时生成（成本与正式哈希同为 DefaultCost）。
func DummyCompare(password string) {
	dummyOnce.Do(func() {
		dummyHash, _ = bcrypt.GenerateFromPassword([]byte("saig-dummy-password"), bcrypt.DefaultCost)
	})
	_ = bcrypt.CompareHashAndPassword(dummyHash, []byte(password))
}
