package invoice

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/mail"
	"net/url"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/shopspring/decimal"

	"cryptopay/internal/domain"
	"cryptopay/internal/rates"
	"cryptopay/internal/store"
)

// Limits of CreateInvoice (docs/api.md).
const (
	MaxOrderIDLen     = 128
	MaxTitleLen       = 200
	MaxDescriptionLen = 2000
	MaxURLLen         = 2048
	MaxEmailLen       = 254
	MaxMetadataBytes  = 4096
	MinExpiresIn      = 60
	MaxExpiresIn      = 86400
	// MaxFiatDecimals / MaxCryptoDecimals bound the precision of the price
	// amount (significant fractional digits).
	MaxFiatDecimals   = 6
	MaxCryptoDecimals = 18
	// usdScale is the precision amount_usd is stored with.
	usdScale = 6
)

// MaxAmount is the largest accepted price amount.
var MaxAmount = decimal.New(1, 12) // 1 000 000 000 000

// usdLookupTimeout bounds the best-effort amount_usd rate lookup.
const usdLookupTimeout = 5 * time.Second

// CreateParams is the `CreateInvoice` body of docs/api.md.
type CreateParams struct {
	Amount        string          // required decimal string, > 0
	Currency      string          // required: supported fiat code or symbol of an enabled asset
	OrderID       string          // optional idempotency key
	Title         string          //
	Description   string          //
	AssetID       string          // optional: create the quote immediately
	SuccessURL    string          // optional http(s) URL
	CancelURL     string          // optional http(s) URL
	CustomerEmail string          //
	ExpiresIn     *int            // seconds, 60..86400; nil = settings.invoice_ttl_seconds
	Metadata      json.RawMessage // optional JSON object, at most 4 KB
}

// Create creates an invoice for merchant.
//
// Validation failures return a *ValidationError (ErrInvalid) naming the
// JSON fields. amount_usd is computed best effort: a missing rate never
// fails the creation, the column just stays NULL.
//
// Idempotency: when OrderID is non-empty and the merchant already has an
// invoice with it, that invoice is returned with created=false if amount and
// currency are the same (nothing is changed, AssetID is ignored), else
// ErrOrderConflict.
//
// With AssetID the quote is created in the same transaction; if that fails
// (ErrAssetUnavailable, ErrRateUnavailable, ErrNoUniqueAmount) nothing is
// persisted.
func (s *Service) Create(ctx context.Context, merchant domain.Merchant, p CreateParams) (view store.InvoiceView, created bool, err error) {
	st, err := s.store.GetSettings(ctx)
	if err != nil {
		return view, false, err
	}
	assets, err := s.store.ListAssets(ctx, store.AssetFilter{EnabledOnly: true})
	if err != nil {
		return view, false, err
	}
	now := s.clock()
	inv, err := validateCreate(p, st, assets, now)
	if err != nil {
		return view, false, err
	}
	inv.MerchantID = merchant.ID

	// Idempotent replay: answer before doing any rate lookups.
	if inv.MerchantOrderID != "" {
		existing, err := s.store.GetInvoiceByOrderID(ctx, merchant.ID, inv.MerchantOrderID)
		switch {
		case err == nil:
			return s.replay(ctx, existing, inv)
		case !errors.Is(err, store.ErrNotFound):
			return view, false, err
		}
	}

	var plan *quotePlan
	if assetID := strings.TrimSpace(p.AssetID); assetID != "" {
		qp, err := s.plan(ctx, merchant.ID, assetID, inv.PriceCurrency)
		if err != nil {
			return view, false, err
		}
		plan = &qp
	}
	inv.AmountUSD = s.amountUSD(ctx, inv.PriceAmount, inv.PriceCurrency, st, assets)

	var existing *domain.Invoice
	err = s.store.WithTx(ctx, func(tx *store.Store) error {
		existing = nil
		out, err := tx.CreateInvoice(ctx, inv)
		if err != nil {
			if store.IsConstraint(err, store.ConstraintInvoiceMerchantOrder) {
				// Lost a race against a concurrent request with the same order id.
				e, gerr := tx.GetInvoiceByOrderID(ctx, merchant.ID, inv.MerchantOrderID)
				if gerr != nil {
					return gerr
				}
				existing = &e
				return nil
			}
			if errors.Is(err, store.ErrForeignKey) {
				return fmt.Errorf("%w: merchant %s", ErrNotFound, merchant.ID)
			}
			return err
		}
		inv = out
		if plan != nil {
			if _, err := s.selectTx(ctx, tx, out.ID, *plan); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return view, false, err
	}
	if existing != nil {
		return s.replay(ctx, *existing, inv)
	}
	s.log.InfoContext(ctx, "invoice created", "invoice_id", inv.ID, "merchant_id", merchant.ID,
		"amount", inv.PriceAmount.String(), "currency", inv.PriceCurrency, "asset_id", p.AssetID)
	view, err = s.GetView(ctx, inv.ID)
	return view, err == nil, err
}

// replay answers a Create whose order id already exists.
func (s *Service) replay(ctx context.Context, existing, want domain.Invoice) (store.InvoiceView, bool, error) {
	if !existing.PriceAmount.Equal(want.PriceAmount) || !strings.EqualFold(existing.PriceCurrency, want.PriceCurrency) {
		return store.InvoiceView{}, false, ErrOrderConflict
	}
	v, err := s.GetView(ctx, existing.ID)
	return v, false, err
}

// validateCreate checks p and returns the invoice to insert (without
// merchant and amount_usd).
func validateCreate(p CreateParams, st domain.Settings, assets []domain.Asset, now time.Time) (domain.Invoice, error) {
	fields := map[string]string{}
	inv := domain.Invoice{
		MerchantOrderID: strings.TrimSpace(p.OrderID),
		Title:           strings.TrimSpace(p.Title),
		Description:     strings.TrimSpace(p.Description),
		SuccessURL:      strings.TrimSpace(p.SuccessURL),
		CancelURL:       strings.TrimSpace(p.CancelURL),
		CustomerEmail:   strings.TrimSpace(p.CustomerEmail),
		Status:          domain.InvoicePending,
	}

	// Currency first: the allowed amount precision depends on it.
	maxDecimals := MaxFiatDecimals
	cur := strings.TrimSpace(p.Currency)
	switch {
	case cur == "":
		fields["currency"] = "is required"
	case rates.IsSupportedCurrency(cur):
		inv.PriceCurrency = strings.ToUpper(cur)
	default:
		if a, ok := assetBySymbol(assets, cur); ok {
			inv.PriceCurrency = a.Symbol
			maxDecimals = min(MaxCryptoDecimals, max(a.Decimals, 0))
		} else {
			fields["currency"] = "must be a supported fiat currency code or the symbol of an enabled asset"
		}
	}

	if amount, msg := parseAmount(p.Amount, maxDecimals); msg != "" {
		fields["amount"] = msg
	} else {
		inv.PriceAmount = amount
	}

	checkLen := func(field, v string, limit int) {
		if utf8.RuneCountInString(v) > limit {
			fields[field] = fmt.Sprintf("must be at most %d characters", limit)
		} else if !utf8.ValidString(v) || strings.ContainsRune(v, 0) {
			fields[field] = "contains invalid characters"
		}
	}
	checkLen("order_id", inv.MerchantOrderID, MaxOrderIDLen)
	checkLen("title", inv.Title, MaxTitleLen)
	checkLen("description", inv.Description, MaxDescriptionLen)
	checkURL := func(field, v string) {
		if v == "" {
			return
		}
		if len(v) > MaxURLLen {
			fields[field] = fmt.Sprintf("must be at most %d characters", MaxURLLen)
			return
		}
		u, err := url.Parse(v)
		if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
			fields[field] = "must be an absolute http or https URL"
		}
	}
	checkURL("success_url", inv.SuccessURL)
	checkURL("cancel_url", inv.CancelURL)
	if e := inv.CustomerEmail; e != "" {
		addr, err := mail.ParseAddress(e)
		if len(e) > MaxEmailLen || err != nil || addr.Address != e {
			fields["customer_email"] = "must be a valid e-mail address"
		}
	}

	ttl := st.InvoiceTTLSeconds
	if p.ExpiresIn != nil {
		ttl = *p.ExpiresIn
		if ttl < MinExpiresIn || ttl > MaxExpiresIn {
			fields["expires_in"] = fmt.Sprintf("must be between %d and %d seconds", MinExpiresIn, MaxExpiresIn)
		}
	} else if ttl <= 0 {
		ttl = domain.DefaultSettings().InvoiceTTLSeconds
	}
	inv.ExpiresAt = now.Add(time.Duration(ttl) * time.Second)

	meta := bytes.TrimSpace(p.Metadata)
	switch {
	case len(meta) == 0 || string(meta) == "null":
		inv.Metadata = json.RawMessage(`{}`)
	case len(meta) > MaxMetadataBytes:
		fields["metadata"] = fmt.Sprintf("must be at most %d bytes", MaxMetadataBytes)
	case meta[0] != '{' || !json.Valid(meta) || bytes.Contains(meta, []byte(`\u0000`)):
		fields["metadata"] = "must be a JSON object"
	default:
		inv.Metadata = json.RawMessage(meta)
	}

	if len(fields) > 0 {
		return domain.Invoice{}, &ValidationError{Fields: fields}
	}
	return inv, nil
}

// parseAmount strictly parses a positive plain decimal ("12", "0.5"): no
// sign, exponent or whitespace, at most maxDecimals significant fractional
// digits and at most MaxAmount. It returns a message on failure.
func parseAmount(s string, maxDecimals int) (decimal.Decimal, string) {
	if s == "" {
		return decimal.Decimal{}, "is required"
	}
	if len(s) > 64 {
		return decimal.Decimal{}, "is too long"
	}
	whole, frac, hasDot := strings.Cut(s, ".")
	if !digits(whole) || (hasDot && !digits(frac)) {
		return decimal.Decimal{}, "must be a positive decimal number such as \"12.50\""
	}
	if len(strings.TrimRight(frac, "0")) > maxDecimals {
		return decimal.Decimal{}, fmt.Sprintf("must have at most %d decimal places", maxDecimals)
	}
	d, err := decimal.NewFromString(s)
	if err != nil {
		return decimal.Decimal{}, "must be a positive decimal number such as \"12.50\""
	}
	if d.Sign() <= 0 {
		return decimal.Decimal{}, "must be greater than 0"
	}
	if d.GreaterThan(MaxAmount) {
		return decimal.Decimal{}, "is too large (maximum " + MaxAmount.String() + ")"
	}
	return d, ""
}

func digits(s string) bool {
	if s == "" {
		return false
	}
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return false
		}
	}
	return true
}

// amountUSD returns the USD value of amount (for reporting), or nil when it
// cannot be determined right now. It never fails.
func (s *Service) amountUSD(ctx context.Context, amount decimal.Decimal, currency string, st domain.Settings, assets []domain.Asset) *decimal.Decimal {
	ctx, cancel := context.WithTimeout(ctx, usdLookupTimeout)
	defer cancel()
	usd, err := s.usdValue(ctx, amount, currency, st, assets)
	if err != nil {
		s.log.WarnContext(ctx, "amount_usd unavailable", "currency", currency, "error", err)
		return nil
	}
	usd = usd.Round(usdScale)
	return &usd
}

func (s *Service) usdValue(ctx context.Context, amount decimal.Decimal, currency string, st domain.Settings, assets []domain.Asset) (decimal.Decimal, error) {
	if strings.EqualFold(currency, "USD") {
		return amount, nil
	}
	if !rates.IsSupportedCurrency(currency) {
		a, ok := assetBySymbol(assets, currency)
		if !ok {
			return decimal.Decimal{}, fmt.Errorf("%w: unknown currency %q", ErrRateUnavailable, currency)
		}
		price, err := s.usdPrice(ctx, a, st)
		if err != nil {
			return decimal.Decimal{}, err
		}
		return amount.Mul(price), nil
	}
	// Another fiat currency: cross through one asset's price in both
	// currencies (preferably a stablecoin, whose prices are the most liquid).
	var ref *domain.Asset
	for i := range assets {
		if assets[i].RateKey == "" {
			continue
		}
		if ref == nil || (assets[i].IsStablecoin && !ref.IsStablecoin) {
			ref = &assets[i]
		}
	}
	if ref == nil {
		return decimal.Decimal{}, fmt.Errorf("%w: no reference asset", ErrRateUnavailable)
	}
	inUSD, err := s.providerRate(ctx, ref.RateKey, "USD")
	if err != nil {
		return decimal.Decimal{}, err
	}
	inCur, err := s.providerRate(ctx, ref.RateKey, currency)
	if err != nil {
		return decimal.Decimal{}, err
	}
	return amount.Mul(inUSD).DivRound(inCur, usdScale+2), nil
}
