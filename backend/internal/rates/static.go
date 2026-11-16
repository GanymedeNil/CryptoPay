package rates

import (
	"context"
	"fmt"
	"strings"
	"sync"

	"github.com/shopspring/decimal"
)

// Static is an in-memory Provider for tests of other packages. Rates are
// looked up by rate key (case-insensitive) and currency (case-insensitive).
// It is safe for concurrent use.
type Static struct {
	mu    sync.RWMutex
	rates map[string]map[string]decimal.Decimal
	err   error
	calls int
}

// NewStatic returns a Static provider seeded with rates
// (rateKey → currency → rate). The map is copied; nil is allowed.
func NewStatic(rates map[string]map[string]decimal.Decimal) *Static {
	s := &Static{rates: map[string]map[string]decimal.Decimal{}}
	for k, m := range rates {
		for cur, r := range m {
			s.setLocked(k, cur, r)
		}
	}
	return s
}

func (s *Static) setLocked(rateKey, currency string, rate decimal.Decimal) {
	k := strings.ToLower(strings.TrimSpace(rateKey))
	if s.rates[k] == nil {
		s.rates[k] = map[string]decimal.Decimal{}
	}
	s.rates[k][normCurrency(currency)] = rate
}

// Set adds or replaces one rate.
func (s *Static) Set(rateKey, currency string, rate decimal.Decimal) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.setLocked(rateKey, currency, rate)
}

// SetError makes every subsequent Rate call return err (nil restores normal
// behaviour). Use it to simulate an outage, e.g. SetError(rates.ErrUnavailable).
func (s *Static) SetError(err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.err = err
}

// Calls returns how many times Rate has been called.
func (s *Static) Calls() int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.calls
}

// Rate implements Provider. Resolution order: the context's error, the error
// set with SetError, a configured rate (any currency code is accepted), then
// ErrUnsupportedCurrency for currencies outside SupportedCurrencies(), else
// ErrUnknownAsset.
func (s *Static) Rate(ctx context.Context, rateKey, currency string) (decimal.Decimal, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls++
	if err := ctx.Err(); err != nil {
		return decimal.Decimal{}, err
	}
	if s.err != nil {
		return decimal.Decimal{}, s.err
	}
	cur := normCurrency(currency)
	if r, ok := s.rates[strings.ToLower(strings.TrimSpace(rateKey))][cur]; ok {
		return r, nil
	}
	if !IsSupportedCurrency(cur) {
		return decimal.Decimal{}, fmt.Errorf("%w: %q", ErrUnsupportedCurrency, currency)
	}
	return decimal.Decimal{}, fmt.Errorf("%w: %s/%s", ErrUnknownAsset, rateKey, cur)
}
