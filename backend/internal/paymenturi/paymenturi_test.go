package paymenturi

import (
	"math/big"
	"testing"

	"cryptopay/internal/domain"
)

func TestBuild(t *testing.T) {
	eth := domain.Chain{ID: "ethereum", Family: domain.FamilyEVM, NetworkID: "1"}
	bsc := domain.Chain{ID: "bsc", Family: domain.FamilyEVM, NetworkID: "56"}
	btc := domain.Chain{ID: "bitcoin", Family: domain.FamilyUTXO, Params: domain.ChainParam{URIScheme: "bitcoin"}}
	ltc := domain.Chain{ID: "litecoin", Family: domain.FamilyUTXO, Params: domain.ChainParam{URIScheme: "litecoin"}}
	sol := domain.Chain{ID: "solana", Family: domain.FamilySolana}
	trx := domain.Chain{ID: "tron", Family: domain.FamilyTron}

	const to = "0xf39Fd6e51aad88F6F4ce6aB8827279cffFb92266"
	const usdc = "0xA0b86991c6218b36c1d19D4a2e9Eb0cE3606eB48"
	const mint = "EPjFWdd5AufqSSqeM2qN1xzybapC8G4wEGGkZwyTDt1v"
	const solAddr = "7EcDhSYGxXyscszYEp35KHN8vvw3svAuLKTzXwCFLtV"

	n := func(s string) *big.Int { v, _ := new(big.Int).SetString(s, 10); return v }
	for _, c := range []struct {
		name  string
		chain domain.Chain
		asset domain.Asset
		to    string
		amt   *big.Int
		want  string
	}{
		{"eth native", eth, domain.Asset{Decimals: 18}, to, n("1500000000000000000"),
			"ethereum:" + to + "@1?value=1500000000000000000"},
		{"bsc native", bsc, domain.Asset{Decimals: 18}, to, n("1"),
			"ethereum:" + to + "@56?value=1"},
		{"erc20", eth, domain.Asset{ContractAddress: usdc, Decimals: 6}, to, n("10500000"),
			"ethereum:" + usdc + "@1/transfer?address=" + to + "&uint256=10500000"},
		{"bitcoin", btc, domain.Asset{Decimals: 8}, "bc1qcr8te4kr609gcawutmrza0j4xv80jy8z306fyu", n("12345"),
			"bitcoin:bc1qcr8te4kr609gcawutmrza0j4xv80jy8z306fyu?amount=0.00012345"},
		{"bitcoin whole", btc, domain.Asset{Decimals: 8}, "bc1qx", n("100000000"),
			"bitcoin:bc1qx?amount=1"},
		{"litecoin", ltc, domain.Asset{Decimals: 8}, "ltc1qx", n("150000000"),
			"litecoin:ltc1qx?amount=1.5"},
		{"solana native", sol, domain.Asset{Decimals: 9}, solAddr, n("1500000000"),
			"solana:" + solAddr + "?amount=1.5"},
		{"solana spl", sol, domain.Asset{ContractAddress: mint, Decimals: 6}, solAddr, n("2000000"),
			"solana:" + solAddr + "?amount=2&spl-token=" + mint},
		{"tron native", trx, domain.Asset{Decimals: 6}, "TXYZ", n("1"), ""},
		{"tron trc20", trx, domain.Asset{ContractAddress: "TR7NHqjeKQxGTCi8q8ZY4pL8otSzgjLj6t", Decimals: 6}, "TXYZ", n("1"), ""},
	} {
		if got := Build(c.chain, c.asset, c.to, c.amt); got != c.want {
			t.Errorf("%s:\n got  %s\n want %s", c.name, got, c.want)
		}
	}
	if got := Build(btc, domain.Asset{Decimals: 8}, "bc1qx", nil); got != "bitcoin:bc1qx?amount=0" {
		t.Errorf("nil amount: %s", got)
	}
}
