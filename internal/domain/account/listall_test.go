package account

import (
	"testing"

	"github.com/ruanun/simple-ai-gateway/internal/dbtest"
	"github.com/ruanun/simple-ai-gateway/internal/model"
	"github.com/stretchr/testify/require"
)

func TestListAll(t *testing.T) {
	g := dbtest.OpenInMemory(t)
	require.NoError(t, g.AutoMigrate(&model.Account{}))
	svc := NewService(NewRepo(g))
	_, err := svc.Create(model.Account{ChannelID: 1, Name: "a1", Status: 1})
	require.NoError(t, err)
	_, err = svc.Create(model.Account{ChannelID: 2, Name: "a2", Status: 1})
	require.NoError(t, err)

	all, err := svc.ListAll()
	require.NoError(t, err)
	require.Len(t, all, 2)
	require.Equal(t, uint64(1), all[0].ChannelID) // 按 id 升序，第一条为 a1
}
