package address

import (
	"encoding/hex"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"cryptopay/internal/domain"
)

var (
	btc = domain.ChainParam{Bech32HRP: "bc", P2PKHVersion: 0x00, P2SHVersion: 0x05}
	ltc = domain.ChainParam{Bech32HRP: "ltc", P2PKHVersion: 0x30, P2SHVersion: 0x32}
)

func TestParseEVM(t *testing.T) {
	// EIP-55 reference vectors.
	for _, a := range []string{
		"0x5aAeb6053F3E94C9b9A09f33669435E7Ef1BeAed",
		"0xfB6916095ca1df60bB79Ce92cE3Ea74c37c5d359",
		"0xdAC17F958D2ee523a2206206994597C13D831ec7",
	} {
		got, err := ParseEVM(a)
		require.NoError(t, err)
		assert.Equal(t, a, got)
		got, err = ParseEVM("0x" + lower(a[2:]))
		require.NoError(t, err)
		assert.Equal(t, a, got, "lower-case input is checksummed")
	}
	for _, bad := range []string{
		"", "0x123", "5aAeb6053F3E94C9b9A09f33669435E7Ef1BeAed00",
		"0x5aAeb6053F3E94C9b9A09f33669435E7Ef1BeAeD", // wrong checksum case
		"0xZZAeb6053F3E94C9b9A09f33669435E7Ef1BeAed",
	} {
		_, err := ParseEVM(bad)
		assert.ErrorIs(t, err, ErrInvalid, bad)
	}
}

func TestTron(t *testing.T) {
	const b58 = "TR7NHqjeKQxGTCi8q8ZY4pL8otSzgjLj6t" // USDT contract
	const hx = "41a614f803b6fd780986a42c78ec9c7f77e6ded13c"

	got, err := TronHex(b58)
	require.NoError(t, err)
	assert.Equal(t, hx, got)

	for _, in := range []string{b58, hx, "0x" + hx, hx[2:]} {
		got, err = ParseTron(in)
		require.NoError(t, err, in)
		assert.Equal(t, b58, got)
	}
	for _, bad := range []string{"", "TR7NHqjeKQxGTCi8q8ZY4pL8otSzgjLj6u", "T123", "zz"} {
		_, err := ParseTron(bad)
		assert.ErrorIs(t, err, ErrInvalid, bad)
	}
}

func TestParseSolana(t *testing.T) {
	for _, a := range []string{
		"EPjFWdd5AufqSSqeM2qN1xzybapC8G4wEGGkZwyTDt1v",
		"11111111111111111111111111111111",
	} {
		got, err := ParseSolana(a)
		require.NoError(t, err)
		assert.Equal(t, a, got)
	}
	for _, bad := range []string{"", "0x5aAeb6053F3E94C9b9A09f33669435E7Ef1BeAed", "EPjFWdd5", "EPjFWdd5AufqSSqeM2qN1xzybapC8G4wEGGkZwyTDt1O"} {
		_, err := ParseSolana(bad)
		assert.ErrorIs(t, err, ErrInvalid, bad)
	}
}

func TestParseUTXO(t *testing.T) {
	ok := []struct {
		addr string
		p    domain.ChainParam
		want string
	}{
		{"bc1qar0srrr7xfkvy5l643lydnw9re59gtzzwf5mdq", btc, "bc1qar0srrr7xfkvy5l643lydnw9re59gtzzwf5mdq"},
		{"BC1QAR0SRRR7XFKVY5L643LYDNW9RE59GTZZWF5MDQ", btc, "bc1qar0srrr7xfkvy5l643lydnw9re59gtzzwf5mdq"},
		{"bc1p5cyxnuxmeuwuvkwfem96lqzszd02n6xdcjrs20cac6yqjjwudpxqkedrcr", btc, "bc1p5cyxnuxmeuwuvkwfem96lqzszd02n6xdcjrs20cac6yqjjwudpxqkedrcr"},
		{"1A1zP1eP5QGefi2DMPTfTL5SLmv7DivfNa", btc, "1A1zP1eP5QGefi2DMPTfTL5SLmv7DivfNa"},
		{"3J98t1WpEZ73CNmQviecrnyiWrnqRhWNLy", btc, "3J98t1WpEZ73CNmQviecrnyiWrnqRhWNLy"},
	}
	for _, c := range ok {
		got, err := ParseUTXO(c.addr, c.p)
		require.NoError(t, err, c.addr)
		assert.Equal(t, c.want, got)
	}
	bad := []struct {
		addr string
		p    domain.ChainParam
	}{
		{"bc1qar0srrr7xfkvy5l643lydnw9re59gtzzwf5mdq", ltc}, // wrong chain
		{"1A1zP1eP5QGefi2DMPTfTL5SLmv7DivfNa", ltc},
		{"bc1qar0srrr7xfkvy5l643lydnw9re59gtzzwf5mdx", btc}, // bad checksum
		{"1A1zP1eP5QGefi2DMPTfTL5SLmv7DivfNb", btc},
		{"", btc},
	}
	for _, c := range bad {
		_, err := ParseUTXO(c.addr, c.p)
		assert.ErrorIs(t, err, ErrInvalid, c.addr)
	}
}

func TestSegwitEncodeRoundTrip(t *testing.T) {
	prog, _ := hex.DecodeString("751e76e8199196d454941c45d1b3a323f1433bd6")
	got, err := SegwitEncode("bc", 0, prog)
	require.NoError(t, err)
	assert.Equal(t, "bc1qw508d6qejxtdg4y5r3zarvary0c5xw7kv8f3t4", got) // BIP-173 vector

	got, err = SegwitEncode("ltc", 0, prog)
	require.NoError(t, err)
	_, err = ParseUTXO(got, ltc)
	require.NoError(t, err)
}

func TestNormalize(t *testing.T) {
	got, err := Normalize(domain.Chain{Family: domain.FamilyEVM}, " 0xdac17f958d2ee523a2206206994597c13d831ec7 ")
	require.NoError(t, err)
	assert.Equal(t, "0xdAC17F958D2ee523a2206206994597C13D831ec7", got)
	_, err = Normalize(domain.Chain{Family: "x"}, "abc")
	assert.ErrorIs(t, err, ErrInvalid)
}

func lower(s string) string {
	b := []byte(s)
	for i, c := range b {
		if c >= 'A' && c <= 'Z' {
			b[i] = c + 32
		}
	}
	return string(b)
}
