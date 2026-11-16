package api

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"net/url"
	"reflect"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"cryptopay/internal/store"
)

// writeJSON writes v as the JSON response body.
func writeJSON(w http.ResponseWriter, status int, v any) {
	body, err := json.Marshal(v)
	if err != nil {
		status = http.StatusInternalServerError
		body = []byte(`{"error":{"code":"internal","message":"could not encode the response"}}`)
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Content-Length", strconv.Itoa(len(body)+1))
	w.WriteHeader(status)
	_, _ = w.Write(append(body, '\n'))
}

// noContent writes 204.
func noContent(w http.ResponseWriter) { w.WriteHeader(http.StatusNoContent) }

// decodeJSON strictly decodes the request body into dst (a pointer to a
// struct). An empty body is treated as `{}`; a non-empty body must be sent
// as application/json, be a single JSON object and contain only known
// fields. Errors are *Error values (400 invalid_request).
func decodeJSON(r *http.Request, dst any) error {
	body, err := io.ReadAll(r.Body)
	if err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			return errInvalid(fmt.Sprintf("the request body is larger than %d bytes", tooLarge.Limit), nil)
		}
		return errInvalid("could not read the request body", nil)
	}
	body = bytes.TrimSpace(body)
	if len(body) == 0 {
		body = []byte("{}")
	} else if !isJSONContentType(r.Header.Get("Content-Type")) {
		return errInvalid("Content-Type must be application/json", nil)
	}
	if body[0] != '{' {
		return errInvalid("the request body must be a JSON object", nil)
	}
	dec := json.NewDecoder(bytes.NewReader(body))
	dec.DisallowUnknownFields()
	if err := dec.Decode(dst); err != nil {
		return jsonError(err)
	}
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		return errInvalid("the request body must contain a single JSON object", nil)
	}
	return nil
}

func isJSONContentType(ct string) bool {
	mt, _, err := mime.ParseMediaType(ct)
	return err == nil && (mt == "application/json" || (strings.HasPrefix(mt, "application/") && strings.HasSuffix(mt, "+json")))
}

func jsonError(err error) *Error {
	var syntax *json.SyntaxError
	var typ *json.UnmarshalTypeError
	switch {
	case errors.As(err, &syntax):
		return errInvalid(fmt.Sprintf("malformed JSON at byte %d", syntax.Offset), nil)
	case errors.Is(err, io.ErrUnexpectedEOF), errors.Is(err, io.EOF):
		return errInvalid("malformed JSON: unexpected end of input", nil)
	case errors.As(err, &typ):
		if typ.Field == "" {
			return errInvalid("the request body must be a JSON object", nil)
		}
		return errField(typ.Field, "must be "+jsonKind(typ.Type))
	}
	if name, ok := strings.CutPrefix(err.Error(), "json: unknown field "); ok {
		name = strings.Trim(name, `"`)
		return errInvalid(fmt.Sprintf("unknown field %q", name), map[string]string{name: "unknown field"})
	}
	var e *Error
	if errors.As(err, &e) {
		return e
	}
	return errInvalid("malformed JSON", nil)
}

func jsonKind(t reflect.Type) string {
	for t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	switch t.Kind() {
	case reflect.String:
		return "a string"
	case reflect.Bool:
		return "a boolean"
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64,
		reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		return "an integer"
	case reflect.Float32, reflect.Float64:
		return "a number"
	case reflect.Slice, reflect.Array:
		return "an array"
	case reflect.Map, reflect.Struct:
		return "an object"
	}
	return "of another type"
}

// optional is a JSON field that distinguishes "absent" (Set false) from
// "null" (Set and Null) and a value.
type optional[T any] struct {
	Set   bool
	Null  bool
	Value T
}

func (o *optional[T]) UnmarshalJSON(b []byte) error {
	o.Set = true
	if string(bytes.TrimSpace(b)) == "null" {
		o.Null = true
		return nil
	}
	return json.Unmarshal(b, &o.Value)
}

// ---- URL parameters ----

// pathUUID parses the {name} URL parameter; a malformed id is reported as
// 404 (such an object cannot exist).
func pathUUID(r *http.Request, name string) (uuid.UUID, error) {
	id, err := uuid.Parse(chi.URLParam(r, name))
	if err != nil {
		return uuid.Nil, errNotFound("not found")
	}
	return id, nil
}

// query reads query parameters, collecting validation errors.
type query struct {
	v      url.Values
	fields fields
}

func newQuery(r *http.Request) *query { return &query{v: r.URL.Query(), fields: fields{}} }

func (q *query) str(name string) string { return strings.TrimSpace(q.v.Get(name)) }

// page returns the paging parameters (page ≥ 1 default 1, page_size
// 1..store.MaxPageSize default store.DefaultPageSize).
func (q *query) page() (page, size int) {
	page = q.intIn("page", 1, 1, 1<<30)
	size = q.intIn("page_size", store.DefaultPageSize, 1, store.MaxPageSize)
	return page, size
}

func (q *query) intIn(name string, def, lo, hi int) int {
	s := q.str(name)
	if s == "" {
		return def
	}
	n, err := strconv.Atoi(s)
	if err != nil || n < lo || n > hi {
		q.fields.add(name, fmt.Sprintf("must be an integer between %d and %d", lo, hi))
		return def
	}
	return n
}

func (q *query) uuid(name string) *uuid.UUID {
	s := q.str(name)
	if s == "" {
		return nil
	}
	id, err := uuid.Parse(s)
	if err != nil {
		q.fields.add(name, "must be a UUID")
		return nil
	}
	return &id
}

func (q *query) boolean(name string) *bool {
	switch q.str(name) {
	case "":
		return nil
	case "true":
		b := true
		return &b
	case "false":
		b := false
		return &b
	}
	q.fields.add(name, `must be "true" or "false"`)
	return nil
}

func (q *query) time(name string) time.Time {
	s := q.str(name)
	if s == "" {
		return time.Time{}
	}
	t, err := time.Parse(time.RFC3339Nano, s)
	if err != nil {
		q.fields.add(name, "must be an RFC 3339 timestamp")
		return time.Time{}
	}
	return t.UTC()
}

// oneOf returns the parameter if it is one of allowed ("" when absent).
func (q *query) oneOf(name string, allowed ...string) string {
	s := q.str(name)
	if s == "" {
		return ""
	}
	for _, a := range allowed {
		if s == a {
			return s
		}
	}
	q.fields.add(name, "must be one of "+strings.Join(allowed, ", "))
	return ""
}

func (q *query) err() error { return q.fields.err() }

// ---- response shapes ----

// pageJSON is `Page<T>` of docs/api.md.
type pageJSON[T any] struct {
	Items    []T `json:"items"`
	Total    int `json:"total"`
	Page     int `json:"page"`
	PageSize int `json:"page_size"`
}

// itemsJSON is `{ items: T[] }`.
type itemsJSON[T any] struct {
	Items []T `json:"items"`
}

func mapPage[S, T any](p store.Page[S], f func(S) T) pageJSON[T] {
	out := pageJSON[T]{Items: make([]T, 0, len(p.Items)), Total: p.Total, Page: p.Page, PageSize: p.PageSize}
	for _, s := range p.Items {
		out.Items = append(out.Items, f(s))
	}
	return out
}

func mapSlice[S, T any](in []S, f func(S) T) []T {
	out := make([]T, 0, len(in))
	for _, s := range in {
		out = append(out, f(s))
	}
	return out
}
