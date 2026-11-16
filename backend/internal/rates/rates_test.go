package rates

import (
	"context"
	"errors"
	"slices"
	"sync"
	"testing"

	"github.com/shopspring/decimal"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSupportedCurrencies(t *testing.T) {
	list := SupportedCurrencies()
	assert.True(t, slices.IsSorted(list), "must be sorted for binary search")
	assert.Equal(t, len(list), len(slices.Compact(slices.Clone(list))), "no duplicates")
	for _, c := range []string{"usd", "eur", "cny", "jpy", "gbp", "krw", "hkd", "twd", "sgd", "aud", "cad", "chf",
		"inr", "rub", "brl", "try", "thb", "vnd", "idr", "myr", "php", "aed", "sar", "mxn", "nzd", "sek", "nok",
		"dkk", "pln", "czk", "huf", "ils", "zar", "ngn", "uah", "pkr", "bdt", "clp", "ars", "kwd", "bhd"} {
		assert.Contains(t, list, c)
		assert.True(t, IsSupportedCurrency(c), c)
	}
	for _, c := range list {
		assert.Len(t, c, 3, c)
	}
	assert.True(t, IsSupportedCurrency("USD"))
	assert.True(t, IsSupportedCurrency(" Eur "))
	assert.False(t, IsSupportedCurrency(""))
	assert.False(t, IsSupportedCurrency("btc"))
	assert.False(t, IsSupportedCurrency("usdt"))

	// The returned slice is a copy.
	list[0] = "zzz"
	assert.NotEqual(t, "zzz", SupportedCurrencies()[0])
}

func TestStatic(t *testing.T) {
	s := NewStatic(map[string]map[string]decimal.Decimal{
		"bitcoin": {"USD": dec("60000.5"), "eur": dec("55000")},
	})
	ctx := context.Background()

	r, err := s.Rate(ctx, "bitcoin", "usd")
	require.NoError(t, err)
	assert.Equal(t, "60000.5", r.String())
	r, err = s.Rate(ctx, "Bitcoin", "EUR")
	require.NoError(t, err)
	assert.Equal(t, "55000", r.String())

	_, err = s.Rate(ctx, "ethereum", "usd")
	assert.ErrorIs(t, err, ErrUnknownAsset)
	_, err = s.Rate(ctx, "bitcoin", "xxx")
	assert.ErrorIs(t, err, ErrUnsupportedCurrency)

	s.Set("ethereum", "USD", dec("3000"))
	r, err = s.Rate(ctx, "ethereum", "usd")
	require.NoError(t, err)
	assert.Equal(t, "3000", r.String())

	s.SetError(ErrUnavailable)
	_, err = s.Rate(ctx, "bitcoin", "usd")
	assert.ErrorIs(t, err, ErrUnavailable)
	s.SetError(nil)
	_, err = s.Rate(ctx, "bitcoin", "usd")
	assert.NoError(t, err)

	cctx, cancel := context.WithCancel(ctx)
	cancel()
	_, err = s.Rate(cctx, "bitcoin", "usd")
	assert.True(t, errors.Is(err, context.Canceled))

	assert.Equal(t, 8, s.Calls())

	empty := NewStatic(nil)
	_, err = empty.Rate(ctx, "bitcoin", "usd")
	assert.ErrorIs(t, err, ErrUnknownAsset)
}

func TestStaticConcurrent(t *testing.T) {
	s := NewStatic(nil)
	var wg sync.WaitGroup
	for i := range 20 {
		wg.Go(func() {
			s.Set("coin", "usd", decimal.NewFromInt(int64(i)))
			_, _ = s.Rate(context.Background(), "coin", "usd")
			_ = s.Calls()
		})
	}
	wg.Wait()
	assert.Equal(t, 20, s.Calls())
}
