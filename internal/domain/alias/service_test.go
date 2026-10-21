package alias

import (
	"testing"

	"github.com/ruanun/simple-ai-gateway/internal/dbtest"
	"github.com/ruanun/simple-ai-gateway/internal/model"
	"github.com/stretchr/testify/require"
)

func TestAliasCRUD(t *testing.T) {
	g := dbtest.OpenInMemory(t)
	require.NoError(t, g.AutoMigrate(&model.ModelAlias{}, &model.AliasRoute{}))
	svc := NewService(NewRepo(g))

	a, err := svc.CreateAlias("smart")
	require.NoError(t, err)
	require.NotZero(t, a.ID)

	r, err := svc.AddRoute(a.ID, 1, "gpt-4o", 70, 1)
	require.NoError(t, err)
	require.Equal(t, 70, r.Weight)

	routes, err := svc.ListRoutes(a.ID)
	require.NoError(t, err)
	require.Len(t, routes, 1)

	require.NoError(t, svc.DeleteRoute(r.ID))
	routes, _ = svc.ListRoutes(a.ID)
	require.Empty(t, routes)
}
