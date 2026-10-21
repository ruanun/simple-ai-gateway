package audit

import (
	"crypto/rand"
	"encoding/hex"
)

// GenRequestID 生成 16 字节 hex 请求标识。
func GenRequestID() string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "req-fallback"
	}
	return hex.EncodeToString(b)
}
