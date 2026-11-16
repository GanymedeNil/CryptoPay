package store

import (
	"encoding/json"
	"fmt"
	"math"
	"math/big"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/shopspring/decimal"
)

// ---- paging ----

const (
	// DefaultPageSize is used when a list request has no page size.
	DefaultPageSize = 20
	// MaxPageSize caps the page size of list requests.
	MaxPageSize = 100
)

// Page is one page of a list result. Items is never nil.
type Page[T any] struct {
	Items    []T
	Total    int // number of rows matching the filter (all pages)
	Page     int // 1-based
	PageSize int
}

// normPage normalises 1-based paging parameters (page < 1 → 1, size < 1 →
// DefaultPageSize, size > MaxPageSize → MaxPageSize).
func normPage(page, size int) (p, ps, offset int) {
	if page < 1 {
		page = 1
	}
	if size < 1 {
		size = DefaultPageSize
	}
	if size > MaxPageSize {
		size = MaxPageSize
	}
	// Guard against overflow for absurd page numbers.
	if page > math.MaxInt32/size {
		page = math.MaxInt32 / size
	}
	return page, size, (page - 1) * size
}

// ---- WHERE builder ----

// conds accumulates AND-ed SQL conditions with positional parameters. Only
// fixed SQL fragments written in this package are added; user input always
// travels as a bound parameter.
type conds struct {
	parts []string
	args  []any
}

// arg binds v and returns its placeholder ("$n").
func (c *conds) arg(v any) string {
	c.args = append(c.args, v)
	return "$" + strconv.Itoa(len(c.args))
}

// add appends a condition fragment.
func (c *conds) add(sql string) { c.parts = append(c.parts, sql) }

// where returns " WHERE a AND b" or "".
func (c *conds) where() string {
	if len(c.parts) == 0 {
		return ""
	}
	return " WHERE " + strings.Join(c.parts, " AND ")
}

// likeEscape escapes LIKE wildcards so user input matches literally (used
// with the default escape character '\').
func likeEscape(s string) string {
	r := strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`)
	return r.Replace(s)
}

// ---- value conversion ----

// numText renders a big integer for a numeric parameter (nil → "0").
func numText(b *big.Int) string {
	if b == nil {
		return "0"
	}
	return b.String()
}

// bigScan scans a numeric column selected as text into a *big.Int.
type bigScan struct{ dst **big.Int }

func (b bigScan) Scan(src any) error {
	if src == nil {
		*b.dst = nil
		return nil
	}
	s, ok := src.(string)
	if !ok {
		return fmt.Errorf("store: numeric scan: unexpected %T", src)
	}
	v, ok := new(big.Int).SetString(s, 10)
	if !ok {
		return fmt.Errorf("store: numeric scan: %q is not an integer", s)
	}
	*b.dst = v
	return nil
}

// decScan scans a numeric column selected as text into a decimal.Decimal.
type decScan struct{ dst *decimal.Decimal }

func (d decScan) Scan(src any) error {
	if src == nil {
		*d.dst = decimal.Zero
		return nil
	}
	s, ok := src.(string)
	if !ok {
		return fmt.Errorf("store: decimal scan: unexpected %T", src)
	}
	v, err := decimal.NewFromString(s)
	if err != nil {
		return fmt.Errorf("store: decimal scan: %w", err)
	}
	*d.dst = v
	return nil
}

// decPtrScan scans a nullable numeric column selected as text.
type decPtrScan struct{ dst **decimal.Decimal }

func (d decPtrScan) Scan(src any) error {
	if src == nil {
		*d.dst = nil
		return nil
	}
	var v decimal.Decimal
	if err := (decScan{&v}).Scan(src); err != nil {
		return err
	}
	*d.dst = &v
	return nil
}

// u64Scan scans a bigint into a uint64 (negative values are rejected).
type u64Scan struct{ dst *uint64 }

func (u u64Scan) Scan(src any) error {
	switch v := src.(type) {
	case nil:
		*u.dst = 0
	case int64:
		if v < 0 {
			return fmt.Errorf("store: negative value %d for unsigned column", v)
		}
		*u.dst = uint64(v)
	default:
		return fmt.Errorf("store: uint64 scan: unexpected %T", src)
	}
	return nil
}

// u32PtrScan scans a nullable bigint into a *uint32.
type u32PtrScan struct{ dst **uint32 }

func (u u32PtrScan) Scan(src any) error {
	switch v := src.(type) {
	case nil:
		*u.dst = nil
	case int64:
		if v < 0 || v > math.MaxUint32 {
			return fmt.Errorf("store: value %d out of uint32 range", v)
		}
		x := uint32(v)
		*u.dst = &x
	default:
		return fmt.Errorf("store: uint32 scan: unexpected %T", src)
	}
	return nil
}

// strScan scans a nullable text column into a string ("" for NULL).
type strScan struct{ dst *string }

func (s strScan) Scan(src any) error {
	switch v := src.(type) {
	case nil:
		*s.dst = ""
	case string:
		*s.dst = v
	default:
		return fmt.Errorf("store: string scan: unexpected %T", src)
	}
	return nil
}

// jsonScan scans a json/jsonb column selected as text into a RawMessage.
type jsonScan struct{ dst *json.RawMessage }

func (j jsonScan) Scan(src any) error {
	switch v := src.(type) {
	case nil:
		*j.dst = nil
	case string:
		*j.dst = json.RawMessage(v)
	default:
		return fmt.Errorf("store: json scan: unexpected %T", src)
	}
	return nil
}

// jsonText renders raw JSON for a jsonb parameter; empty → def.
func jsonText(raw json.RawMessage, def string) string {
	if len(raw) == 0 {
		return def
	}
	return string(raw)
}

// u64Param converts an unsigned block number / cursor for a bigint column.
func u64Param(v uint64) (int64, error) {
	if v > math.MaxInt64 {
		return 0, fmt.Errorf("%w: value %d exceeds bigint range", ErrInvalid, v)
	}
	return int64(v), nil
}

// uuidOrNew returns id, or a fresh v4 UUID when id is zero.
func uuidOrNew(id uuid.UUID) uuid.UUID {
	if id == uuid.Nil {
		return uuid.New()
	}
	return id
}

// timeOr returns t (UTC, µs precision) or def when t is zero.
func timeOr(t, def time.Time) time.Time {
	if t.IsZero() {
		return def
	}
	return t.UTC().Truncate(time.Microsecond)
}

// utcPtr normalises an optional time to UTC / µs precision.
func utcPtr(t *time.Time) *time.Time {
	if t == nil {
		return nil
	}
	v := t.UTC().Truncate(time.Microsecond)
	return &v
}

// cleanText makes s storable in a PostgreSQL text column (valid UTF-8, no
// NUL bytes) and truncates it to at most max bytes on a rune boundary.
func cleanText(s string, max int) string {
	s = strings.ToValidUTF8(s, "�")
	s = strings.ReplaceAll(s, "\x00", "")
	if max > 0 && len(s) > max {
		s = s[:max]
		for !utf8.ValidString(s) {
			s = s[:len(s)-1]
		}
	}
	return s
}

// nonNil returns a non-nil slice.
func nonNil[T any](s []T) []T {
	if s == nil {
		return []T{}
	}
	return s
}
