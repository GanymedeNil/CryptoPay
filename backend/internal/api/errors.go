package api

import (
	"context"
	"errors"
	"net/http"
	"strings"

	"cryptopay/internal/invoice"
	"cryptopay/internal/store"
)

// Error codes of docs/api.md.
const (
	CodeInvalidRequest = "invalid_request" // 400
	CodeUnauthorized   = "unauthorized"    // 401
	CodeForbidden      = "forbidden"       // 403
	CodeNotFound       = "not_found"       // 404
	CodeConflict       = "conflict"        // 409
	CodeUnprocessable  = "unprocessable"   // 422
	CodeRateLimited    = "rate_limited"    // 429
	CodeInternal       = "internal"        // 500
	CodeUnavailable    = "unavailable"     // 503

	// CodeTwoFactorSetupRequired (403): require_2fa is on and the caller has
	// not set up two-factor authentication yet.
	CodeTwoFactorSetupRequired = "two_factor_setup_required"
)

// Error is an error with its HTTP representation. Handlers return it (or a
// service error that fail maps) and fail renders the envelope.
type Error struct {
	Status  int
	Code    string
	Message string
	Fields  map[string]string
}

func (e *Error) Error() string { return e.Code + ": " + e.Message }

// envelope is the JSON error body of docs/api.md.
type envelope struct {
	Error envelopeBody `json:"error"`
}

type envelopeBody struct {
	Code    string            `json:"code"`
	Message string            `json:"message"`
	Fields  map[string]string `json:"fields,omitempty"`
}

func errInvalid(msg string, fields map[string]string) *Error {
	if msg == "" {
		msg = "the request is invalid"
		if len(fields) > 0 {
			msg = "some fields are invalid"
		}
	}
	return &Error{Status: http.StatusBadRequest, Code: CodeInvalidRequest, Message: msg, Fields: fields}
}

func errField(field, msg string) *Error {
	return errInvalid("", map[string]string{field: msg})
}

func errUnauthorized(msg string) *Error {
	return &Error{Status: http.StatusUnauthorized, Code: CodeUnauthorized, Message: msg}
}

func errForbidden(msg string) *Error {
	return &Error{Status: http.StatusForbidden, Code: CodeForbidden, Message: msg}
}

func errNotFound(msg string) *Error {
	return &Error{Status: http.StatusNotFound, Code: CodeNotFound, Message: msg}
}

func errConflict(msg string) *Error {
	return &Error{Status: http.StatusConflict, Code: CodeConflict, Message: msg}
}

func errUnprocessable(msg string) *Error {
	return &Error{Status: http.StatusUnprocessableEntity, Code: CodeUnprocessable, Message: msg}
}

// fields collects per-field validation messages.
type fields map[string]string

func (f fields) add(field, msg string) {
	if _, ok := f[field]; !ok {
		f[field] = msg
	}
}

// err returns a 400 invalid_request error when any field was reported.
func (f fields) err() error {
	if len(f) == 0 {
		return nil
	}
	return errInvalid("", map[string]string(f))
}

// toError maps an error of a service or the store to its HTTP
// representation. Unknown errors become 500 internal (and are logged by fail).
func toError(err error) *Error {
	var e *Error
	if errors.As(err, &e) {
		return e
	}
	switch {
	case errors.Is(err, invoice.ErrInvalid):
		return errInvalid("", invoice.FieldErrors(err))
	case errors.Is(err, invoice.ErrNotFound):
		return errNotFound("not found")
	case errors.Is(err, invoice.ErrOrderConflict):
		return errConflict(publicText(invoice.ErrOrderConflict))
	case errors.Is(err, invoice.ErrNotOpen):
		return errConflict(publicText(invoice.ErrNotOpen))
	case errors.Is(err, invoice.ErrFundsDetected):
		return errConflict(publicText(invoice.ErrFundsDetected))
	case errors.Is(err, invoice.ErrConflict):
		// The service wraps ErrConflict with an explanation built from
		// statuses only ("only pending invoices can be cancelled ...").
		return errConflict(publicText(err))
	case errors.Is(err, invoice.ErrAssetUnavailable):
		return errUnprocessable(publicText(invoice.ErrAssetUnavailable))
	case errors.Is(err, invoice.ErrRateUnavailable):
		return errUnprocessable(publicText(invoice.ErrRateUnavailable))
	case errors.Is(err, invoice.ErrNoMatchingQuote):
		return errUnprocessable(publicText(invoice.ErrNoMatchingQuote))
	case errors.Is(err, invoice.ErrNoUniqueAmount):
		return &Error{Status: http.StatusServiceUnavailable, Code: CodeUnavailable, Message: publicText(invoice.ErrNoUniqueAmount)}
	case errors.Is(err, store.ErrNotFound):
		return errNotFound("not found")
	case errors.Is(err, store.ErrInUse):
		return errConflict("the object is still in use")
	case errors.Is(err, store.ErrConflict):
		return errConflict("conflicts with the current state")
	case errors.Is(err, store.ErrInvalid):
		return errInvalid("invalid value", nil)
	case errors.Is(err, store.ErrForeignKey):
		return errUnprocessable("references an object that does not exist")
	case errors.Is(err, context.DeadlineExceeded):
		return &Error{Status: http.StatusServiceUnavailable, Code: CodeUnavailable, Message: "the request timed out"}
	}
	return &Error{Status: http.StatusInternalServerError, Code: CodeInternal, Message: "internal server error"}
}

// publicText is the message of a service error without its package prefix.
func publicText(err error) string {
	return strings.TrimPrefix(err.Error(), "invoice: ")
}

// fail writes the error envelope for err. 5xx errors (other than
// deliberate ones) are logged with the request id.
func (a *API) fail(w http.ResponseWriter, r *http.Request, err error) {
	ctx := r.Context()
	if errors.Is(err, context.Canceled) && ctx.Err() != nil {
		// The client went away; nobody reads the answer.
		w.WriteHeader(499)
		return
	}
	e := toError(err)
	var explicit *Error
	if e.Status >= 500 && !errors.As(err, &explicit) {
		a.log.ErrorContext(ctx, "request failed", "request_id", requestIDFrom(ctx), "error", err)
	}
	if e.Status == http.StatusUnauthorized && w.Header().Get("WWW-Authenticate") == "" && strings.HasPrefix(r.URL.Path, "/api/v1") {
		w.Header().Set("WWW-Authenticate", `Bearer realm="cryptopay"`)
	}
	writeJSON(w, e.Status, envelope{Error: envelopeBody{Code: e.Code, Message: e.Message, Fields: e.Fields}})
}
