package db

import (
	"testing"

	"github.com/ruanun/simple-ai-gateway/internal/config"
	"github.com/stretchr/testify/require"
)

func TestOpenSQLite(t *testing.T) {
	cfg := config.DatabaseConfig{Driver: "sqlite", DSN: "file::memory:?cache=shared"}
	g, err := Open(cfg)
	require.NoError(t, err)
	require.NotNil(t, g)
	require.Equal(t, "sqlite", g.Dialector.Name())
}

func TestOpenUnknownDriverFallsToSQLite(t *testing.T) {
	cfg := config.DatabaseConfig{Driver: "unknown", DSN: "file::memory:?cache=shared"}
	g, err := Open(cfg)
	require.NoError(t, err)
	require.Equal(t, "sqlite", g.Dialector.Name())
}
