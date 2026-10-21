package server

import (
	"context"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
	goredis "github.com/redis/go-redis/v9"
	"github.com/ruanun/simple-ai-gateway/internal/storage"
	"gorm.io/gorm"
)

// healthHandler 提供 liveness / readiness 两个语义不同的端点：
//   - /healthz、/health：进程存活，不查依赖（DB 抖动时不应被 K8s 重启）；
//   - /readyz：DB / Redis（启用时）/ 审计存储都可用才 200，供反代或 K8s readiness 摘流。
type healthHandler struct {
	db    *gorm.DB
	redis *goredis.Client
	store storage.Store
}

func (h *healthHandler) Live(c *gin.Context) {
	c.JSON(http.StatusOK, gin.H{"status": "ok"})
}

func (h *healthHandler) Ready(c *gin.Context) {
	ctx, cancel := context.WithTimeout(c.Request.Context(), 2*time.Second)
	defer cancel()

	checks := make(map[string]string, 3)
	ok := true

	if h.db != nil {
		sqlDB, err := h.db.DB()
		if err != nil {
			checks["database"] = "error"
			ok = false
		} else if err := sqlDB.PingContext(ctx); err != nil {
			checks["database"] = "error"
			ok = false
		} else {
			checks["database"] = "ok"
		}
	}

	if h.redis != nil {
		if err := h.redis.Ping(ctx).Err(); err != nil {
			checks["redis"] = "error"
			ok = false
		} else {
			checks["redis"] = "ok"
		}
	}

	if h.store != nil {
		if err := h.store.Health(ctx); err != nil {
			checks["storage"] = "error"
			ok = false
		} else {
			checks["storage"] = "ok"
		}
	}

	status := http.StatusOK
	state := "ok"
	if !ok {
		status = http.StatusServiceUnavailable
		state = "degraded"
	}
	c.JSON(status, gin.H{"status": state, "checks": checks})
}
