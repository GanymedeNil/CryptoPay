package store

import (
	"context"
	"time"

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
type Overview struct {
	RangeDays int
	From, To  time.Time
	Totals    OverviewTotals
	Daily     []DailyStat   // one row per day, oldest first, zero-filled
	ByAsset   []AssetStat   // by volume desc, then count desc
	ByStatus  []StatusCount // every invoice status in lifecycle order, zero-filled
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
	if days == 0 {
		days = DefaultOverviewDays
	}
	days = min(max(days, 1), MaxOverviewDays)
	now = now.UTC()
	to := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.UTC).AddDate(0, 0, 1)
	from := to.AddDate(0, 0, -days)
	ov := Overview{RangeDays: days, From: from, To: to}

	run := func(tx *Store) error { return tx.overview(ctx, &ov) }
	if s.InTx() {
		return ov, run(s)
	}
	err := s.WithTxOptions(ctx, TxOptions{Isolation: IsolationRepeatableRead, ReadOnly: true}, run)
	return ov, err
}

func (s *Store) overview(ctx context.Context, ov *Overview) error {
	const op = "overview"
	from, to := ov.From, ov.To

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
		WHERE (created_at >= $1 AND created_at < $2)
		   OR (status = 'paid' AND paid_at >= $1 AND paid_at < $2)
		   OR status IN ('pending', 'partial', 'confirming')`, from, to).
		Scan(&t.Invoices, &t.PaidInvoices, &t.OpenInvoices, decScan{&t.VolumeUSD}, &createdPaid)
	if err != nil {
		return wrap(op+" totals", err)
	}
	if t.Invoices > 0 {
		t.ConversionRate = float64(createdPaid) / float64(t.Invoices)
	}
	if err := s.db.QueryRow(ctx, `SELECT
			(SELECT count(*) FROM transfers WHERE invoice_id IS NULL AND status <> 'orphaned'),
			(SELECT count(*) FROM webhook_events WHERE status = 'failed')`).
		Scan(&t.UnmatchedTransfers, &t.FailedWebhooks); err != nil {
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
			FROM invoices WHERE created_at >= $1 AND created_at < $2 GROUP BY 1
		), paid AS (
			SELECT (paid_at AT TIME ZONE 'UTC')::date AS day, count(*) AS n, COALESCE(sum(amount_usd), 0) AS volume
			FROM invoices WHERE status = 'paid' AND paid_at >= $1 AND paid_at < $2 GROUP BY 1
		)
		SELECT to_char(days.day, 'YYYY-MM-DD'), COALESCE(created.n, 0), COALESCE(paid.n, 0), COALESCE(paid.volume, 0)::text
		FROM days LEFT JOIN created USING (day) LEFT JOIN paid USING (day)
		ORDER BY days.day`, from, to)
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
		WHERE i.status = 'paid' AND i.paid_at >= $1 AND i.paid_at < $2
		GROUP BY a.id, a.symbol, a.chain_id, c.name
		ORDER BY COALESCE(sum(i.amount_usd), 0) DESC, count(*) DESC, a.id`, from, to)
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
	rows, err = s.db.Query(ctx, `SELECT status, count(*) FROM invoices WHERE created_at >= $1 AND created_at < $2
		GROUP BY status`, from, to)
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
