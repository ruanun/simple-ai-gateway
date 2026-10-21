package gateway

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// —— 候选 URL 推导 ——

func TestBuildModelsURLCandidates(t *testing.T) {
	for _, tc := range []struct {
		name, base, protocol string
		want                 []string
	}{
		// openai 渠道的 base_url 直接拼 /chat/completions，裸域名时与之同级的模型端点是
		// {base}/models——这必须是首候选，否则原本探测得通的渠道会变成恒 404。
		{"openai 裸根", "https://api.example.com", "openai",
			[]string{"https://api.example.com/models", "https://api.example.com/v1/models"}},
		// 尾斜杠先裁掉，不能拼出 //models。
		{"尾斜杠", "https://api.example.com/", "openai",
			[]string{"https://api.example.com/models", "https://api.example.com/v1/models"}},
		// 协议为空按 openai 处理（与 buildChannelInfo 的 provType 回退一致）。
		{"协议为空回退 openai", "https://api.example.com", "",
			[]string{"https://api.example.com/models", "https://api.example.com/v1/models"}},
		// base_url 已含 /v1（openai 渠道最常见形态）→ 版本号已在路径里，只补 /models，
		// 不能再拼出 /v1/v1/models。
		{"已含 /v1", "https://api.example.com/v1", "openai",
			[]string{"https://api.example.com/v1/models"}},
		// 非 v1 版本段（智谱 Coding Plan 的 /v4）：正确路径 {base}/models 在前，
		// {base}/v1/models 作兜底次候选。
		{"非 v1 版本段", "https://open.bigmodel.cn/api/coding/paas/v4", "openai",
			[]string{
				"https://open.bigmodel.cn/api/coding/paas/v4/models",
				"https://open.bigmodel.cn/api/coding/paas/v4/v1/models",
			}},
		// anthropic 渠道的 base_url 不含 /v1（主链路据此拼 /v1/messages）→ 与之同级的
		// 模型端点是 {base}/v1/models，优先级与 openai 相反。
		{"anthropic 裸根", "https://api.anthropic.com", "anthropic",
			[]string{"https://api.anthropic.com/v1/models", "https://api.anthropic.com/models"}},
		// Anthropic 兼容层挂在子路径：子路径下试完再剥离后缀回根域名试。
		{"剥离兼容子路径", "https://api.deepseek.com/anthropic", "anthropic",
			[]string{
				"https://api.deepseek.com/anthropic/v1/models",
				"https://api.deepseek.com/anthropic/models",
				"https://api.deepseek.com/v1/models",
				"https://api.deepseek.com/models",
			}},
		// 后缀取最长匹配：/apps/anthropic 不能被更短的 /anthropic 提前吃掉，
		// 否则剥离后会剩下 https://dashscope.aliyuncs.com/apps 这种错误根。
		{"最长后缀优先", "https://dashscope.aliyuncs.com/apps/anthropic", "anthropic",
			[]string{
				"https://dashscope.aliyuncs.com/apps/anthropic/v1/models",
				"https://dashscope.aliyuncs.com/apps/anthropic/models",
				"https://dashscope.aliyuncs.com/v1/models",
				"https://dashscope.aliyuncs.com/models",
			}},
		// 剥完不是完整源站就不当候选，别拼出 "https:/v1/models" 这种残根地址。
		{"剥离后只剩协议头", "https://anthropic", "anthropic",
			[]string{"https://anthropic/v1/models", "https://anthropic/models"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := BuildModelsURLCandidates(tc.base, tc.protocol)
			require.NoError(t, err)
			require.Equal(t, tc.want, got)
		})
	}
}

// 空 base_url 无法推导。
func TestBuildModelsURLCandidatesEmpty(t *testing.T) {
	_, err := BuildModelsURLCandidates("   ", "openai")
	require.Error(t, err)
}

// 版本段判定边界：/vX、/api、/models 都不是版本段。
func TestEndsWithVersionSegment(t *testing.T) {
	require.True(t, endsWithVersionSegment("https://x.com/v1"))
	require.True(t, endsWithVersionSegment("https://x.com/v10"))
	require.True(t, endsWithVersionSegment("https://open.bigmodel.cn/api/coding/paas/v4"))
	require.False(t, endsWithVersionSegment("https://x.com/vX"))
	require.False(t, endsWithVersionSegment("https://x.com/api"))
	require.False(t, endsWithVersionSegment("https://x.com/models"))
	require.False(t, endsWithVersionSegment("https://api.siliconflow.cn"))
}

// —— 按候选顺序拉取 ——

// 首候选 404 → 继续试下一个；命中 200 即返回模型名。
func TestFetchModelsFallsBackOn404(t *testing.T) {
	var tried []string
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		tried = append(tried, r.URL.Path)
		if r.URL.Path != "/v1/models" {
			w.WriteHeader(404)
			return
		}
		_, _ = w.Write([]byte(`{"data":[{"id":"claude-sonnet-5"},{"id":"claude-haiku-4-5"}]}`))
	}))
	defer up.Close()

	models, err := FetchModels(&http.Client{}, up.URL+"/anthropic", "k", "anthropic", "auto", nil)
	require.NoError(t, err)
	require.Equal(t, []string{"claude-sonnet-5", "claude-haiku-4-5"}, models)
	require.Equal(t, []string{"/anthropic/v1/models", "/anthropic/models", "/v1/models"}, tried,
		"404 后应继续试下一个候选")
}

// 405 与 404 同等对待：都表示「这个端点不对」，继续试。
func TestFetchModelsFallsBackOn405(t *testing.T) {
	var tried []string
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		tried = append(tried, r.URL.Path)
		if r.URL.Path != "/models" {
			w.WriteHeader(405)
			return
		}
		_, _ = w.Write([]byte(`{"data":[{"id":"deepseek-chat"}]}`))
	}))
	defer up.Close()

	models, err := FetchModels(&http.Client{}, up.URL+"/anthropic", "k", "anthropic", "auto", nil)
	require.NoError(t, err)
	require.Equal(t, []string{"deepseek-chat"}, models)
	require.Len(t, tried, 4, "剥离兼容后缀后的根域名候选排在最后")
}

// 401/403/5xx 是「端点对了但请求有问题」→ 立即失败，不浪费后续候选。
func TestFetchModelsStopsOnAuthFailure(t *testing.T) {
	var tried []string
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		tried = append(tried, r.URL.Path)
		w.WriteHeader(401)
		_, _ = w.Write([]byte(`{"error":"invalid key"}`))
	}))
	defer up.Close()

	_, err := FetchModels(&http.Client{}, up.URL+"/anthropic", "k", "anthropic", "auto", nil)
	require.Error(t, err)
	require.Len(t, tried, 1, "鉴权失败不应继续试其它候选")
	var fe *ModelsFetchError
	require.ErrorAs(t, err, &fe)
	require.Equal(t, 401, fe.Status)
	require.Contains(t, fe.Body, "invalid key")
}

// 全部候选都 404 → 报错并带上试过的完整候选清单，便于排查。
func TestFetchModelsAllCandidatesFail(t *testing.T) {
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(404)
		_, _ = w.Write([]byte(`not found`))
	}))
	defer up.Close()

	_, err := FetchModels(&http.Client{}, up.URL+"/anthropic", "k", "anthropic", "auto", nil)
	require.Error(t, err)
	var fe *ModelsFetchError
	require.ErrorAs(t, err, &fe)
	require.Len(t, fe.Tried, 4)
	require.Equal(t, 404, fe.Status)
	require.Contains(t, err.Error(), "404")
}

// 上游返回合法 JSON 但没有模型：算成功，返回空列表（由调用方提示「无模型」）。
func TestFetchModelsEmptyDataIsSuccess(t *testing.T) {
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"data":[]}`))
	}))
	defer up.Close()

	models, err := FetchModels(&http.Client{}, up.URL, "k", "openai", "auto", nil)
	require.NoError(t, err)
	require.Empty(t, models)
}

// 响应体不是 JSON → 明确报「格式不合法」，不要静默当成空列表。
func TestFetchModelsRejectsNonJSON(t *testing.T) {
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`<html>hello</html>`))
	}))
	defer up.Close()

	_, err := FetchModels(&http.Client{}, up.URL, "k", "openai", "auto", nil)
	require.Error(t, err)
	require.Contains(t, err.Error(), "格式")
}

// 上游错误体回显了凭证 → 进错误信息前必须脱敏（管理面也不该看到明文 key）。
func TestFetchModelsRedactsCredentialsInError(t *testing.T) {
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(401)
		_, _ = w.Write([]byte(`{"message":"bad key: up-secret-value"}`))
	}))
	defer up.Close()

	_, err := FetchModels(&http.Client{}, up.URL, "up-secret-value", "openai", "auto", nil)
	require.Error(t, err)
	require.NotContains(t, err.Error(), "up-secret-value")
	require.Contains(t, err.Error(), "[REDACTED]")
}

// 超长错误体截断，避免几十 KB 的 HTML 404 页整页灌进错误信息。
func TestFetchModelsTruncatesLongErrorBody(t *testing.T) {
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(500)
		_, _ = w.Write([]byte(strings.Repeat("x", 4096)))
	}))
	defer up.Close()

	_, err := FetchModels(&http.Client{}, up.URL, "k", "openai", "auto", nil)
	require.Error(t, err)
	var fe *ModelsFetchError
	require.ErrorAs(t, err, &fe)
	require.Less(t, len([]rune(fe.Body)), 600)
}

// 出站头仍走统一治理：anthropic 渠道拉模型带 x-api-key + anthropic-version。
func TestFetchModelsUsesOutboundHeaderGovernance(t *testing.T) {
	var gotKey, gotVersion string
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotKey = r.Header.Get("X-Api-Key")
		gotVersion = r.Header.Get("Anthropic-Version")
		_, _ = w.Write([]byte(`{"data":[{"id":"m"}]}`))
	}))
	defer up.Close()

	_, err := FetchModels(&http.Client{}, up.URL, "up-secret", "anthropic", "auto", nil)
	require.NoError(t, err)
	require.Equal(t, "up-secret", gotKey)
	require.Equal(t, "2023-06-01", gotVersion)
}
