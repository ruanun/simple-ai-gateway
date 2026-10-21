package captcha

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"

	"github.com/stretchr/testify/require"
)

// 图形验证码一次性：答对后作废；答错同样作废，不能对同一张图反复试。
func TestVerifyImageOneTime(t *testing.T) {
	s := New(nil)
	id, img, err := s.NewImage()
	require.NoError(t, err)
	require.Contains(t, img, "data:image/png;base64,")

	answer := s.store.Get(id, false)
	require.Len(t, answer, 4)
	require.True(t, s.VerifyImage(id, " "+answer+" "), "首尾空白应容忍")
	require.False(t, s.VerifyImage(id, answer), "用过的验证码须作废")

	id2, _, err := s.NewImage()
	require.NoError(t, err)
	answer2 := s.store.Get(id2, false)
	require.False(t, s.VerifyImage(id2, "wrong"))
	require.False(t, s.VerifyImage(id2, answer2), "答错一次后该图须作废")

	require.False(t, s.VerifyImage("", ""), "空 id/答案须拒绝")
}

// 内存存储达上限后回收，不无界增长。
func TestMemoryStoreBounded(t *testing.T) {
	m := newMemoryStore()
	for i := 0; i < memoryMaxEntries+100; i++ {
		require.NoError(t, m.Set(strconv.Itoa(i), "1234"))
	}
	require.LessOrEqual(t, len(m.m), memoryMaxEntries)
}

func TestVerifyTurnstile(t *testing.T) {
	var gotSecret, gotToken, gotIP string
	reply := `{"success":true}`
	status := http.StatusOK
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		gotSecret, gotToken, gotIP = r.PostForm.Get("secret"), r.PostForm.Get("response"), r.PostForm.Get("remoteip")
		w.WriteHeader(status)
		_, _ = w.Write([]byte(reply))
	}))
	defer srv.Close()
	s := New(nil)
	s.TurnstileURL = srv.URL
	ctx := context.Background()

	ok, err := s.VerifyTurnstile(ctx, "sec", "tok", "1.2.3.4")
	require.NoError(t, err)
	require.True(t, ok)
	require.Equal(t, []string{"sec", "tok", "1.2.3.4"}, []string{gotSecret, gotToken, gotIP})

	reply = `{"success":false,"error-codes":["invalid-input-response"]}`
	ok, err = s.VerifyTurnstile(ctx, "sec", "tok", "")
	require.NoError(t, err)
	require.False(t, ok, "token 无效应判未通过")

	// 实测 Cloudflare 对无效 secret 回 HTTP 400 + JSON 判定体，须解析出错误码而非当作服务不可达
	status, reply = http.StatusBadRequest, `{"success":false,"error-codes":["invalid-input-secret"],"messages":[]}`
	r, err := s.CheckTurnstile(ctx, "bad", "tok", "")
	require.NoError(t, err)
	require.True(t, r.SecretInvalid(), "400 带错误码应解析为密钥无效")
	_, err = s.VerifyTurnstile(ctx, "bad", "tok", "")
	require.Error(t, err, "密钥配错应作为系统性错误报出")

	status, reply = http.StatusBadGateway, `<html>bad gateway</html>`
	_, err = s.CheckTurnstile(ctx, "sec", "tok", "")
	require.Error(t, err, "非 200 且无判定体应报错")

	status, reply = http.StatusInternalServerError, ``
	_, err = s.VerifyTurnstile(ctx, "sec", "tok", "")
	require.Error(t, err, "校验服务异常应报错（调用方 fail-closed）")

	ok, err = s.VerifyTurnstile(ctx, "sec", "", "")
	require.NoError(t, err)
	require.False(t, ok, "空 token 不发请求直接拒绝")
}
