// Package secret encrypts small secrets at rest (RPC URLs carrying API keys,
// merchant webhook secrets) with AES-256-GCM.
//
// The 256-bit key is derived from the application secret (APP_SECRET) with
// HKDF-SHA256, so the raw APP_SECRET never touches the cipher directly and
// the derivation can be versioned. Ciphertexts are self-describing strings:
//
//	v1:<base64url(nonce || ciphertext || tag)>
//
// A fresh random 96-bit nonce is used for every encryption, so encrypting the
// same plaintext twice yields different ciphertexts. The empty string is
// treated specially: it encrypts to "" and "" decrypts to "", which lets
// "not configured" columns stay empty in the database.
package secret

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/hkdf"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"
)

// MinSecretLength is the minimum length (in bytes) of the application secret.
const MinSecretLength = 32

// Version1 is the prefix of ciphertexts produced by this package.
const Version1 = "v1"

const (
	keyInfo  = "cryptopay/secret/v1 aes-256-gcm"
	keySize  = 32
	sep      = ":"
	v1Prefix = Version1 + sep
)

var (
	// ErrSecretTooShort is returned by New when the application secret is
	// shorter than MinSecretLength.
	ErrSecretTooShort = fmt.Errorf("secret: application secret must be at least %d characters", MinSecretLength)
	// ErrMalformed is returned by Decrypt when the input is not a ciphertext
	// produced by this package (unknown version, bad encoding, too short).
	ErrMalformed = errors.New("secret: malformed ciphertext")
	// ErrDecrypt is returned by Decrypt when authentication fails: the
	// ciphertext was tampered with or was encrypted with a different key.
	ErrDecrypt = errors.New("secret: decryption failed (tampered data or wrong key)")
)

// Box encrypts and decrypts strings. It is safe for concurrent use.
type Box struct {
	aead cipher.AEAD
}

// New derives an AES-256-GCM key from appSecret (at least MinSecretLength
// bytes) and returns a Box using it.
func New(appSecret string) (*Box, error) {
	if len(appSecret) < MinSecretLength {
		return nil, ErrSecretTooShort
	}
	key, err := hkdf.Key(sha256.New, []byte(appSecret), nil, keyInfo, keySize)
	if err != nil {
		return nil, fmt.Errorf("secret: derive key: %w", err)
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, fmt.Errorf("secret: init cipher: %w", err)
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("secret: init gcm: %w", err)
	}
	return &Box{aead: aead}, nil
}

// MustNew is like New but panics on error. Intended for tests and fixtures.
func MustNew(appSecret string) *Box {
	b, err := New(appSecret)
	if err != nil {
		panic(err)
	}
	return b
}

// Encrypt encrypts plaintext and returns a versioned, base64url encoded
// ciphertext ("v1:..."). The empty string encrypts to the empty string.
func (b *Box) Encrypt(plaintext string) (string, error) {
	if plaintext == "" {
		return "", nil
	}
	nonce := make([]byte, b.aead.NonceSize(), b.aead.NonceSize()+len(plaintext)+b.aead.Overhead())
	if _, err := rand.Read(nonce); err != nil {
		return "", fmt.Errorf("secret: generate nonce: %w", err)
	}
	sealed := b.aead.Seal(nonce, nonce, []byte(plaintext), []byte(Version1))
	return v1Prefix + base64.RawURLEncoding.EncodeToString(sealed), nil
}

// Decrypt reverses Encrypt. The empty string decrypts to the empty string.
// It returns an error wrapping ErrMalformed for inputs that are not
// ciphertexts of this package and ErrDecrypt when authentication fails.
func (b *Box) Decrypt(ciphertext string) (string, error) {
	if ciphertext == "" {
		return "", nil
	}
	payload, ok := strings.CutPrefix(ciphertext, v1Prefix)
	if !ok {
		return "", fmt.Errorf("%w: unknown version", ErrMalformed)
	}
	raw, err := base64.RawURLEncoding.DecodeString(payload)
	if err != nil {
		return "", fmt.Errorf("%w: %v", ErrMalformed, err)
	}
	ns := b.aead.NonceSize()
	if len(raw) < ns+b.aead.Overhead() {
		return "", fmt.Errorf("%w: too short", ErrMalformed)
	}
	plain, err := b.aead.Open(nil, raw[:ns], raw[ns:], []byte(Version1))
	if err != nil {
		return "", ErrDecrypt
	}
	return string(plain), nil
}

// IsEncrypted reports whether s looks like a ciphertext produced by Encrypt
// (it does not verify it). The empty string is not considered encrypted.
func IsEncrypted(s string) bool {
	return strings.HasPrefix(s, v1Prefix)
}
