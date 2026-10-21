package account

import (
	"testing"

	"github.com/ruanun/simple-ai-gateway/internal/dbtest"
	"github.com/ruanun/simple-ai-gateway/internal/model"
	"github.com/stretchr/testify/require"
)

func TestAccountCRUD(t *testing.T) {
	g := dbtest.OpenInMemory(t)
	require.NoError(t, g.AutoMigrate(&model.Account{}))
	svc := NewService(NewRepo(g))

	a, err := svc.Create(model.Account{ChannelID: 1, Name: "acc-a", APIKeyEnc: "enc1", Weight: 70, Status: 1})
	require.NoError(t, err)
	require.NotZero(t, a.ID)
	_, err = svc.Create(model.Account{ChannelID: 1, Name: "acc-b", APIKeyEnc: "enc2", Weight: 30, Status: 0})
	require.NoError(t, err)

	all, err := svc.ListByChannel(1)
	require.NoError(t, err)
	require.Len(t, all, 2)

	enabled, err := svc.ListEnabledByChannel(1)
	require.NoError(t, err)
	require.Len(t, enabled, 1) // 只有启用的 acc-a
	require.Equal(t, "acc-a", enabled[0].Name)

	require.NoError(t, svc.Delete(a.ID))
	all, _ = svc.ListByChannel(1)
	require.Len(t, all, 1)
}
