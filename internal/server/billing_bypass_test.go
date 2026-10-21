package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/ruanun/simple-ai-gateway/internal/model"
	"github.com/stretchr/testify/require"
)

// strictUsageUpstream 行为贴近真实 OpenAI：流式只有请求里 stream_options.include_usage=true 才回带 usage 块。
// 同时记录上游实际收到的请求体，用于断言出站改写（普通透传 mock 不校验请求体，会漏掉改写被绕过的 bug）。
func strictUsageUpstream(t *testing.T) (*httptest.Server, func() []map[string]any) {
	t.Helper()
	var mu sync.Mutex
	var got []map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := readAll(r)
		var m map[string]any
		dec := json.NewDecoder(strings.NewReader(string(raw)))
		dec.UseNumber() // 上游侧也按原文保留数字，便于断言 1e999 未被篡改
		_ = dec.Decode(&m)
		mu.Lock()
		got = append(got, m)
		mu.Unlock()

		switch r.URL.Path {
		case "/chat/completions":
			so, _ := m["stream_options"].(map[string]any)
			wantUsage, _ := so["include_usage"].(bool)
			if stream, _ := m["stream"].(bool); stream {
				w.Header().Set("Content-Type", "text/event-stream")
				_, _ = w.Write([]byte("data: {\"choices\":[{\"delta\":{\"content\":\"hi\"},\"finish_reason\":\"stop\"}]}\n\n"))
				if wantUsage {
					_, _ = w.Write([]byte("data: {\"choices\":[],\"usage\":{\"prompt_tokens\":10,\"completion_tokens\":5,\"total_tokens\":15}}\n\n"))
				}
				_, _ = w.Write([]byte("data: [DONE]\n\n"))
				return
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"id":"x","choices":[{"message":{"content":"hi"},"finish_reason":"stop"}],"usage":{"prompt_tokens":10,"completion_tokens":5,"total_tokens":15}}`))
		case "/embeddings":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"object":"list","data":[{"embedding":[0.1]}],"usage":{"prompt_tokens":8,"total_tokens":8}}`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	return srv, func() []map[string]any {
		mu.Lock()
		defer mu.Unlock()
		return append([]map[string]any(nil), got...)
	}
}

// chatWith 发一条指定内容的 chat 请求（chatReq 固定内容 "hi" 的通用版）。
func chatWith(r http.Handler, key, model, content string) (int, string) {
	return v1Post(r, key, "/v1/chat/completions",
		`{"model":"`+model+`","messages":[{"role":"user","content":"`+content+`"}]}`)
}

// v1Post 以令牌身份 POST 一个 /v1 端点，返回状态码与响应体。
func v1Post(r http.Handler, key, path, body string) (int, string) {
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+key)
	req.Header.Set("Content-Type", "application/json")
	r.ServeHTTP(w, req)
	return w.Code, w.Body.String()
}

// 回归：流式请求带 "stream_options":null 曾不注入 include_usage，上游不回 usage，本次按 0 计费。
// 修复后须照常注入并计费；客户端自己没要 usage，回写时剥掉注入产生的用量块。
func TestStreamOptionsNullStillBilled(t *testing.T) {
	gin.SetMode(gin.TestMode)
	up, received := strictUsageUpstream(t)
	defer up.Close()
	r, key, g := setupMultiProto(t, up.URL)

	for _, so := range []string{`null`, `"x"`, `1`, `{"include_usage":false}`} {
		code, body := v1Post(r, key, "/v1/chat/completions",
			`{"model":"gpt-4o","stream":true,"stream_options":`+so+`,"messages":[{"role":"user","content":"hi"}]}`)
		require.Equal(t, http.StatusOK, code, "stream_options=%s resp=%s", so, body)
		require.NotContains(t, body, `"usage"`, "客户端未要 usage，注入的用量块不应回写给客户端 (stream_options=%s)", so)

		reqs := received()
		out, _ := reqs[len(reqs)-1]["stream_options"].(map[string]any)
		require.Equal(t, true, out["include_usage"], "出站须强制 include_usage=true (stream_options=%s)", so)

		var row model.RequestLog
		require.NoError(t, g.Order("id desc").First(&row).Error)
		require.Equal(t, 15, row.TotalTokens, "须拿到上游 usage (stream_options=%s)", so)
		require.Positive(t, row.CostMicros, "不得按 0 计费 (stream_options=%s)", so)
	}
}

// 回归：请求体含超出 float64 的数字（1e999）时，入口读 model 成功而出站改写解析失败、原样发出，
// 模型映射/白名单被绕过（按映射后的模型治理计费，上游却跑客户端原始模型）且未注入 include_usage。
// chat 透传与 embeddings（DispatchRaw）两条出站路径都验。
func TestHugeNumberCannotBypassModelRewrite(t *testing.T) {
	gin.SetMode(gin.TestMode)
	up, received := strictUsageUpstream(t)
	defer up.Close()
	r, key, g := setupMultiProto(t, up.URL)
	token := doJSON(t, r, http.MethodPost, "/admin/login", "",
		`{"username":"admin","password":"sag12345"}`, http.StatusOK)["token"].(string)
	doJSON(t, r, http.MethodPost, "/admin/api-keys/"+strconv.FormatUint(lastKeyID(t, g), 10), token,
		`{"model_mappings":"{\"gpt-4\":\"gpt-4o\"}"}`, http.StatusOK)

	code, body := v1Post(r, key, "/v1/chat/completions",
		`{"model":"gpt-4","stream":true,"messages":[{"role":"user","content":"hi"}],`+
			`"tools":[{"type":"function","function":{"name":"f","parameters":{"type":"object","maximum":1e999}}}]}`)
	require.Equal(t, http.StatusOK, code, "resp=%s", body)
	reqs := received()
	out := reqs[len(reqs)-1]
	require.Equal(t, "gpt-4o", out["model"], "出站 model 须为映射后模型，不得原样发出客户端模型")
	so, _ := out["stream_options"].(map[string]any)
	require.Equal(t, true, so["include_usage"], "须注入 include_usage")
	params := out["tools"].([]any)[0].(map[string]any)["function"].(map[string]any)["parameters"].(map[string]any)
	require.Equal(t, json.Number("1e999"), params["maximum"], "客户端数字须按原文保留")
	var row model.RequestLog
	require.NoError(t, g.Order("id desc").First(&row).Error)
	require.Positive(t, row.CostMicros, "须照常计费")

	code, body = v1Post(r, key, "/v1/embeddings", `{"model":"gpt-4","input":"hi","dimensions":1e999}`)
	require.Equal(t, http.StatusOK, code, "resp=%s", body)
	reqs = received()
	require.Equal(t, "gpt-4o", reqs[len(reqs)-1]["model"], "embeddings 出站 model 同样须改写")

	// 非对象/尾随垃圾：入口直接 400，不进派发
	for _, bad := range []string{`[{"model":"gpt-4o"}]`, `{"model":"gpt-4o"} {"model":"x"}`} {
		code, _ = v1Post(r, key, "/v1/chat/completions", bad)
		require.Equal(t, http.StatusBadRequest, code, "非法请求体须 400: %s", bad)
	}
}

// 回归：stream 判定曾用大小写不敏感的 struct 解码，{"stream":true,"Stream":false} 被网关判为非流式
// （不注入 include_usage），按精确键名解析的上游照常流式返回 → 按 0 计费。非布尔 stream 同理须拒绝。
func TestStreamFlagCannotBeSpoofed(t *testing.T) {
	gin.SetMode(gin.TestMode)
	up, received := strictUsageUpstream(t)
	defer up.Close()
	r, key, g := setupMultiProto(t, up.URL)

	code, body := v1Post(r, key, "/v1/chat/completions",
		`{"model":"gpt-4o","stream":true,"Stream":false,"messages":[{"role":"user","content":"hi"}]}`)
	require.Equal(t, http.StatusOK, code, "resp=%s", body)
	reqs := received()
	so, _ := reqs[len(reqs)-1]["stream_options"].(map[string]any)
	require.Equal(t, true, so["include_usage"], "网关须按精确键名判为流式并注入 include_usage")
	var row model.RequestLog
	require.NoError(t, g.Order("id desc").First(&row).Error)
	require.True(t, row.Stream, "请求日志应记为流式")
	require.Positive(t, row.CostMicros, "不得按 0 计费")

	for _, bad := range []string{`"true"`, `1`, `{}`} {
		code, body = v1Post(r, key, "/v1/chat/completions",
			`{"model":"gpt-4o","stream":`+bad+`,"messages":[{"role":"user","content":"hi"}]}`)
		require.Equal(t, http.StatusBadRequest, code, "非布尔 stream=%s 须 400: %s", bad, body)
	}
	code, _ = v1Post(r, key, "/v1/chat/completions", `{"model":"gpt-4o","stream":null,"messages":[{"role":"user","content":"hi"}]}`)
	require.Equal(t, http.StatusOK, code, "stream:null 视为非流式，合法")
}

// 回归：价格前置校验曾只查首个候选。别名的主渠道已配价、后备渠道未配价时，主渠道一故障，
// 请求就落到后备渠道按 0 计费。现未配价候选在派发前即被剔除。
func TestUnpricedFallbackCandidateNotUsed(t *testing.T) {
	gin.SetMode(gin.TestMode)
	failing := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer failing.Close()
	up := multiProtoUpstream()
	defer up.Close()
	r, key, g := setupMultiProto(t, up.URL)
	admin := doJSON(t, r, http.MethodPost, "/admin/login", "",
		`{"username":"admin","password":"sag12345"}`, http.StatusOK)["token"].(string)

	var healthy model.Channel
	require.NoError(t, g.Where("name = ?", "c1").First(&healthy).Error)
	bad := doJSON(t, r, http.MethodPost, "/admin/channels", admin,
		`{"type":"openai","name":"primary-down","base_url":"`+failing.URL+`","api_key":"sk-down"}`, http.StatusOK)
	// 别名边只在渠道也声明了该模型时生效
	doJSON(t, r, http.MethodPost, "/admin/channel-models", admin,
		`{"channel_id":`+itoa(bad["id"].(float64))+`,"model":"gpt-4o","enabled":true}`, http.StatusOK)
	doJSON(t, r, http.MethodPost, "/admin/channel-models", admin,
		`{"channel_id":`+itoa(float64(healthy.ID))+`,"model":"unpriced-x","enabled":true}`, http.StatusOK)
	alias := doJSON(t, r, http.MethodPost, "/admin/aliases", admin, `{"alias":"smart"}`, http.StatusOK)
	aid := itoa(alias["id"].(float64))
	// 主：已配价（gpt-4o），优先级高但上游 500；后备：未配价模型，上游健康
	doJSON(t, r, http.MethodPost, "/admin/alias-routes", admin,
		`{"alias_id":`+aid+`,"channel_id":`+itoa(bad["id"].(float64))+`,"model":"gpt-4o","weight":1,"priority":10}`, http.StatusOK)
	doJSON(t, r, http.MethodPost, "/admin/alias-routes", admin,
		`{"alias_id":`+aid+`,"channel_id":`+itoa(float64(healthy.ID))+`,"model":"unpriced-x","weight":1,"priority":1}`, http.StatusOK)

	code, body := chatReq(r, key, "smart")
	require.Equal(t, http.StatusBadGateway, code, "主渠道失败后不得落到未配价的后备渠道: %s", body)
	var free int64
	g.Model(&model.RequestLog{}).Where("status = ? AND cost_micros = 0", http.StatusOK).Count(&free)
	require.Zero(t, free, "不得产生 0 计费的成功请求")
}
