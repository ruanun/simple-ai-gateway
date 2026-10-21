package channel

import (
	"testing"

	"github.com/ruanun/simple-ai-gateway/internal/crypto"
	"github.com/ruanun/simple-ai-gateway/internal/dbtest"
	"github.com/ruanun/simple-ai-gateway/internal/model"
	"github.com/stretchr/testify/require"
)

const testAESKey = "0123456789abcdef0123456789abcdef" // 32 字节

func newTestService(t *testing.T) *Service {
	t.Helper()
	g := dbtest.OpenInMemory(t)
	require.NoError(t, g.AutoMigrate(&model.Channel{}, &model.Account{}))
	return NewService(NewRepo(g), testAESKey)
}

// Create 应在同一事务内建「渠道 + 默认账号」，且上游 key 以 AES 密文落库（可解回明文）。
func TestChannelCreateTransactionWithDefaultAccount(t *testing.T) {
	svc := newTestService(t)

	ch, err := svc.Create(CreateInput{
		Type: "openai", Name: "ch-a", BaseURL: "http://up", APIKey: "sk-upstream",
		Weight: 2, RateMultiplier: 1.5,
	})
	require.NoError(t, err)
	require.NotZero(t, ch.ID)
	require.Equal(t, int8(1), ch.Status)
	require.Equal(t, 60, ch.Timeout, "timeout=0 应兜底 60")

	// 默认账号随渠道一起落库，channel_id 回填、key 加密存储。
	var accs []model.Account
	require.NoError(t, svc.repo.g.Where("channel_id = ?", ch.ID).Find(&accs).Error)
	require.Len(t, accs, 1)
	acc := accs[0]
	require.Equal(t, "默认账号", acc.Name)
	require.Equal(t, int8(1), acc.Status)
	require.Equal(t, 1, acc.Weight)
	require.NotEqual(t, "sk-upstream", acc.APIKeyEnc, "api_key 不应明文落库")
	plain, err := crypto.Decrypt(testAESKey, acc.APIKeyEnc)
	require.NoError(t, err)
	require.Equal(t, "sk-upstream", plain, "密文应能解回明文")
}

// Create 的兜底：type 空回落 openai、weight<1 兜底 1、rate_multiplier<=0 兜底 1。
func TestChannelCreateDefaults(t *testing.T) {
	svc := newTestService(t)

	ch, err := svc.Create(CreateInput{Name: "ch-b", APIKey: "sk"})
	require.NoError(t, err)
	require.Equal(t, "openai", ch.Type)
	require.Equal(t, 1, ch.Weight)
	require.Equal(t, float64(1), ch.RateMultiplier)
	require.Equal(t, "auto", ch.AuthStyle, "auth_style 空应归一为 auto")
}

// Create 的校验：协议白名单、custom_headers 非法拒绝，均不落库。
func TestChannelCreateValidation(t *testing.T) {
	svc := newTestService(t)

	_, err := svc.Create(CreateInput{Type: "gemini", Name: "bad", APIKey: "sk"})
	require.ErrorIs(t, err, ErrInvalidProtocol)

	_, err = svc.Create(CreateInput{Type: "openai", Name: "bad", APIKey: "sk", CustomHeaders: `null`})
	require.ErrorIs(t, err, ErrInvalidCustomHeaders)

	var n int64
	require.NoError(t, svc.repo.g.Model(&model.Channel{}).Count(&n).Error)
	require.Zero(t, n, "校验失败不应落库任何渠道")
}

// Update：type 过协议白名单、custom_headers 校验、无字段时 changed=false 不触库。
func TestChannelUpdate(t *testing.T) {
	svc := newTestService(t)
	ch, err := svc.Create(CreateInput{Type: "openai", Name: "ch-c", APIKey: "sk"})
	require.NoError(t, err)

	bad := "gemini"
	_, err = svc.Update(ch.ID, UpdateInput{Type: &bad})
	require.ErrorIs(t, err, ErrInvalidProtocol)

	badHdr := `{"My Header":"v"}`
	_, err = svc.Update(ch.ID, UpdateInput{CustomHeaders: &badHdr})
	require.ErrorIs(t, err, ErrInvalidCustomHeaders)

	changed, err := svc.Update(ch.ID, UpdateInput{})
	require.NoError(t, err)
	require.False(t, changed, "无字段时不应触库")

	w, m := 0, float64(-1)
	name := "renamed"
	changed, err = svc.Update(ch.ID, UpdateInput{Name: &name, Weight: &w, RateMultiplier: &m})
	require.NoError(t, err)
	require.True(t, changed)
	got, err := svc.Get(ch.ID)
	require.NoError(t, err)
	require.Equal(t, "renamed", got.Name)
	require.Equal(t, 1, got.Weight, "weight<1 应兜底 1")
	require.Equal(t, float64(1), got.RateMultiplier, "rate_multiplier<=0 应兜底 1")
}

// IsValidProtocol 协议白名单：仅 openai/anthropic。
func TestIsValidProtocol(t *testing.T) {
	require.True(t, IsValidProtocol("openai"))
	require.True(t, IsValidProtocol("anthropic"))
	require.False(t, IsValidProtocol(""))
	require.False(t, IsValidProtocol("gemini"))
	require.False(t, IsValidProtocol("OpenAI"))
}

// NormalizeAuthStyle 归一：白名单内原样返回，空/非法回落 auto。
func TestNormalizeAuthStyle(t *testing.T) {
	for _, s := range []string{"auto", "x-api-key", "bearer", "both"} {
		require.Equal(t, s, NormalizeAuthStyle(s))
	}
	require.Equal(t, "auto", NormalizeAuthStyle(""))
	require.Equal(t, "auto", NormalizeAuthStyle("basic"))
}

// IsValidCustomHeadersJSON 应接受合法 header 名的 JSON 对象，拒 null/非对象/非法 header 名。
// （自 internal/admin 搬入，随校验逻辑下沉到 channel 域。）
func TestIsValidCustomHeadersJSON(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want bool
	}{
		{"合法单头", `{"HTTP-Referer":"https://x.test"}`, true},
		{"合法多头", `{"X-Foo":"1","X-Bar":"2"}`, true},
		{"空对象", `{}`, true},
		{"JSON null", `null`, false},
		{"纯数字", `123`, false},
		{"数组", `[]`, false},
		{"非 JSON", `not-json`, false},
		{"header 名含空格", `{"My Header":"v"}`, false},
		{"混入非法名", `{"X-Ok":"1","Bad Name":"2"}`, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := IsValidCustomHeadersJSON(tc.in); got != tc.want {
				t.Fatalf("IsValidCustomHeadersJSON(%q) = %v, 期望 %v", tc.in, got, tc.want)
			}
		})
	}
}
