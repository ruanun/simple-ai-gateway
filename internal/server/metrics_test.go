package server

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/ruanun/simple-ai-gateway/internal/dbtest"
	"github.com/ruanun/simple-ai-gateway/internal/domain/usage"
	"github.com/stretchr/testify/require"
)

func TestMetricsEndpoint(t *testing.T) {
	gin.SetMode(gin.TestMode)
	g := dbtest.OpenInMemory(t)
	h := &metricsHandler{db: g, usage: usage.NewService(usage.NewRepo(g))}
	r := gin.New()
	r.GET("/metrics", h.Serve)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	require.Equal(t, http.StatusOK, w.Code)
	require.Contains(t, w.Body.String(), "saig_up 1")
	require.Contains(t, w.Body.String(), "saig_db_up 1")
	require.Contains(t, w.Body.String(), "saig_build_info{")
}
