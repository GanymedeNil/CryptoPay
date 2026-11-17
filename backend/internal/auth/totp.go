package auth

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha1"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base32"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"time"
)

// TOTP parameters (RFC 6238 with the defaults every authenticator app
// supports): HMAC-SHA1, 6 digits, 30-second steps. Codes of the previous,
// current and next step are accepted to tolerate clock drift.
const (
	TOTPDigits     = 6
	TOTPPeriod     = 30 * time.Second
	TOTPSkew       = 1  // steps accepted on either side of the current one
	TOTPSecretSize = 20 // bytes (160 bits, the RFC 4226 recommendation)
	TOTPIssuer     = "CryptoPay"
)

// ErrBadTOTPSecret is returned for a secret that is not valid base32.
var ErrBadTOTPSecret = errors.New("auth: invalid TOTP secret")

var b32 = base32.StdEncoding.WithPadding(base32.NoPadding)

// NewTOTPSecret returns a fresh random secret, base32 encoded (upper case,
// no padding; 32 characters).
func NewTOTPSecret() string {
	b := make([]byte, TOTPSecretSize)
	_, _ = rand.Read(b)
	return b32.EncodeToString(b)
}

// decodeSecret accepts the base32 secret with any case, spaces and padding.
func decodeSecret(secret string) ([]byte, error) {
	s := strings.ToUpper(strings.ReplaceAll(strings.TrimSpace(secret), " ", ""))
	s = strings.TrimRight(s, "=")
	key, err := b32.DecodeString(s)
	if err != nil || len(key) == 0 {
		return nil, ErrBadTOTPSecret
	}
	return key, nil
}

// TOTPStep returns the time step (Unix time / 30 s) of t.
func TOTPStep(t time.Time) int64 { return t.Unix() / int64(TOTPPeriod/time.Second) }

// hotp computes the RFC 4226 HMAC-SHA1 value for counter, truncated to
// digits decimal digits.
func hotp(key []byte, counter uint64, digits int) string {
	var msg [8]byte
	binary.BigEndian.PutUint64(msg[:], counter)
	mac := hmac.New(sha1.New, key)
	mac.Write(msg[:])
	sum := mac.Sum(nil)
	off := sum[len(sum)-1] & 0x0f
	bin := binary.BigEndian.Uint32(sum[off:off+4]) & 0x7fffffff
	mod := uint32(1)
	for range digits {
		mod *= 10
	}
	return fmt.Sprintf("%0*d", digits, bin%mod)
}

// TOTPCode returns the 6-digit code of secret for time step step.
func TOTPCode(secret string, step int64) (string, error) {
	key, err := decodeSecret(secret)
	if err != nil {
		return "", err
	}
	return hotp(key, uint64(step), TOTPDigits), nil
}

// NormalizeTOTPCode strips spaces and dashes a user may type ("123 456").
func NormalizeTOTPCode(code string) string {
	return strings.NewReplacer(" ", "", "-", "", "\t", "").Replace(strings.TrimSpace(code))
}

// VerifyTOTP checks code against secret at time now, accepting the steps
// now−TOTPSkew … now+TOTPSkew that are newer than lastStep (the last step
// accepted for this user: a code is only good once). It returns the matched
// step. Every candidate is compared in constant time.
func VerifyTOTP(secret, code string, now time.Time, lastStep int64) (step int64, ok bool) {
	code = NormalizeTOTPCode(code)
	if len(code) != TOTPDigits {
		return 0, false
	}
	for _, c := range code {
		if c < '0' || c > '9' {
			return 0, false
		}
	}
	key, err := decodeSecret(secret)
	if err != nil {
		return 0, false
	}
	cur := TOTPStep(now)
	matched := int64(-1)
	for s := cur - TOTPSkew; s <= cur+TOTPSkew; s++ {
		want := hotp(key, uint64(s), TOTPDigits)
		eq := subtle.ConstantTimeCompare([]byte(want), []byte(code)) == 1
		if eq && s > lastStep && matched < 0 {
			matched = s
		}
	}
	if matched < 0 {
		return 0, false
	}
	return matched, true
}

// OTPAuthURI returns the provisioning URI for authenticator apps (Google
// Authenticator key URI format): otpauth://totp/CryptoPay:<account>?secret=…
// &issuer=CryptoPay&algorithm=SHA1&digits=6&period=30.
func OTPAuthURI(account, secret string) string {
	label := url.PathEscape(TOTPIssuer) + ":" + url.PathEscape(account)
	q := "secret=" + url.QueryEscape(secret) +
		"&issuer=" + url.QueryEscape(TOTPIssuer) +
		"&algorithm=SHA1" +
		fmt.Sprintf("&digits=%d&period=%d", TOTPDigits, int(TOTPPeriod/time.Second))
	return "otpauth://totp/" + label + "?" + q
}

// ---- recovery codes ----

// RecoveryCodeCount is the size of a set of recovery codes.
const RecoveryCodeCount = 10

// recoveryAlphabet is Crockford's base32 in lower case: no i, l, o, u, so
// nothing can be misread; when a code is typed back, i/l are read as 1 and
// o as 0.
const recoveryAlphabet = "0123456789abcdefghjkmnpqrstvwxyz"

// NewRecoveryCodes returns n random recovery codes "xxxxx-xxxxx" (50 bits
// each).
func NewRecoveryCodes(n int) []string {
	out := make([]string, 0, n)
	seen := map[string]bool{}
	for len(out) < n {
		b := make([]byte, 10)
		_, _ = rand.Read(b)
		var sb strings.Builder
		for i, x := range b {
			if i == 5 {
				sb.WriteByte('-')
			}
			sb.WriteByte(recoveryAlphabet[x&31])
		}
		if c := sb.String(); !seen[c] {
			seen[c] = true
			out = append(out, c)
		}
	}
	return out
}

// NormalizeRecoveryCode canonicalises a typed recovery code: lower case,
// separators and spaces removed, Crockford substitutions (i/l → 1, o → 0).
// It returns "" when the result is not 10 alphabet characters.
func NormalizeRecoveryCode(code string) string {
	var sb strings.Builder
	for _, r := range strings.ToLower(code) {
		switch r {
		case ' ', '-', '\t':
			continue
		case 'i', 'l':
			r = '1'
		case 'o':
			r = '0'
		}
		if !strings.ContainsRune(recoveryAlphabet, r) {
			return ""
		}
		sb.WriteRune(r)
	}
	if sb.Len() != 10 {
		return ""
	}
	return sb.String()
}

// HashRecoveryCode returns the stored form of a user's recovery code: the
// hex SHA-256 of the normalized code bound to the user id. "" for a code
// that cannot be normalized.
func HashRecoveryCode(userID, code string) string {
	n := NormalizeRecoveryCode(code)
	if n == "" {
		return ""
	}
	sum := sha256.Sum256([]byte("cryptopay/recovery-code/v1:" + userID + ":" + n))
	return hex.EncodeToString(sum[:])
}
