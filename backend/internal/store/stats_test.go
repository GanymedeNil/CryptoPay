package store_test

import (
	"testing"
	"time"

	"github.com/shopspring/decimal"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"cryptopay/internal/domain"
	"cryptopay/internal/store"
	"cryptopay/internal/store/storetest"
)

func TestOverview(t *testing.T) {
	t.Parallel()
	s := storetest.NewSeeded(t)
	m := storetest.Merchant(t, s)
	tw := storetest.Wallet(t, s, domain.FamilyTron, domain.WalletXpub)
	bw := storetest.Wallet(t, s, domain.FamilyUTXO, domain.WalletXpub)

	d := func(day, hour, min int) time.Time { return time.Date(2030, 3, day, hour, min, 0, 0, time.UTC) }
	now := d(10, 15, 0)

	type spec struct {
		created time.Time
		status  domain.InvoiceStatus
		paidAt  *time.Time
		usd     string // "" = NULL
		asset   string // paid quote asset ("" = no paid quote)
	}
	tp := func(t time.Time) *time.Time { return &t }
	specs := map[string]spec{
		"A": {d(2, 10, 0), domain.InvoicePaid, tp(d(4, 1, 0)), "100", "usdt-tron"},              // created before range, paid in range
		"B": {d(4, 9, 0), domain.InvoicePaid, tp(d(4, 9, 30)), "50.5", "usdt-tron"},             //
		"C": {d(5, 12, 0), domain.InvoicePaid, tp(d(6, 0, 0)), "20", "btc-bitcoin"},             // paid exactly at midnight
		"D": {d(5, 13, 0), domain.InvoiceExpired, nil, "30", ""},                                //
		"E": {d(10, 14, 0), domain.InvoicePending, nil, "10", ""},                               // open
		"F": {d(10, 14, 30), domain.InvoicePartial, nil, "", ""},                                // open
		"G": {time.Date(2030, 2, 1, 0, 0, 0, 0, time.UTC), domain.InvoicePending, nil, "5", ""}, // old but open
		"H": {d(3, 23, 59).Add(59*time.Second + 999999*time.Microsecond), domain.InvoicePaid,
			tp(d(3, 23, 59).Add(59*time.Second + 999999*time.Microsecond)), "1000", "usdt-tron"}, // just before range
		"I": {d(7, 8, 0), domain.InvoicePaid, tp(d(7, 9, 0)), "", "usdt-tron"}, // paid without USD value
		"J": {d(8, 10, 0), domain.InvoiceCancelled, nil, "40", ""},
		"K": {d(9, 10, 0), domain.InvoicePaid, tp(d(9, 11, 0)), "7", ""}, // marked paid manually: no quote
	}
	for name, sp := range specs {
		inv := storetest.Invoice(t, s, m.ID, func(i *domain.Invoice) {
			i.Title = name
			i.CreatedAt, i.UpdatedAt = sp.created, sp.created
			i.Status, i.PaidAt = sp.status, sp.paidAt
			i.ExpiresAt = sp.created.Add(30 * time.Minute)
			i.AmountUSD = nil
			if sp.usd != "" {
				v := decimal.RequireFromString(sp.usd)
				i.AmountUSD = &v
			}
		})
		if sp.asset != "" {
			w := tw
			if sp.asset == "btc-bitcoin" {
				w = bw
			}
			q := storetest.Quote(t, s, inv, sp.asset, w, func(q *domain.Quote) { q.Status = domain.QuotePaid })
			st := store.StateOf(inv)
			st.PaidQuoteID = &q.ID
			_, err := s.UpdateInvoiceState(ctx, inv.ID, st)
			require.NoError(t, err)
		}
	}
	// Transfers: one matched, one unmatched pending, one unmatched orphaned.
	invE, err := s.ListInvoices(ctx, store.InvoiceFilter{Statuses: []domain.InvoiceStatus{domain.InvoicePartial}})
	require.NoError(t, err)
	require.Len(t, invE.Items, 1)
	qF := storetest.Quote(t, s, invE.Items[0].Invoice, "trx-tron", tw)
	storetest.TransferFor(t, s, qF, 5)
	storetest.Transfer(t, s)
	storetest.Transfer(t, s, func(x *domain.Transfer) { x.Status = domain.TransferOrphaned })
	// Webhooks: one failed, one pending.
	anyInv := invE.Items[0].Invoice
	storetest.WebhookEvent(t, s, anyInv, func(e *domain.WebhookEvent) { e.Status = domain.WebhookFailed })
	storetest.WebhookEvent(t, s, anyInv)

	ov, err := s.Overview(ctx, 7, now)
	require.NoError(t, err)
	assert.Equal(t, 7, ov.RangeDays)
	assert.Equal(t, d(4, 0, 0), ov.From)
	assert.Equal(t, d(11, 0, 0), ov.To)

	tot := ov.Totals
	assert.Equal(t, 8, tot.Invoices, "B C D E F I J K")
	assert.Equal(t, 5, tot.PaidInvoices, "A B C I K")
	assert.Equal(t, 3, tot.OpenInvoices, "E F G")
	assert.Equal(t, "177.5", tot.VolumeUSD.String(), "100 + 50.5 + 20 + 0 + 7")
	assert.InDelta(t, 0.5, tot.ConversionRate, 1e-9, "4 of 8 created invoices are paid")
	assert.Equal(t, 1, tot.UnmatchedTransfers)
	assert.Equal(t, 1, tot.FailedWebhooks)

	type day struct {
		date      string
		inv, paid int
		volume    string
	}
	want := []day{
		{"2030-03-04", 1, 2, "150.5"},
		{"2030-03-05", 2, 0, "0"},
		{"2030-03-06", 0, 1, "20"},
		{"2030-03-07", 1, 1, "0"},
		{"2030-03-08", 1, 0, "0"},
		{"2030-03-09", 1, 1, "7"},
		{"2030-03-10", 2, 0, "0"},
	}
	require.Len(t, ov.Daily, len(want))
	for i, w := range want {
		got := ov.Daily[i]
		assert.Equal(t, w.date, got.Date)
		assert.Equal(t, w.inv, got.Invoices, w.date)
		assert.Equal(t, w.paid, got.Paid, w.date)
		assert.Equal(t, w.volume, got.VolumeUSD.String(), w.date)
	}

	require.Len(t, ov.ByAsset, 2)
	assert.Equal(t, store.AssetStat{AssetID: "usdt-tron", Symbol: "USDT", ChainID: "tron", ChainName: "Tron", Paid: 3,
		VolumeUSD: ov.ByAsset[0].VolumeUSD}, ov.ByAsset[0])
	assert.Equal(t, "150.5", ov.ByAsset[0].VolumeUSD.String())
	assert.Equal(t, "btc-bitcoin", ov.ByAsset[1].AssetID)
	assert.Equal(t, "Bitcoin", ov.ByAsset[1].ChainName)
	assert.Equal(t, 1, ov.ByAsset[1].Paid)
	assert.Equal(t, "20", ov.ByAsset[1].VolumeUSD.String())

	wantStatus := map[domain.InvoiceStatus]int{
		domain.InvoicePending: 1, domain.InvoicePartial: 1, domain.InvoiceConfirming: 0, domain.InvoicePaid: 4,
		domain.InvoiceUnderpaid: 0, domain.InvoiceExpired: 1, domain.InvoiceCancelled: 1,
	}
	require.Len(t, ov.ByStatus, len(store.AllInvoiceStatuses))
	for i, sc := range ov.ByStatus {
		assert.Equal(t, store.AllInvoiceStatuses[i], sc.Status)
		assert.Equal(t, wantStatus[sc.Status], sc.Count, sc.Status)
	}

	// One day: only today.
	ov1, err := s.Overview(ctx, 1, now)
	require.NoError(t, err)
	require.Len(t, ov1.Daily, 1)
	assert.Equal(t, "2030-03-10", ov1.Daily[0].Date)
	assert.Equal(t, 2, ov1.Totals.Invoices)
	assert.Zero(t, ov1.Totals.PaidInvoices)
	assert.Zero(t, ov1.Totals.ConversionRate)
	assert.Empty(t, ov1.ByAsset)
	assert.NotNil(t, ov1.ByAsset)

	// Defaults and clamping.
	ov30, err := s.Overview(ctx, 0, now)
	require.NoError(t, err)
	assert.Equal(t, store.DefaultOverviewDays, ov30.RangeDays)
	assert.Len(t, ov30.Daily, 30)
	assert.Equal(t, 6, ov30.Totals.PaidInvoices, "H is inside 30 days")
	ovMax, err := s.Overview(ctx, 10_000, now)
	require.NoError(t, err)
	assert.Len(t, ovMax.Daily, store.MaxOverviewDays)
	assert.Equal(t, 11, ovMax.Totals.Invoices, "A…K are all inside 365 days")
}

func TestOverviewEmpty(t *testing.T) {
	t.Parallel()
	s := storetest.New(t)
	ov, err := s.Overview(ctx, 3, time.Date(2030, 1, 1, 0, 0, 0, 0, time.UTC))
	require.NoError(t, err)
	assert.Len(t, ov.Daily, 3)
	assert.Equal(t, "2029-12-30", ov.Daily[0].Date)
	assert.Equal(t, "2030-01-01", ov.Daily[2].Date)
	for _, d := range ov.Daily {
		assert.Zero(t, d.Invoices)
		assert.True(t, d.VolumeUSD.IsZero())
	}
	assert.True(t, ov.Totals.VolumeUSD.IsZero())
	assert.NotNil(t, ov.ByAsset)
	assert.Len(t, ov.ByStatus, 7)

	// Works inside a caller's transaction too.
	err = s.WithTx(ctx, func(tx *store.Store) error {
		_, err := tx.Overview(ctx, 3, time.Now())
		return err
	})
	require.NoError(t, err)
}
