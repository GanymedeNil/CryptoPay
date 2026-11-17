package webhook_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"cryptopay/internal/alert"
	"cryptopay/internal/domain"
	"cryptopay/internal/store"
	"cryptopay/internal/webhook"
)

// TestFinalFailureAlert: only the attempt that makes an event failed is
// reported, after it was recorded; intermediate failures and deliveries
// are not.
func TestFinalFailureAlert(t *testing.T) {
	s := domain.DefaultSettings()
	s.WebhookMaxAttempts = 3
	e := setup(t, &s)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusBadGateway) }))
	defer srv.Close()
	ev := e.event(srv.URL)
	rec := alert.NewRecorder()
	d := e.disp(webhook.WithNotifier(rec))

	for i := 1; i <= 3; i++ {
		if _, failed, err := d.DeliverDue(context.Background()); err != nil || failed != 1 {
			t.Fatalf("attempt %d: failed=%d err=%v", i, failed, err)
		}
		calls := rec.Calls()
		if i < 3 && len(calls) != 0 {
			t.Fatalf("attempt %d: alert before the final failure: %+v", i, calls)
		}
		if i == 3 {
			if len(calls) != 1 || calls[0].Kind != "webhook_failed" || calls[0].Webhook.ID != ev.ID ||
				calls[0].Webhook.Status != domain.WebhookFailed || calls[0].Webhook.Attempts != 3 {
				t.Fatalf("want one final alert, got %+v", calls)
			}
			// Reported after it was recorded.
			if got := e.get(ev.ID); got.Status != domain.WebhookFailed {
				t.Fatalf("status %s", got.Status)
			}
		}
		e.clock.Advance(7 * time.Hour)
	}

	// A permanent failure (merchant disabled) is final at once.
	ev2 := e.event(srv.URL)
	if _, err := e.st.UpdateMerchant(context.Background(), e.m.ID, storeMerchantDisabled()); err != nil {
		t.Fatal(err)
	}
	if _, failed, _ := d.DeliverDue(context.Background()); failed != 1 {
		t.Fatalf("failed=%d", failed)
	}
	if calls := rec.Calls(); len(calls) != 1 || calls[0].Webhook.ID != ev2.ID {
		t.Fatalf("got %+v", calls)
	}
}

type panicNotifier struct{ alert.Notifier }

func (panicNotifier) WebhookFailed(context.Context, domain.WebhookEvent) { panic("notifier bug") }

// TestAlertPanicDoesNotAffectDelivery: a broken notifier changes nothing.
func TestAlertPanicDoesNotAffectDelivery(t *testing.T) {
	s := domain.DefaultSettings()
	s.WebhookMaxAttempts = 1
	e := setup(t, &s)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusInternalServerError) }))
	defer srv.Close()
	ev := e.event(srv.URL)
	_, failed, err := e.disp(webhook.WithNotifier(panicNotifier{})).DeliverDue(context.Background())
	if err != nil || failed != 1 {
		t.Fatalf("failed=%d err=%v", failed, err)
	}
	if got := e.get(ev.ID); got.Status != domain.WebhookFailed || got.Attempts != 1 {
		t.Fatalf("%+v", got)
	}
}

func storeMerchantDisabled() store.MerchantUpdate {
	st := domain.MerchantDisabled
	return store.MerchantUpdate{Status: &st}
}
