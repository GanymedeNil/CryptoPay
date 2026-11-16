package invoice

import (
	"errors"
	"sort"
	"strings"
)

// Sentinel errors of the invoice service. Compare with errors.Is. The
// suggested HTTP mapping (docs/api.md error codes) is given per error.
var (
	// ErrNotFound: the invoice / transfer does not exist. → 404 not_found.
	ErrNotFound = errors.New("invoice: not found")
	// ErrInvalid: the request failed validation; the error is a
	// *ValidationError carrying per-field messages. → 400 invalid_request.
	ErrInvalid = errors.New("invoice: invalid request")
	// ErrOrderConflict: order_id was already used by this merchant with a
	// different amount or currency. → 409 conflict.
	ErrOrderConflict = errors.New("invoice: order id already used with a different amount or currency")
	// ErrNotOpen: the invoice cannot be paid any more (expired, paid,
	// underpaid, cancelled), so no asset can be selected. → 409 conflict.
	ErrNotOpen = errors.New("invoice: not open for payment")
	// ErrFundsDetected: funds were already seen for the invoice, so the
	// customer cannot switch to another asset. → 409 conflict.
	ErrFundsDetected = errors.New("invoice: funds already detected, asset cannot be changed")
	// ErrConflict: the invoice (or transfer) is in a state that does not
	// allow the action: cancelling a non-pending invoice, marking a paid or
	// cancelled invoice as paid, assigning an already matched transfer.
	// → 409 conflict.
	ErrConflict = errors.New("invoice: action not allowed in the current state")
	// ErrAssetUnavailable: the asset is unknown or not payable right now for
	// the invoice's merchant (asset or chain disabled, neither a merchant
	// wallet nor a default wallet on the chain, wallet misconfigured).
	// → 422 unprocessable.
	ErrAssetUnavailable = errors.New("invoice: asset not available for payment")
	// ErrRateUnavailable: no exchange rate could be obtained for the asset /
	// currency pair. → 422 unprocessable.
	ErrRateUnavailable = errors.New("invoice: exchange rate unavailable")
	// ErrNoUniqueAmount: no free unique amount could be found on a static
	// wallet (too many concurrent invoices of the same price). Retrying
	// later may work. → 503 unavailable.
	ErrNoUniqueAmount = errors.New("invoice: no unique amount available on the static wallet")
	// ErrNoMatchingQuote: AssignTransfer — the invoice has no quote for the
	// transfer's chain and asset (or the transfer's token is not a configured
	// asset). → 422 unprocessable.
	ErrNoMatchingQuote = errors.New("invoice: invoice has no quote for the transfer's chain and asset")
)

// ValidationError reports invalid request fields (JSON field name →
// message). It matches ErrInvalid.
type ValidationError struct {
	Fields map[string]string
}

func (e *ValidationError) Error() string {
	keys := make([]string, 0, len(e.Fields))
	for k := range e.Fields {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		parts = append(parts, k+": "+e.Fields[k])
	}
	return ErrInvalid.Error() + ": " + strings.Join(parts, "; ")
}

// Is makes errors.Is(err, ErrInvalid) true.
func (e *ValidationError) Is(target error) bool { return target == ErrInvalid }

// FieldErrors returns the per-field messages if err is (or wraps) a
// *ValidationError, else nil.
func FieldErrors(err error) map[string]string {
	var ve *ValidationError
	if errors.As(err, &ve) {
		return ve.Fields
	}
	return nil
}

func invalidField(field, msg string) error {
	return &ValidationError{Fields: map[string]string{field: msg}}
}
