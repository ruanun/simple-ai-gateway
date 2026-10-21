package proxy

import (
	"testing"

	"github.com/ruanun/simple-ai-gateway/internal/dbtest"
	"github.com/ruanun/simple-ai-gateway/internal/model"
	"github.com/stretchr/testify/require"
)

func TestProxyCRUD(t *testing.T) {
	g := dbtest.OpenInMemory(t)
	require.NoError(t, g.AutoMigrate(&model.Proxy{}))
	svc := NewService(NewRepo(g))

	a, err := svc.Create(model.Proxy{Group: "pool-a", Name: "p1", Protocol: "http", Host: "127.0.0.1", Port: 1080, Weight: 70, Status: 1})
	require.NoError(t, err)
	require.NotZero(t, a.ID)
	_, _ = svc.Create(model.Proxy{Group: "pool-a", Name: "p2", Protocol: "socks5", Host: "127.0.0.1", Port: 1081, Weight: 30, Status: 0})

	all, err := svc.List()
	require.NoError(t, err)
	require.Len(t, all, 2)

	enabled, err := svc.ListEnabledByGroup("pool-a")
	require.NoError(t, err)
	require.Len(t, enabled, 1) // 仅启用的 p1
	require.Equal(t, "p1", enabled[0].Name)

	require.NoError(t, svc.Delete(a.ID))
	all, _ = svc.List()
	require.Len(t, all, 1)
}
