package auth

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"strings"
)

const (
	// APIKeyScheme prefixes every merchant API key.
	APIKeyScheme = "sk_live_"
	// APIKeyPrefixLen is the number of leading characters of an API key kept
	// in clear (api_keys.prefix) so admins can recognise a key.
	APIKeyPrefixLen = 12
	// WebhookSecretScheme prefixes every webhook signing secret.
	WebhookSecretScheme = "whsec_"

	// randomBytes is the entropy of every generated token (256 bits), which
	// encodes to 43 base64url characters without padding.
	randomBytes = 32
	// randomChars is the encoded length of randomBytes.
	randomChars = 43
)

// randomToken returns 32 bytes from crypto/rand encoded as unpadded base64url.
// crypto/rand.Read never returns an error on supported platforms (Go ≥ 1.24
// crashes the program irrecoverably instead), so the error is ignored.
func randomToken() string {
	b := make([]byte, randomBytes)
	_, _ = rand.Read(b)
	return base64.RawURLEncoding.EncodeToString(b)
}

// HashToken returns the lower-case hex SHA-256 of token. API keys and session
// tokens are stored and looked up only by this digest.
func HashToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

// NewAPIKey generates a merchant API key "sk_live_<43 base64url chars>".
// plaintext is shown to the user once, prefix (its first APIKeyPrefixLen
// characters) is stored for display and hash (HashToken(plaintext)) is
// stored for lookup.
func NewAPIKey() (plaintext, prefix, hash string) {
	plaintext = APIKeyScheme + randomToken()
	return plaintext, plaintext[:APIKeyPrefixLen], HashToken(plaintext)
}

// LooksLikeAPIKey reports whether s is syntactically an API key (scheme plus
// at least 43 base64url characters). Use it to reject garbage before hashing
// and hitting the database.
func LooksLikeAPIKey(s string) bool {
	rest, ok := strings.CutPrefix(s, APIKeyScheme)
	if !ok || len(rest) < randomChars || len(rest) > 256 {
		return false
	}
	for i := 0; i < len(rest); i++ {
		c := rest[i]
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9', c == '-', c == '_':
		default:
			return false
		}
	}
	return true
}

// NewSessionToken generates an admin session token (43 base64url chars) for
// the session cookie, and its HashToken digest for admin_sessions.token_hash.
func NewSessionToken() (plaintext, hash string) {
	plaintext = randomToken()
	return plaintext, HashToken(plaintext)
}

// NewWebhookSecret generates a webhook signing secret "whsec_<43 base64url chars>".
func NewWebhookSecret() string { return WebhookSecretScheme + randomToken() }
