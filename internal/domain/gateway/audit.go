package gateway

import (
	"github.com/ruanun/simple-ai-gateway/internal/audit"
)

// auditSubmit 若该 App 启用审计则投递一条审计条目（非阻塞）。
// auditEnabled 由 Prepare 从 App 行带出（appID==0 无归属时恒为 false），不再回表查开关；
// w==nil 或未启用不审计；空 RequestID 自动补一个随机 id。Submit 本身非阻塞，通道满则丢弃计数。
func auditSubmit(w *audit.AsyncWriter, auditEnabled bool, e audit.Entry) {
	if w == nil || !auditEnabled {
		return
	}
	if e.RequestID == "" {
		e.RequestID = audit.GenRequestID()
	}
	w.Submit(e)
}
