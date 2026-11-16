package webhook

import (
	"encoding/json"
	"fmt"
	"time"

	"github.com/google/uuid"
)

// Envelope is the JSON document POSTed to merchants.
type Envelope struct {
	ID        uuid.UUID `json:"id"`
	Type      string    `json:"type"`
	CreatedAt string    `json:"created_at"`
	Data      any       `json:"data"`
}

// BuildEnvelope returns the canonical webhook payload
// {"id","type","created_at","data"}: the event id doubles as the delivery id
// (X-CryptoPay-Delivery), created_at is RFC 3339 in UTC with nanosecond
// precision trimmed of trailing zeros. The bytes are compact JSON; the
// dispatcher sends the stored payload verbatim.
func BuildEnvelope(eventID uuid.UUID, eventType string, createdAt time.Time, data any) (json.RawMessage, error) {
	if eventType == "" {
		return nil, fmt.Errorf("webhook: event type is required")
	}
	b, err := json.Marshal(Envelope{
		ID:        eventID,
		Type:      eventType,
		CreatedAt: createdAt.UTC().Format(time.RFC3339Nano),
		Data:      data,
	})
	if err != nil {
		return nil, fmt.Errorf("webhook: marshal envelope: %w", err)
	}
	return b, nil
}
