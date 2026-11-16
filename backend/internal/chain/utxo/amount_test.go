package utxo

import (
	"errors"
	"testing"
)

func TestParseAmount(t *testing.T) {
	ok := []struct {
		in   string
		want string // satoshi
	}{
		{"0.00000001", "1"},
		{"0.00000546", "546"},
		{"0.00114662", "114662"},
		{"15.28000000", "1528000000"},
		{"20999999.9769", "2099999997690000"},
		{"20999999.97690000", "2099999997690000"},
		{"21000000", "2100000000000000"},
		{"21000000.00000000", "2100000000000000"},
		{"550553.30227541", "55055330227541"},
		{"361.80999123", "36180999123"},
		// Exponent forms produced by re-serializing proxies (JS, Python).
		{"1e-8", "1"},
		{"1E-8", "1"},
		{"1.0e-8", "1"},
		{"5.46e-06", "546"},
		{"1.117e-05", "1117"},
		{"2.09999999769e7", "2099999997690000"},
		{"2.09999999769E+7", "2099999997690000"},
		{"1e0", "100000000"},
		{"1e2", "10000000000"},
		{"0.1", "10000000"},
		{".5", "50000000"},
		{"1.", "100000000"},
		{"+1", "100000000"},
		// Zero in all its spellings.
		{"0", "0"},
		{"0.00000000", "0"},
		{"0E-8", "0"},
		{"-0.0", "0"},
		// Extra precision is fine as long as it is zero.
		{"1.000000000000", "100000000"},
		{"0.000000010", "1"},
		// Quoted (some gateways stringify numbers) and padded.
		{`"0.5"`, "50000000"},
		{` 0.25 `, "25000000"},
		// Above any real supply: still exact.
		{"92233720368.54775808", "9223372036854775808"},
	}
	for _, tc := range ok {
		got, err := parseAmount(tc.in, 8)
		if err != nil {
			t.Errorf("parseAmount(%q): %v", tc.in, err)
			continue
		}
		if got.String() != tc.want {
			t.Errorf("parseAmount(%q) = %s, want %s", tc.in, got, tc.want)
		}
	}

	bad := []string{
		"",
		`""`,
		"0.000000001", // below one satoshi
		"1e-9",        // below one satoshi
		"20999999.976900001",
		"-1",
		"-0.00000001",
		"abc",
		"1.2.3",
		"1e",
		"1e+",
		"e5",
		".",
		"0x10",
		"1_000",
		"NaN",
		"Infinity",
		"1e999999999", // exponent bomb
		"1e65",
		"12345678901234567890123456789012345678901234567890123456789012345", // too long
		"true",
		"null",
	}
	for _, in := range bad {
		got, err := parseAmount(in, 8)
		if err == nil {
			t.Errorf("parseAmount(%q) = %s, want error", in, got)
			continue
		}
		if !errors.Is(err, errBadAmount) {
			t.Errorf("parseAmount(%q) error %v does not wrap errBadAmount", in, err)
		}
	}
}

func TestParseAmountOtherDecimals(t *testing.T) {
	got, err := parseAmount("1.5", 0)
	if err == nil {
		t.Fatalf("1.5 with 0 decimals = %s, want error", got)
	}
	got, err = parseAmount("1.5e1", 0)
	if err != nil || got.String() != "15" {
		t.Fatalf("1.5e1 with 0 decimals = %v, %v; want 15", got, err)
	}
}
