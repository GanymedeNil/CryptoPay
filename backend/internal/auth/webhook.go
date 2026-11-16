package auth

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"strconv"
	"time"
)

// HTTP headers sent with every webhook delivery (see docs/architecture.md).
const (
	// HeaderEvent carries the event type, e.g. "invoice.paid".
	HeaderEvent = "X-CryptoPay-Event"
	// HeaderDelivery carries the webhook event id (stable across retries).
	HeaderDelivery = "X-CryptoPay-Delivery"
	// HeaderTimestamp carries the signing time in unix seconds.
	HeaderTimestamp = "X-CryptoPay-Timestamp"
	// HeaderSignature carries hex(HMAC-SHA256(secret, timestamp + "." + body)).
	HeaderSignature = "X-CryptoPay-Signature"
)

// DefaultSignatureTolerance is the recommended maximum clock difference
// between the signing timestamp and the receiver's clock.
const DefaultSignatureTolerance = 5 * time.Minute

var (
	// ErrSignatureMismatch is returned by Verify when the signature is not
	// valid for the secret, timestamp and body (or is not hex).
	ErrSignatureMismatch = errors.New("auth: webhook signature mismatch")
	// ErrTimestampOutOfRange is returned by Verify when the signature is valid
	// but the timestamp is further than the tolerance from now.
	ErrTimestampOutOfRange = errors.New("auth: webhook timestamp outside tolerance")
)

func mac(secret string, timestamp int64, body []byte) []byte {
	m := hmac.New(sha256.New, []byte(secret))
	m.Write([]byte(strconv.FormatInt(timestamp, 10)))
	m.Write([]byte{'.'})
	m.Write(body)
	return m.Sum(nil)
}

// Sign returns the webhook signature: lower-case
// hex(HMAC-SHA256(secret, "<timestamp>.<body>")), where timestamp is unix
// seconds formatted in decimal (the exact value sent in HeaderTimestamp).
func Sign(secret string, timestamp int64, body []byte) string {
	return hex.EncodeToString(mac(secret, timestamp, body))
}

// Verify checks a webhook signature in constant time. signature is the hex
// value of HeaderSignature (any case). When tolerance > 0 the timestamp must
// also lie within ±tolerance of now, otherwise ErrTimestampOutOfRange is
// returned; tolerance ≤ 0 disables the window check.
func Verify(secret string, timestamp int64, body []byte, signature string, now time.Time, tolerance time.Duration) error {
	got, err := hex.DecodeString(signature)
	if err != nil || !hmac.Equal(got, mac(secret, timestamp, body)) {
		return ErrSignatureMismatch
	}
	if tolerance > 0 {
		diff := now.Sub(time.Unix(timestamp, 0)) // saturates at ±math.MaxInt64
		if diff < 0 {
			diff = -diff // -MinInt64 stays negative: handled below
		}
		if diff < 0 || diff > tolerance {
			return ErrTimestampOutOfRange
		}
	}
	return nil
}
