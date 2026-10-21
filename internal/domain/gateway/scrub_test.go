package gateway

import (
	"net/http"
	"strings"
	"testing"
)

func TestScrubSecrets(t *testing.T) {
	cases := []struct {
		name string
		in   string
		leak string // 脱敏后不应再出现的子串
	}{
		{"openai掩码key", `{"error":{"message":"Incorrect API key provided: sk-abc***xyz"}}`, "sk-abc"},
		{"完整sk-key", `{"error":"invalid key sk-ant-api03-AAAA-BBBB"}`, "sk-ant-api03"},
		{"bearer凭证", `upstream said: Authorization: Bearer eyJhbGciOiJI.abcdef`, "eyJhbGciOiJI"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			out := string(scrubSecrets([]byte(c.in)))
			if strings.Contains(out, c.leak) {
				t.Fatalf("脱敏后仍含 %q: %s", c.leak, out)
			}
			if !strings.Contains(out, "[REDACTED]") {
				t.Fatalf("未产生脱敏标记: %s", out)
			}
		})
	}
}

func TestScrubSecretsKeepsNormalError(t *testing.T) {
	// 不含凭证的普通校验错误应原样保留，保证错误透明度。
	in := `{"error":{"message":"temperature must be between 0 and 2","type":"invalid_request_error"}}`
	out := string(scrubSecrets([]byte(in)))
	if out != in {
		t.Fatalf("普通错误被改动: %s", out)
	}
}

func TestScrubIfErrorOnlyScrubsErrors(t *testing.T) {
	body := []byte(`{"key":"sk-leak-secret"}`)
	if got := string(scrubIfError(&http.Response{StatusCode: 200}, body)); !strings.Contains(got, "sk-leak-secret") {
		t.Fatalf("2xx 不应脱敏: %s", got)
	}
	if got := string(scrubIfError(&http.Response{StatusCode: 401}, body)); strings.Contains(got, "sk-leak-secret") {
		t.Fatalf("4xx 应脱敏: %s", got)
	}
}

func TestClientSafeDetail(t *testing.T) {
	detail := "all candidates failed [ch3(gpt-4o):连接失败(dial tcp 10.0.0.5:443); ch7:上游401]"
	if got := clientSafeDetail(502, detail); got != "all upstream candidates failed" {
		t.Fatalf("502 应收敛为泛化文案, got=%q", got)
	}
	if strings.Contains(clientSafeDetail(502, detail), "10.0.0.5") {
		t.Fatal("502 对外文案不应含内部 IP")
	}
	// 非 5xx 的网关短文案原样返回。
	if got := clientSafeDetail(429, "channel rate limited"); got != "channel rate limited" {
		t.Fatalf("429 文案应原样, got=%q", got)
	}
}
