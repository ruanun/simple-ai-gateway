package gateway

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestProbeModels(t *testing.T) {
	var gotAuth, gotPath string
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotAuth = r.Header.Get("Authorization")
		w.WriteHeader(200)
		_, _ = w.Write([]byte(`{"data":[{"id":"gpt-4o"}]}`))
	}))
	defer up.Close()

	status, body, err := ProbeModels(&http.Client{}, up.URL, "sk-up", "openai", "auto", nil)
	require.NoError(t, err)
	require.Equal(t, 200, status)
	// openai 裸域名：模型端点与主链路的 {base}/chat/completions 同级，仍是 {base}/models
	require.Equal(t, "/models", gotPath)
	require.Equal(t, "Bearer sk-up", gotAuth) // openai auto → Bearer
	require.Contains(t, string(body), "gpt-4o")

	// 上游 500 → 状态透出，无 err
	up500 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(500) }))
	defer up500.Close()
	status, _, err = ProbeModels(&http.Client{}, up500.URL, "k", "openai", "auto", nil)
	require.NoError(t, err)
	require.Equal(t, 500, status)
}

// 探测端点取候选首条，即与主链路端点同级的那条。
func TestProbeModelsCandidatePath(t *testing.T) {
	var gotPath string
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		w.WriteHeader(200)
		_, _ = w.Write([]byte(`{"data":[]}`))
	}))
	defer up.Close()

	for _, tc := range []struct{ name, suffix, protocol, want string }{
		// base_url 已含版本段（openai 惯例 /v1、火山 /api/v3）→ 与改造前一致，不引入回归。
		{"版本段 base_url", "/v1", "openai", "/v1/models"},
		// anthropic 的 base_url 不含 /v1 → 由恒 404 的 {base}/models（熔断永远等不到闭合）
		// 修正为与 {base}/v1/messages 同级。这是本次改造对探测器的实质修复。
		{"anthropic 裸根", "", "anthropic", "/v1/models"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			status, _, err := ProbeModels(&http.Client{}, up.URL+tc.suffix, "k", tc.protocol, "auto", nil)
			require.NoError(t, err)
			require.Equal(t, 200, status)
			require.Equal(t, tc.want, gotPath)
		})
	}
}

// 探测器只发一个请求：拿候选首条即可，不为「探连通性」把出站请求量翻三倍。
func TestProbeModelsSendsSingleRequest(t *testing.T) {
	var hits int
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		w.WriteHeader(404)
	}))
	defer up.Close()

	status, _, err := ProbeModels(&http.Client{}, up.URL+"/anthropic", "k", "anthropic", "auto", nil)
	require.NoError(t, err)
	require.Equal(t, 404, status)
	require.Equal(t, 1, hits, "候选有 3 条，探测器只应打第一条")
}

// base_url 为空无法推导端点 → 返回错误，不发请求。
func TestProbeModelsRejectsEmptyBaseURL(t *testing.T) {
	_, _, err := ProbeModels(&http.Client{}, "  ", "k", "openai", "auto", nil)
	require.Error(t, err)
}
