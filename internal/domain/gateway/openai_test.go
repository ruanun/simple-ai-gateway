package gateway

import (
	"io"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestOpenAIBuildRequest(t *testing.T) {
	a := OpenAIAdapter{}
	body := []byte(`{"model":"gpt-4o","messages":[]}`)
	spec := HeaderSpec{Protocol: "openai", BaseURL: "https://api.openai.com/v1", UpstreamKey: "sk-up"}
	req, err := a.BuildRequest(spec, body)
	require.NoError(t, err)
	require.Equal(t, "https://api.openai.com/v1/chat/completions", req.URL.String())
	require.Equal(t, "Bearer sk-up", req.Header.Get("Authorization"))
	require.Equal(t, "application/json", req.Header.Get("Content-Type"))
	require.Equal(t, "openai", a.Protocol())
	got, _ := io.ReadAll(req.Body)
	require.JSONEq(t, string(body), string(got))
}
