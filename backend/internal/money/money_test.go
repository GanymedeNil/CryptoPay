package money

import (
	"math/big"
	"testing"

	"github.com/shopspring/decimal"
)

func bi(s string) *big.Int {
	n, ok := new(big.Int).SetString(s, 10)
	if !ok {
		panic(s)
	}
	return n
}

func TestFormat(t *testing.T) {
	for _, c := range []struct {
		in   string
		dec  int
		want string
	}{
		{"0", 18, "0"},
		{"1", 6, "0.000001"},
		{"1500000", 6, "1.5"},
		{"12000000", 6, "12"},
		{"1000000000000000000", 18, "1"},
		{"1", 18, "0.000000000000000001"},
		{"123456789012345678901234567890", 18, "123456789012.34567890123456789"},
		{"100", 0, "100"},
		{"-1500000", 6, "-1.5"},
		{"100000000", 8, "1"},
		{"10", 1, "1"},
	} {
		if got := Format(bi(c.in), c.dec); got != c.want {
			t.Errorf("Format(%s,%d)=%q want %q", c.in, c.dec, got, c.want)
		}
	}
	if Format(nil, 6) != "0" {
		t.Error("nil")
	}
}

func TestParse(t *testing.T) {
	ok := []struct {
		in   string
		dec  int
		want string
	}{
		{"0", 6, "0"},
		{"1", 6, "1000000"},
		{"1.5", 6, "1500000"},
		{"0.000001", 6, "1"},
		{"0.1", 18, "100000000000000000"},
		{"001.50", 6, "1500000"},
		{"5", 0, "5"},
		{"340282366920938463463374607431768211456.000000000000000001", 18, "340282366920938463463374607431768211456000000000000000001"},
	}
	for _, c := range ok {
		got, err := Parse(c.in, c.dec)
		if err != nil || got.String() != c.want {
			t.Errorf("Parse(%q,%d)=%v,%v want %s", c.in, c.dec, got, err, c.want)
		}
	}
	for _, s := range []string{"", "-1", "+1", "1e3", "1E3", "0.0000001", "abc", "1.", ".5", ".", "1.2.3", " 1", "1 ", "1,5", "0x10", "1_0", "١٢", "-0"} {
		if _, err := Parse(s, 6); err == nil {
			t.Errorf("Parse(%q) should fail", s)
		}
	}
	if _, err := Parse("1.0", 0); err == nil {
		t.Error("fraction with 0 decimals should fail")
	}
}

func TestRoundTrip(t *testing.T) {
	for _, s := range []string{"0", "1", "999999999999999999999999", "123456789123456789123456789"} {
		for _, d := range []int{0, 6, 8, 18} {
			n := bi(s)
			back, err := Parse(Format(n, d), d)
			if err != nil || back.Cmp(n) != 0 {
				t.Errorf("roundtrip %s/%d: %v %v", s, d, back, err)
			}
		}
	}
}

func TestToFromBase(t *testing.T) {
	d := decimal.RequireFromString("1.2345679")
	if got := ToBase(d, 6); got.String() != "1234567" {
		t.Errorf("truncate: %s", got)
	}
	if got := ToBase(decimal.RequireFromString("0.1"), 18); got.String() != "100000000000000000" {
		t.Errorf("0.1: %s", got)
	}
	if got := ToBase(decimal.RequireFromString("1e30"), 18); got.String() != "1"+zeros(48) {
		t.Errorf("huge: %s", got)
	}
	if got := ToBase(decimal.RequireFromString("0.0000009"), 6); got.Sign() != 0 {
		t.Errorf("sub-unit: %s", got)
	}
	f := FromBase(bi("1500000000000000000"), 18)
	if f.String() != "1.5" {
		t.Errorf("FromBase: %s", f)
	}
	if FromBase(bi("1"), 18).String() != "0.000000000000000001" {
		t.Error("FromBase 1 wei")
	}
	if !FromBase(nil, 6).IsZero() {
		t.Error("nil")
	}
	if FromBase(bi("123456789012345678901234567890"), 18).String() != "123456789012.34567890123456789" {
		t.Error("huge FromBase")
	}
}

func zeros(n int) string {
	b := make([]byte, n)
	for i := range b {
		b[i] = '0'
	}
	return string(b)
}

func TestCeilToPrecision(t *testing.T) {
	for _, c := range []struct {
		in        string
		dec, prec int
		want      string
	}{
		{"1234567", 6, 2, "1240000"},
		{"1230000", 6, 2, "1230000"},
		{"1", 6, 2, "10000"},
		{"0", 6, 2, "0"},
		{"1234567", 6, 6, "1234567"},
		{"1234567", 6, 8, "1234567"},
		{"1234567", 6, 0, "2000000"},
		{"1000000000000000001", 18, 6, "1000000000000000000" + "" + ""},
		{"123456789012345678901234567891", 18, 6, "123456789012345679000000000000"},
	} {
		in := bi(c.in)
		got := CeilToPrecision(in, c.dec, c.prec)
		want := c.want
		if c.in == "1000000000000000001" {
			want = "1000001000000000000"
		}
		if got.String() != want {
			t.Errorf("Ceil(%s,%d,%d)=%s want %s", c.in, c.dec, c.prec, got, want)
		}
		if in.String() != c.in {
			t.Error("input mutated")
		}
	}
}

func TestQuoteAmount(t *testing.T) {
	d := decimal.RequireFromString
	for _, c := range []struct {
		name        string
		price, rate string
		bps         int
		dec, prec   int
		want        string
	}{
		{"usdt 1:1", "10", "1", 0, 6, 2, "10000000"},
		{"usdt markup 1%", "10", "1", 100, 6, 2, "10100000"},
		{"eth", "100", "2000", 0, 18, 6, "50000000000000000"},
		{"eth ceil", "100", "3000", 0, 18, 6, "33334000000000000"},
		{"btc", "50", "100000", 0, 8, 8, "50000"},
		{"btc ceil subunit", "0.01", "100000", 0, 8, 8, "10"},
		{"one third", "1", "3", 0, 6, 6, "333334"},
		{"min step", "0.0000001", "1", 0, 6, 2, "10000"},
		{"markup rounds up", "1", "1", 1, 6, 6, "1000100"},
		{"huge price", "1000000000000000000000", "1", 0, 18, 6, "1" + zeros(21+18)},
		{"tiny rate", "1", "0.000000000000000001", 0, 18, 18, "1" + zeros(36)},
		{"fractional rate", "1.5", "0.5", 0, 6, 6, "3000000"},
	} {
		got, err := QuoteAmount(d(c.price), d(c.rate), c.bps, c.dec, c.prec)
		if err != nil || got.String() != c.want {
			t.Errorf("%s: got %v err %v want %s", c.name, got, err, c.want)
		}
	}
	for _, c := range []struct{ price, rate string }{{"0", "1"}, {"-1", "1"}, {"1", "0"}, {"1", "-2"}} {
		if _, err := QuoteAmount(d(c.price), d(c.rate), 0, 6, 2); err == nil {
			t.Errorf("expected error for %v", c)
		}
	}
	if _, err := QuoteAmount(d("1"), d("1"), -1, 6, 2); err == nil {
		t.Error("negative markup")
	}
}
