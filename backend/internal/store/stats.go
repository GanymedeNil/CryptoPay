package store

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/shopspring/decimal"

	"cryptopay/internal/domain"
)

// Overview range limits (docs/api.md: days 1..365, default 30).
const (
	DefaultOverviewDays = 30
	MaxOverviewDays     = 365
)

// Overview is the dashboard summary served by GET /api/admin/overview.
//
// The range is the last RangeDays UTC calendar days including today:
// [From, To) with From = midnight UTC RangeDays-1 days ago and To = next
// midnight UTC. Definitions:
//
//   - Totals.Invoices / ByStatus / Daily[].Invoices count invoices CREATED
//     in the range (ByStatus by their current status);
//   - Totals.PaidInvoices / VolumeUSD / ByAsset / Daily[].Paid / VolumeUSD
//     count invoices PAID (status paid, by paid_at) in the range; volume is
//     the sum of invoices.amount_usd (NULL counts as 0); ByAsset groups by
//     the asset of the paid quote (invoices without a paid quote, e.g.
//     marked paid manually, are not in ByAsset);
//   - Totals.ConversionRate = invoices created in the range that are now
//     paid / invoices created in the range (0 when there are none);
//   - Totals.OpenInvoices (pending/partial/confirming), UnmatchedTransfers
//     (not orphaned, no invoice) and FailedWebhooks are current counts,
//     independent of the range.
//
// With MerchantID set (OverviewFor) every figure counts only that
// merchant's invoices and webhook events; UnmatchedTransfers is 0 then (a
// transfer without an invoice belongs to no merchant).
type Overview struct {
	RangeDays  int
	MerchantID *uuid.UUID // nil = every merchant
	From, To   time.Time
	Totals     OverviewTotals
	Daily      []DailyStat   // one row per day, oldest first, zero-filled
	ByAsset    []AssetStat   // by volume desc, then count desc
	ByStatus   []StatusCount // every invoice status in lifecycle order, zero-filled
}

// OverviewTotals are the headline numbers of an Overview.
type OverviewTotals struct {
	Invoices           int
	PaidInvoices       int
	OpenInvoices       int
	VolumeUSD          decimal.Decimal
	ConversionRate     float64 // 0..1
	UnmatchedTransfers int
	FailedWebhooks     int
}

// DailyStat is one day of the Overview series.
type DailyStat struct {
	Date      string // YYYY-MM-DD (UTC)
	Invoices  int
	Paid      int
	VolumeUSD decimal.Decimal
}

// AssetStat is the paid volume of one asset.
type AssetStat struct {
	AssetID   string
	Symbol    string
	ChainID   string
	ChainName string
	Paid      int
	VolumeUSD decimal.Decimal
}

// StatusCount is the number of invoices in one status.
type StatusCount struct {
	Status domain.InvoiceStatus
	Count  int
}

// AllInvoiceStatuses lists every invoice status in lifecycle order.
var AllInvoiceStatuses = []domain.InvoiceStatus{
	domain.InvoicePending, domain.InvoicePartial, domain.InvoiceConfirming, domain.InvoicePaid,
	domain.InvoiceUnderpaid, domain.InvoiceExpired, domain.InvoiceCancelled,
}

// Overview computes the dashboard statistics for the last days UTC days
// (including the day of now). days is clamped to 1..MaxOverviewDays; 0
// means DefaultOverviewDays. Runs in one read-only repeatable-read
// transaction for a consistent snapshot (or in the caller's transaction).
func (s *Store) Overview(ctx context.Context, days int, now time.Time) (Overview, error) {
	return s.OverviewFor(ctx, nil, days, now)
}

// OverviewFor is Overview restricted to one merchant (nil = every
// merchant, the same as Overview). It returns ErrNotFound when the merchant
// does not exist.
func (s *Store) OverviewFor(ctx context.Context, merchantID *uuid.UUID, days int, now time.Time) (Overview, error) {
	days, from, to := overviewRange(days, now)
	ov := Overview{RangeDays: days, MerchantID: merchantID, From: from, To: to}

	run := func(tx *Store) error { return tx.overview(ctx, &ov) }
	err := s.readSnapshot(ctx, run)
	return ov, err
}

// overviewRange clamps days (0 = DefaultOverviewDays) and returns the range
// [from, to) of the last days UTC calendar days including the day of now.
func overviewRange(days int, now time.Time) (int, time.Time, time.Time) {
	if days == 0 {
		days = DefaultOverviewDays
	}
	days = min(max(days, 1), MaxOverviewDays)
	now = now.UTC()
	to := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.UTC).AddDate(0, 0, 1)
	return days, to.AddDate(0, 0, -days), to
}

// readSnapshot runs fn in one read-only repeatable-read transaction (a
// consistent snapshot), or in the caller's transaction.
func (s *Store) readSnapshot(ctx context.Context, fn func(tx *Store) error) error {
	if s.InTx() {
		return fn(s)
	}
	return s.WithTxOptions(ctx, TxOptions{Isolation: IsolationRepeatableRead, ReadOnly: true}, fn)
}

func (s *Store) overview(ctx context.Context, ov *Overview) error {
	const op = "overview"
	from, to := ov.From, ov.To

	// The merchant filter is a fixed fragment; the id is bound as $3.
	args := []any{from, to}
	mf := func(string) string { return "" }
	if ov.MerchantID != nil {
		var exists bool
		if err := s.db.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM merchants WHERE id = $1)`, *ov.MerchantID).Scan(&exists); err != nil {
			return wrap(op+" merchant", err)
		}
		if !exists {
			return fmt.Errorf("store: %s: merchant %s: %w", op, *ov.MerchantID, ErrNotFound)
		}
		args = append(args, *ov.MerchantID)
		mf = func(alias string) string { return " AND " + alias + "merchant_id = $3" }
	}

	// Totals. The OR-ed WHERE lets PostgreSQL use the created/paid/status
	// indexes instead of scanning the whole table.
	var t OverviewTotals
	var createdPaid int
	err := s.db.QueryRow(ctx, `
		SELECT
			count(*) FILTER (WHERE created_at >= $1 AND created_at < $2),
			count(*) FILTER (WHERE status = 'paid' AND paid_at >= $1 AND paid_at < $2),
			count(*) FILTER (WHERE status IN ('pending', 'partial', 'confirming')),
			COALESCE(sum(amount_usd) FILTER (WHERE status = 'paid' AND paid_at >= $1 AND paid_at < $2), 0)::text,
			count(*) FILTER (WHERE created_at >= $1 AND created_at < $2 AND status = 'paid')
		FROM invoices
		WHERE ((created_at >= $1 AND created_at < $2)
		    OR (status = 'paid' AND paid_at >= $1 AND paid_at < $2)
		    OR status IN ('pending', 'partial', 'confirming'))`+mf(""), args...).
		Scan(&t.Invoices, &t.PaidInvoices, &t.OpenInvoices, decScan{&t.VolumeUSD}, &createdPaid)
	if err != nil {
		return wrap(op+" totals", err)
	}
	if t.Invoices > 0 {
		t.ConversionRate = float64(createdPaid) / float64(t.Invoices)
	}
	if ov.MerchantID == nil {
		err = s.db.QueryRow(ctx, `SELECT
				(SELECT count(*) FROM transfers WHERE invoice_id IS NULL AND status <> 'orphaned'),
				(SELECT count(*) FROM webhook_events WHERE status = 'failed')`).
			Scan(&t.UnmatchedTransfers, &t.FailedWebhooks)
	} else {
		// Unmatched transfers belong to no merchant: 0.
		err = s.db.QueryRow(ctx, `SELECT count(*) FROM webhook_events WHERE status = 'failed' AND merchant_id = $1`,
			*ov.MerchantID).Scan(&t.FailedWebhooks)
	}
	if err != nil {
		return wrap(op+" counters", err)
	}
	ov.Totals = t

	// Daily series, zero-filled with generate_series over UTC dates.
	rows, err := s.db.Query(ctx, `
		WITH days AS (
			SELECT d::date AS day FROM generate_series(($1::timestamptz AT TIME ZONE 'UTC')::date,
			                                           ($2::timestamptz AT TIME ZONE 'UTC')::date - 1, interval '1 day') d
		), created AS (
			SELECT (created_at AT TIME ZONE 'UTC')::date AS day, count(*) AS n
			FROM invoices WHERE created_at >= $1 AND created_at < $2`+mf("")+` GROUP BY 1
		), paid AS (
			SELECT (paid_at AT TIME ZONE 'UTC')::date AS day, count(*) AS n, COALESCE(sum(amount_usd), 0) AS volume
			FROM invoices WHERE status = 'paid' AND paid_at >= $1 AND paid_at < $2`+mf("")+` GROUP BY 1
		)
		SELECT to_char(days.day, 'YYYY-MM-DD'), COALESCE(created.n, 0), COALESCE(paid.n, 0), COALESCE(paid.volume, 0)::text
		FROM days LEFT JOIN created USING (day) LEFT JOIN paid USING (day)
		ORDER BY days.day`, args...)
	if err != nil {
		return wrap(op+" daily", err)
	}
	ov.Daily, err = pgx.CollectRows(rows, func(r pgx.CollectableRow) (DailyStat, error) {
		var d DailyStat
		err := r.Scan(&d.Date, &d.Invoices, &d.Paid, decScan{&d.VolumeUSD})
		return d, err
	})
	if err != nil {
		return wrap(op+" daily", err)
	}
	ov.Daily = nonNil(ov.Daily)

	// By asset of the paid quote.
	rows, err = s.db.Query(ctx, `
		SELECT a.id, a.symbol, a.chain_id, c.name, count(*), COALESCE(sum(i.amount_usd), 0)::text
		FROM invoices i
		JOIN quotes q ON q.id = i.paid_quote_id
		JOIN assets a ON a.id = q.asset_id
		JOIN chains c ON c.id = a.chain_id
		WHERE i.status = 'paid' AND i.paid_at >= $1 AND i.paid_at < $2`+mf("i.")+`
		GROUP BY a.id, a.symbol, a.chain_id, c.name
		ORDER BY COALESCE(sum(i.amount_usd), 0) DESC, count(*) DESC, a.id`, args...)
	if err != nil {
		return wrap(op+" by asset", err)
	}
	ov.ByAsset, err = pgx.CollectRows(rows, func(r pgx.CollectableRow) (AssetStat, error) {
		var a AssetStat
		err := r.Scan(&a.AssetID, &a.Symbol, &a.ChainID, &a.ChainName, &a.Paid, decScan{&a.VolumeUSD})
		return a, err
	})
	if err != nil {
		return wrap(op+" by asset", err)
	}
	ov.ByAsset = nonNil(ov.ByAsset)

	// By status of invoices created in the range, zero-filled.
	rows, err = s.db.Query(ctx, `SELECT status, count(*) FROM invoices WHERE created_at >= $1 AND created_at < $2`+mf("")+`
		GROUP BY status`, args...)
	if err != nil {
		return wrap(op+" by status", err)
	}
	counts := map[domain.InvoiceStatus]int{}
	var st string
	var n int
	if _, err := pgx.ForEachRow(rows, []any{&st, &n}, func() error {
		counts[domain.InvoiceStatus(st)] = n
		return nil
	}); err != nil {
		return wrap(op+" by status", err)
	}
	ov.ByStatus = make([]StatusCount, len(AllInvoiceStatuses))
	for i, st := range AllInvoiceStatuses {
		ov.ByStatus[i] = StatusCount{Status: st, Count: counts[st]}
	}
	return nil
}

// MerchantOverview is one merchant's row of the per-merchant comparison
// (GET /api/admin/overview/merchants). The definitions are exactly those of
// Overview.Totals restricted to the merchant (OverviewFor):
//
//   - Invoices: created in the range; PaidInvoices / VolumeUSD: paid (by
//     paid_at) in the range, volume = sum of amount_usd (NULL counts as 0);
//     ConversionRate: created in the range and now paid ÷ Invoices (0 when
//     there are none);
//   - OpenInvoices and FailedWebhooks are current counts, independent of the
//     range;
//   - LastInvoiceAt / LastPaidAt: the newest invoice ever created / the
//     newest paid_at of a paid invoice ever (nil when there is none).
type MerchantOverview struct {
	MerchantID     uuid.UUID
	MerchantName   string
	Status         domain.MerchantStatus
	Invoices       int
	PaidInvoices   int
	VolumeUSD      decimal.Decimal
	ConversionRate float64 // 0..1
	OpenInvoices   int
	FailedWebhooks int
	LastInvoiceAt  *time.Time
	LastPaidAt     *time.Time
}

// MerchantOverviewSort is a sort key of ListMerchantOverviews.
type MerchantOverviewSort string

const (
	SortMerchantVolume         MerchantOverviewSort = "volume_usd" // default
	SortMerchantInvoices       MerchantOverviewSort = "invoices"
	SortMerchantPaidInvoices   MerchantOverviewSort = "paid_invoices"
	SortMerchantConversion     MerchantOverviewSort = "conversion_rate"
	SortMerchantOpenInvoices   MerchantOverviewSort = "open_invoices"
	SortMerchantFailedWebhooks MerchantOverviewSort = "failed_webhooks"
	SortMerchantName           MerchantOverviewSort = "name"
)

// merchantOverviewSortSQL whitelists the sort keys: each maps to a fixed
// column of the stats CTE (never user input).
var merchantOverviewSortSQL = map[MerchantOverviewSort]string{
	SortMerchantVolume:         "volume_usd", // numeric
	SortMerchantInvoices:       "invoices",
	SortMerchantPaidInvoices:   "paid_invoices",
	SortMerchantConversion:     "conversion_rate",
	SortMerchantOpenInvoices:   "open_invoices",
	SortMerchantFailedWebhooks: "failed_webhooks",
	SortMerchantName:           "name",
}

// MerchantOverviewSorts lists the sort keys in documentation order.
var MerchantOverviewSorts = []MerchantOverviewSort{
	SortMerchantVolume, SortMerchantInvoices, SortMerchantPaidInvoices, SortMerchantConversion,
	SortMerchantOpenInvoices, SortMerchantFailedWebhooks, SortMerchantName,
}

// Sort orders of ListMerchantOverviews.
const (
	OrderAsc  = "asc"
	OrderDesc = "desc"
)

// MerchantOverviewFilter selects a page of ListMerchantOverviews.
type MerchantOverviewFilter struct {
	Days  int                  // as Overview: 0 = DefaultOverviewDays, clamped to 1..MaxOverviewDays
	Sort  MerchantOverviewSort // "" = SortMerchantVolume
	Order string               // OrderAsc / OrderDesc; "" = desc, but asc for SortMerchantName
	// Page is 1-based; PageSize is normalised like every list.
	Page, PageSize int
}

// MerchantOverviewList is a page of merchant rows over one range.
type MerchantOverviewList struct {
	RangeDays int
	From, To  time.Time
	Rows      Page[MerchantOverview] // every merchant, idle ones included
}

// ListMerchantOverviews returns one row per merchant (every merchant,
// including ones without any activity) for the last Days UTC days, sorted
// by f.Sort / f.Order with ties broken by name, then id (both ascending).
// Two statements (count + page) in one read-only snapshot, like Overview.
func (s *Store) ListMerchantOverviews(ctx context.Context, f MerchantOverviewFilter, now time.Time) (MerchantOverviewList, error) {
	const op = "list merchant overviews"
	if f.Sort == "" {
		f.Sort = SortMerchantVolume
	}
	col, ok := merchantOverviewSortSQL[f.Sort]
	if !ok {
		return MerchantOverviewList{}, invalidf(op, "unknown sort %q", f.Sort)
	}
	if f.Order == "" {
		f.Order = OrderDesc
		if f.Sort == SortMerchantName {
			f.Order = OrderAsc
		}
	}
	if f.Order != OrderAsc && f.Order != OrderDesc {
		return MerchantOverviewList{}, invalidf(op, "unknown order %q", f.Order)
	}
	days, from, to := overviewRange(f.Days, now)
	page, size, offset := normPage(f.Page, f.PageSize)
	out := MerchantOverviewList{RangeDays: days, From: from, To: to,
		Rows: Page[MerchantOverview]{Items: []MerchantOverview{}, Page: page, PageSize: size}}

	dir := " DESC"
	if f.Order == OrderAsc {
		dir = " ASC"
	}
	// Qualified, so the outer ORDER BY never binds to an output column (the
	// selected volume is text).
	order := func(t string) string {
		if f.Sort == SortMerchantName {
			return t + ".name" + dir + ", " + t + ".id"
		}
		return t + "." + col + dir + ", " + t + ".name, " + t + ".id"
	}

	err := s.readSnapshot(ctx, func(tx *Store) error {
		if err := tx.db.QueryRow(ctx, `SELECT count(*) FROM merchants`).Scan(&out.Rows.Total); err != nil {
			return wrap(op, err)
		}
		if out.Rows.Total == 0 || offset >= out.Rows.Total {
			return nil
		}
		// Aggregates per merchant over the same index-backed sets as
		// Overview, joined to every merchant; the page is cut before the
		// "ever" lookups, which run only for the rows returned.
		rows, err := tx.db.Query(ctx, `
			WITH created AS (
				SELECT merchant_id, count(*) AS n, count(*) FILTER (WHERE status = 'paid') AS n_paid
				FROM invoices WHERE created_at >= $1 AND created_at < $2 GROUP BY merchant_id
			), paid AS (
				SELECT merchant_id, count(*) AS n, COALESCE(sum(amount_usd), 0) AS volume
				FROM invoices WHERE status = 'paid' AND paid_at >= $1 AND paid_at < $2 GROUP BY merchant_id
			), open AS (
				SELECT merchant_id, count(*) AS n
				FROM invoices WHERE status IN ('pending', 'partial', 'confirming') GROUP BY merchant_id
			), failed AS (
				SELECT merchant_id, count(*) AS n
				FROM webhook_events WHERE status = 'failed' GROUP BY merchant_id
			), stats AS (
				SELECT m.id, m.name, m.status,
				       COALESCE(c.n, 0) AS invoices,
				       COALESCE(p.n, 0) AS paid_invoices,
				       COALESCE(p.volume, 0) AS volume_usd,
				       CASE WHEN c.n > 0 THEN c.n_paid::float8 / c.n ELSE 0 END AS conversion_rate,
				       COALESCE(o.n, 0) AS open_invoices,
				       COALESCE(f.n, 0) AS failed_webhooks
				FROM merchants m
				LEFT JOIN created c ON c.merchant_id = m.id
				LEFT JOIN paid p ON p.merchant_id = m.id
				LEFT JOIN open o ON o.merchant_id = m.id
				LEFT JOIN failed f ON f.merchant_id = m.id
			), page AS (
				SELECT * FROM stats ORDER BY `+order("stats")+fmt.Sprintf(` LIMIT %d OFFSET %d`, size, offset)+`
			)
			SELECT page.id, page.name, page.status, page.invoices, page.paid_invoices, page.volume_usd::text,
			       page.conversion_rate, page.open_invoices, page.failed_webhooks,
			       (SELECT max(i.created_at) FROM invoices i WHERE i.merchant_id = page.id),
			       (SELECT max(i.paid_at) FROM invoices i WHERE i.merchant_id = page.id AND i.status = 'paid')
			FROM page
			ORDER BY `+order("page"), from, to)
		if err != nil {
			return wrap(op, err)
		}
		items, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (MerchantOverview, error) {
			var m MerchantOverview
			var status string
			err := r.Scan(&m.MerchantID, &m.MerchantName, &status, &m.Invoices, &m.PaidInvoices, decScan{&m.VolumeUSD},
				&m.ConversionRate, &m.OpenInvoices, &m.FailedWebhooks, &m.LastInvoiceAt, &m.LastPaidAt)
			m.Status = domain.MerchantStatus(status)
			m.LastInvoiceAt, m.LastPaidAt = utcPtr(m.LastInvoiceAt), utcPtr(m.LastPaidAt)
			return m, err
		})
		if err != nil {
			return wrap(op, err)
		}
		out.Rows.Items = nonNil(items)
		return nil
	})
	return out, err
}
