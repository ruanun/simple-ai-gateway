package server

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/ruanun/simple-ai-gateway/internal/dbtest"
	"github.com/ruanun/simple-ai-gateway/internal/storage"
	"github.com/stretchr/testify/require"
)

type unhealthyStore struct{ *storage.Memory }

func (unhealthyStore) Health(context.Context) error { return errors.New("模拟存储故障") }

func doHealthRequest(t *testing.T, h *healthHandler, path string) *httptest.ResponseRecorder {
	t.Helper()
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.GET("/healthz", h.Live)
	r.GET("/readyz", h.Ready)
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, path, nil)
	r.ServeHTTP(w, req)
	return w
}

func TestHealthLive(t *testing.T) {
	w := doHealthRequest(t, &healthHandler{}, "/healthz")
	require.Equal(t, http.StatusOK, w.Code)
	require.Contains(t, w.Body.String(), `"status":"ok"`)
}

func TestHealthReadyOK(t *testing.T) {
	g := dbtest.OpenInMemory(t)
	h := &healthHandler{db: g, store: storage.NewMemory()}
	w := doHealthRequest(t, h, "/readyz")
	require.Equal(t, http.StatusOK, w.Code)
	require.Contains(t, w.Body.String(), `"database":"ok"`)
	require.Contains(t, w.Body.String(), `"storage":"ok"`)
}

func TestHealthReadyDBDown(t *testing.T) {
	g := dbtest.OpenInMemory(t)
	sqlDB, err := g.DB()
	require.NoError(t, err)
	require.NoError(t, sqlDB.Close())

	h := &healthHandler{db: g, store: storage.NewMemory()}
	w := doHealthRequest(t, h, "/readyz")
	require.Equal(t, http.StatusServiceUnavailable, w.Code)
	require.Contains(t, w.Body.String(), `"database":"error"`)
}

func TestHealthReadyStoreDown(t *testing.T) {
	g := dbtest.OpenInMemory(t)
	h := &healthHandler{db: g, store: unhealthyStore{Memory: storage.NewMemory()}}
	w := doHealthRequest(t, h, "/readyz")
	require.Equal(t, http.StatusServiceUnavailable, w.Code)
	require.Contains(t, w.Body.String(), `"storage":"error"`)
}
