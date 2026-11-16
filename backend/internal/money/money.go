// Package money converts exactly between base-unit integers and decimal
// strings. No floating point is used anywhere.
package money

import (
	"errors"
	"fmt"
	"math/big"
	"strings"

	"github.com/shopspring/decimal"
)

func pow10(n int) *big.Int {
	return new(big.Int).Exp(big.NewInt(10), big.NewInt(int64(n)), nil)
}

// Format renders base units as a whole-unit decimal string without trailing
// zeros ("1.5", "0.000001", "12"). A nil value formats as "0".
func Format(base *big.Int, decimals int) string {
	if base == nil || base.Sign() == 0 {
		return "0"
	}
	if decimals <= 0 {
		return base.String()
	}
	neg := base.Sign() < 0
	digits := new(big.Int).Abs(base).String()
	if len(digits) <= decimals {
		digits = strings.Repeat("0", decimals-len(digits)+1) + digits
	}
	whole, frac := digits[:len(digits)-decimals], strings.TrimRight(digits[len(digits)-decimals:], "0")
	out := whole
	if frac != "" {
		out += "." + frac
	}
	if neg {
		out = "-" + out
	}
	return out
}

// Parse converts a plain decimal string ("12", "1.5", "0.000001") into base
// units. It is strict: no sign, exponent, whitespace, separators, empty parts
// ("1." / ".5") or more fractional digits than decimals.
func Parse(s string, decimals int) (*big.Int, error) {
	if decimals < 0 {
		return nil, errors.New("money: negative decimals")
	}
	if s == "" {
		return nil, errors.New("money: empty amount")
	}
	whole, frac, hasDot := strings.Cut(s, ".")
	if !allDigits(whole) {
		return nil, fmt.Errorf("money: invalid amount %q", s)
	}
	if hasDot {
		if !allDigits(frac) {
			return nil, fmt.Errorf("money: invalid amount %q", s)
		}
		if len(frac) > decimals {
			return nil, fmt.Errorf("money: %q has more than %d fractional digits", s, decimals)
		}
	}
	digits := whole + frac + strings.Repeat("0", decimals-len(frac))
	n, ok := new(big.Int).SetString(digits, 10)
	if !ok {
		return nil, fmt.Errorf("money: invalid amount %q", s)
	}
	return n, nil
}

func allDigits(s string) bool {
	if s == "" {
		return false
	}
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return false
		}
	}
	return true
}

// ToBase converts a decimal to base units, truncating toward zero.
func ToBase(d decimal.Decimal, decimals int) *big.Int {
	return d.Shift(int32(decimals)).Truncate(0).BigInt()
}

// FromBase converts base units to an exact decimal.
func FromBase(base *big.Int, decimals int) decimal.Decimal {
	if base == nil {
		return decimal.Zero
	}
	return decimal.NewFromBigInt(base, -int32(decimals))
}

// CeilToPrecision rounds base UP to a multiple of 10^(decimals-precision).
// precision >= decimals leaves the value unchanged. The input is not mutated.
func CeilToPrecision(base *big.Int, decimals, precision int) *big.Int {
	if base == nil {
		return new(big.Int)
	}
	if precision < 0 {
		precision = 0
	}
	if precision >= decimals {
		return new(big.Int).Set(base)
	}
	step := pow10(decimals - precision)
	q, m := new(big.Int).DivMod(base, step, new(big.Int)) // Euclidean: m >= 0
	if m.Sign() != 0 {
		q.Add(q, big.NewInt(1))
	}
	return q.Mul(q, step)
}

func ratOf(d decimal.Decimal) *big.Rat {
	r := new(big.Rat).SetInt(d.Coefficient())
	e := int(d.Exponent())
	if e >= 0 {
		return r.Mul(r, new(big.Rat).SetInt(pow10(e)))
	}
	return r.Quo(r, new(big.Rat).SetInt(pow10(-e)))
}

// QuoteAmount returns ceil(price / rate * (1 + markupBps/10000)) in base
// units, rounded up to precision, and never less than one precision step.
func QuoteAmount(price, rate decimal.Decimal, markupBps int, decimals, precision int) (*big.Int, error) {
	if rate.Sign() <= 0 {
		return nil, errors.New("money: rate must be positive")
	}
	if price.Sign() <= 0 {
		return nil, errors.New("money: price must be positive")
	}
	if markupBps < 0 {
		return nil, errors.New("money: markup must not be negative")
	}
	if decimals < 0 {
		return nil, errors.New("money: negative decimals")
	}
	r := new(big.Rat).Quo(ratOf(price), ratOf(rate))
	r.Mul(r, big.NewRat(int64(10000+markupBps), 10000))
	r.Mul(r, new(big.Rat).SetInt(pow10(decimals)))
	// ceil(num/den)
	q, m := new(big.Int).DivMod(r.Num(), r.Denom(), new(big.Int))
	if m.Sign() != 0 {
		q.Add(q, big.NewInt(1))
	}
	out := CeilToPrecision(q, decimals, precision)
	min := big.NewInt(1)
	if precision < decimals {
		if precision < 0 {
			precision = 0
		}
		min = pow10(decimals - precision)
	}
	if out.Cmp(min) < 0 {
		out = min
	}
	return out, nil
}
