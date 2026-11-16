// Package rates provides crypto asset prices in fiat currencies.
//
// The gateway quotes invoices priced in a fiat currency (USD, EUR, CNY, ...)
// in crypto: amount_due = price / rate. A Provider answers "how much of
// currency does one whole unit of the asset cost". Assets are identified by
// their rate key (assets.rate_key, a CoinGecko coin id such as "bitcoin" or
// "tether"). Crypto-denominated invoices (currency equals the asset symbol)
// are handled by the invoice service with a rate of 1 and never reach the
// provider.
//
// Rates are decimal.Decimal parsed exactly from the upstream JSON text; they
// never pass through float64.
package rates

import (
	"context"
	"errors"
	"slices"
	"strings"

	"github.com/shopspring/decimal"
)

// Provider returns exchange rates. Implementations must be safe for
// concurrent use.
type Provider interface {
	// Rate returns how many units of `currency` one whole unit of the asset identified by rateKey costs.
	//
	// currency is an ISO 4217 code in any case ("USD", "usd"). Errors wrap
	// ErrUnsupportedCurrency, ErrUnknownAsset or ErrUnavailable, or are the
	// context's error.
	Rate(ctx context.Context, rateKey, currency string) (decimal.Decimal, error)
}

var (
	// ErrUnavailable means no sufficiently fresh rate could be obtained
	// (upstream down, rate limited, malformed response, ...). The API maps it
	// to 422/503; callers may retry later.
	ErrUnavailable = errors.New("rates: price provider unavailable")
	// ErrUnsupportedCurrency means the requested quote currency is not one of
	// SupportedCurrencies().
	ErrUnsupportedCurrency = errors.New("rates: unsupported currency")
	// ErrUnknownAsset means the provider has no (positive) price for the rate
	// key, e.g. a misconfigured assets.rate_key.
	ErrUnknownAsset = errors.New("rates: no price for asset")
)

// supportedCurrencies are the fiat vs_currencies CoinGecko supports, sorted.
var supportedCurrencies = []string{
	"aed", "ars", "aud", "bdt", "bhd", "bmd", "brl", "cad", "chf", "clp",
	"cny", "czk", "dkk", "eur", "gbp", "gel", "hkd", "huf", "idr", "ils",
	"inr", "jpy", "krw", "kwd", "lkr", "mmk", "mxn", "myr", "ngn", "nok",
	"nzd", "php", "pkr", "pln", "rub", "sar", "sek", "sgd", "thb", "try",
	"twd", "uah", "usd", "vnd", "zar",
}

// SupportedCurrencies returns the lower-case ISO 4217 fiat codes a price can
// be requested in, sorted. The returned slice is a copy.
func SupportedCurrencies() []string { return slices.Clone(supportedCurrencies) }

// IsSupportedCurrency reports whether currency (any case, surrounding spaces
// ignored) is one of SupportedCurrencies().
func IsSupportedCurrency(currency string) bool {
	_, ok := slices.BinarySearch(supportedCurrencies, normCurrency(currency))
	return ok
}

func normCurrency(c string) string { return strings.ToLower(strings.TrimSpace(c)) }

// normKey lower-cases a rate key and checks it only contains characters used
// by CoinGecko coin ids, so it can never break the comma-separated query.
func normKey(k string) (string, bool) {
	k = strings.ToLower(strings.TrimSpace(k))
	if k == "" || len(k) > 128 {
		return "", false
	}
	for i := 0; i < len(k); i++ {
		c := k[i]
		if !(c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '-' || c == '_' || c == '.') {
			return "", false
		}
	}
	return k, true
}
