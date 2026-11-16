package secret

import (
	"encoding/base64"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const testSecret = "0123456789abcdef0123456789abcdef"

func TestNewRequiresLongSecret(t *testing.T) {
	_, err := New("")
	require.ErrorIs(t, err, ErrSecretTooShort)
	_, err = New(strings.Repeat("x", MinSecretLength-1))
	require.ErrorIs(t, err, ErrSecretTooShort)
	b, err := New(strings.Repeat("x", MinSecretLength))
	require.NoError(t, err)
	require.NotNil(t, b)
	assert.Panics(t, func() { MustNew("short") })
}

func TestRoundTrip(t *testing.T) {
	b := MustNew(testSecret)
	for _, plain := range []string{
		"a",
		"https://eth-mainnet.g.alchemy.com/v2/SECRET-KEY",
		`["https://a.example/key1","https://b.example/key2"]`,
		"whsec_" + strings.Repeat("Z", 43),
		"ünïcødé ✓ 支付",
		strings.Repeat("long", 10_000),
	} {
		ct, err := b.Encrypt(plain)
		require.NoError(t, err)
		assert.True(t, strings.HasPrefix(ct, "v1:"), ct)
		assert.True(t, IsEncrypted(ct))
		if len(plain) > 4 {
			assert.NotContains(t, ct, plain)
		}
		got, err := b.Decrypt(ct)
		require.NoError(t, err)
		assert.Equal(t, plain, got)
	}
}

func TestEmptyRoundTrip(t *testing.T) {
	b := MustNew(testSecret)
	ct, err := b.Encrypt("")
	require.NoError(t, err)
	assert.Equal(t, "", ct)
	assert.False(t, IsEncrypted(ct))
	pt, err := b.Decrypt("")
	require.NoError(t, err)
	assert.Equal(t, "", pt)
}

func TestNonceIsRandom(t *testing.T) {
	b := MustNew(testSecret)
	seen := map[string]bool{}
	for range 100 {
		ct, err := b.Encrypt("same plaintext")
		require.NoError(t, err)
		require.False(t, seen[ct], "ciphertext repeated")
		seen[ct] = true
	}
}

func TestTamperDetection(t *testing.T) {
	b := MustNew(testSecret)
	ct, err := b.Encrypt("top secret")
	require.NoError(t, err)
	raw, err := base64.RawURLEncoding.DecodeString(strings.TrimPrefix(ct, "v1:"))
	require.NoError(t, err)
	for i := range raw {
		mod := append([]byte(nil), raw...)
		mod[i] ^= 0x01
		_, err := b.Decrypt("v1:" + base64.RawURLEncoding.EncodeToString(mod))
		require.ErrorIs(t, err, ErrDecrypt, "flipping byte %d must be detected", i)
	}
	// Truncation.
	_, err = b.Decrypt(ct[:len(ct)-4])
	require.Error(t, err)
}

func TestWrongKey(t *testing.T) {
	a := MustNew(testSecret)
	b := MustNew(testSecret + "-other")
	ct, err := a.Encrypt("secret")
	require.NoError(t, err)
	_, err = b.Decrypt(ct)
	require.ErrorIs(t, err, ErrDecrypt)

	// The same secret always derives the same key.
	c := MustNew(testSecret)
	got, err := c.Decrypt(ct)
	require.NoError(t, err)
	assert.Equal(t, "secret", got)
}

func TestMalformed(t *testing.T) {
	b := MustNew(testSecret)
	for _, in := range []string{
		"plaintext",
		"v2:AAAA",
		"v1:",
		"v1:***",
		"v1:" + base64.RawURLEncoding.EncodeToString([]byte("short")),
	} {
		_, err := b.Decrypt(in)
		require.ErrorIs(t, err, ErrMalformed, in)
	}
}

func TestConcurrentUse(t *testing.T) {
	b := MustNew(testSecret)
	var wg sync.WaitGroup
	for i := range 32 {
		wg.Go(func() {
			plain := strings.Repeat("x", i+1)
			ct, err := b.Encrypt(plain)
			assert.NoError(t, err)
			got, err := b.Decrypt(ct)
			assert.NoError(t, err)
			assert.Equal(t, plain, got)
		})
	}
	wg.Wait()
}
