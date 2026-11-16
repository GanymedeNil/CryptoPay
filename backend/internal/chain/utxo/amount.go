package utxo

import (
	"errors"
	"fmt"
	"math/big"
	"strconv"
	"strings"
)

// coinDecimals is the number of decimal places of BTC and LTC
// (1 coin = 10^8 satoshi / litoshi).
const coinDecimals = 8

// Limits that keep a hostile or broken node from making us allocate huge
// integers ("1e999999999") while parsing an amount.
const (
	maxAmountLen      = 64
	maxAmountExponent = 64
)

var errBadAmount = errors.New("invalid amount")

// parseAmount converts a decimal coin amount as printed by the node
// ("0.00114662", "21000000", "1e-8", "2.09999999769E7", optionally as a JSON
// string) into exact base units (10^-decimals of a coin).
//
// The conversion is done on the decimal digits with big-integer arithmetic,
// never through float64. Negative amounts and amounts with more precision
// than one base unit are rejected rather than rounded.
func parseAmount(s string, decimals int) (*big.Int, error) {
	s = strings.TrimSpace(s)
	if len(s) >= 2 && s[0] == '"' && s[len(s)-1] == '"' {
		s = strings.TrimSpace(s[1 : len(s)-1])
	}
	if s == "" {
		return nil, fmt.Errorf("%w: empty", errBadAmount)
	}
	if len(s) > maxAmountLen {
		return nil, fmt.Errorf("%w: %d characters", errBadAmount, len(s))
	}
	orig := s

	neg := false
	switch s[0] {
	case '-':
		neg = true
		s = s[1:]
	case '+':
		s = s[1:]
	}

	// Split off the exponent.
	exp := 0
	if i := strings.IndexAny(s, "eE"); i >= 0 {
		e, err := strconv.Atoi(s[i+1:])
		if err != nil || s[i+1:] == "" {
			return nil, fmt.Errorf("%w: %q has a bad exponent", errBadAmount, orig)
		}
		if e > maxAmountExponent || e < -maxAmountExponent {
			return nil, fmt.Errorf("%w: %q exponent out of range", errBadAmount, orig)
		}
		exp = e
		s = s[:i]
	}

	intPart, frac, _ := strings.Cut(s, ".")
	if intPart == "" && frac == "" {
		return nil, fmt.Errorf("%w: %q has no digits", errBadAmount, orig)
	}
	if !allDigits(intPart) || !allDigits(frac) {
		return nil, fmt.Errorf("%w: %q is not a decimal number", errBadAmount, orig)
	}

	digits := strings.TrimLeft(intPart+frac, "0")
	if digits == "" {
		return new(big.Int), nil // zero, whatever its sign or exponent
	}
	if neg {
		return nil, fmt.Errorf("%w: %q is negative", errBadAmount, orig)
	}

	// value = digits × 10^(exp − len(frac)); base units = value × 10^decimals.
	scale := decimals + exp - len(frac)
	v, ok := new(big.Int).SetString(digits, 10)
	if !ok {
		return nil, fmt.Errorf("%w: %q", errBadAmount, orig)
	}
	switch {
	case scale > 0:
		v.Mul(v, pow10(scale))
	case scale < 0:
		q, r := new(big.Int).QuoRem(v, pow10(-scale), new(big.Int))
		if r.Sign() != 0 {
			return nil, fmt.Errorf("%w: %q has more than %d decimal places", errBadAmount, orig, decimals)
		}
		v = q
	}
	return v, nil
}

func allDigits(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return false
		}
	}
	return true
}

func pow10(n int) *big.Int {
	return new(big.Int).Exp(big.NewInt(10), big.NewInt(int64(n)), nil)
}
