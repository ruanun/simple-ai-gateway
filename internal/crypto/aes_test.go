package crypto

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestEncryptDecryptRoundTrip(t *testing.T) {
	key := "0123456789abcdef0123456789abcdef" // 32 bytes
	enc, err := Encrypt(key, "sk-upstream-secret")
	require.NoError(t, err)
	require.NotEqual(t, "sk-upstream-secret", enc)
	dec, err := Decrypt(key, enc)
	require.NoError(t, err)
	require.Equal(t, "sk-upstream-secret", dec)
}
