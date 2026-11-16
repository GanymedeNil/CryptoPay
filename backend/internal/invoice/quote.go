package invoice

import (
	"context"
	"errors"
	"fmt"
	"math/big"
	"strings"

	"github.com/google/uuid"
	"github.com/shopspring/decimal"

	"cryptopay/internal/domain"
	"cryptopay/internal/hdwallet"
	"cryptopay/internal/money"
	"cryptopay/internal/rates"
	"cryptopay/internal/store"
)

// maxStaticAmountTries bounds how often a static-wallet amount is bumped
// to find a unique one before giving up with ErrNoUniqueAmount.
const maxStaticAmountTries = 200

// staticExtraDigits is how many digits below the asset's quote precision
// are used to make static-wallet amounts unique.
const staticExtraDigits = 2

// rateScale is the number of decimal places a rate is stored with
// (quotes.rate is numeric(38,18)).
const rateScale = 18

var one = decimal.NewFromInt(1)

// usdPrice returns the USD price of one unit of asset a: exactly 1 for a
// stablecoin while the peg setting is on, else the provider's rate.
func (s *Service) usdPrice(ctx context.Context, a domain.Asset, st domain.Settings) (decimal.Decimal, error) {
	if a.IsStablecoin && st.StablecoinPeg {
		return one, nil
	}
	return s.providerRate(ctx, a.RateKey, "USD")
}

// providerRate asks the rate provider and maps its failures to
// ErrRateUnavailable (context errors pass through unchanged).
func (s *Service) providerRate(ctx context.Context, rateKey, currency string) (decimal.Decimal, error) {
	if rateKey == "" {
		return decimal.Decimal{}, fmt.Errorf("%w: asset has no rate key", ErrRateUnavailable)
	}
	r, err := s.rates.Rate(ctx, rateKey, currency)
	if err != nil {
		if cerr := ctx.Err(); cerr != nil {
			return decimal.Decimal{}, cerr
		}
		return decimal.Decimal{}, fmt.Errorf("%w: %s in %s: %v", ErrRateUnavailable, rateKey, currency, err)
	}
	if r.Sign() <= 0 {
		return decimal.Decimal{}, fmt.Errorf("%w: %s in %s: non-positive rate", ErrRateUnavailable, rateKey, currency)
	}
	return r, nil
}

// rateFor returns the price of one whole unit of asset a in the invoice's
// price currency (docs/architecture.md):
//
//   - currency equals the asset's symbol → 1;
//   - fiat currency: 1 for a pegged stablecoin priced in USD, else the
//     provider's rate (a pegged stablecoin priced in another fiat currency
//     uses the provider's rate for that currency);
//   - another crypto symbol (invoice priced in BTC, paid in ETH): the ratio
//     of the two USD prices.
//
// assets is the list of enabled assets used to resolve a crypto price
// currency; nil loads it on demand.
func (s *Service) rateFor(ctx context.Context, currency string, a domain.Asset, st domain.Settings, assets []domain.Asset) (decimal.Decimal, error) {
	if strings.EqualFold(currency, a.Symbol) {
		return one, nil
	}
	var rate decimal.Decimal
	if rates.IsSupportedCurrency(currency) {
		if a.IsStablecoin && st.StablecoinPeg && strings.EqualFold(currency, "USD") {
			return one, nil
		}
		r, err := s.providerRate(ctx, a.RateKey, currency)
		if err != nil {
			return decimal.Decimal{}, err
		}
		rate = r
	} else {
		if assets == nil {
			var err error
			if assets, err = s.store.ListAssets(ctx, store.AssetFilter{EnabledOnly: true}); err != nil {
				return decimal.Decimal{}, err
			}
		}
		cur, ok := assetBySymbol(assets, currency)
		if !ok {
			return decimal.Decimal{}, fmt.Errorf("%w: unknown price currency %q", ErrRateUnavailable, currency)
		}
		pa, err := s.usdPrice(ctx, a, st)
		if err != nil {
			return decimal.Decimal{}, err
		}
		pc, err := s.usdPrice(ctx, cur, st)
		if err != nil {
			return decimal.Decimal{}, err
		}
		rate = pa.DivRound(pc, rateScale)
	}
	rate = rate.Round(rateScale)
	if rate.Sign() <= 0 {
		return decimal.Decimal{}, fmt.Errorf("%w: rate of %s in %s rounds to zero", ErrRateUnavailable, a.ID, currency)
	}
	return rate, nil
}

// assetBySymbol finds the first asset (in sort order) with the given symbol,
// case-insensitively.
func assetBySymbol(assets []domain.Asset, symbol string) (domain.Asset, bool) {
	for _, a := range assets {
		if strings.EqualFold(a.Symbol, symbol) {
			return a, true
		}
	}
	return domain.Asset{}, false
}

// quotePlan is everything needed to create a quote, gathered before the
// transaction so no row lock is held during a rate lookup.
type quotePlan struct {
	asset    domain.Asset
	chain    domain.Chain
	wallet   domain.Wallet
	settings domain.Settings
	rate     *decimal.Decimal // nil: look it up only if a new quote is needed
}

// plan loads the asset (which must be payable), its chain and wallet and the
// settings. With currency != "" the rate is looked up as well.
func (s *Service) plan(ctx context.Context, assetID, currency string) (quotePlan, error) {
	var p quotePlan
	a, err := s.store.GetPayableAsset(ctx, assetID)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return p, fmt.Errorf("%w: %q", ErrAssetUnavailable, assetID)
		}
		return p, err
	}
	c, err := s.store.GetChain(ctx, a.ChainID)
	if err != nil {
		return p, err
	}
	if !c.Enabled || c.WalletID == nil {
		return p, fmt.Errorf("%w: %q", ErrAssetUnavailable, assetID)
	}
	w, err := s.store.GetWallet(ctx, *c.WalletID)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return p, fmt.Errorf("%w: %q", ErrAssetUnavailable, assetID)
		}
		return p, err
	}
	st, err := s.store.GetSettings(ctx)
	if err != nil {
		return p, err
	}
	p = quotePlan{asset: a, chain: c, wallet: w, settings: st}
	if currency != "" {
		r, err := s.rateFor(ctx, currency, a, st, nil)
		if err != nil {
			return p, err
		}
		p.rate = &r
	}
	return p, nil
}

// SelectAsset makes assetID the asset the invoice is paid with and returns
// the active quote for it.
//
//   - The invoice must be open (pending / partial / confirming) and not past
//     expires_at, else ErrNotOpen. Unknown invoice → ErrNotFound.
//   - The asset must be payable right now, else ErrAssetUnavailable.
//   - Selecting the asset of the currently active quote returns that quote
//     unchanged (so a double submit is harmless).
//   - Once funds were detected on any quote of the invoice, switching is
//     refused with ErrFundsDetected.
//   - A live (superseded) quote of the invoice for that asset is re-activated
//     with its original amount and address.
//   - Otherwise the other live quotes become superseded (still watched until
//     their watch_until) and a new quote is created at the current rate
//     (ErrRateUnavailable when there is none).
//
// Everything happens in one transaction holding the invoice row lock, so
// concurrent calls serialise and an invoice never has two live quotes for
// one asset.
func (s *Service) SelectAsset(ctx context.Context, invoiceID uuid.UUID, assetID string) (domain.Quote, error) {
	inv, err := s.store.GetInvoice(ctx, invoiceID)
	if err != nil {
		return domain.Quote{}, mapNotFound(err)
	}
	if !s.payableNow(inv) {
		return domain.Quote{}, ErrNotOpen
	}
	// Skip the rate lookup when the invoice already has a live quote for the
	// asset (the common "same asset again" case); selectTx fetches it itself
	// should that quote be gone by the time the lock is held.
	currency := inv.PriceCurrency
	if _, err := s.store.GetLiveQuote(ctx, invoiceID, assetID); err == nil {
		currency = ""
	} else if !errors.Is(err, store.ErrNotFound) {
		return domain.Quote{}, err
	}
	p, err := s.plan(ctx, assetID, currency)
	if err != nil {
		return domain.Quote{}, err
	}
	var q domain.Quote
	err = s.store.WithTx(ctx, func(tx *store.Store) error {
		var err error
		q, err = s.selectTx(ctx, tx, invoiceID, p)
		return err
	})
	if err != nil {
		return domain.Quote{}, err
	}
	return q, nil
}

// payableNow reports whether the customer may still pick an asset.
func (s *Service) payableNow(inv domain.Invoice) bool {
	return inv.Status.Open() && s.clock().Before(inv.ExpiresAt)
}

// selectTx is SelectAsset inside a transaction.
func (s *Service) selectTx(ctx context.Context, tx *store.Store, invoiceID uuid.UUID, p quotePlan) (domain.Quote, error) {
	inv, err := tx.GetInvoiceForUpdate(ctx, invoiceID)
	if err != nil {
		return domain.Quote{}, mapNotFound(err)
	}
	if !s.payableNow(inv) {
		return domain.Quote{}, ErrNotOpen
	}
	quotes, err := tx.ListQuotesByInvoice(ctx, invoiceID)
	if err != nil {
		return domain.Quote{}, err
	}
	funds := inv.Status != domain.InvoicePending
	var live *domain.Quote
	for i := range quotes {
		q := &quotes[i]
		if q.AmountReceived != nil && q.AmountReceived.Sign() > 0 {
			funds = true
		}
		if q.AssetID != p.asset.ID {
			continue
		}
		switch q.Status {
		case domain.QuoteActive:
			return *q, nil // same asset again: unchanged
		case domain.QuoteSuperseded:
			live = q
		}
	}
	if funds {
		return domain.Quote{}, ErrFundsDetected
	}
	if _, err := tx.SupersedeActiveQuotes(ctx, invoiceID); err != nil {
		return domain.Quote{}, err
	}
	if live != nil {
		q, err := tx.SetQuoteStatus(ctx, live.ID, domain.QuoteActive)
		if err != nil {
			return domain.Quote{}, err
		}
		return q, nil
	}

	if p.rate == nil {
		r, err := s.rateFor(ctx, inv.PriceCurrency, p.asset, p.settings, nil)
		if err != nil {
			return domain.Quote{}, err
		}
		p.rate = &r
	}
	due, err := money.QuoteAmount(inv.PriceAmount, *p.rate, max(p.settings.RateMarkupBps, 0), p.asset.Decimals, p.asset.QuotePrecision)
	if err != nil {
		return domain.Quote{}, fmt.Errorf("%w: %v", ErrRateUnavailable, err)
	}
	q := domain.Quote{
		InvoiceID:  inv.ID,
		AssetID:    p.asset.ID,
		ChainID:    p.chain.ID,
		WalletID:   p.wallet.ID,
		WalletKind: p.wallet.Kind,
		Rate:       *p.rate,
		AmountDue:  due,
		Status:     domain.QuoteActive,
		WatchUntil: inv.ExpiresAt.Add(gracePeriod(p.settings)),
	}
	switch p.wallet.Kind {
	case domain.WalletXpub:
		return s.createXpubQuote(ctx, tx, q, p)
	case domain.WalletStatic:
		return s.createStaticQuote(ctx, tx, q, p)
	}
	return domain.Quote{}, fmt.Errorf("%w: wallet %s has unknown kind %q", ErrAssetUnavailable, p.wallet.ID, p.wallet.Kind)
}

// createXpubQuote gives the quote the invoice's address on the wallet: the
// derivation index an earlier quote of the invoice got from the same wallet
// (one invoice → one address across all chains of a wallet), else a freshly
// allocated one.
func (s *Service) createXpubQuote(ctx context.Context, tx *store.Store, q domain.Quote, p quotePlan) (domain.Quote, error) {
	var idx uint32
	_, prev, err := tx.FindInvoiceAddress(ctx, q.InvoiceID, p.wallet.ID)
	switch {
	case err == nil && prev != nil:
		idx = *prev
	case err == nil || errors.Is(err, store.ErrNotFound):
		if idx, err = tx.AllocateAddressIndex(ctx, p.wallet.ID); err != nil {
			return domain.Quote{}, fmt.Errorf("allocate address on wallet %s: %w", p.wallet.ID, err)
		}
	default:
		return domain.Quote{}, err
	}
	// Derive per chain (not copy the earlier address): chains of one family
	// may encode the same key differently (Bitcoin vs Litecoin).
	addr, err := hdwallet.Derive(p.chain, p.wallet.Xpub, idx)
	if err != nil {
		s.log.ErrorContext(ctx, "address derivation failed", "wallet_id", p.wallet.ID, "chain_id", p.chain.ID, "index", idx, "error", err)
		return domain.Quote{}, fmt.Errorf("%w: cannot derive an address on %s: %v", ErrAssetUnavailable, p.chain.ID, err)
	}
	q.Address, q.AddressIndex = addr, &idx
	out, err := tx.CreateQuote(ctx, q)
	if err != nil {
		return domain.Quote{}, err
	}
	return out, nil
}

// createStaticQuote uses the wallet's fixed address and makes the amount
// unique among the live quotes on it: starting at the quoted amount it adds
// one step of 10^-(min(decimals, quote_precision+2)) at a time — upward
// only, so the merchant never receives less than quoted — until the insert
// no longer collides.
func (s *Service) createStaticQuote(ctx context.Context, tx *store.Store, q domain.Quote, p quotePlan) (domain.Quote, error) {
	if p.wallet.Address == "" {
		return domain.Quote{}, fmt.Errorf("%w: static wallet %s has no address", ErrAssetUnavailable, p.wallet.ID)
	}
	q.Address = p.wallet.Address
	step := StaticAmountStep(p.asset)
	amount := new(big.Int).Set(q.AmountDue)
	for range maxStaticAmountTries {
		q.AmountDue = amount
		out, err := tx.CreateQuote(ctx, q)
		if err == nil {
			return out, nil
		}
		if !store.IsConstraint(err, store.ConstraintQuoteStaticAmount) {
			return domain.Quote{}, err
		}
		amount = new(big.Int).Add(amount, step)
	}
	return domain.Quote{}, fmt.Errorf("%w (asset %s, %d amounts tried)", ErrNoUniqueAmount, p.asset.ID, maxStaticAmountTries)
}

// StaticAmountStep is the increment (base units) used to make amounts on a
// static wallet unique: 10^(decimals − min(decimals, quote_precision+2)),
// i.e. two digits below the quote precision when the asset has them,
// otherwise its last digit.
func StaticAmountStep(a domain.Asset) *big.Int {
	digits := min(max(a.Decimals, 0), max(a.QuotePrecision, 0)+staticExtraDigits)
	return new(big.Int).Exp(big.NewInt(10), big.NewInt(int64(max(a.Decimals, 0)-digits)), nil)
}

// Estimate is the approximate amount due if an invoice were paid with Asset.
type Estimate struct {
	Asset  domain.Asset
	Amount *big.Int // base units; nil when no rate is available
}

// Estimates returns, for every asset payable right now (in catalog sort
// order), the amount the invoice would cost at the current rate — the
// checkout asset list. Nothing is created or reserved; the real amount is
// fixed by SelectAsset (and may differ by the unique-amount bump on static
// wallets). Assets without a rate have a nil Amount.
func (s *Service) Estimates(ctx context.Context, inv domain.Invoice) ([]Estimate, error) {
	payable, err := s.store.ListPayableAssets(ctx)
	if err != nil {
		return nil, err
	}
	st, err := s.store.GetSettings(ctx)
	if err != nil {
		return nil, err
	}
	enabled, err := s.store.ListAssets(ctx, store.AssetFilter{EnabledOnly: true})
	if err != nil {
		return nil, err
	}
	out := make([]Estimate, 0, len(payable))
	for _, a := range payable {
		e := Estimate{Asset: a}
		rate, err := s.rateFor(ctx, inv.PriceCurrency, a, st, enabled)
		switch {
		case err == nil:
			if amt, qerr := money.QuoteAmount(inv.PriceAmount, rate, max(st.RateMarkupBps, 0), a.Decimals, a.QuotePrecision); qerr == nil {
				e.Amount = amt
			}
		case errors.Is(err, ErrRateUnavailable):
			// no estimate for this asset
		default:
			return nil, err
		}
		out = append(out, e)
	}
	return out, nil
}
