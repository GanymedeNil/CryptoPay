package store_test

import (
	"testing"
	"time"

	"github.com/google/uuid"
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

// TestMerchantOverviews seeds several merchants (activity inside and
// outside the range, midnight UTC edges, an idle merchant twice under the
// same name, a disabled merchant, a merchant with only old activity, a paid
// invoice without USD value, failed webhooks) and checks the per-merchant
// rows, the filtered overview and that both agree with the unfiltered one.
func TestMerchantOverviews(t *testing.T) {
	t.Parallel()
	s := storetest.NewSeeded(t)
	tw := storetest.Wallet(t, s, domain.FamilyTron, domain.WalletXpub)
	bw := storetest.Wallet(t, s, domain.FamilyUTXO, domain.WalletXpub)
	named := func(name string, st domain.MerchantStatus) domain.Merchant {
		return storetest.Merchant(t, s, func(m *domain.Merchant) { m.Name, m.Status = name, st })
	}
	alpha := named("Alpha", domain.MerchantActive)
	bravo := named("Bravo", domain.MerchantActive)
	charlie := named("Charlie", domain.MerchantDisabled)
	delta1 := named("Delta", domain.MerchantActive) // no invoices at all, twice under one name (id tie-break)
	delta2 := named("Delta", domain.MerchantActive)
	echo := named("Echo", domain.MerchantActive) // only activity before the range

	at := func(month time.Month, day, hour, min int) time.Time {
		return time.Date(2030, month, day, hour, min, 0, 0, time.UTC)
	}
	d := func(day, hour, min int) time.Time { return at(3, day, hour, min) }
	justBefore := func(t time.Time) time.Time { return t.Add(-time.Microsecond) }
	now := d(10, 15, 0) // 7 days: [03-04 00:00, 03-11 00:00)
	tp := func(t time.Time) *time.Time { return &t }

	type spec struct {
		m       domain.Merchant
		created time.Time
		status  domain.InvoiceStatus
		paidAt  *time.Time
		usd     string // "" = NULL
		asset   string // paid quote asset ("" = no paid quote)
	}
	invoices := map[string]spec{
		// Alpha
		"A1": {alpha, d(2, 10, 0), domain.InvoicePaid, tp(d(4, 0, 0)), "100", "usdt-tron"},  // created before, paid exactly at From
		"A2": {alpha, d(4, 0, 0), domain.InvoicePaid, tp(d(4, 9, 30)), "50.5", "usdt-tron"}, // created exactly at From
		"A3": {alpha, d(5, 12, 0), domain.InvoicePaid, tp(d(6, 0, 0)), "20", "btc-bitcoin"}, // paid at midnight
		"A4": {alpha, d(5, 13, 0), domain.InvoiceExpired, nil, "30", ""},
		"A5": {alpha, d(10, 14, 0), domain.InvoicePending, nil, "10", ""},                                          // open
		"A6": {alpha, justBefore(d(4, 0, 0)), domain.InvoicePaid, tp(justBefore(d(4, 0, 0))), "1000", "usdt-tron"}, // just before the range
		"A7": {alpha, at(2, 1, 0, 0), domain.InvoicePending, nil, "5", ""},                                         // old but open
		// Bravo
		"B1": {bravo, d(7, 8, 0), domain.InvoicePaid, tp(d(7, 9, 0)), "", "usdt-tron"}, // paid without USD value
		"B2": {bravo, d(8, 10, 0), domain.InvoiceCancelled, nil, "40", ""},
		"B3": {bravo, d(9, 10, 0), domain.InvoicePaid, tp(d(9, 11, 0)), "7", ""},            // marked paid manually: no quote
		"B4": {bravo, d(10, 14, 30), domain.InvoicePartial, nil, "", ""},                    // open
		"B5": {bravo, d(1, 10, 0), domain.InvoicePaid, tp(d(11, 0, 0)), "500", "usdt-tron"}, // paid exactly at To: outside
		// Charlie (disabled)
		"C1": {charlie, d(6, 10, 0), domain.InvoicePaid, tp(d(6, 11, 0)), "200", "usdt-tron"},
		"C2": {charlie, at(1, 15, 9, 0), domain.InvoiceExpired, nil, "3", ""},
		// Echo
		"E1": {echo, at(2, 20, 10, 0), domain.InvoicePaid, tp(at(2, 20, 11, 0)), "15", "btc-bitcoin"},
	}
	byName := map[string]domain.Invoice{}
	for name, sp := range invoices {
		inv := storetest.Invoice(t, s, sp.m.ID, func(i *domain.Invoice) {
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
		byName[name] = inv
	}
	failed := func(e *domain.WebhookEvent) { e.Status = domain.WebhookFailed }
	storetest.WebhookEvent(t, s, byName["A4"], failed)
	storetest.WebhookEvent(t, s, byName["A6"], failed) // not range-limited
	storetest.WebhookEvent(t, s, byName["A2"])         // pending: not counted
	storetest.WebhookEvent(t, s, byName["B2"], failed)
	storetest.WebhookEvent(t, s, byName["E1"], failed)
	// Transfers: matched, unmatched, unmatched orphaned.
	storetest.TransferFor(t, s, storetest.Quote(t, s, byName["B4"], "trx-tron", tw), 5)
	storetest.Transfer(t, s)
	storetest.Transfer(t, s, func(x *domain.Transfer) { x.Status = domain.TransferOrphaned })

	type row struct {
		invoices, paid int
		volume         string
		conversion     float64
		open, failed   int
		lastInvoice    *time.Time
		lastPaid       *time.Time
	}
	want := map[uuid.UUID]row{
		alpha.ID:   {4, 3, "170.5", 0.5, 2, 2, tp(d(10, 14, 0)), tp(d(6, 0, 0))}, // A2 A3 A4 A5 / A1 A2 A3 / A2 A3 of 4 / A5 A7
		bravo.ID:   {4, 2, "7", 0.5, 1, 1, tp(d(10, 14, 30)), tp(d(11, 0, 0))},   // B1 B2 B3 B4 / B1 B3 / B1 B3 of 4 / B4
		charlie.ID: {1, 1, "200", 1, 0, 0, tp(d(6, 10, 0)), tp(d(6, 11, 0))},
		delta1.ID:  {0, 0, "0", 0, 0, 0, nil, nil},
		delta2.ID:  {0, 0, "0", 0, 0, 0, nil, nil},
		echo.ID:    {0, 0, "0", 0, 0, 1, tp(at(2, 20, 10, 0)), tp(at(2, 20, 11, 0))},
	}

	list, err := s.ListMerchantOverviews(ctx, store.MerchantOverviewFilter{Days: 7, PageSize: 100}, now)
	require.NoError(t, err)
	assert.Equal(t, 7, list.RangeDays)
	assert.Equal(t, d(4, 0, 0), list.From)
	assert.Equal(t, d(11, 0, 0), list.To)
	require.Equal(t, 6, list.Rows.Total)
	require.Len(t, list.Rows.Items, 6)

	all, err := s.Overview(ctx, 7, now)
	require.NoError(t, err)
	assert.Nil(t, all.MerchantID)
	var sum store.OverviewTotals
	var createdPaid float64
	daily := make([]store.DailyStat, len(all.Daily))
	byAsset := map[string]store.AssetStat{}
	byStatus := map[domain.InvoiceStatus]int{}
	for _, r := range list.Rows.Items {
		w, ok := want[r.MerchantID]
		require.True(t, ok, r.MerchantName)
		assert.Equal(t, w.invoices, r.Invoices, r.MerchantName)
		assert.Equal(t, w.paid, r.PaidInvoices, r.MerchantName)
		assert.Equal(t, w.volume, r.VolumeUSD.String(), r.MerchantName)
		assert.InDelta(t, w.conversion, r.ConversionRate, 1e-12, r.MerchantName)
		assert.Equal(t, w.open, r.OpenInvoices, r.MerchantName)
		assert.Equal(t, w.failed, r.FailedWebhooks, r.MerchantName)
		assert.Equal(t, w.lastInvoice, r.LastInvoiceAt, r.MerchantName)
		assert.Equal(t, w.lastPaid, r.LastPaidAt, r.MerchantName)
		if r.MerchantID == charlie.ID {
			assert.Equal(t, domain.MerchantDisabled, r.Status)
		} else {
			assert.Equal(t, domain.MerchantActive, r.Status)
		}

		// The row is exactly the merchant's filtered overview.
		ov, err := s.OverviewFor(ctx, &r.MerchantID, 7, now)
		require.NoError(t, err)
		require.NotNil(t, ov.MerchantID)
		assert.Equal(t, r.MerchantID, *ov.MerchantID)
		assert.Equal(t, store.OverviewTotals{Invoices: r.Invoices, PaidInvoices: r.PaidInvoices, OpenInvoices: r.OpenInvoices,
			VolumeUSD: ov.Totals.VolumeUSD, ConversionRate: r.ConversionRate, FailedWebhooks: r.FailedWebhooks}, ov.Totals, r.MerchantName)
		assert.True(t, r.VolumeUSD.Equal(ov.Totals.VolumeUSD), r.MerchantName)
		assert.Zero(t, ov.Totals.UnmatchedTransfers, "unmatched transfers belong to no merchant")

		sum.Invoices += ov.Totals.Invoices
		sum.PaidInvoices += ov.Totals.PaidInvoices
		sum.OpenInvoices += ov.Totals.OpenInvoices
		sum.FailedWebhooks += ov.Totals.FailedWebhooks
		sum.VolumeUSD = sum.VolumeUSD.Add(r.VolumeUSD)
		createdPaid += r.ConversionRate * float64(r.Invoices)
		require.Len(t, ov.Daily, len(all.Daily))
		for i, x := range ov.Daily {
			daily[i].Date = x.Date
			daily[i].Invoices += x.Invoices
			daily[i].Paid += x.Paid
			daily[i].VolumeUSD = daily[i].VolumeUSD.Add(x.VolumeUSD)
		}
		for _, a := range ov.ByAsset {
			cur := byAsset[a.AssetID]
			cur.Paid += a.Paid
			cur.VolumeUSD = cur.VolumeUSD.Add(a.VolumeUSD)
			byAsset[a.AssetID] = cur
		}
		for _, sc := range ov.ByStatus {
			byStatus[sc.Status] += sc.Count
		}
	}

	// Summed over every merchant, the rows (and filtered overviews) give the
	// unfiltered overview — except unmatched transfers.
	tot := all.Totals
	assert.Equal(t, 9, tot.Invoices)
	assert.Equal(t, 6, tot.PaidInvoices)
	assert.Equal(t, 3, tot.OpenInvoices)
	assert.Equal(t, "377.5", tot.VolumeUSD.String())
	assert.InDelta(t, 5.0/9, tot.ConversionRate, 1e-12)
	assert.Equal(t, 4, tot.FailedWebhooks)
	assert.Equal(t, 1, tot.UnmatchedTransfers)
	assert.Equal(t, tot.Invoices, sum.Invoices)
	assert.Equal(t, tot.PaidInvoices, sum.PaidInvoices)
	assert.Equal(t, tot.OpenInvoices, sum.OpenInvoices)
	assert.Equal(t, tot.FailedWebhooks, sum.FailedWebhooks)
	assert.True(t, tot.VolumeUSD.Equal(sum.VolumeUSD), "%s vs %s", tot.VolumeUSD, sum.VolumeUSD)
	assert.InDelta(t, tot.ConversionRate*float64(tot.Invoices), createdPaid, 1e-9)
	for i, x := range all.Daily {
		assert.Equal(t, x.Date, daily[i].Date)
		assert.Equal(t, x.Invoices, daily[i].Invoices, x.Date)
		assert.Equal(t, x.Paid, daily[i].Paid, x.Date)
		assert.True(t, x.VolumeUSD.Equal(daily[i].VolumeUSD), x.Date)
	}
	require.Len(t, byAsset, len(all.ByAsset))
	for _, a := range all.ByAsset {
		assert.Equal(t, a.Paid, byAsset[a.AssetID].Paid, a.AssetID)
		assert.True(t, a.VolumeUSD.Equal(byAsset[a.AssetID].VolumeUSD), a.AssetID)
	}
	for _, sc := range all.ByStatus {
		assert.Equal(t, sc.Count, byStatus[sc.Status], sc.Status)
	}

	// Alpha by hand: daily, by asset, by status.
	ov, err := s.OverviewFor(ctx, &alpha.ID, 7, now)
	require.NoError(t, err)
	type day struct {
		date      string
		inv, paid int
		volume    string
	}
	wantDays := []day{
		{"2030-03-04", 1, 2, "150.5"}, {"2030-03-05", 2, 0, "0"}, {"2030-03-06", 0, 1, "20"}, {"2030-03-07", 0, 0, "0"},
		{"2030-03-08", 0, 0, "0"}, {"2030-03-09", 0, 0, "0"}, {"2030-03-10", 1, 0, "0"},
	}
	require.Len(t, ov.Daily, len(wantDays))
	for i, w := range wantDays {
		assert.Equal(t, w.date, ov.Daily[i].Date)
		assert.Equal(t, w.inv, ov.Daily[i].Invoices, w.date)
		assert.Equal(t, w.paid, ov.Daily[i].Paid, w.date)
		assert.Equal(t, w.volume, ov.Daily[i].VolumeUSD.String(), w.date)
	}
	require.Len(t, ov.ByAsset, 2)
	assert.Equal(t, "usdt-tron", ov.ByAsset[0].AssetID)
	assert.Equal(t, 2, ov.ByAsset[0].Paid)
	assert.Equal(t, "150.5", ov.ByAsset[0].VolumeUSD.String())
	assert.Equal(t, "btc-bitcoin", ov.ByAsset[1].AssetID)
	assert.Equal(t, "20", ov.ByAsset[1].VolumeUSD.String())
	wantStatus := map[domain.InvoiceStatus]int{domain.InvoicePaid: 2, domain.InvoiceExpired: 1, domain.InvoicePending: 1}
	for _, sc := range ov.ByStatus {
		assert.Equal(t, wantStatus[sc.Status], sc.Count, sc.Status)
	}

	// An idle merchant: zeros, zero-filled series, empty (non-nil) lists.
	idle, err := s.OverviewFor(ctx, &delta1.ID, 7, now)
	require.NoError(t, err)
	assert.Zero(t, idle.Totals.Invoices)
	assert.True(t, idle.Totals.VolumeUSD.IsZero())
	assert.Len(t, idle.Daily, 7)
	assert.NotNil(t, idle.ByAsset)
	assert.Empty(t, idle.ByAsset)
	assert.Len(t, idle.ByStatus, len(store.AllInvoiceStatuses))

	// Unknown merchant.
	unknown := uuid.New()
	_, err = s.OverviewFor(ctx, &unknown, 7, now)
	assert.ErrorIs(t, err, store.ErrNotFound)

	// Sorting: every key in both directions; ties by name, then id. The two
	// "Delta" merchants are told apart by id.
	deltaLo, deltaHi := delta1.ID, delta2.ID
	if deltaHi.String() < deltaLo.String() {
		deltaLo, deltaHi = deltaHi, deltaLo
	}
	ids := map[string]uuid.UUID{"A": alpha.ID, "B": bravo.ID, "C": charlie.ID, "D": deltaLo, "d": deltaHi, "E": echo.ID}
	order := func(keys string) []uuid.UUID {
		out := make([]uuid.UUID, 0, len(keys))
		for _, k := range keys {
			out = append(out, ids[string(k)])
		}
		return out
	}
	names := map[uuid.UUID]string{}
	for k, id := range ids {
		names[id] = k
	}
	cases := []struct {
		sort  store.MerchantOverviewSort
		order string
		want  string
	}{
		{"", "", "CABDdE"}, // default: volume, descending
		{store.SortMerchantVolume, store.OrderDesc, "CABDdE"},
		{store.SortMerchantVolume, store.OrderAsc, "DdEBAC"}, // numeric: 7 < 170.5 < 200
		{store.SortMerchantInvoices, "", "ABCDdE"},
		{store.SortMerchantInvoices, store.OrderAsc, "DdECAB"},
		{store.SortMerchantPaidInvoices, store.OrderDesc, "ABCDdE"},
		{store.SortMerchantPaidInvoices, store.OrderAsc, "DdECBA"},
		{store.SortMerchantConversion, store.OrderDesc, "CABDdE"},
		{store.SortMerchantConversion, store.OrderAsc, "DdEABC"},
		{store.SortMerchantOpenInvoices, store.OrderDesc, "ABCDdE"},
		{store.SortMerchantOpenInvoices, store.OrderAsc, "CDdEBA"},
		{store.SortMerchantFailedWebhooks, store.OrderDesc, "ABECDd"},
		{store.SortMerchantFailedWebhooks, store.OrderAsc, "CDdBEA"},
		{store.SortMerchantName, "", "ABCDdE"}, // default for name: ascending
		{store.SortMerchantName, store.OrderAsc, "ABCDdE"},
		{store.SortMerchantName, store.OrderDesc, "EDdCBA"}, // equal names: id ascending
	}
	for _, tc := range cases {
		l, err := s.ListMerchantOverviews(ctx, store.MerchantOverviewFilter{Days: 7, Sort: tc.sort, Order: tc.order}, now)
		require.NoError(t, err)
		got := make([]uuid.UUID, 0, len(l.Rows.Items))
		for _, r := range l.Rows.Items {
			got = append(got, r.MerchantID)
		}
		gotKeys := ""
		for _, id := range got {
			gotKeys += names[id]
		}
		assert.Equal(t, order(tc.want), got, "sort %q order %q: got %s", tc.sort, tc.order, gotKeys)
	}

	// Paging: pages concatenate to the full list; past the end is empty.
	var paged []uuid.UUID
	for p := 1; p <= 3; p++ {
		l, err := s.ListMerchantOverviews(ctx, store.MerchantOverviewFilter{Days: 7, Page: p, PageSize: 2}, now)
		require.NoError(t, err)
		assert.Equal(t, 6, l.Rows.Total)
		assert.Equal(t, p, l.Rows.Page)
		assert.Equal(t, 2, l.Rows.PageSize)
		require.Len(t, l.Rows.Items, 2)
		for _, r := range l.Rows.Items {
			paged = append(paged, r.MerchantID)
		}
	}
	assert.Equal(t, order("CABDdE"), paged)
	past, err := s.ListMerchantOverviews(ctx, store.MerchantOverviewFilter{Days: 7, Page: 4, PageSize: 2}, now)
	require.NoError(t, err)
	assert.Equal(t, 6, past.Rows.Total)
	assert.NotNil(t, past.Rows.Items)
	assert.Empty(t, past.Rows.Items)

	// Range: default 30 days (B5's and A6's payments fall inside), clamping.
	l30, err := s.ListMerchantOverviews(ctx, store.MerchantOverviewFilter{}, now)
	require.NoError(t, err)
	assert.Equal(t, store.DefaultOverviewDays, l30.RangeDays)
	ov30, err := s.Overview(ctx, 0, now)
	require.NoError(t, err)
	var paid30 int
	for _, r := range l30.Rows.Items {
		paid30 += r.PaidInvoices
	}
	assert.Equal(t, ov30.Totals.PaidInvoices, paid30)
	lMax, err := s.ListMerchantOverviews(ctx, store.MerchantOverviewFilter{Days: 10_000}, now)
	require.NoError(t, err)
	assert.Equal(t, store.MaxOverviewDays, lMax.RangeDays)

	// Invalid sort / order are rejected (the API validates first).
	_, err = s.ListMerchantOverviews(ctx, store.MerchantOverviewFilter{Sort: "created_at"}, now)
	assert.ErrorIs(t, err, store.ErrInvalid)
	_, err = s.ListMerchantOverviews(ctx, store.MerchantOverviewFilter{Order: "sideways"}, now)
	assert.ErrorIs(t, err, store.ErrInvalid)

	// Works inside a caller's transaction too.
	err = s.WithTx(ctx, func(tx *store.Store) error {
		if _, err := tx.ListMerchantOverviews(ctx, store.MerchantOverviewFilter{Days: 7}, now); err != nil {
			return err
		}
		_, err := tx.OverviewFor(ctx, &alpha.ID, 7, now)
		return err
	})
	require.NoError(t, err)
}

func TestMerchantOverviewsEmpty(t *testing.T) {
	t.Parallel()
	s := storetest.New(t)
	l, err := s.ListMerchantOverviews(ctx, store.MerchantOverviewFilter{}, time.Date(2030, 1, 1, 0, 0, 0, 0, time.UTC))
	require.NoError(t, err)
	assert.Zero(t, l.Rows.Total)
	assert.NotNil(t, l.Rows.Items)
	assert.Equal(t, 1, l.Rows.Page)
	assert.Equal(t, store.DefaultPageSize, l.Rows.PageSize)
}
