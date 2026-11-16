package auth

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"math"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/crypto/bcrypt"
)

var fast = Hasher{Cost: bcrypt.MinCost}

func TestValidatePassword(t *testing.T) {
	cases := []struct {
		pw string
		ok bool
	}{
		{"", false},
		{"1234567", false},
		{"12345678", true},
		{"pässwörd", true}, // 8 runes, 10 bytes
		{"密码密码密码密", false}, // 7 runes
		{"密码密码密码密码", true}, // 8 runes, 24 bytes
		{strings.Repeat("a", MaxPasswordBytes), true},
		{strings.Repeat("a", MaxPasswordBytes+1), false},
		{strings.Repeat("密", 25), false}, // 25 runes but 75 bytes
	}
	for _, c := range cases {
		err := ValidatePassword(c.pw)
		if c.ok {
			assert.NoError(t, err, "%q", c.pw)
		} else {
			assert.ErrorIs(t, err, ErrWeakPassword, "%q", c.pw)
		}
	}
}

func TestHasherRoundTrip(t *testing.T) {
	h, err := fast.Hash("correct horse battery")
	require.NoError(t, err)
	assert.True(t, strings.HasPrefix(h, "$2a$04$"), h)
	assert.NoError(t, fast.Verify(h, "correct horse battery"))
	assert.ErrorIs(t, fast.Verify(h, "correct horse batterY"), ErrPasswordMismatch)
	assert.True(t, fast.Check(h, "correct horse battery"))
	assert.False(t, fast.Check(h, ""))
	// Verification uses the cost stored in the hash, not the hasher's.
	assert.True(t, Hasher{}.Check(h, "correct horse battery"))
	assert.True(t, CheckPassword(h, "correct horse battery"))
	assert.NoError(t, VerifyPassword(h, "correct horse battery"))

	h2, err := fast.Hash("correct horse battery")
	require.NoError(t, err)
	assert.NotEqual(t, h, h2, "salted")
}

func TestHasherMalformedHash(t *testing.T) {
	assert.ErrorIs(t, fast.Verify("", "x"), ErrPasswordMismatch)
	assert.ErrorIs(t, fast.Verify("not-a-hash", "x"), ErrPasswordMismatch)
	assert.True(t, fast.NeedsRehash("not-a-hash"))
}

func TestHasherCost(t *testing.T) {
	assert.Equal(t, DefaultBcryptCost, Hasher{}.cost())
	h, err := HashPasswordCost("password1", 5)
	require.NoError(t, err)
	c, err := bcrypt.Cost([]byte(h))
	require.NoError(t, err)
	assert.Equal(t, 5, c)
	assert.True(t, fast.NeedsRehash(h))
	assert.False(t, Hasher{Cost: 5}.NeedsRehash(h))
	assert.True(t, Hasher{}.NeedsRehash(h))

	_, err = HashPasswordCost("password1", 3)
	assert.Error(t, err)
	_, err = HashPasswordCost("password1", 32)
	assert.Error(t, err)
}

func TestHasherRejectsTooLong(t *testing.T) {
	_, err := fast.Hash(strings.Repeat("a", 73))
	assert.ErrorIs(t, err, bcrypt.ErrPasswordTooLong)
	_, err = fast.Hash(strings.Repeat("a", 72))
	assert.NoError(t, err)
}

func TestHashPasswordDefaultCost(t *testing.T) {
	if testing.Short() {
		t.Skip("cost 12 is slow")
	}
	h, err := HashPassword("password1")
	require.NoError(t, err)
	c, err := bcrypt.Cost([]byte(h))
	require.NoError(t, err)
	assert.Equal(t, DefaultBcryptCost, c)
}

func TestVerifyDummyConcurrent(t *testing.T) {
	var wg sync.WaitGroup
	for range 8 {
		wg.Go(func() { fast.VerifyDummy("whatever") })
	}
	wg.Wait()
}

func isBase64URL(s string) bool {
	for _, c := range s {
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '-' || c == '_') {
			return false
		}
	}
	return true
}

func TestNewAPIKey(t *testing.T) {
	seen := map[string]bool{}
	for range 200 {
		key, prefix, hash := NewAPIKey()
		require.True(t, strings.HasPrefix(key, "sk_live_"), key)
		rest := strings.TrimPrefix(key, "sk_live_")
		require.Len(t, rest, 43)
		require.True(t, isBase64URL(rest), key)
		require.Len(t, prefix, 12)
		require.Equal(t, key[:12], prefix)
		require.Equal(t, HashToken(key), hash)
		require.True(t, LooksLikeAPIKey(key))
		require.False(t, seen[key], "duplicate key")
		seen[key] = true
	}
}

func TestLooksLikeAPIKey(t *testing.T) {
	good := "sk_live_" + strings.Repeat("A", 43)
	assert.True(t, LooksLikeAPIKey(good))
	assert.True(t, LooksLikeAPIKey(good+"-_x"))
	assert.False(t, LooksLikeAPIKey(""))
	assert.False(t, LooksLikeAPIKey("sk_live_"))
	assert.False(t, LooksLikeAPIKey("sk_live_"+strings.Repeat("A", 42)))
	assert.False(t, LooksLikeAPIKey("sk_test_"+strings.Repeat("A", 43)))
	assert.False(t, LooksLikeAPIKey("sk_live_"+strings.Repeat("A", 42)+"+"))
	assert.False(t, LooksLikeAPIKey("sk_live_"+strings.Repeat("A", 42)+"="))
	assert.False(t, LooksLikeAPIKey("Bearer "+good))
	assert.False(t, LooksLikeAPIKey("sk_live_"+strings.Repeat("A", 300)))
}

func TestNewSessionToken(t *testing.T) {
	tok, hash := NewSessionToken()
	assert.Len(t, tok, 43)
	assert.True(t, isBase64URL(tok))
	assert.Equal(t, HashToken(tok), hash)
	tok2, _ := NewSessionToken()
	assert.NotEqual(t, tok, tok2)
}

func TestNewWebhookSecret(t *testing.T) {
	s := NewWebhookSecret()
	require.True(t, strings.HasPrefix(s, "whsec_"), s)
	assert.Len(t, strings.TrimPrefix(s, "whsec_"), 43)
	assert.True(t, isBase64URL(strings.TrimPrefix(s, "whsec_")))
	assert.NotEqual(t, s, NewWebhookSecret())
}

func TestHashToken(t *testing.T) {
	// Known SHA-256 test vector.
	assert.Equal(t, "ba7816bf8f01cfea414140de5dae2223b00361a396177a9cb410ff61f20015ad", HashToken("abc"))
	assert.Equal(t, "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855", HashToken(""))
}

func TestSignKnownVector(t *testing.T) {
	// Computed independently:
	//   printf '1700000000.{"a":1}' | openssl dgst -sha256 -hmac whsec_test
	sig := Sign("whsec_test", 1700000000, []byte(`{"a":1}`))
	assert.Equal(t, "38877139021993b830af32feea6e18a8da83eb2f6e49ee50bd9e4cf4ca4d3789", sig)
	assert.NotEqual(t, Sign("k", 1, []byte("b")), Sign("k", 2, []byte("b")))
	assert.NotEqual(t, Sign("k", 1, []byte("b")), Sign("k2", 1, []byte("b")))
}

func TestSignMatchesSpec(t *testing.T) {
	ts := int64(1731744000)
	body := []byte(`{"id":"evt_1","type":"invoice.paid"}`)
	m := hmac.New(sha256.New, []byte("secret"))
	m.Write([]byte(strconv.FormatInt(ts, 10) + "." + string(body)))
	assert.Equal(t, hex.EncodeToString(m.Sum(nil)), Sign("secret", ts, body))
	// Negative timestamps are formatted with a sign, like strconv does.
	m = hmac.New(sha256.New, []byte("secret"))
	m.Write([]byte("-5." + string(body)))
	assert.Equal(t, hex.EncodeToString(m.Sum(nil)), Sign("secret", -5, body))
}

func TestVerify(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	body := []byte(`{"id":"e1"}`)
	ts := now.Unix()
	sig := Sign("whsec_x", ts, body)

	assert.NoError(t, Verify("whsec_x", ts, body, sig, now, 5*time.Minute))
	assert.NoError(t, Verify("whsec_x", ts, body, strings.ToUpper(sig), now, 5*time.Minute), "hex is case-insensitive")

	assert.ErrorIs(t, Verify("whsec_y", ts, body, sig, now, 5*time.Minute), ErrSignatureMismatch)
	assert.ErrorIs(t, Verify("whsec_x", ts+1, body, sig, now, 5*time.Minute), ErrSignatureMismatch)
	assert.ErrorIs(t, Verify("whsec_x", ts, []byte(`{"id":"e2"}`), sig, now, 5*time.Minute), ErrSignatureMismatch)
	assert.ErrorIs(t, Verify("whsec_x", ts, body, "zz"+sig[2:], now, 5*time.Minute), ErrSignatureMismatch)
	assert.ErrorIs(t, Verify("whsec_x", ts, body, sig[:62], now, 5*time.Minute), ErrSignatureMismatch)
	assert.ErrorIs(t, Verify("whsec_x", ts, body, "", now, 5*time.Minute), ErrSignatureMismatch)

	// Window: both directions, inclusive bound.
	assert.NoError(t, Verify("whsec_x", ts, body, sig, now.Add(5*time.Minute), 5*time.Minute))
	assert.NoError(t, Verify("whsec_x", ts, body, sig, now.Add(-5*time.Minute), 5*time.Minute))
	assert.ErrorIs(t, Verify("whsec_x", ts, body, sig, now.Add(5*time.Minute+time.Second), 5*time.Minute), ErrTimestampOutOfRange)
	assert.ErrorIs(t, Verify("whsec_x", ts, body, sig, now.Add(-5*time.Minute-time.Second), 5*time.Minute), ErrTimestampOutOfRange)

	// tolerance <= 0 disables the window.
	assert.NoError(t, Verify("whsec_x", ts, body, sig, now.Add(1000*time.Hour), 0))
	assert.NoError(t, Verify("whsec_x", ts, body, sig, now.Add(-1000*time.Hour), -1))

	// Extreme timestamps must not overflow into acceptance.
	for _, extreme := range []int64{math.MaxInt64, math.MinInt64, math.MaxInt64 / 2, math.MinInt64 / 2} {
		s := Sign("whsec_x", extreme, body)
		assert.ErrorIs(t, Verify("whsec_x", extreme, body, s, now, 5*time.Minute), ErrTimestampOutOfRange, extreme)
	}
}

func TestHeaderNames(t *testing.T) {
	assert.Equal(t, "X-CryptoPay-Event", HeaderEvent)
	assert.Equal(t, "X-CryptoPay-Delivery", HeaderDelivery)
	assert.Equal(t, "X-CryptoPay-Timestamp", HeaderTimestamp)
	assert.Equal(t, "X-CryptoPay-Signature", HeaderSignature)
}
