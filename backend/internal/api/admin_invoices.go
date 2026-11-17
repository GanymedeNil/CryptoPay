package api

import (
	"errors"
	"net/http"
	"strings"

	"github.com/google/uuid"

	"cryptopay/internal/domain"
	"cryptopay/internal/store"
	"cryptopay/internal/view"
)

func (a *API) adminOverview(w http.ResponseWriter, r *http.Request) {
	q := newQuery(r)
	days := q.intIn("days", store.DefaultOverviewDays, 1, store.MaxOverviewDays)
	merchantID := q.uuid("merchant_id")
	if err := q.err(); err != nil {
		a.fail(w, r, err)
		return
	}
	o, err := a.store.OverviewFor(r.Context(), merchantID, days, a.now())
	if errors.Is(err, store.ErrNotFound) {
		a.fail(w, r, errNotFound("unknown merchant"))
		return
	}
	if err != nil {
		a.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, newOverviewJSON(o))
}

// adminOverviewMerchants is the per-merchant comparison: one row per
// merchant with the figures of GET /overview?merchant_id= (docs/api.md).
func (a *API) adminOverviewMerchants(w http.ResponseWriter, r *http.Request) {
	q := newQuery(r)
	sorts := make([]string, 0, len(store.MerchantOverviewSorts))
	for _, s := range store.MerchantOverviewSorts {
		sorts = append(sorts, string(s))
	}
	f := store.MerchantOverviewFilter{
		Days:  q.intIn("days", store.DefaultOverviewDays, 1, store.MaxOverviewDays),
		Sort:  store.MerchantOverviewSort(q.oneOf("sort", sorts...)),
		Order: q.oneOf("order", store.OrderAsc, store.OrderDesc),
	}
	f.Page, f.PageSize = q.page()
	if err := q.err(); err != nil {
		a.fail(w, r, err)
		return
	}
	l, err := a.store.ListMerchantOverviews(r.Context(), f, a.now())
	if err != nil {
		a.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, merchantOverviewPageJSON{
		pageJSON:  mapPage(l.Rows, newMerchantOverviewJSON),
		RangeDays: l.RangeDays,
	})
}

// ---- invoices ----

func (a *API) adminListInvoices(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	q := newQuery(r)
	f := store.InvoiceFilter{
		MerchantID: q.uuid("merchant_id"),
		Q:          q.str("q"),
		From:       q.time("from"),
		To:         q.time("to"),
	}
	if s := q.oneOf("status", invoiceStatusNames()...); s != "" {
		f.Statuses = []domain.InvoiceStatus{domain.InvoiceStatus(s)}
	}
	f.Page, f.PageSize = q.page()
	if len(f.Q) > 200 {
		q.fields.add("q", "is too long")
	}
	if !f.From.IsZero() && !f.To.IsZero() && !f.From.Before(f.To) {
		q.fields.add("to", "must be after from")
	}
	if err := q.err(); err != nil {
		a.fail(w, r, err)
		return
	}
	p, err := a.store.ListInvoices(ctx, f)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	snap, err := a.snapshot(ctx)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, mapPage(p, snap.adminInvoice))
}

func (a *API) adminCreateInvoice(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	var body struct {
		createInvoiceBody
		MerchantID string `json:"merchant_id"`
	}
	if err := decodeJSON(r, &body); err != nil {
		a.fail(w, r, err)
		return
	}
	mid, err := uuid.Parse(strings.TrimSpace(body.MerchantID))
	if err != nil {
		msg := "must be a merchant id"
		if strings.TrimSpace(body.MerchantID) == "" {
			msg = "is required"
		}
		a.fail(w, r, errField("merchant_id", msg))
		return
	}
	m, err := a.store.GetMerchant(ctx, mid)
	if errors.Is(err, store.ErrNotFound) {
		a.fail(w, r, errField("merchant_id", "unknown merchant"))
		return
	}
	if err != nil {
		a.fail(w, r, err)
		return
	}
	if m.Status != domain.MerchantActive {
		a.fail(w, r, errField("merchant_id", "the merchant is disabled"))
		return
	}
	v, created, err := a.invoices.Create(ctx, m, body.params())
	if err != nil {
		a.fail(w, r, err)
		return
	}
	status := http.StatusOK
	if created {
		status = http.StatusCreated
		a.auditAfter(ctx, auditEntry{Action: "invoice.create", TargetType: "invoice", TargetID: v.ID.String(),
			Detail: map[string]any{"merchant_id": m.ID.String(), "amount": v.PriceAmount.String(),
				"currency": v.PriceCurrency, "order_id": v.MerchantOrderID}})
	}
	snap, err := a.snapshot(ctx)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	writeJSON(w, status, snap.adminInvoice(v))
}

func (a *API) adminGetInvoice(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	id, err := pathUUID(r, "id")
	if err != nil {
		a.fail(w, r, err)
		return
	}
	d, err := a.invoices.Get(ctx, id)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	events, err := a.store.ListWebhookEventsByInvoice(ctx, id)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	snap, err := a.snapshot(ctx)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, invoiceDetailJSON{
		Invoice:       snap.invoice(d.InvoiceView),
		MerchantName:  d.MerchantName,
		Quotes:        view.NewQuotes(d.Quotes, snap.cat),
		Transfers:     view.NewTransfers(d.Transfers, snap.cat),
		WebhookEvents: mapSlice(events, newWebhookEventJSON),
	})
}

func (a *API) adminCancelInvoice(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	id, err := pathUUID(r, "id")
	if err != nil {
		a.fail(w, r, err)
		return
	}
	if err := decodeJSON(r, &struct{}{}); err != nil {
		a.fail(w, r, err)
		return
	}
	ad, _ := adminFrom(ctx)
	v, err := a.invoices.Cancel(ctx, id, "admin:"+ad.User.Email)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	a.auditAfter(ctx, auditEntry{Action: "invoice.cancel", TargetType: "invoice", TargetID: id.String(),
		Detail: map[string]any{"merchant_id": v.MerchantID.String()}})
	snap, err := a.snapshot(ctx)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, snap.adminInvoice(v))
}

const maxNoteLen = 1000

func (a *API) adminMarkPaid(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	id, err := pathUUID(r, "id")
	if err != nil {
		a.fail(w, r, err)
		return
	}
	var body struct {
		Note string `json:"note"`
	}
	if err := decodeJSON(r, &body); err != nil {
		a.fail(w, r, err)
		return
	}
	note := strings.TrimSpace(body.Note)
	switch {
	case note == "":
		a.fail(w, r, errField("note", "is required"))
		return
	case len([]rune(note)) > maxNoteLen:
		a.fail(w, r, errField("note", "must be at most 1000 characters"))
		return
	}
	ad, _ := adminFrom(ctx)
	before, err := a.invoices.GetView(ctx, id)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	v, err := a.invoices.MarkPaid(ctx, id, note, "admin:"+ad.User.Email)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	a.auditAfter(ctx, auditEntry{Action: "invoice.mark_paid", TargetType: "invoice", TargetID: id.String(),
		Detail: map[string]any{"note": note, "previous_status": string(before.Status), "merchant_id": v.MerchantID.String()}})
	snap, err := a.snapshot(ctx)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, snap.adminInvoice(v))
}

// ---- transfers ----

func (a *API) adminListTransfers(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	q := newQuery(r)
	f := store.TransferFilter{
		ChainID: q.str("chain_id"),
		Status:  domain.TransferStatus(q.oneOf("status", "pending", "confirmed", "orphaned")),
		Matched: q.boolean("matched"),
		Q:       q.str("q"),
	}
	f.Page, f.PageSize = q.page()
	if len(f.Q) > 200 {
		q.fields.add("q", "is too long")
	}
	if err := q.err(); err != nil {
		a.fail(w, r, err)
		return
	}
	p, err := a.store.ListTransfers(ctx, f)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	cat, err := a.store.LoadCatalog(ctx)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, mapPage(p, func(t domain.Transfer) view.Transfer { return view.NewTransfer(t, cat) }))
}

func (a *API) adminAssignTransfer(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	id, err := pathUUID(r, "id")
	if err != nil {
		a.fail(w, r, err)
		return
	}
	var body struct {
		InvoiceID string `json:"invoice_id"`
	}
	if err := decodeJSON(r, &body); err != nil {
		a.fail(w, r, err)
		return
	}
	invoiceID, err := uuid.Parse(strings.TrimSpace(body.InvoiceID))
	if err != nil {
		msg := "must be an invoice id"
		if strings.TrimSpace(body.InvoiceID) == "" {
			msg = "is required"
		}
		a.fail(w, r, errField("invoice_id", msg))
		return
	}
	t, err := a.invoices.AssignTransfer(ctx, id, invoiceID)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	a.auditAfter(ctx, auditEntry{Action: "transfer.assign", TargetType: "transfer", TargetID: id.String(),
		Detail: map[string]any{"invoice_id": invoiceID.String(), "chain_id": t.ChainID, "tx_hash": t.TxHash}})
	cat, err := a.store.LoadCatalog(ctx)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, view.NewTransfer(t, cat))
}

// ---- webhook events ----

func (a *API) adminListWebhookEvents(w http.ResponseWriter, r *http.Request) {
	q := newQuery(r)
	f := store.WebhookFilter{
		Status:     domain.WebhookStatus(q.oneOf("status", "pending", "delivered", "failed")),
		MerchantID: q.uuid("merchant_id"),
		InvoiceID:  q.uuid("invoice_id"),
	}
	f.Page, f.PageSize = q.page()
	if err := q.err(); err != nil {
		a.fail(w, r, err)
		return
	}
	p, err := a.store.ListWebhookEvents(r.Context(), f)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, mapPage(p, newWebhookEventJSON))
}

func (a *API) adminRetryWebhookEvent(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	id, err := pathUUID(r, "id")
	if err != nil {
		a.fail(w, r, err)
		return
	}
	if err := decodeJSON(r, &struct{}{}); err != nil {
		a.fail(w, r, err)
		return
	}
	var ev domain.WebhookEvent
	err = a.inTx(ctx, func(tx *store.Store) error {
		var err error
		if ev, err = tx.RequeueWebhook(ctx, id, a.now()); err != nil {
			return err
		}
		return a.audit(ctx, tx, nil, auditEntry{Action: "webhook.retry", TargetType: "webhook_event", TargetID: id.String(),
			Detail: map[string]any{"invoice_id": ev.InvoiceID.String(), "event_type": ev.EventType, "attempts": ev.Attempts}})
	})
	if err != nil {
		a.fail(w, r, err)
		return
	}
	if a.hooks.WebhookKick != nil {
		a.hooks.WebhookKick()
	}
	writeJSON(w, http.StatusOK, newWebhookEventJSON(ev))
}

// ---- audit log ----

func (a *API) adminListAuditLogs(w http.ResponseWriter, r *http.Request) {
	q := newQuery(r)
	f := store.AuditFilter{Action: q.str("action"), Actor: q.str("actor")}
	f.Page, f.PageSize = q.page()
	if err := q.err(); err != nil {
		a.fail(w, r, err)
		return
	}
	p, err := a.store.ListAuditLogs(r.Context(), f)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, mapPage(p, newAuditLogJSON))
}
