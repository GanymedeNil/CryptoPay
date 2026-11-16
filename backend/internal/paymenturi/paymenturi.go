// Package paymenturi builds the wallet URIs encoded in checkout QR codes and
// "open in wallet" links (EIP-681, BIP-21, Solana Pay).
package paymenturi

import (
	"math/big"
	"net/url"

	"cryptopay/internal/domain"
	"cryptopay/internal/money"
)

// Build returns the payment URI for paying amountBase (base units of asset a)
// to toAddress on chain c. It returns "" when the family has no standard URI
// (Tron); the UI then shows the plain address.
func Build(c domain.Chain, a domain.Asset, toAddress string, amountBase *big.Int) string {
	if amountBase == nil {
		amountBase = new(big.Int)
	}
	switch c.Family {
	case domain.FamilyEVM:
		// EIP-681. Native: ethereum:<to>@<chainId>?value=<wei>
		// ERC-20: ethereum:<token>@<chainId>/transfer?address=<to>&uint256=<units>
		target := toAddress
		if !a.IsNative() {
			target = a.ContractAddress
		}
		u := "ethereum:" + target
		if c.NetworkID != "" {
			u += "@" + c.NetworkID
		}
		if a.IsNative() {
			return u + "?value=" + amountBase.String()
		}
		return u + "/transfer?address=" + url.QueryEscape(toAddress) + "&uint256=" + amountBase.String()
	case domain.FamilyUTXO:
		// BIP-21: <scheme>:<address>?amount=<whole units>
		return c.Params.URIScheme + ":" + toAddress + "?amount=" + money.Format(amountBase, a.Decimals)
	case domain.FamilySolana:
		// Solana Pay transfer request.
		u := "solana:" + toAddress + "?amount=" + money.Format(amountBase, a.Decimals)
		if !a.IsNative() {
			u += "&spl-token=" + a.ContractAddress
		}
		return u
	}
	return ""
}
