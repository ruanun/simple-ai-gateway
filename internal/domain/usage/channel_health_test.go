package usage

import (
	"testing"
	"time"

	"github.com/ruanun/simple-ai-gateway/internal/dbtest"
	"github.com/ruanun/simple-ai-gateway/internal/model"
	"github.com/stretchr/testify/require"
)

func TestChannelHealth(t *testing.T) {
	g := dbtest.OpenInMemory(t)
	require.NoError(t, g.AutoMigrate(&model.Channel{}, &model.UsageDailyChannel{}))
	require.NoError(t, g.Create(&model.Channel{Name: "ch-a", Type: "openai", Status: 1}).Error)
	today := time.Now().Format("2006-01-02")
	require.NoError(t, g.Create(&model.UsageDailyChannel{StatDate: today, ChannelID: 1, Model: "m", Requests: 10, Errors: 2, AvgLatencyMs: 100}).Error)
	require.NoError(t, g.Create(&model.UsageDailyChannel{StatDate: today, ChannelID: 1, Model: "m2", Requests: 90, Errors: 8, AvgLatencyMs: 10}).Error)

	s := NewService(NewRepo(g))
	rows, err := s.ChannelHealth(7)
	require.NoError(t, err)
	require.Len(t, rows, 1)
	require.Equal(t, "ch-a", rows[0].Name)
	require.Equal(t, int64(100), rows[0].Requests)
	require.Equal(t, int64(10), rows[0].Errors)
	require.InDelta(t, 0.10, rows[0].ErrorRate, 0.0001)
	require.Equal(t, int64(19), rows[0].AvgLatencyMs) // (10*100 + 90*10)/100 = 19
}
