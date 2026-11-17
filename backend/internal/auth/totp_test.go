package auth

import (
	"encoding/base32"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// rfc6238Secret is the SHA-1 seed of RFC 6238 appendix B ("12345678901234567890").
var rfc6238Secret = base32.StdEncoding.EncodeToString([]byte("12345678901234567890"))

// TestTOTPRFC6238Vectors checks the SHA-1 test vectors of RFC 6238
// appendix B (8 digits) and their 6-digit truncation.
func TestTOTPRFC6238Vectors(t *testing.T) {
	key, err := decodeSecret(rfc6238Secret)
	require.NoError(t, err)
	vectors := []struct {
		unix int64
		code string
	}{
		{59, "94287082"},
		{1111111109, "07081804"},
		{1111111111, "14050471"},
		{1234567890, "89005924"},
		{2000000000, "69279037"},
		{20000000000, "65353130"},
	}
	for _, v := range vectors {
		step := TOTPStep(time.Unix(v.unix, 0))
		assert.Equal(t, v.code, hotp(key, uint64(step), 8), "T=%d", v.unix)
		six, err := TOTPCode(rfc6238Secret, step)
		require.NoError(t, err)
		assert.Equal(t, v.code[2:], six, "6-digit code is the low 6 digits (T=%d)", v.unix)
		got, ok := VerifyTOTP(rfc6238Secret, v.code[2:], time.Unix(v.unix, 0), 0)
		assert.True(t, ok, "T=%d", v.unix)
		assert.Equal(t, step, got)
	}
	// RFC 4226 appendix D HOTP values (same key, counters 0..2).
	assert.Equal(t, "755224", hotp(key, 0, 6))
	assert.Equal(t, "287082", hotp(key, 1, 6))
	assert.Equal(t, "359152", hotp(key, 2, 6))
}

func TestTOTPWindowAndReplay(t *testing.T) {
	secret := NewTOTPSecret()
	assert.Len(t, secret, 32)
	now := time.Date(2026, 11, 17, 8, 0, 10, 0, time.UTC)
	step := TOTPStep(now)
	code := func(s int64) string {
		c, err := TOTPCode(secret, s)
		require.NoError(t, err)
		return c
	}

	// Previous, current and next step are accepted; two steps away is not.
	for _, d := range []int64{-1, 0, 1} {
		got, ok := VerifyTOTP(secret, code(step+d), now, 0)
		assert.True(t, ok, "offset %d", d)
		assert.Equal(t, step+d, got)
	}
	for _, d := range []int64{-2, 2, 10} {
		_, ok := VerifyTOTP(secret, code(step+d), now, 0)
		assert.False(t, ok, "offset %d", d)
	}

	// Replay protection: a step at or below the last accepted one is refused.
	_, ok := VerifyTOTP(secret, code(step), now, step)
	assert.False(t, ok, "same step twice")
	_, ok = VerifyTOTP(secret, code(step-1), now, step)
	assert.False(t, ok, "older step after a newer one")
	got, ok := VerifyTOTP(secret, code(step+1), now, step)
	assert.True(t, ok, "the next step is still fine")
	assert.Equal(t, step+1, got)

	// Clock injection: the same code is valid 30 s later, not 90 s later.
	_, ok = VerifyTOTP(secret, code(step), now.Add(30*time.Second), 0)
	assert.True(t, ok)
	_, ok = VerifyTOTP(secret, code(step), now.Add(90*time.Second), 0)
	assert.False(t, ok)

	// Formatting: spaces and dashes are ignored; anything else fails.
	c := code(step)
	_, ok = VerifyTOTP(secret, c[:3]+" "+c[3:], now, 0)
	assert.True(t, ok)
	for _, bad := range []string{"", "12345", "1234567", "abcdef", c + "0", "１２３４５６"} {
		_, ok = VerifyTOTP(secret, bad, now, 0)
		assert.False(t, ok, "%q", bad)
	}
	_, ok = VerifyTOTP("not base32 !!", "123456", now, 0)
	assert.False(t, ok)

	// Lower case and padded secrets decode to the same key.
	_, ok = VerifyTOTP(strings.ToLower(secret)+"====", code(step), now, 0)
	assert.True(t, ok)
	_, err := TOTPCode("!!!", step)
	assert.ErrorIs(t, err, ErrBadTOTPSecret)
}

func TestOTPAuthURI(t *testing.T) {
	uri := OTPAuthURI("ops+admin@example.com", "JBSWY3DPEHPK3PXP")
	assert.Equal(t, "otpauth://totp/CryptoPay:ops+admin@example.com?secret=JBSWY3DPEHPK3PXP&issuer=CryptoPay&algorithm=SHA1&digits=6&period=30", uri)
	u, err := url.Parse(uri)
	require.NoError(t, err)
	assert.Equal(t, "totp", u.Host)
	assert.Equal(t, "/CryptoPay:ops+admin@example.com", u.Path)
	assert.Equal(t, "JBSWY3DPEHPK3PXP", u.Query().Get("secret"))

	// Characters that would break the URI are escaped.
	u, err = url.Parse(OTPAuthURI("we ird/name?x=1#y", "ABC"))
	require.NoError(t, err)
	assert.Equal(t, "/CryptoPay:we ird/name?x=1#y", u.Path)
	assert.Equal(t, "ABC", u.Query().Get("secret"))
	assert.Equal(t, "30", u.Query().Get("period"))
}

func TestRecoveryCodes(t *testing.T) {
	codes := NewRecoveryCodes(RecoveryCodeCount)
	require.Len(t, codes, RecoveryCodeCount)
	seen := map[string]bool{}
	for _, c := range codes {
		assert.Regexp(t, `^[0-9a-hjkmnp-tv-z]{5}-[0-9a-hjkmnp-tv-z]{5}$`, c)
		assert.False(t, seen[c], "unique")
		seen[c] = true
		assert.Equal(t, strings.ReplaceAll(c, "-", ""), NormalizeRecoveryCode(c))
		assert.Len(t, HashRecoveryCode("user-1", c), 64)
	}

	// Typed back sloppily: upper case, spaces, Crockford look-alikes.
	assert.Equal(t, "0123456789", NormalizeRecoveryCode(" O1234-56789 "))
	assert.Equal(t, "1100abcdef", NormalizeRecoveryCode("IL0O-ABC DEF"))
	assert.Equal(t, HashRecoveryCode("u", "abcde-fghjk"), HashRecoveryCode("u", "ABCDEFGHJK"))
	for _, bad := range []string{"", "abcd-efgh", "abcde-fghjkm", "abcde-fghj!", "abcde-fghju"} {
		assert.Empty(t, NormalizeRecoveryCode(bad), "%q", bad)
		assert.Empty(t, HashRecoveryCode("u", bad), "%q", bad)
	}

	// Hashes are bound to the user.
	assert.NotEqual(t, HashRecoveryCode("u1", codes[0]), HashRecoveryCode("u2", codes[0]))
}
