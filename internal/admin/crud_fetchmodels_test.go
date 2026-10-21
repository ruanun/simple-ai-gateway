package admin

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/ruanun/simple-ai-gateway/internal/crypto"
	"github.com/ruanun/simple-ai-gateway/internal/dbtest"
	"github.com/ruanun/simple-ai-gateway/internal/domain/account"
	"github.com/ruanun/simple-ai-gateway/internal/domain/channel"
	"github.com/ruanun/simple-ai-gateway/internal/model"
	"github.com/stretchr/testify/require"
)

// FetchUpstreamModels 应按渠道 auth_style 走统一出站头治理：anthropic 渠道(auto+非官方域名)
// 拉模型时上游应收到 x-api-key（修复前恒发 Bearer、无 x-api-key，只认 x-api-key 的中转会 401）。
func TestFetchUpstreamModelsAuthByChannel(t *testing.T) {
	gin.SetMode(gin.TestMode)
	var gotAPIKey, gotAuth, gotVersion string
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAPIKey = r.Header.Get("X-Api-Key")
		gotAuth = r.Header.Get("Authorization")
		gotVersion = r.Header.Get("Anthropic-Version")
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(200)
		_, _ = w.Write([]byte(`{"data":[{"id":"claude-3-5"}]}`))
	}))
	defer up.Close()

	g := dbtest.OpenInMemory(t)
	require.NoError(t, g.AutoMigrate(&model.Channel{}, &model.Account{}))
	aesKey := "0123456789abcdef0123456789abcdef"                                  // 32 字节
	ch := model.Channel{Name: "c", Type: "anthropic", BaseURL: up.URL, Status: 1} // AuthStyle 空=auto
	require.NoError(t, g.Create(&ch).Error)
	enc, err := crypto.Encrypt(aesKey, "up-secret")
	require.NoError(t, err)
	require.NoError(t, g.Create(&model.Account{ChannelID: ch.ID, APIKeyEnc: enc, Status: 1}).Error)

	h := &CRUDHandler{g: g, aesKey: aesKey,
		channels: channel.NewService(channel.NewRepo(g), aesKey),
		accounts: account.NewService(account.NewRepo(g))}
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Params = gin.Params{{Key: "id", Value: strconv.FormatUint(ch.ID, 10)}}
	c.Request = httptest.NewRequest(http.MethodGet, "/admin/channels/x/fetch-models", nil)

	h.FetchUpstreamModels(c)

	require.Equal(t, 200, w.Code)
	require.Equal(t, "up-secret", gotAPIKey, "anthropic 渠道拉模型应带 x-api-key（本次修复核心）")
	require.Equal(t, "Bearer up-secret", gotAuth, "auto+非官方域名=both，Authorization 也在")
	require.Equal(t, "2023-06-01", gotVersion, "anthropic-version 由治理补齐")
}

// fetchModelsFixture 建一条渠道 + 一个启用账号，返回可直接调 FetchUpstreamModels 的 handler 与 ctx。
func fetchModelsFixture(t *testing.T, ch model.Channel, accBaseURL string) (*CRUDHandler, *gin.Context, *httptest.ResponseRecorder) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	g := dbtest.OpenInMemory(t)
	require.NoError(t, g.AutoMigrate(&model.Channel{}, &model.Account{}))
	aesKey := "0123456789abcdef0123456789abcdef"
	require.NoError(t, g.Create(&ch).Error)
	enc, err := crypto.Encrypt(aesKey, "up-secret")
	require.NoError(t, err)
	require.NoError(t, g.Create(&model.Account{ChannelID: ch.ID, APIKeyEnc: enc, BaseURL: accBaseURL, Status: 1}).Error)

	h := &CRUDHandler{g: g, aesKey: aesKey,
		channels: channel.NewService(channel.NewRepo(g), aesKey),
		accounts: account.NewService(account.NewRepo(g))}
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Params = gin.Params{{Key: "id", Value: strconv.FormatUint(ch.ID, 10)}}
	c.Request = httptest.NewRequest(http.MethodPost, "/admin/channels/x/fetch-models", nil)
	return h, c, w
}

// Anthropic 兼容层挂在子路径的上游：子路径下的 /v1/models 是 404，模型列表在根域名上。
// 改造前只试 {base}/models 一条，这类渠道恒 502；现在应降级到候选并拉到模型。
func TestFetchUpstreamModelsFallsBackAcrossCandidates(t *testing.T) {
	var tried []string
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		tried = append(tried, r.URL.Path)
		if r.URL.Path != "/models" {
			w.WriteHeader(404)
			return
		}
		_, _ = w.Write([]byte(`{"data":[{"id":"deepseek-chat"},{"id":"deepseek-reasoner"}]}`))
	}))
	defer up.Close()

	h, c, w := fetchModelsFixture(t, model.Channel{Name: "c", Type: "anthropic", BaseURL: up.URL + "/anthropic", Status: 1}, "")
	h.FetchUpstreamModels(c)

	require.Equal(t, 200, w.Code)
	var body struct {
		Models []string `json:"models"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &body))
	require.Equal(t, []string{"deepseek-chat", "deepseek-reasoner"}, body.Models)
	require.Equal(t, []string{"/anthropic/v1/models", "/anthropic/models", "/v1/models", "/models"}, tried)
}

// 账号配了自己的 base_url 时，主链路走账号的（buildChannelInfo），拉模型也必须走同一个，
// 否则管理员在管理面看到的模型来自另一个上游。
func TestFetchUpstreamModelsPrefersAccountBaseURL(t *testing.T) {
	chUp := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"data":[{"id":"from-channel"}]}`))
	}))
	defer chUp.Close()
	accUp := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"data":[{"id":"from-account"}]}`))
	}))
	defer accUp.Close()

	h, c, w := fetchModelsFixture(t, model.Channel{Name: "c", Type: "openai", BaseURL: chUp.URL, Status: 1}, accUp.URL)
	h.FetchUpstreamModels(c)

	require.Equal(t, 200, w.Code)
	require.Contains(t, w.Body.String(), "from-account")
	require.NotContains(t, w.Body.String(), "from-channel")
}

// 鉴权失败：502 的 message 要带上状态码与上游正文，管理员据此才知道是 key 的问题；
// 正文里回显的凭证必须脱敏。
func TestFetchUpstreamModelsSurfacesUpstreamError(t *testing.T) {
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(401)
		_, _ = w.Write([]byte(`{"message":"invalid key: up-secret"}`))
	}))
	defer up.Close()

	h, c, w := fetchModelsFixture(t, model.Channel{Name: "c", Type: "openai", BaseURL: up.URL, Status: 1}, "")
	h.FetchUpstreamModels(c)

	require.Equal(t, 502, w.Code)
	msg := w.Body.String()
	require.Contains(t, msg, "401")
	require.Contains(t, msg, "invalid key")
	require.NotContains(t, msg, "up-secret", "上游回显的凭证必须脱敏")
}

// 上游模型列表为空是合法状态（如账号没开通任何模型）→ 200 + 空数组，
// 交给前端提示「无新模型可导入」，不再当成 502「格式不合法」。
func TestFetchUpstreamModelsEmptyListIsOK(t *testing.T) {
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"data":[]}`))
	}))
	defer up.Close()

	h, c, w := fetchModelsFixture(t, model.Channel{Name: "c", Type: "openai", BaseURL: up.URL, Status: 1}, "")
	h.FetchUpstreamModels(c)

	require.Equal(t, 200, w.Code)
	require.Contains(t, w.Body.String(), `"models":[]`)
}
